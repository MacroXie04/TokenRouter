package relay

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestRelayModelCatalogHonorsTokenLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	userID := initializeRelayModelCatalog(t)

	t.Run("OpenAI and Gemini lists expose the same allowed subset", func(t *testing.T) {
		router := relayModelCatalogRouter(&model.Token{UserId: userID, ModelLimitsEnabled: true, ModelLimits: "gpt-4"})

		openAI := performCatalogRequest(router, "/v1/models")
		require.Equal(t, http.StatusOK, openAI.Code)
		var openAIBody struct {
			Data []struct {
				ID                     string   `json:"id"`
				SupportedEndpointTypes []string `json:"supported_endpoint_types"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(openAI.Body.Bytes(), &openAIBody))
		require.Len(t, openAIBody.Data, 1)
		assert.Equal(t, "gpt-4", openAIBody.Data[0].ID)
		assert.Equal(t, []string{"openai"}, openAIBody.Data[0].SupportedEndpointTypes)

		gemini := performCatalogRequest(router, "/v1beta/models")
		require.Equal(t, http.StatusOK, gemini.Code)
		var geminiBody struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		require.NoError(t, json.Unmarshal(gemini.Body.Bytes(), &geminiBody))
		require.Len(t, geminiBody.Models, 1)
		assert.Equal(t, "gpt-4", geminiBody.Models[0].Name)
	})

	t.Run("empty and malformed limits produce empty catalogs", func(t *testing.T) {
		for _, limits := range []string{"", "gpt-4,"} {
			router := relayModelCatalogRouter(&model.Token{UserId: userID, ModelLimitsEnabled: true, ModelLimits: limits})
			response := performCatalogRequest(router, "/v1/models")
			require.Equal(t, http.StatusOK, response.Code)
			var body struct {
				Data []any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			assert.Empty(t, body.Data)
		}
	})

	t.Run("retrieve hides models outside the token allow-list", func(t *testing.T) {
		router := relayModelCatalogRouter(&model.Token{UserId: userID, ModelLimitsEnabled: true, ModelLimits: "gpt-4"})
		assert.Equal(t, http.StatusOK, performCatalogRequest(router, "/v1/models/gpt-4").Code)

		denied := performCatalogRequest(router, "/v1/models/gpt-4o")
		assert.Equal(t, http.StatusNotFound, denied.Code)
		assert.Contains(t, denied.Body.String(), `"code":"model_not_found"`)
	})
}

func initializeRelayModelCatalog(t *testing.T) int {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "model-catalog.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.User{}, &model.Option{}))
	model.DB = db
	require.NoError(t, setting.Init())
	user := model.User{
		Username: "catalog-user", Password: "x", DisplayName: "Catalog User",
		Role: roles.RoleCommonUser, Status: model.UserStatusEnabled, Group: userssvc.GroupDefault,
	}
	require.NoError(t, db.Create(&user).Error)
	channels := []model.Channel{
		{Type: int(channelcatalog.ChannelTypeOpenAI), Key: "openai", Name: "openai", Status: channelcatalog.ChannelStatusEnabled},
		{Type: int(channelcatalog.ChannelTypeAnthropic), Key: "anthropic", Name: "anthropic", Status: channelcatalog.ChannelStatusEnabled},
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: userssvc.GroupDefault, Model: "gpt-4", ChannelId: channels[0].Id, Enabled: true},
		{Group: userssvc.GroupDefault, Model: "gpt-4o", ChannelId: channels[0].Id, Enabled: true},
		{Group: "vip", Model: "claude-sonnet", ChannelId: channels[1].Id, Enabled: true},
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
	return user.Id
}

func TestRelayModelCatalogUnionsAuthorizedAutoGroups(t *testing.T) {
	gin.SetMode(gin.TestMode)
	userID := initializeRelayModelCatalog(t)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		requestctx.SetUserId(c, userID)
		middleware.SetupRelayTokenContext(c, &model.Token{UserId: userID, Group: userssvc.GroupAuto})
		middleware.SetRelayGroupPolicy(c, billingsvc.RelayGroupPolicy{Groups: []string{"vip", userssvc.GroupDefault}, Auto: true})
		c.Next()
	})
	router.GET("/v1/models", RelayListModels)
	router.GET("/v1/models/:model", RelayRetrieveModel)

	response := performCatalogRequest(router, "/v1/models")
	require.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), `"id":"claude-sonnet"`)
	assert.Contains(t, response.Body.String(), `"id":"gpt-4"`)
	assert.Contains(t, response.Body.String(), `"supported_endpoint_types":["anthropic","openai"]`)
	assert.Equal(t, http.StatusOK, performCatalogRequest(router, "/v1/models/claude-sonnet").Code)
}

func TestRelayRetrieveModelAdvertisesEndpointTypes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	userID := initializeRelayModelCatalog(t)
	router := relayModelCatalogRouter(&model.Token{UserId: userID})

	response := performCatalogRequest(router, "/v1/models/gpt-4")
	require.Equal(t, http.StatusOK, response.Code)
	var body struct {
		SupportedEndpointTypes []string `json:"supported_endpoint_types"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, []string{"openai"}, body.SupportedEndpointTypes)
}

func TestRelayModelCatalogHonorsUnsetRatioPreference(t *testing.T) {
	gin.SetMode(gin.TestMode)
	userID := initializeRelayModelCatalog(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"gpt-4":"reference"}`,
		setting.PerCallModelPriceOption: `{}`,
		setting.ModelRatioOption:        `{}`,
		setting.CompletionRatioOption:   `{}`,
	}))
	router := relayModelCatalogRouter(&model.Token{UserId: userID})

	rejected := performCatalogRequest(router, "/v1/models")
	require.Equal(t, http.StatusOK, rejected.Code, rejected.Body.String())
	assert.NotContains(t, rejected.Body.String(), `"id":"gpt-4"`)
	assert.Contains(t, rejected.Body.String(), `"id":"gpt-4o"`)
	assert.Equal(t, http.StatusNotFound, performCatalogRequest(router, "/v1/models/gpt-4").Code)

	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Update(
		"setting", `{"accept_unset_model_ratio_model":true}`,
	).Error)
	accepted := performCatalogRequest(router, "/v1/models")
	require.Equal(t, http.StatusOK, accepted.Code, accepted.Body.String())
	assert.Contains(t, accepted.Body.String(), `"id":"gpt-4"`)
	assert.Equal(t, http.StatusOK, performCatalogRequest(router, "/v1/models/gpt-4").Code)
}

func relayModelCatalogRouter(token *model.Token) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		requestctx.SetUserId(c, token.UserId)
		middleware.SetupRelayTokenContext(c, token)
		c.Set(requestctx.ContextKeyGroup, userssvc.GroupDefault)
		c.Next()
	})
	router.GET("/v1/models", RelayListModels)
	router.GET("/v1/models/:model", RelayRetrieveModel)
	router.GET("/v1beta/models", RelayListModelsGemini)
	return router
}

func performCatalogRequest(router http.Handler, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
