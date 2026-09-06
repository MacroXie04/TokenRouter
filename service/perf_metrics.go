package service

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

const (
	PerfMetricSeriesSchema             = "dbcd0a3c01b55203"
	invalidPerfMetricRetentionFallback = 30
)

type PerfMetricSample struct {
	Model        string
	Group        string
	LatencyMs    int64
	TtftMs       int64
	HasTtft      bool
	Success      bool
	OutputTokens int64
	GenerationMs int64
}

type PerfMetricBucketPoint struct {
	Ts           int64   `json:"ts"`
	AvgTtftMs    int64   `json:"avg_ttft_ms"`
	AvgLatencyMs int64   `json:"avg_latency_ms"`
	SuccessRate  float64 `json:"success_rate"`
	AvgTps       float64 `json:"avg_tps"`
}

type PerfMetricGroupResult struct {
	Group        string                  `json:"group"`
	AvgTtftMs    int64                   `json:"avg_ttft_ms"`
	AvgLatencyMs int64                   `json:"avg_latency_ms"`
	SuccessRate  float64                 `json:"success_rate"`
	AvgTps       float64                 `json:"avg_tps"`
	Series       []PerfMetricBucketPoint `json:"series"`
}

type PerfMetricQueryResult struct {
	ModelName    string                  `json:"model_name"`
	SeriesSchema string                  `json:"series_schema"`
	Groups       []PerfMetricGroupResult `json:"groups"`
}

type PerfMetricModelSummary struct {
	ModelName          string    `json:"model_name"`
	AvgLatencyMs       int64     `json:"avg_latency_ms"`
	SuccessRate        float64   `json:"success_rate"`
	AvgTps             float64   `json:"avg_tps"`
	RecentSuccessRates []float64 `json:"recent_success_rates,omitempty"`
	requestCount       int64
}

type PerfMetricSummaryResult struct {
	Models []PerfMetricModelSummary `json:"models"`
}

type perfMetricBucketKey struct {
	model    string
	group    string
	bucketTs int64
}

type perfMetricCounters struct {
	requestCount   int64
	successCount   int64
	totalLatencyMs int64
	ttftSumMs      int64
	ttftCount      int64
	outputTokens   int64
	generationMs   int64
}

type atomicPerfMetricBucket struct {
	requestCount   atomic.Int64
	successCount   atomic.Int64
	totalLatencyMs atomic.Int64
	ttftSumMs      atomic.Int64
	ttftCount      atomic.Int64
	outputTokens   atomic.Int64
	generationMs   atomic.Int64
}

var (
	perfMetricBuckets sync.Map
	perfMetricFlushMu sync.RWMutex
	perfMetricFlusher sync.Once
)

func RecordPerfMetricSample(sample PerfMetricSample) {
	recordPerfMetricSampleAt(sample, time.Now())
}

func recordPerfMetricSampleAt(sample PerfMetricSample, now time.Time) {
	if !perfMetricsEnabled() || sample.Model == "" {
		return
	}
	if sample.Group == "" {
		sample.Group = GroupDefault
	}
	if sample.LatencyMs < 0 {
		sample.LatencyMs = 0
	}
	key := perfMetricBucketKey{
		model:    sample.Model,
		group:    sample.Group,
		bucketTs: perfMetricBucketStart(now.Unix()),
	}
	value, _ := perfMetricBuckets.LoadOrStore(key, &atomicPerfMetricBucket{})
	value.(*atomicPerfMetricBucket).add(sample)
}

func QueryPerfMetrics(modelName, group string, hours int) (PerfMetricQueryResult, error) {
	return queryPerfMetricsAt(modelName, group, hours, time.Now())
}

func queryPerfMetricsAt(modelName, group string, hours int, now time.Time) (PerfMetricQueryResult, error) {
	hours = normalizePerfMetricHours(hours)
	endTs := now.Unix()
	startTs := endTs - int64(hours)*3600
	merged := map[perfMetricBucketKey]perfMetricCounters{}

	perfMetricFlushMu.RLock()
	defer perfMetricFlushMu.RUnlock()
	rows, err := model.ListPerfMetrics(modelName, group, startTs, endTs)
	if err != nil {
		return PerfMetricQueryResult{}, err
	}
	for _, row := range rows {
		mergePerfMetricCounters(merged, perfMetricBucketKey{model: row.ModelName, group: row.Group, bucketTs: row.BucketTs}, perfMetricCounters{
			requestCount:   row.RequestCount,
			successCount:   row.SuccessCount,
			totalLatencyMs: row.TotalLatencyMs,
			ttftSumMs:      row.TtftSumMs,
			ttftCount:      row.TtftCount,
			outputTokens:   row.OutputTokens,
			generationMs:   row.GenerationMs,
		})
	}
	perfMetricBuckets.Range(func(rawKey, rawValue any) bool {
		key := rawKey.(perfMetricBucketKey)
		if key.model != modelName || key.bucketTs < startTs || key.bucketTs > endTs {
			return true
		}
		if group != "" && key.group != group {
			return true
		}
		mergePerfMetricCounters(merged, key, rawValue.(*atomicPerfMetricBucket).snapshot())
		return true
	})
	return buildPerfMetricQueryResult(modelName, merged), nil
}

func QueryPerfMetricsSummary(hours int, groups []string) (PerfMetricSummaryResult, error) {
	return queryPerfMetricsSummaryAt(hours, groups, time.Now())
}

func queryPerfMetricsSummaryAt(hours int, groups []string, now time.Time) (PerfMetricSummaryResult, error) {
	hours = normalizePerfMetricHours(hours)
	endTs := now.Unix()
	startTs := endTs - int64(hours)*3600
	allowedGroups := perfMetricGroupSet(groups)
	totals := map[string]perfMetricCounters{}
	modelBuckets := map[string]map[int64]perfMetricCounters{}

	perfMetricFlushMu.RLock()
	defer perfMetricFlushMu.RUnlock()
	rows, err := model.ListPerfMetricSummaryBuckets(startTs, endTs, groups)
	if err != nil {
		return PerfMetricSummaryResult{}, err
	}
	for _, row := range rows {
		counters := perfMetricCounters{
			requestCount:   row.RequestCount,
			successCount:   row.SuccessCount,
			totalLatencyMs: row.TotalLatencyMs,
			outputTokens:   row.OutputTokens,
			generationMs:   row.GenerationMs,
		}
		mergePerfMetricModelTotal(totals, row.ModelName, counters)
		mergePerfMetricModelBucket(modelBuckets, row.ModelName, row.BucketTs, counters)
	}
	perfMetricBuckets.Range(func(rawKey, rawValue any) bool {
		key := rawKey.(perfMetricBucketKey)
		if key.bucketTs < startTs || key.bucketTs > endTs {
			return true
		}
		if allowedGroups != nil {
			if _, ok := allowedGroups[key.group]; !ok {
				return true
			}
		}
		counters := rawValue.(*atomicPerfMetricBucket).snapshot()
		mergePerfMetricModelTotal(totals, key.model, counters)
		mergePerfMetricModelBucket(modelBuckets, key.model, key.bucketTs, counters)
		return true
	})

	models := make([]PerfMetricModelSummary, 0, len(totals))
	for modelName, total := range totals {
		if total.requestCount == 0 {
			continue
		}
		models = append(models, PerfMetricModelSummary{
			ModelName:          modelName,
			AvgLatencyMs:       perfMetricAverage(total.totalLatencyMs, total.requestCount),
			SuccessRate:        roundPerfMetric(perfMetricSuccessRate(total)),
			AvgTps:             roundPerfMetric(perfMetricAverageTps(total)),
			RecentSuccessRates: recentPerfMetricSuccessRates(modelBuckets[modelName], 3),
			requestCount:       total.requestCount,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].requestCount == models[j].requestCount {
			return models[i].ModelName < models[j].ModelName
		}
		return models[i].requestCount > models[j].requestCount
	})
	return PerfMetricSummaryResult{Models: models}, nil
}

func ActivePerfMetricGroups() []string {
	set := map[string]struct{}{GroupDefault: {}, "auto": {}}
	for group := range ExportedGroupRatios() {
		if group != "" {
			set[group] = struct{}{}
		}
	}
	groups := make([]string, 0, len(set))
	for group := range set {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return groups
}

func FilterActivePerfMetricGroups(groups []PerfMetricGroupResult) []PerfMetricGroupResult {
	active := perfMetricGroupSet(ActivePerfMetricGroups())
	filtered := make([]PerfMetricGroupResult, 0, len(groups))
	for _, group := range groups {
		if _, ok := active[group.Group]; ok {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func StartPerfMetricFlusher() {
	perfMetricFlusher.Do(func() {
		go func() {
			for {
				time.Sleep(time.Duration(perfMetricFlushIntervalMinutes()) * time.Minute)
				if !perfMetricsEnabled() {
					continue
				}
				now := time.Now()
				if err := flushCompletedPerfMetricsAt(now); err != nil {
					common.SysError("failed to flush performance metrics: " + err.Error())
				}
				if days := perfMetricRetentionDays(); days > 0 {
					cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).Unix()
					if err := model.DeletePerfMetricsBefore(cutoff); err != nil {
						common.SysError("failed to clean up performance metrics: " + err.Error())
					}
				}
			}
		}()
	})
}

func FlushCompletedPerfMetrics() error {
	return flushCompletedPerfMetricsAt(time.Now())
}

func flushCompletedPerfMetricsAt(now time.Time) error {
	currentBucket := perfMetricBucketStart(now.Unix())
	perfMetricFlushMu.Lock()
	defer perfMetricFlushMu.Unlock()
	var firstErr error
	perfMetricBuckets.Range(func(rawKey, rawValue any) bool {
		key := rawKey.(perfMetricBucketKey)
		if key.bucketTs >= currentBucket {
			return true
		}
		bucket := rawValue.(*atomicPerfMetricBucket)
		counters := bucket.drain()
		if counters.requestCount > 0 {
			err := model.UpsertPerfMetric(&model.PerfMetric{
				ModelName: key.model, Group: key.group, BucketTs: key.bucketTs,
				RequestCount: counters.requestCount, SuccessCount: counters.successCount,
				TotalLatencyMs: counters.totalLatencyMs, TtftSumMs: counters.ttftSumMs,
				TtftCount: counters.ttftCount, OutputTokens: counters.outputTokens,
				GenerationMs: counters.generationMs,
			})
			if err != nil {
				bucket.addCounters(counters)
				if firstErr == nil {
					firstErr = err
				}
				return true
			}
		}
		if key.bucketTs < perfMetricBucketStart(now.Add(-24*time.Hour).Unix()) {
			perfMetricBuckets.Delete(rawKey)
		}
		return true
	})
	return firstErr
}

func buildPerfMetricQueryResult(modelName string, merged map[perfMetricBucketKey]perfMetricCounters) PerfMetricQueryResult {
	groupBuckets := map[string]map[int64]perfMetricCounters{}
	for key, counters := range merged {
		if counters.requestCount == 0 {
			continue
		}
		if groupBuckets[key.group] == nil {
			groupBuckets[key.group] = map[int64]perfMetricCounters{}
		}
		groupBuckets[key.group][key.bucketTs] = counters
	}
	groups := make([]string, 0, len(groupBuckets))
	for group := range groupBuckets {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	results := make([]PerfMetricGroupResult, 0, len(groups))
	for _, group := range groups {
		buckets := groupBuckets[group]
		timestamps := make([]int64, 0, len(buckets))
		for timestamp := range buckets {
			timestamps = append(timestamps, timestamp)
		}
		sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })
		total := perfMetricCounters{}
		series := make([]PerfMetricBucketPoint, 0, len(timestamps))
		for _, timestamp := range timestamps {
			counters := buckets[timestamp]
			total.add(counters)
			series = append(series, PerfMetricBucketPoint{
				Ts: timestamp, AvgTtftMs: perfMetricAverage(counters.ttftSumMs, counters.ttftCount),
				AvgLatencyMs: perfMetricAverage(counters.totalLatencyMs, counters.requestCount),
				SuccessRate:  perfMetricSuccessRate(counters), AvgTps: perfMetricAverageTps(counters),
			})
		}
		results = append(results, PerfMetricGroupResult{
			Group: group, AvgTtftMs: perfMetricAverage(total.ttftSumMs, total.ttftCount),
			AvgLatencyMs: perfMetricAverage(total.totalLatencyMs, total.requestCount),
			SuccessRate:  perfMetricSuccessRate(total), AvgTps: perfMetricAverageTps(total), Series: series,
		})
	}
	return PerfMetricQueryResult{ModelName: modelName, SeriesSchema: PerfMetricSeriesSchema, Groups: results}
}

func mergePerfMetricCounters(target map[perfMetricBucketKey]perfMetricCounters, key perfMetricBucketKey, value perfMetricCounters) {
	if value.requestCount == 0 {
		return
	}
	current := target[key]
	current.add(value)
	target[key] = current
}

func mergePerfMetricModelTotal(target map[string]perfMetricCounters, modelName string, value perfMetricCounters) {
	if value.requestCount == 0 {
		return
	}
	current := target[modelName]
	current.add(value)
	target[modelName] = current
}

func mergePerfMetricModelBucket(target map[string]map[int64]perfMetricCounters, modelName string, bucketTs int64, value perfMetricCounters) {
	if value.requestCount == 0 {
		return
	}
	if target[modelName] == nil {
		target[modelName] = map[int64]perfMetricCounters{}
	}
	current := target[modelName][bucketTs]
	current.add(value)
	target[modelName][bucketTs] = current
}

func recentPerfMetricSuccessRates(buckets map[int64]perfMetricCounters, limit int) []float64 {
	if len(buckets) == 0 || limit <= 0 {
		return nil
	}
	timestamps := make([]int64, 0, len(buckets))
	for timestamp := range buckets {
		timestamps = append(timestamps, timestamp)
	}
	sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })
	if len(timestamps) > limit {
		timestamps = timestamps[len(timestamps)-limit:]
	}
	rates := make([]float64, 0, len(timestamps))
	for _, timestamp := range timestamps {
		rates = append(rates, roundPerfMetric(perfMetricSuccessRate(buckets[timestamp])))
	}
	return rates
}

func normalizePerfMetricHours(hours int) int {
	if hours <= 0 {
		return 24
	}
	if hours > 24*30 {
		return 24 * 30
	}
	return hours
}

func perfMetricBucketStart(timestamp int64) int64 {
	seconds := perfMetricBucketSeconds()
	return timestamp - timestamp%seconds
}

func perfMetricBucketSeconds() int64 {
	switch setting.GetOptionOrDefault(setting.PerfMetricsBucketTimeOption, "hour") {
	case "minute":
		return 60
	case "5min":
		return 300
	default:
		return 3600
	}
}

func perfMetricsEnabled() bool {
	return setting.GetOptionBool(setting.PerfMetricsEnabledOption, true)
}

func perfMetricFlushIntervalMinutes() int {
	interval := setting.GetOptionIntOrDefault(setting.PerfMetricsFlushIntervalOption, 5)
	if interval < 1 || interval > 24*60 {
		return 5
	}
	return interval
}

func perfMetricRetentionDays() int {
	raw := setting.GetOption(setting.PerfMetricsRetentionDaysOption)
	if raw == "" {
		return 0
	}
	if raw != strings.TrimSpace(raw) || len(raw) > 16 {
		return invalidPerfMetricRetentionFallback
	}
	days, err := strconv.Atoi(raw)
	if err != nil || strconv.Itoa(days) != raw || days < 0 || days > 36_500 {
		return invalidPerfMetricRetentionFallback
	}
	return days
}

func perfMetricGroupSet(groups []string) map[string]struct{} {
	if groups == nil {
		return nil
	}
	set := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		set[group] = struct{}{}
	}
	return set
}

func perfMetricAverage(sum, count int64) int64 {
	if count <= 0 {
		return 0
	}
	return sum / count
}

func perfMetricSuccessRate(counters perfMetricCounters) float64 {
	if counters.requestCount <= 0 {
		return 0
	}
	return float64(counters.successCount) / float64(counters.requestCount) * 100
}

func perfMetricAverageTps(counters perfMetricCounters) float64 {
	if counters.outputTokens <= 0 || counters.generationMs <= 0 {
		return 0
	}
	return float64(counters.outputTokens) / (float64(counters.generationMs) / 1000)
}

func roundPerfMetric(value float64) float64 {
	return math.Round(value*100) / 100
}

func (bucket *atomicPerfMetricBucket) add(sample PerfMetricSample) {
	bucket.requestCount.Add(1)
	if sample.Success {
		bucket.successCount.Add(1)
	}
	if sample.LatencyMs > 0 {
		bucket.totalLatencyMs.Add(sample.LatencyMs)
	}
	if sample.HasTtft && sample.TtftMs >= 0 {
		bucket.ttftSumMs.Add(sample.TtftMs)
		bucket.ttftCount.Add(1)
	}
	if sample.OutputTokens > 0 && sample.GenerationMs > 0 {
		bucket.outputTokens.Add(sample.OutputTokens)
		bucket.generationMs.Add(sample.GenerationMs)
	}
}

func (bucket *atomicPerfMetricBucket) snapshot() perfMetricCounters {
	return perfMetricCounters{
		requestCount: bucket.requestCount.Load(), successCount: bucket.successCount.Load(),
		totalLatencyMs: bucket.totalLatencyMs.Load(), ttftSumMs: bucket.ttftSumMs.Load(),
		ttftCount: bucket.ttftCount.Load(), outputTokens: bucket.outputTokens.Load(),
		generationMs: bucket.generationMs.Load(),
	}
}

func (bucket *atomicPerfMetricBucket) drain() perfMetricCounters {
	return perfMetricCounters{
		requestCount: bucket.requestCount.Swap(0), successCount: bucket.successCount.Swap(0),
		totalLatencyMs: bucket.totalLatencyMs.Swap(0), ttftSumMs: bucket.ttftSumMs.Swap(0),
		ttftCount: bucket.ttftCount.Swap(0), outputTokens: bucket.outputTokens.Swap(0),
		generationMs: bucket.generationMs.Swap(0),
	}
}

func (bucket *atomicPerfMetricBucket) addCounters(counters perfMetricCounters) {
	bucket.requestCount.Add(counters.requestCount)
	bucket.successCount.Add(counters.successCount)
	bucket.totalLatencyMs.Add(counters.totalLatencyMs)
	bucket.ttftSumMs.Add(counters.ttftSumMs)
	bucket.ttftCount.Add(counters.ttftCount)
	bucket.outputTokens.Add(counters.outputTokens)
	bucket.generationMs.Add(counters.generationMs)
}

func (counters *perfMetricCounters) add(other perfMetricCounters) {
	counters.requestCount += other.requestCount
	counters.successCount += other.successCount
	counters.totalLatencyMs += other.totalLatencyMs
	counters.ttftSumMs += other.ttftSumMs
	counters.ttftCount += other.ttftCount
	counters.outputTokens += other.outputTokens
	counters.generationMs += other.generationMs
}
