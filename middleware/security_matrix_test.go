package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func TestTokenAuthEnforcesIPAllowListEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "token-ip-allow-list.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Option{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, setting.Init())

	previousRatios := service.ExportedGroupRatios()
	service.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() { service.SetGroupRatios(previousRatios) })

	user := model.User{Username: "token-ip-owner", Status: model.UserStatusEnabled, Group: "default"}
	require.NoError(t, db.Create(&user).Error)
	allowedIP := "198.51.100.10"
	token := model.Token{
		UserId: user.Id, Key: "sk-ip-bound", Status: service.TokenStatusEnabled,
		UnlimitedQuota: true, AllowIps: &allowedIP,
	}
	require.NoError(t, db.Create(&token).Error)

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.Use(TokenAuth())
	router.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := func(remoteAddr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remoteAddr
		req.Header.Set("Authorization", "Bearer "+token.Key)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}

	assert.Equal(t, http.StatusNoContent, request("198.51.100.10:1234").Code)
	denied := request("198.51.100.11:1234")
	assert.Equal(t, http.StatusForbidden, denied.Code)
	assert.Contains(t, denied.Body.String(), `"code":"ip_not_allowed"`)
}

func TestRecoveryReturnsStableEnvelopeWithoutPanicOrStack(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID(), Recovery())
	router.GET("/panic", func(*gin.Context) {
		panic("panic-secret-marker")
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))

	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.JSONEq(t, `{"error":{"message":"internal server error","type":"server_error","code":"internal_error"}}`, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "panic-secret-marker")
	assert.NotContains(t, recorder.Body.String(), "goroutine")
}
