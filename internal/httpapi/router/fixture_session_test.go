package router_test

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setupAuthAdjacent(t *testing.T, role int) (http.Handler, func(method, path, body string, headers ...map[string]string) *httptest.ResponseRecorder, int, string) {
	t.Helper()
	previousMode := gin.Mode()
	t.Cleanup(func() { gin.SetMode(previousMode) })
	gin.SetMode(gin.TestMode)
	t.Setenv("CRITICAL_RATE_LIMIT", "1000")
	newRouterTestDatabase(t, "authadj.db", &model.User{}, &model.Token{}, &model.UserSession{},
		&model.Channel{}, &model.TwoFA{}, &model.TwoFABackupCode{}, &model.PasskeyCredential{},
		&model.AuthFlow{}, &model.ExternalIdentityClaim{}, &model.Log{}, &model.Option{}, &model.CasbinRule{})
	require.NoError(t, auth.InitCasbin())
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOption(setting.QuotaPerUnitOption, "500000"))
	previousRatios := billingsvc.ExportedGroupRatios()
	t.Cleanup(func() { billingsvc.SetGroupRatios(previousRatios) })
	billingsvc.SetGroupRatios(map[string]float64{"default": 1, "vip": 1})

	user := model.User{Username: "adjuser", Password: "pw", Role: role, Status: model.UserStatusEnabled,
		Quota: 1000, Group: userssvc.GroupDefault, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	sid, access, refresh, err := auth.CompleteLogin(&user, "127.0.0.1", "ua", "test")
	require.NoError(t, err)

	r := router.SetUpRouter()
	do := func(method, path, body string, headers ...map[string]string) *httptest.ResponseRecorder {
		return performSessionRequest(r, sid, access, refresh, method, path, body, headers...)
	}
	return r, do, user.Id, sid
}

// setupDashboardSession builds a session for the assembled dashboard surface,
// including channels, catalog, billing, users, operations and their shared stores.
func setupDashboardSession(t *testing.T, role int) (http.Handler, func(method, path, body string) *httptest.ResponseRecorder, int) {
	t.Helper()
	previousMode := gin.Mode()
	t.Cleanup(func() { gin.SetMode(previousMode) })
	gin.SetMode(gin.TestMode)
	t.Setenv("CRITICAL_RATE_LIMIT", "1000000")
	t.Setenv("GLOBAL_API_RATE_LIMIT", "1000000")
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	newRouterTestDatabase(t, "channelread.db", &model.User{}, &model.Token{}, &model.UserSession{},
		&model.Channel{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.AuthFlow{}, &model.Log{},
		&model.Option{}, &model.Model{}, &model.SystemTask{}, &model.SystemTaskLock{}, &model.Ability{},
		&model.Redemption{}, &model.AuthzRole{}, &model.CasbinRule{}, &model.TopUp{}, &model.QuotaData{},
		&model.CustomOAuthProvider{}, &model.UserOAuthBinding{}, &model.SystemInstance{}, &model.PerfMetric{},
		&model.PrefillGroup{}, &model.AuditLogOutbox{})
	require.NoError(t, auth.InitCasbin())
	require.NoError(t, auth.InitPermissionAuthz())
	billingsvc.ResetQuotaDataCache()
	require.NoError(t, setting.UpdateOption(setting.QuotaPerUnitOption, "500000"))

	user := model.User{Username: "chreader", Password: "pw", Role: role, Status: model.UserStatusEnabled,
		Quota: 1000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	sid, access, refresh, err := auth.CompleteLogin(&user, "127.0.0.1", "ua", "test")
	require.NoError(t, err)

	r := router.SetUpRouter()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		return performSessionRequest(r, sid, access, refresh, method, path, body)
	}
	return r, do, user.Id
}
