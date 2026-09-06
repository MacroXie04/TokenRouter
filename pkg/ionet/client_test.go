package ionet

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (function doerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

type wireExpectation struct {
	baseURL  string
	method   string
	path     string
	query    url.Values
	body     string
	response string
}

func TestClientExactWireContract(t *testing.T) {
	expectations := []wireExpectation{
		{DefaultEnterpriseBaseURL, http.MethodGet, "/enterprise/v1/io-cloud/caas/hardware/max-gpus-per-container", nil, "", `{"data":{"hardware":[{"max_gpus_per_container":8,"available":3,"hardware_id":7,"hardware_name":"H100","brand_name":"NVIDIA"}],"total":3}}`},
		{DefaultEnterpriseBaseURL, http.MethodGet, "/enterprise/v1/io-cloud/caas/deployments", url.Values{"page": {"2"}, "page_size": {"25"}, "sort_by": {"created_at"}, "sort_order": {"desc"}, "status": {"running"}}, "", `{"data":{"deployments":[{"id":"dep-1","status":"RUNNING","name":"alpha","completed_percent":10,"hardware_quantity":2,"brand_name":"NVIDIA","hardware_name":"H100","compute_minutes_served":5,"compute_minutes_remaining":55,"created_at":"2026-01-02T03:04:05"}],"total":1,"statuses":["RUNNING"]}}`},
		{DefaultEnterpriseBaseURL, http.MethodGet, "/enterprise/v1/io-cloud/caas/deployment/dep-1", nil, "", validDetailWrapper("dep-1")},
		{DefaultEnterpriseBaseURL, http.MethodPatch, "/enterprise/v1/io-cloud/caas/deployment/dep-1", nil, `{"env_variables":{"MODE":"prod"},"traffic_port":8080,"image_url":"repo/image:v2"}`, `{"status":"UPDATE REQUESTED","deployment_id":"dep-1"}`},
		{DefaultEnterpriseBaseURL, http.MethodPost, "/enterprise/v1/io-cloud/caas/deployment/dep-1/extend", nil, `{"duration_hours":24}`, validDetailWrapper("dep-1")},
		{DefaultEnterpriseBaseURL, http.MethodDelete, "/enterprise/v1/io-cloud/caas/deployment/dep-1", nil, "", `{"status":"TERMINATION REQUESTED","deployment_id":"dep-1"}`},
		{DefaultEnterpriseBaseURL, http.MethodPost, "/enterprise/v1/io-cloud/caas/deploy", nil, `{"resource_private_name":"alpha","duration_hours":24,"gpus_per_container":1,"hardware_id":7,"location_ids":[9],"container_config":{"replica_count":1,"secret_env_variables":{"TOKEN":"hidden"},"traffic_port":8080},"registry_config":{"image_url":"repo/image:v1","registry_username":"robot","registry_secret":"registry-hidden"}}`, `{"status":"DEPLOYMENT REQUESTED","deployment_id":"dep-2"}`},
		{DefaultEnterpriseBaseURL, http.MethodGet, "/enterprise/v1/io-cloud/caas/hardware/max-gpus-per-container", nil, "", `{"data":{"hardware":[{"max_gpus_per_container":8,"available":3,"hardware_id":7,"hardware_name":"H100","brand_name":"NVIDIA"}],"total":0}}`},
		{DefaultBaseURL, http.MethodGet, "/v1/io-cloud/caas/locations", nil, "", `{"data":{"locations":[{"id":9,"name":"Frankfurt","iso2":"de","available":4}],"total":4}}`},
		{DefaultEnterpriseBaseURL, http.MethodGet, "/enterprise/v1/io-cloud/caas/available-replicas", url.Values{"hardware_id": {"7"}, "hardware_qty": {"2"}}, "", `{"data":[{"id":9,"iso2":"DE","name":"Frankfurt","available_replicas":3}]}`},
		{DefaultEnterpriseBaseURL, http.MethodGet, "/enterprise/v1/io-cloud/caas/price", url.Values{"currency": {"usdc"}, "duration_qty": {"2"}, "duration_type": {"daily"}, "gpus_per_container": {"2"}, "hardware_id": {"7"}, "hardware_qty": {"2"}, "location_ids": {"[9]"}, "replica_count": {"1"}}, "", `{"data":{"ionet_fee":2,"currency_conversion_fee":1,"total_cost_usdc":27}}`},
		{DefaultEnterpriseBaseURL, http.MethodGet, "/enterprise/v1/io-cloud/caas/clusters/check_cluster_name_availability", url.Values{"cluster_name": {"alpha two"}}, "", `true`},
		{DefaultEnterpriseBaseURL, http.MethodPut, "/enterprise/v1/io-cloud/caas/clusters/dep-1/update-name", nil, `{"cluster_name":"alpha two"}`, `{"status":"UPDATED","message":"renamed"}`},
		{DefaultEnterpriseBaseURL, http.MethodGet, "/enterprise/v1/io-cloud/caas/deployment/dep-1/containers", nil, "", validContainerListWrapper()},
		{DefaultEnterpriseBaseURL, http.MethodGet, "/enterprise/v1/io-cloud/caas/deployment/dep-1/container/ctr-1", nil, "", validContainerJSON()},
		{DefaultBaseURL, http.MethodGet, "/v1/io-cloud/caas/deployment/dep-1/log/ctr-1", url.Values{"cursor": {"next"}, "end_time": {"2026-01-02T04:04:05Z"}, "follow": {"true"}, "level": {"info"}, "limit": {"250"}, "start_time": {"2026-01-02T03:04:05Z"}, "stream": {"stdout"}}, "", "line one\nline two"},
	}

	var position int
	doer := doerFunc(func(request *http.Request) (*http.Response, error) {
		require.Less(t, position, len(expectations))
		expected := expectations[position]
		position++
		assert.Equal(t, expected.method, request.Method)
		assert.True(t, strings.HasPrefix(request.URL.String(), expected.baseURL+"/"), request.URL.String())
		assert.Equal(t, expected.path, request.URL.Path)
		if expected.query == nil {
			assert.Empty(t, request.URL.Query())
		} else {
			assert.Equal(t, expected.query, request.URL.Query())
		}
		assert.Equal(t, "wire-secret", request.Header.Get("X-API-KEY"))
		assert.Empty(t, request.Header.Get("Authorization"))
		assert.NotContains(t, request.URL.String(), "wire-secret")
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		if expected.body == "" {
			assert.Empty(t, body)
		} else {
			assert.JSONEq(t, expected.body, string(body))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(expected.response)), Header: make(http.Header)}, nil
	})
	enterprise := NewEnterpriseClient("wire-secret")
	enterprise.httpClient = doer
	public := NewClient("wire-secret")
	public.httpClient = doer
	ctx := context.Background()

	maxGPU, err := enterprise.GetMaxGPUsPerContainer(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, maxGPU.Total)
	list, err := enterprise.ListDeployments(ctx, &ListDeploymentsOptions{Status: "running", Page: 2, PageSize: 25, SortBy: "created_at", SortOrder: "desc"})
	require.NoError(t, err)
	assert.Equal(t, 2, list.Deployments[0].GPUCount)
	detail, err := enterprise.GetDeployment(ctx, "dep-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1767323045), detail.CreatedAt.Unix())
	port := 8080
	_, err = enterprise.UpdateDeployment(ctx, "dep-1", &UpdateDeploymentRequest{EnvVariables: map[string]string{"MODE": "prod"}, TrafficPort: &port, ImageURL: "repo/image:v2"})
	require.NoError(t, err)
	_, err = enterprise.ExtendDeployment(ctx, "dep-1", &ExtendDurationRequest{DurationHours: 24})
	require.NoError(t, err)
	_, err = enterprise.DeleteDeployment(ctx, "dep-1")
	require.NoError(t, err)
	_, err = enterprise.DeployContainer(ctx, &DeploymentRequest{
		ResourcePrivateName: "alpha", DurationHours: 24, GPUsPerContainer: 1, HardwareID: 7, LocationIDs: []int{9},
		ContainerConfig: ContainerConfig{ReplicaCount: 1, SecretEnvVariables: map[string]string{"TOKEN": "hidden"}, TrafficPort: 8080},
		RegistryConfig:  RegistryConfig{ImageURL: "repo/image:v1", RegistryUsername: "robot", RegistrySecret: "registry-hidden"},
	})
	require.NoError(t, err)
	hardwareTypes, total, err := enterprise.ListHardwareTypes(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Equal(t, "H100", hardwareTypes[0].Name)
	locations, err := public.ListLocations(ctx)
	require.NoError(t, err)
	assert.Equal(t, "DE", locations.Locations[0].ISO2)
	_, err = enterprise.GetAvailableReplicas(ctx, 7, 2)
	require.NoError(t, err)
	price, err := enterprise.GetPriceEstimation(ctx, &PriceEstimationRequest{LocationIDs: []int{9}, HardwareID: 7, GPUsPerContainer: 2, DurationType: "day", DurationQty: 2, ReplicaCount: 1})
	require.NoError(t, err)
	assert.Equal(t, 24.0, price.PriceBreakdown.ComputeCost)
	assert.Equal(t, 27.0/48.0, price.PriceBreakdown.HourlyRate)
	available, err := enterprise.CheckClusterNameAvailability(ctx, "alpha two")
	require.NoError(t, err)
	assert.True(t, available)
	_, err = enterprise.UpdateClusterName(ctx, "dep-1", &UpdateClusterNameRequest{Name: "alpha two"})
	require.NoError(t, err)
	_, err = enterprise.ListContainers(ctx, "dep-1")
	require.NoError(t, err)
	_, err = enterprise.GetContainerDetails(ctx, "dep-1", "ctr-1")
	require.NoError(t, err)
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	end := start.Add(time.Hour)
	logs, err := public.GetContainerLogsRaw(ctx, "dep-1", "ctr-1", &GetLogsOptions{StartTime: &start, EndTime: &end, Level: "info", Stream: "stdout", Limit: 250, Cursor: "next", Follow: true})
	require.NoError(t, err)
	assert.Equal(t, "line one\nline two", logs)
	assert.Equal(t, len(expectations), position)
}

func TestClientSanitizesFailuresAndBoundsResponses(t *testing.T) {
	tests := []struct {
		name string
		doer httpDoer
	}{
		{name: "transport", doer: doerFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial https://wire-secret@private.internal failed")
		})},
		{name: "provider error", doer: doerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"detail":"wire-secret database.internal"}`))}, nil
		})},
		{name: "oversized", doer: doerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(io.LimitReader(strings.NewReader(strings.Repeat("x", int(maxUpstreamJSONBytes)+1)), maxUpstreamJSONBytes+1))}, nil
		})},
		{name: "malformed", doer: doerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":`))}, nil
		})},
		{name: "missing data", doer: doerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"hardware":[]}`))}, nil
		})},
		{name: "duplicate data", doer: doerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{},"data":{}}`))}, nil
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewEnterpriseClient("wire-secret")
			client.httpClient = test.doer
			_, err := client.GetMaxGPUsPerContainer(context.Background())
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "wire-secret")
			assert.NotContains(t, err.Error(), "private.internal")
			assert.NotContains(t, err.Error(), "database.internal")
		})
	}
}

func TestClientDoesNotFollowRedirectsWithCredential(t *testing.T) {
	var attackerRequests atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		attackerRequests.Add(1)
	}))
	defer attacker.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "wire-secret", request.Header.Get("X-API-KEY"))
		http.Redirect(writer, request, attacker.URL, http.StatusFound)
	}))
	defer provider.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := newClient("wire-secret", provider.URL, &http.Client{
		Transport: transport,
		Timeout:   time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	})
	_, err := client.GetMaxGPUsPerContainer(context.Background())
	require.Error(t, err)
	assert.Zero(t, attackerRequests.Load())
}

func TestProductionClientsHaveFixedBasesAndTimeouts(t *testing.T) {
	for _, client := range []*Client{NewClient("wire-secret"), NewEnterpriseClient("wire-secret")} {
		httpClient, ok := client.httpClient.(*http.Client)
		require.True(t, ok)
		assert.Equal(t, DefaultTimeout, httpClient.Timeout)
		assert.Positive(t, httpClient.Timeout)
		transport, ok := httpClient.Transport.(*http.Transport)
		require.True(t, ok)
		assert.Nil(t, transport.Proxy)
		assert.Equal(t, reflect.ValueOf(common.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	}
	assert.Equal(t, DefaultBaseURL, NewClient("wire-secret").baseURL)
	assert.Equal(t, DefaultEnterpriseBaseURL, NewEnterpriseClient("wire-secret").baseURL)
}

func TestClientRejectsCredentialReflectionAndMismatchedProviderIdentity(t *testing.T) {
	t.Run("credential in JSON", func(t *testing.T) {
		client := NewEnterpriseClient("wire-secret")
		client.httpClient = doerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"hardware":[],"total":0,"note":"wire-secret"}}`))}, nil
		})
		_, err := client.GetMaxGPUsPerContainer(context.Background())
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "wire-secret")
	})
	t.Run("escaped credential in JSON", func(t *testing.T) {
		client := NewEnterpriseClient("wire-secret")
		client.httpClient = doerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"hardware":[],"total":0,"note":"wire\u002dsecret"}}`))}, nil
		})
		_, err := client.GetMaxGPUsPerContainer(context.Background())
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "wire-secret")
	})
	t.Run("credential in logs", func(t *testing.T) {
		client := NewClient("wire-secret")
		client.httpClient = doerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("wire-secret"))}, nil
		})
		_, err := client.GetContainerLogsRaw(context.Background(), "dep-1", "ctr-1", &GetLogsOptions{Limit: 100})
		require.Error(t, err)
	})
	t.Run("deployment ID mismatch", func(t *testing.T) {
		client := NewEnterpriseClient("wire-secret")
		client.httpClient = doerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(validDetailWrapper("dep-other")))}, nil
		})
		_, err := client.GetDeployment(context.Background(), "dep-1")
		require.Error(t, err)
	})
}

func TestClientRejectsInvalidCredentialsAndRequestsBeforeDispatch(t *testing.T) {
	var requests atomic.Int32
	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, assert.AnError
	})
	client := NewEnterpriseClient("bad\nkey")
	client.httpClient = doer
	_, err := client.GetMaxGPUsPerContainer(context.Background())
	require.Error(t, err)
	_, err = client.GetDeployment(context.Background(), "../../escape")
	require.Error(t, err)
	_, err = client.GetAvailableReplicas(context.Background(), 1, MaxGPUCount+1)
	require.Error(t, err)
	_, err = client.GetPriceEstimation(context.Background(), &PriceEstimationRequest{LocationIDs: []int{1}, HardwareID: 1, ReplicaCount: 1, DurationType: "century", DurationQty: 1, HardwareQty: 1})
	require.Error(t, err)
	assert.Zero(t, requests.Load())
}

func TestDecodeRequestIsStrict(t *testing.T) {
	type request struct {
		Name string `json:"name"`
	}
	var decoded request
	require.NoError(t, DecodeRequest([]byte(`{"name":"alpha"}`), &decoded))
	assert.Equal(t, "alpha", decoded.Name)
	for _, payload := range []string{
		`null`, `[]`, `{"name":"a","name":"b"}`, `{"name":"a","unknown":true}`, `{"name":"a"} {}`,
	} {
		assert.Error(t, DecodeRequest([]byte(payload), &decoded), payload)
	}
	assert.Error(t, DecodeRequest([]byte{'{', '"', 'n', 'a', 'm', 'e', '"', ':', '"', 0xff, '"', '}'}, &decoded))
	deep := strings.Repeat(`{"x":`, maxJSONDepth+2) + `null` + strings.Repeat(`}`, maxJSONDepth+2)
	assert.Error(t, DecodeRequest([]byte(deep), &decoded))
}

func validDetailWrapper(id string) string {
	return `{"data":{"id":"` + id + `","status":"RUNNING","created_at":"2026-01-02T03:04:05","amount_paid":1,"completed_percent":10,"total_gpus":2,"gpus_per_container":1,"total_containers":2,"hardware_name":"H100","hardware_id":7,"locations":[{"id":9,"iso2":"DE","name":"Frankfurt"}],"brand_name":"NVIDIA","compute_minutes_served":5,"compute_minutes_remaining":55,"container_config":{"entrypoint":["serve"],"env_variables":{"MODE":"prod"},"traffic_port":8080,"image_url":"repo/image:v1"}}}`
}

func validContainerJSON() string {
	return `{"device_id":"device-1","container_id":"ctr-1","hardware":"H100","brand_name":"NVIDIA","created_at":"2026-01-02T03:04:05","uptime_percent":99,"gpus_per_container":1,"status":"RUNNING","container_events":[{"time":"2026-01-02T03:05:05","message":"started"}],"public_url":"https://container.example"}`
}

func validContainerListWrapper() string {
	return `{"data":{"total":1,"workers":[` + validContainerJSON() + `]}}`
}
