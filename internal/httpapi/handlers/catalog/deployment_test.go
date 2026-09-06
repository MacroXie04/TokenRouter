package catalog

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/catalog/ionet"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeDeploymentAPI struct {
	calls   []string
	failure error
	now     time.Time
}

func (fake *fakeDeploymentAPI) record(name string) error {
	fake.calls = append(fake.calls, name)
	return fake.failure
}

func (fake *fakeDeploymentAPI) GetMaxGPUsPerContainer(context.Context) (*ionet.MaxGPUResponse, error) {
	if err := fake.record("max-gpus"); err != nil {
		return nil, err
	}
	return &ionet.MaxGPUResponse{Hardware: []ionet.MaxGPUInfo{{HardwareID: 7, HardwareName: "H100", BrandName: "NVIDIA", MaxGPUsPerContainer: 8, Available: 3}}}, nil
}

func (fake *fakeDeploymentAPI) ListDeployments(_ context.Context, options *ionet.ListDeploymentsOptions) (*ionet.DeploymentList, error) {
	if err := fake.record("list:" + options.Status); err != nil {
		return nil, err
	}
	return &ionet.DeploymentList{Total: 2, Deployments: []ionet.Deployment{
		{ID: "dep-1", Name: "alpha", Status: "RUNNING", HardwareQuantity: 2, BrandName: "NVIDIA", HardwareName: "H100", CompletedPercent: 25, ComputeMinutesRemaining: 70, CreatedAt: fake.now},
		{ID: "dep-2", Name: "beta", Status: "COMPLETED", HardwareQuantity: 1, BrandName: "NVIDIA", HardwareName: "A100", CompletedPercent: 100, CreatedAt: fake.now},
	}}, nil
}

func (fake *fakeDeploymentAPI) GetDeployment(context.Context, string) (*ionet.DeploymentDetail, error) {
	if err := fake.record("get"); err != nil {
		return nil, err
	}
	return fake.detail(), nil
}

func (fake *fakeDeploymentAPI) UpdateDeployment(context.Context, string, *ionet.UpdateDeploymentRequest) (*ionet.UpdateDeploymentResponse, error) {
	if err := fake.record("update"); err != nil {
		return nil, err
	}
	return &ionet.UpdateDeploymentResponse{Status: "UPDATE REQUESTED", DeploymentID: "dep-1"}, nil
}

func (fake *fakeDeploymentAPI) ExtendDeployment(context.Context, string, *ionet.ExtendDurationRequest) (*ionet.DeploymentDetail, error) {
	if err := fake.record("extend"); err != nil {
		return nil, err
	}
	return fake.detail(), nil
}

func (fake *fakeDeploymentAPI) DeleteDeployment(context.Context, string) (*ionet.UpdateDeploymentResponse, error) {
	if err := fake.record("delete"); err != nil {
		return nil, err
	}
	return &ionet.UpdateDeploymentResponse{Status: "TERMINATION REQUESTED", DeploymentID: "dep-1"}, nil
}

func (fake *fakeDeploymentAPI) DeployContainer(context.Context, *ionet.DeploymentRequest) (*ionet.DeploymentResponse, error) {
	if err := fake.record("create"); err != nil {
		return nil, err
	}
	return &ionet.DeploymentResponse{Status: "DEPLOYMENT REQUESTED", DeploymentID: "dep-1"}, nil
}

func (fake *fakeDeploymentAPI) ListHardwareTypes(context.Context) ([]ionet.HardwareType, int, error) {
	if err := fake.record("hardware"); err != nil {
		return nil, 0, err
	}
	return []ionet.HardwareType{{ID: 7, Name: "H100", MaxGPUs: 8, Available: true, AvailableCount: 3}}, 3, nil
}

func (fake *fakeDeploymentAPI) ListLocations(context.Context) (*ionet.LocationsResponse, error) {
	if err := fake.record("locations"); err != nil {
		return nil, err
	}
	return &ionet.LocationsResponse{Locations: []ionet.Location{{ID: 9, Name: "Frankfurt", ISO2: "DE"}}, Total: 1}, nil
}

func (fake *fakeDeploymentAPI) GetAvailableReplicas(context.Context, int, int) (*ionet.AvailableReplicasResponse, error) {
	if err := fake.record("replicas"); err != nil {
		return nil, err
	}
	return &ionet.AvailableReplicasResponse{Replicas: []ionet.AvailableReplica{{LocationID: 9, LocationName: "Frankfurt", HardwareID: 7, AvailableCount: 3, MaxGPUs: 2}}}, nil
}

func (fake *fakeDeploymentAPI) GetPriceEstimation(context.Context, *ionet.PriceEstimationRequest) (*ionet.PriceEstimationResponse, error) {
	if err := fake.record("price"); err != nil {
		return nil, err
	}
	return &ionet.PriceEstimationResponse{EstimatedCost: 12, Currency: "USDC", EstimationValid: true, PriceBreakdown: ionet.PriceBreakdown{ComputeCost: 12, TotalCost: 12, HourlyRate: 1}}, nil
}

func (fake *fakeDeploymentAPI) CheckClusterNameAvailability(context.Context, string) (bool, error) {
	if err := fake.record("check-name"); err != nil {
		return false, err
	}
	return true, nil
}

func (fake *fakeDeploymentAPI) UpdateClusterName(context.Context, string, *ionet.UpdateClusterNameRequest) (*ionet.UpdateClusterNameResponse, error) {
	if err := fake.record("rename"); err != nil {
		return nil, err
	}
	return &ionet.UpdateClusterNameResponse{Status: "UPDATED", Message: "renamed"}, nil
}

func (fake *fakeDeploymentAPI) GetContainerLogsRaw(context.Context, string, string, *ionet.GetLogsOptions) (string, error) {
	if err := fake.record("logs"); err != nil {
		return "", err
	}
	return "line one\nline two", nil
}

func (fake *fakeDeploymentAPI) ListContainers(context.Context, string) (*ionet.ContainerList, error) {
	if err := fake.record("containers"); err != nil {
		return nil, err
	}
	return &ionet.ContainerList{Total: 1, Workers: []ionet.Container{fake.container()}}, nil
}

func (fake *fakeDeploymentAPI) GetContainerDetails(context.Context, string, string) (*ionet.Container, error) {
	if err := fake.record("container"); err != nil {
		return nil, err
	}
	container := fake.container()
	return &container, nil
}

func (fake *fakeDeploymentAPI) detail() *ionet.DeploymentDetail {
	return &ionet.DeploymentDetail{
		ID: "dep-1", Status: "RUNNING", CreatedAt: fake.now, AmountPaid: 1, CompletedPercent: 25,
		TotalGPUs: 2, GPUsPerContainer: 1, TotalContainers: 2, HardwareName: "H100", HardwareID: 7,
		BrandName: "NVIDIA", ComputeMinutesRemaining: 70,
	}
}

func (fake *fakeDeploymentAPI) container() ionet.Container {
	return ionet.Container{
		DeviceID: "device-1", ContainerID: "ctr-1", Hardware: "H100", BrandName: "NVIDIA",
		CreatedAt: fake.now, UptimePercent: 99, GPUsPerContainer: 1, Status: "RUNNING",
		PublicURL: "https://container.example", ContainerEvents: []ionet.ContainerEvent{{Time: fake.now, Message: "started"}},
	}
}

func setupDeploymentControllerTest(t *testing.T) (*gin.Engine, *fakeDeploymentAPI, *[]bool, *[]string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "deployment.db")+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	model.DB = db
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelDeploymentIONetEnabledOption: "true",
		setting.ModelDeploymentIONetAPIKeyOption:  "stored-secret",
	}))
	fake := &fakeDeploymentAPI{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	enterpriseCalls := &[]bool{}
	keys := &[]string{}
	previousFactory := deploymentClientFactory
	deploymentClientFactory = func(apiKey string, enterprise bool) deploymentAPI {
		*enterpriseCalls = append(*enterpriseCalls, enterprise)
		*keys = append(*keys, apiKey)
		return fake
	}
	t.Cleanup(func() { deploymentClientFactory = previousFactory })
	router := gin.New()
	registerDeploymentTestRoutes(router.Group("/api/deployments"))
	return router, fake, enterpriseCalls, keys
}

func registerDeploymentTestRoutes(group *gin.RouterGroup) {
	group.GET("/settings", GetModelDeploymentSettings)
	group.POST("/settings/test-connection", TestIoNetConnection)
	group.GET("/", GetAllDeployments)
	group.GET("/search", SearchDeployments)
	group.POST("/test-connection", TestIoNetConnection)
	group.GET("/hardware-types", GetHardwareTypes)
	group.GET("/locations", GetLocations)
	group.GET("/available-replicas", GetAvailableReplicas)
	group.POST("/price-estimation", GetPriceEstimation)
	group.GET("/check-name", CheckClusterNameAvailability)
	group.POST("/", CreateDeployment)
	group.GET("/:id", GetDeployment)
	group.GET("/:id/logs", GetDeploymentLogs)
	group.GET("/:id/containers", ListDeploymentContainers)
	group.GET("/:id/containers/:container_id", GetContainerDetails)
	group.PUT("/:id", UpdateDeployment)
	group.PUT("/:id/name", UpdateDeploymentName)
	group.POST("/:id/extend", ExtendDeployment)
	group.DELETE("/:id", DeleteDeployment)
}

func performDeploymentRequest(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestDeploymentControllersCoverEveryReferenceRoute(t *testing.T) {
	router, fake, enterpriseCalls, keys := setupDeploymentControllerTest(t)
	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/deployments/settings", ""},
		{http.MethodPost, "/api/deployments/settings/test-connection", `{"api_key":"transient-secret"}`},
		{http.MethodGet, "/api/deployments/?p=2&page_size=25&status=RUNNING", ""},
		{http.MethodGet, "/api/deployments/search?keyword=alp&status=RUNNING", ""},
		{http.MethodPost, "/api/deployments/test-connection", ""},
		{http.MethodGet, "/api/deployments/hardware-types", ""},
		{http.MethodGet, "/api/deployments/locations", ""},
		{http.MethodGet, "/api/deployments/available-replicas?hardware_id=7&gpu_count=2", ""},
		{http.MethodPost, "/api/deployments/price-estimation", `{"location_ids":[9],"hardware_id":7,"gpus_per_container":1,"duration_hours":12,"replica_count":1,"currency":"usdc"}`},
		{http.MethodGet, "/api/deployments/check-name?name=alpha", ""},
		{http.MethodPost, "/api/deployments/", `{"resource_private_name":"alpha","duration_hours":24,"gpus_per_container":1,"hardware_id":7,"location_ids":[9],"container_config":{"replica_count":1,"secret_env_variables":{"TOKEN":"body-secret"}},"registry_config":{"image_url":"repo/image:v1","registry_secret":"registry-secret"}}`},
		{http.MethodGet, "/api/deployments/dep-1", ""},
		{http.MethodGet, "/api/deployments/dep-1/logs?container_id=ctr-1&level=info&stream=stdout&limit=1000&start_time=2026-01-02T03%3A04%3A05Z&end_time=2026-01-02T04%3A04%3A05Z", ""},
		{http.MethodGet, "/api/deployments/dep-1/containers", ""},
		{http.MethodGet, "/api/deployments/dep-1/containers/ctr-1", ""},
		{http.MethodPut, "/api/deployments/dep-1", `{"image_url":"repo/image:v2"}`},
		{http.MethodPut, "/api/deployments/dep-1/name", `{"name":"alpha two"}`},
		{http.MethodPost, "/api/deployments/dep-1/extend", `{"duration_hours":24}`},
		{http.MethodDelete, "/api/deployments/dep-1", ""},
	}
	for _, route := range routes {
		recorder := performDeploymentRequest(router, route.method, route.path, route.body)
		require.Equal(t, http.StatusOK, recorder.Code, "%s %s: %s", route.method, route.path, recorder.Body.String())
		var response map[string]any
		require.NoError(t, jsonutil.Unmarshal(recorder.Body.Bytes(), &response))
		assert.Equal(t, true, response["success"], "%s %s: %s", route.method, route.path, recorder.Body.String())
		assert.Equal(t, "", response["message"])
		assert.Contains(t, response, "data")
		assert.NotContains(t, recorder.Body.String(), "stored-secret")
		assert.NotContains(t, recorder.Body.String(), "transient-secret")
		assert.NotContains(t, recorder.Body.String(), "body-secret")
		assert.NotContains(t, recorder.Body.String(), "registry-secret")
	}
	assert.Contains(t, fake.calls, "list:running")
	assert.Contains(t, fake.calls, "create")
	assert.Contains(t, fake.calls, "rename")
	assert.Equal(t, 2, countBool(*enterpriseCalls, false), "only locations and logs use the public API")
	assert.Contains(t, *keys, "transient-secret")
	for _, key := range *keys {
		assert.Contains(t, []string{"stored-secret", "transient-secret"}, key)
	}
}

func TestDeploymentControllersFailClosedOnSettingsInputsAndProviderErrors(t *testing.T) {
	router, fake, _, _ := setupDeploymentControllerTest(t)
	require.NoError(t, setting.UpdateOption(setting.ModelDeploymentIONetEnabledOption, "false"))
	recorder := performDeploymentRequest(router, http.MethodGet, "/api/deployments/", "")
	assert.False(t, deploymentResponseSuccess(t, recorder))
	assert.NotContains(t, recorder.Body.String(), "stored-secret")

	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelDeploymentIONetEnabledOption: "true",
		setting.ModelDeploymentIONetAPIKeyOption:  "",
	}))
	recorder = performDeploymentRequest(router, http.MethodGet, "/api/deployments/hardware-types", "")
	assert.False(t, deploymentResponseSuccess(t, recorder))
	recorder = performDeploymentRequest(router, http.MethodPost, "/api/deployments/test-connection", `{}`)
	assert.False(t, deploymentResponseSuccess(t, recorder))

	require.NoError(t, setting.UpdateOption(setting.ModelDeploymentIONetAPIKeyOption, "bad\nkey"))
	recorder = performDeploymentRequest(router, http.MethodGet, "/api/deployments/settings", "")
	assert.True(t, deploymentResponseSuccess(t, recorder))
	assert.Contains(t, recorder.Body.String(), `"configured":false`)
	assert.NotContains(t, recorder.Body.String(), "bad\\nkey")

	require.NoError(t, setting.UpdateOption(setting.ModelDeploymentIONetAPIKeyOption, "stored-secret"))
	invalid := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/deployments/?p=1000001", ""},
		{http.MethodGet, "/api/deployments/?status=unknown", ""},
		{http.MethodGet, "/api/deployments/?status=a&status=b", ""},
		{http.MethodGet, "/api/deployments/?status=running;ignored=true", ""},
		{http.MethodGet, "/api/deployments/available-replicas?hardware_id=7&gpu_count=1025", ""},
		{http.MethodGet, "/api/deployments/check-name?name=%0Abad", ""},
		{http.MethodGet, "/api/deployments/dep-1/logs?container_id=ctr-1&limit=1001", ""},
		{http.MethodGet, "/api/deployments/dep-1/logs?container_id=ctr-1&follow=1", ""},
		{http.MethodGet, "/api/deployments/dep-1/logs?container_id=ctr-1&start_time=bad", ""},
		{http.MethodPost, "/api/deployments/", `{"resource_private_name":"a","resource_private_name":"b"}`},
		{http.MethodPost, "/api/deployments/", `{"resource_private_name":"a","unknown":true}`},
		{http.MethodPost, "/api/deployments/", `null`},
		{http.MethodPut, "/api/deployments/dep-1", `{}`},
		{http.MethodPost, "/api/deployments/dep-1/extend", `{"duration_hours":999999}`},
		{http.MethodGet, "/api/deployments/?ignored=" + strings.Repeat("x", maxDeploymentQueryBytes), ""},
	}
	for _, test := range invalid {
		recorder = performDeploymentRequest(router, test.method, test.path, test.body)
		assert.False(t, deploymentResponseSuccess(t, recorder), "%s %s: %s", test.method, test.path, recorder.Body.String())
		assert.NotContains(t, recorder.Body.String(), "stored-secret")
	}

	oversized := strings.Repeat("x", int(ionet.MaxRequestBodyBytes)+1)
	recorder = performDeploymentRequest(router, http.MethodPost, "/api/deployments/", oversized)
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	assert.False(t, deploymentResponseSuccess(t, recorder))

	fake.failure = errors.New("stored-secret provider database.internal failed")
	recorder = performDeploymentRequest(router, http.MethodGet, "/api/deployments/hardware-types", "")
	assert.False(t, deploymentResponseSuccess(t, recorder))
	var failureBody map[string]any
	require.NoError(t, jsonutil.Unmarshal(recorder.Body.Bytes(), &failureBody))
	assert.Equal(t, "io.net request failed", failureBody["message"])
	recorder = performDeploymentRequest(router, http.MethodPost, "/api/deployments/test-connection", `{"api_key":"transient-secret"}`)
	assert.False(t, deploymentResponseSuccess(t, recorder))
	assert.NotContains(t, recorder.Body.String(), "stored-secret")
	assert.NotContains(t, recorder.Body.String(), "transient-secret")
	assert.NotContains(t, recorder.Body.String(), "database.internal")
}

func deploymentResponseSuccess(t *testing.T, recorder *httptest.ResponseRecorder) bool {
	t.Helper()
	var response struct {
		Success bool `json:"success"`
	}
	require.NoError(t, jsonutil.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	return response.Success
}

func countBool(values []bool, expected bool) int {
	count := 0
	for _, value := range values {
		if value == expected {
			count++
		}
	}
	return count
}
