package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func setupDashboardBillingRoutes(t *testing.T) (*model.Token, http.Handler) {
	t.Helper()
	t.Setenv("GLOBAL_API_RATE_LIMIT", "1000000")
	t.Setenv("GLOBAL_API_RATE_LIMIT_DURATION", "60")

	dsn := "file:" + filepath.Join(t.TempDir(), "dashboard-billing-routes.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init())

	user := model.User{
		Username: "dashboard-route-user",
		Password: "not-used",
		Status:   model.UserStatusEnabled,
		Role:     common.RoleCommonUser,
		Group:    service.GroupDefault,
		Quota:    2 * common.QuotaPerUnit,
	}
	require.NoError(t, db.Create(&user).Error)
	token := &model.Token{
		UserId:      user.Id,
		Key:         "sk-dashboard-route",
		Name:        "dashboard-route",
		Status:      1,
		Group:       service.GroupDefault,
		RemainQuota: common.QuotaPerUnit,
		UsedQuota:   common.QuotaPerUnit / 2,
		ExpiredTime: 4102444800,
	}
	require.NoError(t, db.Create(token).Error)
	return token, SetUpRouter()
}

func TestDashboardBillingReferenceRoutesUseRelayAuthenticationAndCORS(t *testing.T) {
	_, handler := setupDashboardBillingRoutes(t)

	tests := []struct {
		method      string
		path        string
		object      string
		amountField string
		expected    float64
	}{
		{method: http.MethodGet, path: "/dashboard/billing/subscription", object: "billing_subscription", amountField: "hard_limit_usd", expected: 1.5},
		{method: http.MethodGet, path: "/v1/dashboard/billing/subscription", object: "billing_subscription", amountField: "hard_limit_usd", expected: 1.5},
		{method: http.MethodGet, path: "/dashboard/billing/usage", object: "list", amountField: "total_usage", expected: 50},
		{method: http.MethodGet, path: "/v1/dashboard/billing/usage", object: "list", amountField: "total_usage", expected: 50},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path+"?start_date=2026-01-01&end_date=2026-01-31", nil)
			request.Header.Set("Authorization", "Bearer sk-dashboard-route")
			request.Header.Set("Origin", "https://client.example.test")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, "*", recorder.Header().Get("Access-Control-Allow-Origin"))
			var body map[string]any
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			assert.Equal(t, test.object, body["object"])
			assert.Equal(t, test.expected, body[test.amountField])
		})
	}

	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, test.path)
		assert.Contains(t, recorder.Body.String(), "invalid_api_key", test.path)
	}
}
