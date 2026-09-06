package operations

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestPerfMetricSchedulerOptionsAreBounded(t *testing.T) {
	setupPerfMetricTest(t)

	for _, invalid := range []string{"0", "-1", "1441", strconv.FormatInt(int64(^uint(0)>>1), 10), "invalid"} {
		require.NoError(t, setting.UpdateOption(setting.PerfMetricsFlushIntervalOption, invalid))
		assert.Equal(t, 5, perfMetricFlushIntervalMinutes(), invalid)
	}
	for _, boundary := range []struct {
		value string
		want  int
	}{{"1", 1}, {"1440", 1440}} {
		require.NoError(t, setting.UpdateOption(setting.PerfMetricsFlushIntervalOption, boundary.value))
		assert.Equal(t, boundary.want, perfMetricFlushIntervalMinutes())
	}

	for _, invalid := range []string{"-1", "36501", strconv.FormatInt(int64(^uint(0)>>1), 10), "invalid"} {
		require.NoError(t, setting.UpdateOption(setting.PerfMetricsRetentionDaysOption, invalid))
		assert.Equal(t, invalidPerfMetricRetentionFallback, perfMetricRetentionDays(), invalid)
	}
	for _, boundary := range []struct {
		value string
		want  int
	}{{"0", 0}, {"36500", 36500}} {
		require.NoError(t, setting.UpdateOption(setting.PerfMetricsRetentionDaysOption, boundary.value))
		assert.Equal(t, boundary.want, perfMetricRetentionDays())
	}
}

func setupPerfMetricTest(t *testing.T) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "perf.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	clearPerfMetricBuckets()
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.PerfMetricsEnabledOption:       "true",
		setting.PerfMetricsBucketTimeOption:    "hour",
		setting.PerfMetricsFlushIntervalOption: "5",
		setting.PerfMetricsRetentionDaysOption: "0",
	}))
	t.Cleanup(clearPerfMetricBuckets)
}

func clearPerfMetricBuckets() {
	perfMetricFlushMu.Lock()
	defer perfMetricFlushMu.Unlock()
	perfMetricBuckets.Range(func(key, _ any) bool {
		perfMetricBuckets.Delete(key)
		return true
	})
}

func TestPerfMetricsHotQueryAndSummary(t *testing.T) {
	setupPerfMetricTest(t)
	now := time.Unix(1_800_000_123, 0)
	billingsvc.SetGroupRatios(map[string]float64{"default": 1, "vip": 2})
	t.Cleanup(func() { billingsvc.SetGroupRatios(map[string]float64{}) })

	recordPerfMetricSampleAt(PerfMetricSample{
		Model: "gpt-test", Group: "default", LatencyMs: 100, TtftMs: 20,
		HasTtft: true, Success: true, OutputTokens: 10, GenerationMs: 500,
	}, now)
	recordPerfMetricSampleAt(PerfMetricSample{
		Model: "gpt-test", Group: "default", LatencyMs: 300, Success: false,
	}, now)
	recordPerfMetricSampleAt(PerfMetricSample{
		Model: "gpt-test", Group: "vip", LatencyMs: 600, Success: true,
		OutputTokens: 30, GenerationMs: 1_000,
	}, now)
	recordPerfMetricSampleAt(PerfMetricSample{
		Model: "gpt-test", Group: "removed", LatencyMs: 50, Success: true,
	}, now)

	result, err := queryPerfMetricsAt("gpt-test", "", 24, now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, result.Groups, 3)
	assert.Equal(t, PerfMetricSeriesSchema, result.SeriesSchema)
	assert.Equal(t, "gpt-test", result.ModelName)
	defaultGroup := result.Groups[0]
	assert.Equal(t, "default", defaultGroup.Group)
	assert.Equal(t, int64(200), defaultGroup.AvgLatencyMs)
	assert.Equal(t, int64(20), defaultGroup.AvgTtftMs)
	assert.Equal(t, 50.0, defaultGroup.SuccessRate)
	assert.Equal(t, 20.0, defaultGroup.AvgTps)
	require.Len(t, defaultGroup.Series, 1)
	assert.Equal(t, perfMetricBucketStart(now.Unix()), defaultGroup.Series[0].Ts)

	filtered := FilterActivePerfMetricGroups(result.Groups)
	require.Len(t, filtered, 2)
	assert.Equal(t, "default", filtered[0].Group)
	assert.Equal(t, "vip", filtered[1].Group)

	vip, err := queryPerfMetricsAt("gpt-test", "vip", 24, now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, vip.Groups, 1)
	assert.Equal(t, int64(600), vip.Groups[0].AvgLatencyMs)
	assert.Equal(t, 30.0, vip.Groups[0].AvgTps)

	summary, err := queryPerfMetricsSummaryAt(24, ActivePerfMetricGroups(), now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, summary.Models, 1)
	assert.Equal(t, "gpt-test", summary.Models[0].ModelName)
	assert.Equal(t, int64(333), summary.Models[0].AvgLatencyMs)
	assert.Equal(t, 66.67, summary.Models[0].SuccessRate)
	assert.Equal(t, 26.67, summary.Models[0].AvgTps)
	assert.Equal(t, []float64{66.67}, summary.Models[0].RecentSuccessRates)
}

func TestPerfMetricsFlushAndRetention(t *testing.T) {
	setupPerfMetricTest(t)
	now := time.Unix(1_800_000_123, 0)
	old := now.Add(-2 * time.Hour)
	sample := PerfMetricSample{
		Model: "gpt-flush", Group: "default", LatencyMs: 200, TtftMs: 40,
		HasTtft: true, Success: true, OutputTokens: 25, GenerationMs: 500,
	}
	recordPerfMetricSampleAt(sample, old)
	require.NoError(t, flushCompletedPerfMetricsAt(now))

	var persisted model.PerfMetric
	require.NoError(t, model.DB.Where("model_name = ?", "gpt-flush").First(&persisted).Error)
	assert.Equal(t, int64(1), persisted.RequestCount)
	assert.Equal(t, int64(25), persisted.OutputTokens)

	recordPerfMetricSampleAt(sample, old)
	require.NoError(t, flushCompletedPerfMetricsAt(now))
	require.NoError(t, model.DB.Where("model_name = ?", "gpt-flush").First(&persisted).Error)
	assert.Equal(t, int64(2), persisted.RequestCount, "conflict upsert adds counters atomically")

	result, err := queryPerfMetricsAt("gpt-flush", "default", 24, now)
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	assert.Equal(t, int64(200), result.Groups[0].AvgLatencyMs, "flushed buckets are not counted twice")

	require.NoError(t, model.DeletePerfMetricsBefore(now.Add(-time.Hour).Unix()))
	var count int64
	require.NoError(t, model.DB.Model(&model.PerfMetric{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestPerfMetricsConfigurationAndConcurrentAccess(t *testing.T) {
	setupPerfMetricTest(t)
	now := time.Unix(1_800_000_123, 0)
	require.NoError(t, setting.UpdateOption(setting.PerfMetricsEnabledOption, "false"))
	recordPerfMetricSampleAt(PerfMetricSample{Model: "disabled", Success: true}, now)
	result, err := queryPerfMetricsAt("disabled", "", 24, now)
	require.NoError(t, err)
	assert.Empty(t, result.Groups)

	require.NoError(t, setting.UpdateOption(setting.PerfMetricsEnabledOption, "true"))
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				recordPerfMetricSampleAt(PerfMetricSample{
					Model: "concurrent", Group: "default", LatencyMs: int64(worker + i), Success: true,
				}, now)
				_, _ = queryPerfMetricsAt("concurrent", "default", 24, now.Add(time.Minute))
			}
		}()
	}
	wg.Wait()

	result, err = queryPerfMetricsAt("concurrent", "default", 24, now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	key := perfMetricBucketKey{model: "concurrent", group: "default", bucketTs: perfMetricBucketStart(now.Unix())}
	value, ok := perfMetricBuckets.Load(key)
	require.True(t, ok)
	assert.Equal(t, int64(800), value.(*atomicPerfMetricBucket).snapshot().requestCount)
	assert.Equal(t, 24, normalizePerfMetricHours(0))
	assert.Equal(t, 24*30, normalizePerfMetricHours(24*31))
}
