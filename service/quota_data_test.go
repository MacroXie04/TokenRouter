package service

import (
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

	SaveQuotaDataCache()

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
	SaveQuotaDataCache()
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
}
