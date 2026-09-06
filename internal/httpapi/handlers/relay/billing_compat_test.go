package relay

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func setupBillingCompatTest(t *testing.T) (*gin.Engine, *model.User, *model.Token) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "billing-compat.db")+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init())
	user := &model.User{Username: "billing-client", Password: "password", Status: model.UserStatusEnabled,
		Quota: 2 * quotamath.QuotaPerUnit, UsedQuota: quotamath.QuotaPerUnit / 2, Role: roles.RoleCommonUser}
	require.NoError(t, db.Create(user).Error)
	token := &model.Token{UserId: user.Id, Key: "sk-dashboard-billing", Name: "billing", Status: 1,
		RemainQuota: quotamath.QuotaPerUnit, UsedQuota: quotamath.QuotaPerUnit / 2, ExpiredTime: 4102444800}
	require.NoError(t, db.Create(token).Error)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("")
	group.Use(middleware.TokenAuth())
	group.GET("/dashboard/billing/subscription", GetDashboardSubscription)
	group.GET("/v1/dashboard/billing/subscription", GetDashboardSubscription)
	group.GET("/dashboard/billing/usage", GetDashboardUsage)
	group.GET("/v1/dashboard/billing/usage", GetDashboardUsage)
	return router, user, token
}

func billingCompatRequest(router http.Handler, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer sk-dashboard-billing")
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestDashboardBillingAliasesUseAuthenticatedTokenStats(t *testing.T) {
	router, _, _ := setupBillingCompatTest(t)
	for _, path := range []string{
		"/dashboard/billing/subscription",
		"/v1/dashboard/billing/subscription",
	} {
		recorder := billingCompatRequest(router, path)
		require.Equal(t, http.StatusOK, recorder.Code, path)
		var payload openAISubscriptionResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		assert.Equal(t, "billing_subscription", payload.Object)
		assert.True(t, payload.HasPaymentMethod)
		assert.Equal(t, 1.5, payload.HardLimitUSD)
		assert.Equal(t, payload.HardLimitUSD, payload.SoftLimitUSD)
		assert.Equal(t, int64(4102444800), payload.AccessUntil)
	}
	for _, path := range []string{"/dashboard/billing/usage", "/v1/dashboard/billing/usage"} {
		recorder := billingCompatRequest(router, path+"?start_date=2026-01-01&end_date=2026-01-31")
		require.Equal(t, http.StatusOK, recorder.Code, path)
		var payload openAIUsageResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		assert.Equal(t, "list", payload.Object)
		assert.Equal(t, 50.0, payload.TotalUsage)
	}
}

func TestDashboardBillingSupportsUserAndTokenDisplayModes(t *testing.T) {
	router, user, token := setupBillingCompatTest(t)
	require.NoError(t, setting.UpdateOption(displayTokenStatOption, "false"))
	recorder := billingCompatRequest(router, "/dashboard/billing/subscription")
	require.Equal(t, http.StatusOK, recorder.Code)
	var subscription openAISubscriptionResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &subscription))
	assert.Equal(t, 2.5, subscription.HardLimitUSD)
	assert.Zero(t, subscription.AccessUntil)

	require.NoError(t, setting.UpdateOption(displayTokenStatOption, "true"))
	require.NoError(t, setting.UpdateOption(setting.QuotaDisplayTypeOption, "TOKENS"))
	token.UnlimitedQuota = true
	require.NoError(t, model.DB.Model(token).Update("unlimited_quota", true).Error)
	recorder = billingCompatRequest(router, "/dashboard/billing/subscription")
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &subscription))
	assert.Equal(t, 100000000.0, subscription.HardLimitUSD)

	user.UsedQuota = quotamath.QuotaPerUnit
	require.NoError(t, model.DB.Model(user).Update("used_quota", user.UsedQuota).Error)
	require.NoError(t, setting.UpdateOption(displayTokenStatOption, "false"))
	recorder = billingCompatRequest(router, "/dashboard/billing/usage")
	var usage openAIUsageResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &usage))
	assert.Equal(t, float64(quotamath.QuotaPerUnit*100), usage.TotalUsage)
}

func TestDashboardBillingUsesValidatedCustomCurrency(t *testing.T) {
	router, _, _ := setupBillingCompatTest(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.QuotaDisplayTypeOption:           "CUSTOM",
		setting.CustomCurrencySymbolOption:       "HK$",
		setting.CustomCurrencyExchangeRateOption: "7.8",
	}))
	recorder := billingCompatRequest(router, "/dashboard/billing/subscription")
	require.Equal(t, http.StatusOK, recorder.Code)
	var subscription openAISubscriptionResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &subscription))
	assert.Equal(t, 1.5*7.8, subscription.HardLimitUSD)

	// A direct malformed database/cache write cannot silently produce NaN,
	// infinity, or a guessed financial conversion.
	require.NoError(t, setting.UpdateOption(setting.CustomCurrencyExchangeRateOption, "0"))
	recorder = billingCompatRequest(router, "/dashboard/billing/subscription")
	var payload struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.Equal(t, "upstream_error", payload.Error.Type)
}

func TestDashboardBillingReturnsCompatibleErrorEnvelope(t *testing.T) {
	router, _, token := setupBillingCompatTest(t)
	require.NoError(t, model.DB.Model(token).Update("used_quota", -1).Error)
	recorder := billingCompatRequest(router, "/dashboard/billing/usage")
	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.Equal(t, "new_api_error", payload.Error.Type)
	assert.Contains(t, payload.Error.Message, "negative quota")
}
