package router

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func setupStatusTest(t *testing.T, role int) (http.Handler, func(method, path string) *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "status.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Token{}, &model.UserSession{}, &model.CasbinRule{},
		&model.Channel{}, &model.Ability{}, &model.Model{}, &model.Vendor{},
	))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, auth.InitCasbin())
	require.NoError(t, channelssvc.InitAbilityCache())

	user := model.User{Username: "statustest", Password: "pw", Role: role, Status: model.UserStatusEnabled, Quota: 1000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	_, access, refresh, err := auth.CompleteLogin(&user, "127.0.0.1", "ua", "test")
	require.NoError(t, err)

	r := SetUpRouter()
	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "s." + refresh})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	return r, do
}

// TestStatusTestEndpoint covers GET /api/status/test: DB ping success under
// an admin session and a 401 for unauthenticated clients.
func TestStatusTestEndpoint(t *testing.T) {
	_, do := setupStatusTest(t, roles.RoleAdminUser)

	rec := do(http.MethodGet, "/api/status/test")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, true, out["success"])
	assert.Equal(t, "Server is running", out["message"])

	// Without a session the route is guarded.
	r := SetUpRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/status/test", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)
}

// TestSubscriptionSelfRoute covers the reference path GET /api/subscription/self:
// 200 for a signed-in user, 401 anonymously. The former TokenRouter-only alias
// /api/user/subscription is gone.
func TestSubscriptionSelfRoute(t *testing.T) {
	r, do := setupStatusTest(t, roles.RoleCommonUser)

	rec := do(http.MethodGet, "/api/subscription/self")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	req := httptest.NewRequest(http.MethodGet, "/api/subscription/self", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)

	// The removed alias no longer serves subscription data: the path now
	// resolves to the admin GET /api/user/:id route (id="subscription"),
	// which rejects a non-admin with 403 — the reference routes identically.
	rec3 := do(http.MethodGet, "/api/user/subscription")
	assert.Equal(t, http.StatusForbidden, rec3.Code, "removed alias must not serve the user")
}

func TestPublicStatusAndContentRoutes(t *testing.T) {
	handler, _ := setupStatusTest(t, roles.RoleCommonUser)

	for _, path := range []string{
		"/api/notice",
		"/api/user-agreement",
		"/api/privacy-policy",
		"/api/about",
		"/api/home_page_content",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "%s body: %s", path, rec.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), path)
		assert.Equal(t, true, body["success"], path)
		_, hasData := body["data"]
		assert.True(t, hasData, path)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/uptime/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var uptime map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &uptime))
	assert.Equal(t, true, uptime["success"])
	assert.Equal(t, "", uptime["message"])
	assert.Empty(t, uptime["data"])
}

func TestPricingAdvertisesStableEndpointContracts(t *testing.T) {
	handler, do := setupStatusTest(t, roles.RoleCommonUser)
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	previousSpecialRatios := billingsvc.ExportedGroupGroupRatios()
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
		billingsvc.SetGroupGroupRatios(previousSpecialRatios)
	})
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"gpt-image-1": {Prompt: 5, Completion: 10},
	})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	billingsvc.SetGroupGroupRatios(map[string]map[string]float64{})
	vendor := model.Vendor{Name: "OpenAI", Status: 1}
	require.NoError(t, model.DB.Create(&vendor).Error)
	metadata := model.Model{ModelName: "gpt-image-1", VendorID: vendor.Id, Status: 1}
	require.NoError(t, model.DB.Create(&metadata).Error)
	channel := model.Channel{
		Type: int(channelcatalog.ChannelTypeOpenAI), Key: "provider-key", Name: "provider",
		Status: channelcatalog.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "gpt-image-1", ChannelId: channel.Id, Enabled: true,
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())

	req := httptest.NewRequest(http.MethodGet, "/api/pricing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Success bool `json:"success"`
		Data    []struct {
			ModelName              string   `json:"model_name"`
			QuotaType              int      `json:"quota_type"`
			ModelRatio             float64  `json:"model_ratio"`
			PromptPrice            float64  `json:"prompt_price"`
			CompletionPrice        float64  `json:"completion_price"`
			SupportedEndpointTypes []string `json:"supported_endpoint_types"`
		} `json:"data"`
		PricingVersion    string             `json:"pricing_version"`
		GroupRatio        map[string]float64 `json:"group_ratio"`
		SupportedEndpoint map[string]struct {
			Path   string `json:"path"`
			Method string `json:"method"`
		} `json:"supported_endpoint"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.True(t, body.Success)
	require.Len(t, body.Data, 1)
	assert.Equal(t, "gpt-image-1", body.Data[0].ModelName)
	assert.Equal(t, 0, body.Data[0].QuotaType)
	assert.Equal(t, 2.5, body.Data[0].ModelRatio)
	assert.Equal(t, 5.0, body.Data[0].PromptPrice)
	assert.Equal(t, 10.0, body.Data[0].CompletionPrice)
	assert.Equal(t, []string{"image-generation", "openai"}, body.Data[0].SupportedEndpointTypes)
	assert.Len(t, body.PricingVersion, 64)
	assert.Equal(t, 1.0, body.GroupRatio["default"])
	assert.Equal(t, "/v1/chat/completions", body.SupportedEndpoint["openai"].Path)
	assert.Equal(t, "POST", body.SupportedEndpoint["openai"].Method)
	assert.Equal(t, "/v1/images/generations", body.SupportedEndpoint["image-generation"].Path)
	assert.NotContains(t, body.SupportedEndpoint, "openai-response-compact", "unused endpoint types must not be advertised")
	assert.NotContains(t, body.SupportedEndpoint, "embeddings", "unused endpoint types must not be advertised")

	billingsvc.SetGroupGroupRatios(map[string]map[string]float64{"default": {"default": 0.25}})
	signed := do(http.MethodGet, "/api/pricing")
	require.Equal(t, http.StatusOK, signed.Code, signed.Body.String())
	var signedBody struct {
		GroupRatio     map[string]float64 `json:"group_ratio"`
		PricingVersion string             `json:"pricing_version"`
	}
	require.NoError(t, json.Unmarshal(signed.Body.Bytes(), &signedBody))
	assert.Equal(t, 0.25, signedBody.GroupRatio["default"])
	assert.NotEqual(t, body.PricingVersion, signedBody.PricingVersion)
}
