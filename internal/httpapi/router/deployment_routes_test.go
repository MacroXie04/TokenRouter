package router

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type deploymentRouteExpectation struct {
	method  string
	path    string
	handler string
}

var deploymentRouteExpectations = []deploymentRouteExpectation{
	{http.MethodGet, "/api/deployments/settings", "GetModelDeploymentSettings"},
	{http.MethodPost, "/api/deployments/settings/test-connection", "TestIoNetConnection"},
	{http.MethodGet, "/api/deployments/", "GetAllDeployments"},
	{http.MethodGet, "/api/deployments/search", "SearchDeployments"},
	{http.MethodPost, "/api/deployments/test-connection", "TestIoNetConnection"},
	{http.MethodGet, "/api/deployments/hardware-types", "GetHardwareTypes"},
	{http.MethodGet, "/api/deployments/locations", "GetLocations"},
	{http.MethodGet, "/api/deployments/available-replicas", "GetAvailableReplicas"},
	{http.MethodPost, "/api/deployments/price-estimation", "GetPriceEstimation"},
	{http.MethodGet, "/api/deployments/check-name", "CheckClusterNameAvailability"},
	{http.MethodPost, "/api/deployments/", "CreateDeployment"},
	{http.MethodGet, "/api/deployments/:id", "GetDeployment"},
	{http.MethodGet, "/api/deployments/:id/logs", "GetDeploymentLogs"},
	{http.MethodGet, "/api/deployments/:id/containers", "ListDeploymentContainers"},
	{http.MethodGet, "/api/deployments/:id/containers/:container_id", "GetContainerDetails"},
	{http.MethodPut, "/api/deployments/:id", "UpdateDeployment"},
	{http.MethodPut, "/api/deployments/:id/name", "UpdateDeploymentName"},
	{http.MethodPost, "/api/deployments/:id/extend", "ExtendDeployment"},
	{http.MethodDelete, "/api/deployments/:id", "DeleteDeployment"},
}

func TestDeploymentRouteFamilyIsExactAndAdminAuthenticated(t *testing.T) {
	t.Setenv("GLOBAL_API_RATE_LIMIT", "1000000")
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "deployment-routes.db")+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.CasbinRule{}, &model.Option{}))
	model.DB = db
	require.NoError(t, auth.InitCasbin())
	require.NoError(t, setting.Init())

	router := SetUpRouter()
	registered := map[string]string{}
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = route.Handler
	}
	for _, expected := range deploymentRouteExpectations {
		handler, ok := registered[expected.method+" "+expected.path]
		require.True(t, ok, "missing route %s %s", expected.method, expected.path)
		assert.True(t, strings.HasSuffix(handler, "."+expected.handler), handler)

		requestPath := strings.ReplaceAll(strings.ReplaceAll(expected.path, ":id", "dep-1"), ":container_id", "ctr-1")
		request := httptest.NewRequest(expected.method, requestPath, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, "%s %s must inherit AdminAuth", expected.method, expected.path)
	}

	accessForRole := func(username string, role int) string {
		user := model.User{Username: username, Password: "pw", Role: role, Status: model.UserStatusEnabled, AuthVersion: 1}
		require.NoError(t, db.Create(&user).Error)
		_, access, _, err := auth.CompleteLogin(&user, "127.0.0.1", "ua", "test")
		require.NoError(t, err)
		return access
	}
	requestRoute := func(access string, expected deploymentRouteExpectation) *httptest.ResponseRecorder {
		requestPath := strings.ReplaceAll(strings.ReplaceAll(expected.path, ":id", "dep-1"), ":container_id", "ctr-1")
		request := httptest.NewRequest(expected.method, requestPath, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: "access_token", Value: access})
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	commonAccess := accessForRole("deployment-common", roles.RoleCommonUser)
	adminAccess := accessForRole("deployment-admin", roles.RoleAdminUser)
	for _, expected := range deploymentRouteExpectations {
		commonResponse := requestRoute(commonAccess, expected)
		assert.Equal(t, http.StatusForbidden, commonResponse.Code, "%s %s must reject common users", expected.method, expected.path)
		adminResponse := requestRoute(adminAccess, expected)
		assert.Equal(t, http.StatusOK, adminResponse.Code, "%s %s must admit admins: %s", expected.method, expected.path, adminResponse.Body.String())
	}
}
