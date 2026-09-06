package service

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// setupQuotaDataTestDB provisions a fresh in-memory DB with the histogram
// and option tables.
func setupQuotaDataTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.QuotaData{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
}

// TestQuotaDataHistogramWritePath covers the consume-time aggregation and the
// flush cycle: hour bucketing, in-cache merging, insert-then-increment, and
// cache reset.
func TestQuotaDataHistogramWritePath(t *testing.T) {
	setupQuotaDataTestDB(t)
	defer ResetQuotaDataCache()

	base := common.NowTimestamp() - common.NowTimestamp()%3600
	// Two consumptions in the same hour merge into one cache entry.
	LogQuotaData(1, "alice", "gpt-4o", "default", 7, 3, 100, 120, base)
	LogQuotaData(1, "alice", "gpt-4o", "default", 7, 3, 50, 60, base+1800)
	// A different hour, model, or token stays separate.
	LogQuotaData(1, "alice", "gpt-4o", "default", 7, 3, 25, 30, base+3600)
	LogQuotaData(1, "alice", "claude-sonnet-4", "vip", 8, 4, 40, 10, base)
	LogQuotaData(2, "bob", "gpt-4o", "default", 9, 3, 70, 20, base)

	require.NoError(t, SaveQuotaDataCacheChecked())

	var rows []model.QuotaData
	require.NoError(t, model.DB.Find(&rows).Error)
	require.Len(t, rows, 4, "4 distinct histogram keys")
	mergedKey := quotaDataCacheKey(&model.QuotaData{
		UserID: 1, Username: "alice", ModelName: "gpt-4o",
		CreatedAt: base - base%3600, UseGroup: "default", TokenID: 7, ChannelID: 3,
		NodeName: NodeName(),
	})
	byKey := map[string]model.QuotaData{}
	for _, r := range rows {
		byKey[quotaDataCacheKey(&r)] = r
	}
	merged, ok := byKey[mergedKey]
	require.True(t, ok, "merged same-hour row present")
	assert.Equal(t, 2, merged.Count)
	assert.Equal(t, 150, merged.Quota)
	assert.Equal(t, 180, merged.TokenUsed)
	assert.Equal(t, base-base%3600, merged.CreatedAt, "hour bucketing: base+1800 lands in the same bucket")

	// A second flush with more traffic increments the existing rows instead
	// of inserting duplicates.
	LogQuotaData(1, "alice", "gpt-4o", "default", 7, 3, 10, 5, base)
	require.NoError(t, SaveQuotaDataCacheChecked())
	require.NoError(t, model.DB.Find(&rows).Error)
	require.Len(t, rows, 4, "still 4 keys after the incremental flush")
	for _, r := range rows {
		if quotaDataCacheKey(&r) == mergedKey {
			assert.Equal(t, 3, r.Count)
			assert.Equal(t, 160, r.Quota)
			return
		}
	}
	t.Fatal("merged row not found after incremental flush")
}

func TestSaveQuotaDataCacheRetainsQueryFailureForRetry(t *testing.T) {
	setupQuotaDataTestDB(t)
	defer ResetQuotaDataCache()
	base := common.NowTimestamp() - common.NowTimestamp()%3600
	require.NoError(t, LogQuotaDataChecked(1, "alice", "gpt-4o", "default", 7, 3, 25, 30, base))

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_quota_data_query"
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if fail.Load() && tx.Statement.Table == (model.QuotaData{}).TableName() {
			tx.AddError(errors.New("injected quota data query failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callbackName) })

	require.ErrorContains(t, SaveQuotaDataCacheChecked(), "injected quota data query failure")
	quotaDataCacheLock.Lock()
	assert.Len(t, quotaDataCache, 1, "failed entries must remain cached")
	quotaDataCacheLock.Unlock()

	fail.Store(false)
	require.NoError(t, SaveQuotaDataCacheChecked())
	var rows []model.QuotaData
	require.NoError(t, model.DB.Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, 1, rows[0].Count)
	assert.Equal(t, 25, rows[0].Quota)
	assert.Equal(t, 30, rows[0].TokenUsed)
	quotaDataCacheLock.Lock()
	assert.Empty(t, quotaDataCache)
	quotaDataCacheLock.Unlock()
}

func TestSaveQuotaDataCacheRetainsUpdateFailureAndRetriesOnce(t *testing.T) {
	setupQuotaDataTestDB(t)
	defer ResetQuotaDataCache()
	base := common.NowTimestamp() - common.NowTimestamp()%3600
	seed := model.QuotaData{
		UserID: 1, Username: "alice", ModelName: "gpt-4o", CreatedAt: base,
		UseGroup: "default", TokenID: 7, ChannelID: 3, NodeName: NodeName(),
		Count: 2, Quota: 100, TokenUsed: 120,
	}
	require.NoError(t, model.DB.Create(&seed).Error)
	require.NoError(t, LogQuotaDataChecked(1, "alice", "gpt-4o", "default", 7, 3, 25, 30, base))

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_quota_data_update"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if fail.Load() && tx.Statement.Table == (model.QuotaData{}).TableName() {
			tx.AddError(errors.New("injected quota data update failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	require.ErrorContains(t, SaveQuotaDataCacheChecked(), "injected quota data update failure")
	var got model.QuotaData
	require.NoError(t, model.DB.First(&got, seed.Id).Error)
	assert.Equal(t, 2, got.Count)
	assert.Equal(t, 100, got.Quota)
	assert.Equal(t, 120, got.TokenUsed)

	fail.Store(false)
	require.NoError(t, SaveQuotaDataCacheChecked())
	require.NoError(t, model.DB.First(&got, seed.Id).Error)
	assert.Equal(t, 3, got.Count, "the retained delta must be applied exactly once")
	assert.Equal(t, 125, got.Quota)
	assert.Equal(t, 150, got.TokenUsed)
	quotaDataCacheLock.Lock()
	assert.Empty(t, quotaDataCache)
	quotaDataCacheLock.Unlock()
}

func TestLogQuotaDataCheckedRejectsAggregateOverflow(t *testing.T) {
	setupQuotaDataTestDB(t)
	defer ResetQuotaDataCache()
	base := common.NowTimestamp() - common.NowTimestamp()%3600
	require.NoError(t, LogQuotaDataChecked(1, "alice", "gpt-4o", "default", 7, 3, int(common.MaxQuota), 1, base))
	require.ErrorIs(t, LogQuotaDataChecked(1, "alice", "gpt-4o", "default", 7, 3, 1, 1, base), ErrQuotaDataOverflow)

	quotaDataCacheLock.Lock()
	require.Len(t, quotaDataCache, 1)
	for _, cached := range quotaDataCache {
		assert.Equal(t, int(common.MaxQuota), cached.Quota)
		assert.Equal(t, 1, cached.Count)
		assert.Equal(t, 1, cached.TokenUsed)
	}
	quotaDataCacheLock.Unlock()
}

// TestDataExportOptionGate verifies the reference option names and defaults.
func TestDataExportOptionGate(t *testing.T) {
	setupQuotaDataTestDB(t)
	require.NoError(t, setting.UpdateOption(DataExportEnabledOption, ""))
	require.NoError(t, setting.UpdateOption(DataExportIntervalOption, ""))
	defer func() {
		_ = setting.UpdateOption(DataExportEnabledOption, "")
		_ = setting.UpdateOption(DataExportIntervalOption, "")
	}()
	assert.True(t, DataExportEnabled(), "default enabled")
	assert.Equal(t, 5, DataExportIntervalMinutes(), "default 5 minutes")

	require.NoError(t, setting.UpdateOption(DataExportEnabledOption, "false"))
	assert.False(t, DataExportEnabled())
	require.NoError(t, setting.UpdateOption(DataExportIntervalOption, "12"))
	assert.Equal(t, 12, DataExportIntervalMinutes())
	for _, invalid := range []string{"0", "-1", "1441", "9223372036854775807", "invalid"} {
		require.NoError(t, setting.UpdateOption(DataExportIntervalOption, invalid))
		assert.Equal(t, defaultDataExportInterval, DataExportIntervalMinutes(), invalid)
	}
	for _, boundary := range []struct {
		value string
		want  int
	}{{"1", 1}, {"1440", 1440}} {
		require.NoError(t, setting.UpdateOption(DataExportIntervalOption, boundary.value))
		assert.Equal(t, boundary.want, DataExportIntervalMinutes())
	}
}
