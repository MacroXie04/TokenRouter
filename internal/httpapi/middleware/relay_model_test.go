package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/jimeng"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequireRelayQueryModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		token      *model.Token
		modelName  string
		wantStatus int
	}{
		{name: "allowed", token: &model.Token{ModelLimitsEnabled: true, ModelLimits: "gpt-4"}, modelName: "gpt-4", wantStatus: http.StatusNoContent},
		{name: "denied", token: &model.Token{ModelLimitsEnabled: true, ModelLimits: "gpt-4"}, modelName: "gpt-4o", wantStatus: http.StatusForbidden},
		{name: "empty limits deny", token: &model.Token{ModelLimitsEnabled: true}, modelName: "gpt-4", wantStatus: http.StatusForbidden},
		{name: "malformed limits deny", token: &model.Token{ModelLimitsEnabled: true, ModelLimits: "gpt-4,"}, modelName: "gpt-4", wantStatus: http.StatusForbidden},
		{name: "unrestricted", token: &model.Token{}, modelName: "gpt-4o", wantStatus: http.StatusNoContent},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				SetupRelayTokenContext(c, test.token)
				c.Next()
			})
			router.GET("/realtime", RequireRelayQueryModel("model"), func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})

			request := httptest.NewRequest(http.MethodGet, "/realtime?model="+test.modelName, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assert.Equal(t, test.wantStatus, response.Code, response.Body.String())
			if test.wantStatus == http.StatusForbidden {
				assert.Contains(t, response.Body.String(), `"code":"model_not_allowed"`)
			}
		})
	}
}

func TestRequireJimengModelSubmit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		limits     string
		reqKey     string
		wantStatus int
	}{
		{name: "allowed", limits: "jimeng-video-3.0", reqKey: "jimeng-video-3.0", wantStatus: http.StatusNoContent},
		{name: "denied", limits: "jimeng-video-3.0", reqKey: "jimeng-video-2.0", wantStatus: http.StatusForbidden},
		{name: "malformed limits deny", limits: "jimeng-video-3.0,", reqKey: "jimeng-video-3.0", wantStatus: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := jimengPolicyTestRouter(&model.Token{ModelLimitsEnabled: true, ModelLimits: test.limits}, 7)
			body := `{"req_key":"` + test.reqKey + `"}`
			request := httptest.NewRequest(http.MethodPost, "/?Action="+jimeng.SubmitAction, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assert.Equal(t, test.wantStatus, response.Code, response.Body.String())
		})
	}
}

func TestRequireJimengModelFetchUsesPersistedOriginModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "jimeng-model-limit.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Task{}))
	model.DB = db
	require.NoError(t, db.Create(&model.Task{
		TaskID: "task-owned", UserId: 7, Properties: `{"origin_model_name":"jimeng-video-3.0"}`,
	}).Error)

	for _, test := range []struct {
		name       string
		limits     string
		wantStatus int
	}{
		{name: "allowed", limits: "jimeng-video-3.0", wantStatus: http.StatusNoContent},
		{name: "denied", limits: "jimeng-video-2.0", wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := jimengPolicyTestRouter(&model.Token{ModelLimitsEnabled: true, ModelLimits: test.limits}, 7)
			request := httptest.NewRequest(http.MethodPost, "/?Action="+jimeng.FetchAction, strings.NewReader(`{"task_id":"task-owned"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assert.Equal(t, test.wantStatus, response.Code, response.Body.String())
		})
	}
}

func jimengPolicyTestRouter(token *model.Token, userID int) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		SetupRelayTokenContext(c, token)
		requestctx.SetUserId(c, userID)
		c.Next()
	})
	router.POST("/", JimengActionValidate(), JimengRequestConvert(), RequireJimengModel(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	return router
}
