package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/tokenrouter/tokenrouter/model"
)

const (
	rankingModelLimit          = 20
	rankingVendorLimit         = 100
	rankingMoverLimit          = 6
	rankingHistoryModelLimit   = 10
	rankingHistoryVendorLimit  = 5
	rankingAggregateModelLimit = 1_000
	rankingMaxSafeInteger      = int64(9_007_199_254_740_991)
	rankingUnknownVendor       = "Unknown"
	rankingOthers              = "Others"
)

var (
	ErrInvalidRankingPeriod = errors.New("invalid ranking period")
	ErrRankingsTooLarge     = errors.New("rankings data exceeds safe limits")
)

type RankedModel struct {
	Rank         int     `json:"rank"`
	PreviousRank *int    `json:"previous_rank,omitempty"`
	ModelName    string  `json:"model_name"`
	Vendor       string  `json:"vendor"`
	VendorIcon   string  `json:"vendor_icon,omitempty"`
	Category     string  `json:"category"`
	TotalTokens  int64   `json:"total_tokens"`
	Share        float64 `json:"share"`
	GrowthPct    float64 `json:"growth_pct"`
}

type RankedVendor struct {
	Rank        int     `json:"rank"`
	Vendor      string  `json:"vendor"`
	VendorIcon  string  `json:"vendor_icon,omitempty"`
	TotalTokens int64   `json:"total_tokens"`
	Share       float64 `json:"share"`
	GrowthPct   float64 `json:"growth_pct"`
	ModelsCount int     `json:"models_count"`
	TopModel    string  `json:"top_model"`
}

type RankingMover struct {
	ModelName   string  `json:"model_name"`
	Vendor      string  `json:"vendor"`
	VendorIcon  string  `json:"vendor_icon,omitempty"`
	RankDelta   int     `json:"rank_delta"`
	CurrentRank int     `json:"current_rank"`
	GrowthPct   float64 `json:"growth_pct"`
}

type ModelHistoryPoint struct {
	Timestamp string `json:"ts"`
	Label     string `json:"label"`
	Model     string `json:"model"`
	Vendor    string `json:"vendor"`
	Tokens    int64  `json:"tokens"`
}

type ModelHistoryModel struct {
	Name   string `json:"name"`
	Vendor string `json:"vendor"`
	Total  int64  `json:"total"`
}

type ModelHistorySeries struct {
	Points  []ModelHistoryPoint `json:"points"`
	Models  []ModelHistoryModel `json:"models"`
	Buckets int                 `json:"buckets"`
}

type VendorSharePoint struct {
	Timestamp string  `json:"ts"`
	Label     string  `json:"label"`
	Vendor    string  `json:"vendor"`
	Share     float64 `json:"share"`
	Tokens    int64   `json:"tokens"`
}

type VendorShareVendor struct {
	Name  string  `json:"name"`
	Total int64   `json:"total"`
	Share float64 `json:"share"`
}

type VendorShareSeries struct {
	Points  []VendorSharePoint  `json:"points"`
	Vendors []VendorShareVendor `json:"vendors"`
	Buckets int                 `json:"buckets"`
}

type RankingsSnapshot struct {
	Models             []RankedModel      `json:"models"`
	Vendors            []RankedVendor     `json:"vendors"`
	TopMovers          []RankingMover     `json:"top_movers"`
	TopDroppers        []RankingMover     `json:"top_droppers"`
	ModelsHistory      ModelHistorySeries `json:"models_history"`
	VendorShareHistory VendorShareSeries  `json:"vendor_share_history"`
}

type rankingPeriod struct {
	duration   time.Duration
	bucketSize int64
	label      string
}

type rankingTotal struct {
	ModelName   string `gorm:"column:model_name"`
	TotalTokens int64  `gorm:"column:total_tokens"`
}

type rankingBucket struct {
	ModelName string `gorm:"column:model_name"`
	Bucket    int64  `gorm:"column:bucket"`
	Tokens    int64  `gorm:"column:tokens"`
}

type rankingMeta struct {
	vendor string
	icon   string
}

type rankingVendorAggregate struct {
	name           string
	icon           string
	total          int64
	previous       int64
	models         map[string]struct{}
	topModel       string
	topModelTokens int64
}

// GetRankingsSnapshot returns the period-sensitive public rankings snapshot.
// Its one-year maximum window and fixed aggregate/series caps bound both the
// query work and the JSON exposed to anonymous callers.
func GetRankingsSnapshot(ctx context.Context, period string) (*RankingsSnapshot, error) {
	return buildRankingsSnapshot(ctx, period, time.Now())
}

func buildRankingsSnapshot(ctx context.Context, period string, now time.Time) (*RankingsSnapshot, error) {
	config, err := rankingPeriodFor(period)
	if err != nil {
		return nil, err
	}
	if model.DB == nil {
		return nil, errors.New("rankings database unavailable")
	}

	end := now.Unix()
	start := now.Add(-config.duration).Unix()
	previousEnd := start - 1
	previousStart := time.Unix(start, 0).Add(-config.duration).Unix()

	current, err := queryRankingTotals(ctx, start, end)
	if err != nil {
		return nil, err
	}
	previous, err := queryRankingTotals(ctx, previousStart, previousEnd)
	if err != nil {
		return nil, err
	}
	metadata, err := rankingMetadata(current, previous)
	if err != nil {
		return nil, err
	}
	totalTokens, err := rankingTotalsSum(current)
	if err != nil {
		return nil, err
	}

	models := buildRankingModels(current, previous, metadata, totalTokens)
	vendors, err := buildRankingVendors(current, previous, metadata, totalTokens)
	if err != nil {
		return nil, err
	}
	allBuckets, err := queryRankingBuckets(ctx, start, end, config.bucketSize, nil)
	if err != nil {
		return nil, err
	}
	modelHistory, err := buildRankingModelHistory(ctx, current, metadata, allBuckets, config, start, end)
	if err != nil {
		return nil, err
	}
	vendorHistory, err := buildRankingVendorHistory(ctx, current, metadata, vendors, allBuckets, config, start, end, totalTokens)
	if err != nil {
		return nil, err
	}
	movers, droppers := buildRankingMovers(models)

	return &RankingsSnapshot{
		Models:             limitRankedModels(models, rankingModelLimit),
		Vendors:            limitRankedVendors(vendors, rankingVendorLimit),
		TopMovers:          movers,
		TopDroppers:        droppers,
		ModelsHistory:      modelHistory,
		VendorShareHistory: vendorHistory,
	}, nil
}

func rankingPeriodFor(period string) (rankingPeriod, error) {
	switch period {
	case "", "week":
		return rankingPeriod{duration: 7 * 24 * time.Hour, bucketSize: 24 * 60 * 60, label: "Jan 2"}, nil
	case "today":
		return rankingPeriod{duration: 24 * time.Hour, bucketSize: 60 * 60, label: "15:04"}, nil
	case "month":
		return rankingPeriod{duration: 30 * 24 * time.Hour, bucketSize: 24 * 60 * 60, label: "Jan 2"}, nil
	case "year":
		return rankingPeriod{duration: 365 * 24 * time.Hour, bucketSize: 7 * 24 * 60 * 60, label: "Jan 2"}, nil
	default:
		return rankingPeriod{}, fmt.Errorf("%w: %q", ErrInvalidRankingPeriod, period)
	}
}

func queryRankingTotals(ctx context.Context, start, end int64) ([]rankingTotal, error) {
	rows := make([]rankingTotal, 0)
	err := model.DB.WithContext(ctx).Table(model.QuotaData{}.TableName()).
		Select("model_name, SUM(token_used) AS total_tokens").
		Where("created_at >= ? AND created_at <= ? AND model_name <> ''", start, end).
		Group("model_name").
		Having("SUM(token_used) > 0").
		Order("total_tokens DESC").
		Order("model_name ASC").
		Limit(rankingAggregateModelLimit + 1).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) > rankingAggregateModelLimit {
		return nil, ErrRankingsTooLarge
	}
	for _, row := range rows {
		if row.TotalTokens <= 0 || row.TotalTokens > rankingMaxSafeInteger || strings.TrimSpace(row.ModelName) != row.ModelName {
			return nil, ErrRankingsTooLarge
		}
	}
	return rows, nil
}

func rankingBucketExpression(dialect string, size int64) (string, error) {
	if size <= 0 || size > int64((365*24*time.Hour)/time.Second) {
		return "", ErrRankingsTooLarge
	}
	switch dialect {
	case "mysql":
		return fmt.Sprintf("FLOOR(created_at / %d) * %d", size, size), nil
	case "postgres":
		return fmt.Sprintf("FLOOR(created_at::numeric / %d)::bigint * %d", size, size), nil
	case "sqlite":
		return fmt.Sprintf("(created_at / %d) * %d", size, size), nil
	default:
		return "", fmt.Errorf("unsupported rankings database dialect %q", dialect)
	}
}

func queryRankingBuckets(ctx context.Context, start, end, size int64, modelNames []string) ([]rankingBucket, error) {
	expression, err := rankingBucketExpression(model.DB.Dialector.Name(), size)
	if err != nil {
		return nil, err
	}
	rows := make([]rankingBucket, 0)
	query := model.DB.WithContext(ctx).Table(model.QuotaData{}.TableName()).
		Where("created_at >= ? AND created_at <= ? AND model_name <> ''", start, end)
	if len(modelNames) > 0 {
		query = query.Select("model_name, "+expression+" AS bucket, SUM(token_used) AS tokens").
			Where("model_name IN ?", modelNames).
			Group("model_name, " + expression)
	} else {
		query = query.Select("'' AS model_name, " + expression + " AS bucket, SUM(token_used) AS tokens").
			Group(expression)
	}
	err = query.Having("SUM(token_used) > 0").Order("bucket ASC").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	maximumBuckets := int(configuredMaximumBuckets(size))
	maximumRows := maximumBuckets
	if len(modelNames) > 0 {
		maximumRows *= len(modelNames)
	}
	if len(rows) > maximumRows {
		return nil, ErrRankingsTooLarge
	}
	for _, row := range rows {
		if row.Tokens <= 0 || row.Tokens > rankingMaxSafeInteger {
			return nil, ErrRankingsTooLarge
		}
	}
	return rows, nil
}

func configuredMaximumBuckets(size int64) int64 {
	const maximumWindowSeconds = int64((365 * 24 * time.Hour) / time.Second)
	return maximumWindowSeconds/size + 2
}

func rankingMetadata(current, previous []rankingTotal) (map[string]rankingMeta, error) {
	names := make(map[string]bool, len(current)+len(previous))
	for _, row := range current {
		names[row.ModelName] = true
	}
	for _, row := range previous {
		names[row.ModelName] = true
	}
	metadata, err := pricingCatalogMetadata(names)
	if err != nil {
		return nil, err
	}
	vendorIDs := make(map[int]struct{})
	for _, item := range metadata {
		if item != nil && item.Status == 1 && item.VendorID > 0 {
			vendorIDs[item.VendorID] = struct{}{}
		}
	}
	vendorNames := make(map[int]rankingMeta, len(vendorIDs))
	if len(vendorIDs) > 0 {
		ids := make([]int, 0, len(vendorIDs))
		for id := range vendorIDs {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		var vendors []model.Vendor
		if err := model.DB.Where("id IN ?", ids).Find(&vendors).Error; err != nil {
			return nil, err
		}
		for _, vendor := range vendors {
			name := strings.TrimSpace(vendor.Name)
			if name != "" {
				vendorNames[vendor.Id] = rankingMeta{vendor: name, icon: vendor.Icon}
			}
		}
	}
	result := make(map[string]rankingMeta, len(names))
	for name := range names {
		item := metadata[name]
		if item != nil && item.Status == 1 {
			if vendor, ok := vendorNames[item.VendorID]; ok {
				result[name] = vendor
				continue
			}
		}
		result[name] = rankingMeta{vendor: rankingUnknownVendor}
	}
	return result, nil
}

func rankingTotalsSum(rows []rankingTotal) (int64, error) {
	var result int64
	for _, row := range rows {
		if row.TotalTokens > rankingMaxSafeInteger-result {
			return 0, ErrRankingsTooLarge
		}
		result += row.TotalTokens
	}
	return result, nil
}

func buildRankingModels(current, previous []rankingTotal, metadata map[string]rankingMeta, total int64) []RankedModel {
	previousRank := make(map[string]int, len(previous))
	previousTokens := make(map[string]int64, len(previous))
	for index, row := range previous {
		previousRank[row.ModelName] = index + 1
		previousTokens[row.ModelName] = row.TotalTokens
	}
	result := make([]RankedModel, 0, len(current))
	for index, row := range current {
		var oldRank *int
		if rank, ok := previousRank[row.ModelName]; ok {
			copyOfRank := rank
			oldRank = &copyOfRank
		}
		meta := metadata[row.ModelName]
		result = append(result, RankedModel{
			Rank: index + 1, PreviousRank: oldRank, ModelName: row.ModelName,
			Vendor: meta.vendor, VendorIcon: meta.icon, Category: "all",
			TotalTokens: row.TotalTokens, Share: rankingShare(row.TotalTokens, total),
			GrowthPct: rankingGrowth(row.TotalTokens, previousTokens[row.ModelName]),
		})
	}
	return result
}

func buildRankingVendors(current, previous []rankingTotal, metadata map[string]rankingMeta, total int64) ([]RankedVendor, error) {
	aggregates := make(map[string]*rankingVendorAggregate)
	ensure := func(meta rankingMeta) *rankingVendorAggregate {
		name := meta.vendor
		if name == "" {
			name = rankingUnknownVendor
		}
		item := aggregates[name]
		if item == nil {
			item = &rankingVendorAggregate{name: name, icon: meta.icon, models: make(map[string]struct{})}
			aggregates[name] = item
		}
		return item
	}
	for _, row := range current {
		item := ensure(metadata[row.ModelName])
		if row.TotalTokens > rankingMaxSafeInteger-item.total {
			return nil, ErrRankingsTooLarge
		}
		item.total += row.TotalTokens
		item.models[row.ModelName] = struct{}{}
		if row.TotalTokens > item.topModelTokens {
			item.topModel = row.ModelName
			item.topModelTokens = row.TotalTokens
		}
	}
	for _, row := range previous {
		item := ensure(metadata[row.ModelName])
		if row.TotalTokens > rankingMaxSafeInteger-item.previous {
			return nil, ErrRankingsTooLarge
		}
		item.previous += row.TotalTokens
	}
	result := make([]RankedVendor, 0, len(aggregates))
	for _, item := range aggregates {
		if item.total == 0 {
			continue
		}
		result = append(result, RankedVendor{
			Vendor: item.name, VendorIcon: item.icon, TotalTokens: item.total,
			Share: rankingShare(item.total, total), GrowthPct: rankingGrowth(item.total, item.previous),
			ModelsCount: len(item.models), TopModel: item.topModel,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TotalTokens == result[j].TotalTokens {
			return result[i].Vendor < result[j].Vendor
		}
		return result[i].TotalTokens > result[j].TotalTokens
	})
	for index := range result {
		result[index].Rank = index + 1
	}
	return result, nil
}

func buildRankingModelHistory(ctx context.Context, current []rankingTotal, metadata map[string]rankingMeta, totals []rankingBucket, config rankingPeriod, start, end int64) (ModelHistorySeries, error) {
	limit := min(len(current), rankingHistoryModelLimit)
	names := make([]string, 0, limit)
	models := make([]ModelHistoryModel, 0, limit+1)
	var selectedTotal int64
	for _, row := range current[:limit] {
		names = append(names, row.ModelName)
		models = append(models, ModelHistoryModel{Name: row.ModelName, Vendor: metadata[row.ModelName].vendor, Total: row.TotalTokens})
		selectedTotal += row.TotalTokens
	}
	if selectedTotal < rankingTotalOrZero(current) {
		models = append(models, ModelHistoryModel{Name: rankingOthers, Vendor: "Various", Total: rankingTotalOrZero(current) - selectedTotal})
	}
	selected, err := queryRankingBuckets(ctx, start, end, config.bucketSize, names)
	if err != nil {
		return ModelHistorySeries{}, err
	}
	byBucket := make(map[int64]map[string]int64)
	for _, row := range selected {
		if byBucket[row.Bucket] == nil {
			byBucket[row.Bucket] = make(map[string]int64)
		}
		byBucket[row.Bucket][row.ModelName] = row.Tokens
	}
	points := make([]ModelHistoryPoint, 0, len(selected)+len(totals))
	for _, total := range totals {
		var selectedInBucket int64
		for _, item := range models {
			if item.Name == rankingOthers {
				continue
			}
			tokens := byBucket[total.Bucket][item.Name]
			if tokens > 0 {
				selectedInBucket += tokens
				points = append(points, ModelHistoryPoint{
					Timestamp: rankingBucketTimestamp(total.Bucket), Label: rankingBucketLabel(total.Bucket, config),
					Model: item.Name, Vendor: item.Vendor, Tokens: tokens,
				})
			}
		}
		if other := total.Tokens - selectedInBucket; other > 0 && len(models) > limit {
			points = append(points, ModelHistoryPoint{
				Timestamp: rankingBucketTimestamp(total.Bucket), Label: rankingBucketLabel(total.Bucket, config),
				Model: rankingOthers, Vendor: "Various", Tokens: other,
			})
		}
	}
	return ModelHistorySeries{Points: points, Models: models, Buckets: len(totals)}, nil
}

func buildRankingVendorHistory(ctx context.Context, current []rankingTotal, metadata map[string]rankingMeta, vendors []RankedVendor, totals []rankingBucket, config rankingPeriod, start, end, totalTokens int64) (VendorShareSeries, error) {
	limit := min(len(vendors), rankingHistoryVendorLimit)
	seriesVendors := make([]VendorShareVendor, 0, limit+1)
	selectedNames := make(map[string]struct{}, limit)
	var selectedTotal int64
	for _, vendor := range vendors[:limit] {
		seriesVendors = append(seriesVendors, VendorShareVendor{Name: vendor.Vendor, Total: vendor.TotalTokens, Share: vendor.Share})
		selectedNames[vendor.Vendor] = struct{}{}
		selectedTotal += vendor.TotalTokens
	}
	if selectedTotal < totalTokens {
		other := totalTokens - selectedTotal
		seriesVendors = append(seriesVendors, VendorShareVendor{Name: rankingOthers, Total: other, Share: rankingShare(other, totalTokens)})
	}
	byBucket := make(map[int64]map[string]int64)
	for _, vendor := range seriesVendors {
		if vendor.Name == rankingOthers {
			continue
		}
		names := make([]string, 0)
		for _, row := range current {
			if metadata[row.ModelName].vendor == vendor.Name {
				names = append(names, row.ModelName)
			}
		}
		rows, err := queryRankingBuckets(ctx, start, end, config.bucketSize, names)
		if err != nil {
			return VendorShareSeries{}, err
		}
		for _, row := range rows {
			if byBucket[row.Bucket] == nil {
				byBucket[row.Bucket] = make(map[string]int64)
			}
			byBucket[row.Bucket][vendor.Name] += row.Tokens
		}
	}
	points := make([]VendorSharePoint, 0, len(totals)*len(seriesVendors))
	for _, bucket := range totals {
		var selectedInBucket int64
		for _, vendor := range seriesVendors {
			if vendor.Name == rankingOthers {
				continue
			}
			tokens := byBucket[bucket.Bucket][vendor.Name]
			if tokens > 0 {
				selectedInBucket += tokens
				points = append(points, VendorSharePoint{
					Timestamp: rankingBucketTimestamp(bucket.Bucket), Label: rankingBucketLabel(bucket.Bucket, config),
					Vendor: vendor.Name, Share: rankingShare(tokens, bucket.Tokens), Tokens: tokens,
				})
			}
		}
		if _, hasOthers := selectedNames[rankingOthers]; !hasOthers {
			other := bucket.Tokens - selectedInBucket
			if other > 0 && len(seriesVendors) > limit {
				points = append(points, VendorSharePoint{
					Timestamp: rankingBucketTimestamp(bucket.Bucket), Label: rankingBucketLabel(bucket.Bucket, config),
					Vendor: rankingOthers, Share: rankingShare(other, bucket.Tokens), Tokens: other,
				})
			}
		}
	}
	return VendorShareSeries{Points: points, Vendors: seriesVendors, Buckets: len(totals)}, nil
}

func buildRankingMovers(models []RankedModel) ([]RankingMover, []RankingMover) {
	movers := make([]RankingMover, 0)
	droppers := make([]RankingMover, 0)
	for _, row := range models {
		if row.PreviousRank == nil {
			continue
		}
		delta := *row.PreviousRank - row.Rank
		if delta == 0 {
			continue
		}
		mover := RankingMover{ModelName: row.ModelName, Vendor: row.Vendor, VendorIcon: row.VendorIcon, RankDelta: delta, CurrentRank: row.Rank, GrowthPct: row.GrowthPct}
		if delta > 0 {
			movers = append(movers, mover)
		} else {
			droppers = append(droppers, mover)
		}
	}
	sort.Slice(movers, func(i, j int) bool {
		if movers[i].RankDelta == movers[j].RankDelta {
			return movers[i].GrowthPct > movers[j].GrowthPct
		}
		return movers[i].RankDelta > movers[j].RankDelta
	})
	sort.Slice(droppers, func(i, j int) bool {
		if droppers[i].RankDelta == droppers[j].RankDelta {
			return droppers[i].GrowthPct < droppers[j].GrowthPct
		}
		return droppers[i].RankDelta < droppers[j].RankDelta
	})
	return limitRankingMovers(movers), limitRankingMovers(droppers)
}

func rankingShare(value, total int64) float64 {
	if value <= 0 || total <= 0 {
		return 0
	}
	return math.Round((float64(value)/float64(total))*10_000) / 10_000
}

func rankingGrowth(current, previous int64) float64 {
	if previous <= 0 {
		if current > 0 {
			return 100
		}
		return 0
	}
	return math.Round(((float64(current-previous)/float64(previous))*100)*10_000) / 10_000
}

func rankingBucketTimestamp(bucket int64) string {
	return time.Unix(bucket, 0).UTC().Format(time.RFC3339)
}

func rankingBucketLabel(bucket int64, config rankingPeriod) string {
	return time.Unix(bucket, 0).Format(config.label)
}

func rankingTotalOrZero(rows []rankingTotal) int64 {
	total, _ := rankingTotalsSum(rows)
	return total
}

func limitRankedModels(rows []RankedModel, limit int) []RankedModel {
	if len(rows) <= limit {
		return rows
	}
	return rows[:limit]
}

func limitRankedVendors(rows []RankedVendor, limit int) []RankedVendor {
	if len(rows) <= limit {
		return rows
	}
	return rows[:limit]
}

func limitRankingMovers(rows []RankingMover) []RankingMover {
	if len(rows) <= rankingMoverLimit {
		return rows
	}
	return rows[:rankingMoverLimit]
}
