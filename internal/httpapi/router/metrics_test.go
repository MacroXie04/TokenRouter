package router_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	operationssvc "github.com/tokenrouter/tokenrouter/internal/operations"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPerfMetricsContract(t *testing.T) {
	handler, _, _ := setupChannelRead(t, roles.RoleRootUser)
	billingsvc.SetGroupRatios(map[string]float64{"default": 1, "vip": 2})
	t.Cleanup(func() { billingsvc.SetGroupRatios(map[string]float64{}) })
	currentBucket := serviceBucketNow()
	bucket := currentBucket - 31*24*3600
	require.NoError(t, model.DB.Create([]model.PerfMetric{
		{ModelName: "gpt-main", Group: "default", BucketTs: bucket, RequestCount: 2, SuccessCount: 1, TotalLatencyMs: 400, TtftSumMs: 100, TtftCount: 1, OutputTokens: 20, GenerationMs: 1000},
		{ModelName: "gpt-main", Group: "vip", BucketTs: bucket, RequestCount: 1, SuccessCount: 1, TotalLatencyMs: 600, OutputTokens: 30, GenerationMs: 1000},
		{ModelName: "gpt-main", Group: "removed", BucketTs: bucket, RequestCount: 100, SuccessCount: 100, TotalLatencyMs: 100},
		{ModelName: "gpt-other", Group: "default", BucketTs: bucket, RequestCount: 1, SuccessCount: 1, TotalLatencyMs: 50},
	}).Error)

	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	rec := request("/api/perf-metrics")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "model is required", decodeBody(t, rec)["message"])

	// The service clamps oversized ranges to the reference maximum of 30 days.
	rec = request("/api/perf-metrics?model=gpt-main&hours=999999")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data := body["data"].(map[string]any)
	assert.Equal(t, "gpt-main", data["model_name"])
	assert.Equal(t, operationssvc.PerfMetricSeriesSchema, data["series_schema"])
	groups := data["groups"].([]any)
	// The fixture is 31 days old, so the clamped query excludes it.
	assert.Empty(t, groups)

	require.NoError(t, model.DB.Model(&model.PerfMetric{}).
		Where("model_name IN ?", []string{"gpt-main", "gpt-other"}).
		Update("bucket_ts", currentBucket).Error)

	rec = request("/api/perf-metrics?model=gpt-main&hours=invalid")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data = decodeBody(t, rec)["data"].(map[string]any)
	groups = data["groups"].([]any)
	require.Len(t, groups, 2, "inactive groups are filtered")
	defaultGroup := groups[0].(map[string]any)
	assert.Equal(t, "default", defaultGroup["group"])
	assert.Equal(t, float64(200), defaultGroup["avg_latency_ms"])
	assert.Equal(t, float64(100), defaultGroup["avg_ttft_ms"])
	assert.Equal(t, 50.0, defaultGroup["success_rate"])
	assert.Equal(t, 20.0, defaultGroup["avg_tps"])
	assert.Len(t, defaultGroup["series"], 1)

	rec = request("/api/perf-metrics?model=gpt-main&group=vip")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	groups = decodeBody(t, rec)["data"].(map[string]any)["groups"].([]any)
	require.Len(t, groups, 1)
	assert.Equal(t, "vip", groups[0].(map[string]any)["group"])

	rec = request("/api/perf-metrics/summary?hours=24")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	models := decodeBody(t, rec)["data"].(map[string]any)["models"].([]any)
	require.Len(t, models, 2)
	mainSummary := models[0].(map[string]any)
	assert.Equal(t, "gpt-main", mainSummary["model_name"])
	assert.Equal(t, float64(333), mainSummary["avg_latency_ms"])
	assert.Equal(t, 66.67, mainSummary["success_rate"])
	assert.Equal(t, 25.0, mainSummary["avg_tps"])
	assert.Equal(t, []any{66.67}, mainSummary["recent_success_rates"])
	_, leaksRequestCount := mainSummary["request_count"]
	assert.False(t, leaksRequestCount)
}

func serviceBucketNow() int64 {
	now := time.Now().Unix()
	return now - now%3600
}
