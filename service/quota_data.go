package service

import (
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// gormExprAdd builds a `column = column + n` update expression (atomic
// counter increments, safe under concurrent writers).
func gormExprAdd(column string, n int) any {
	return gorm.Expr(column+" + ?", n)
}

// Data-export option keys (reference option names).
const (
	DataExportEnabledOption  = "DataExportEnabled"
	DataExportIntervalOption = "DataExportInterval"
)

const (
	defaultDataExportEnabled  = true
	defaultDataExportInterval = 5 // minutes
)

// DataExportEnabled reports whether the per-hour usage histogram is recorded
// (reference option DataExportEnabled, default true).
func DataExportEnabled() bool {
	return setting.GetOptionBool(DataExportEnabledOption, defaultDataExportEnabled)
}

// DataExportIntervalMinutes is the histogram flush interval in minutes
// (reference option DataExportInterval, default 5).
func DataExportIntervalMinutes() int {
	return setting.GetOptionIntOrDefault(DataExportIntervalOption, defaultDataExportInterval)
}

// quotaDataCache aggregates per-hour usage histogram rows in memory between
// flushes (reference: CacheQuotaData + logQuotaDataCache aggregation).
var (
	quotaDataCache     = make(map[string]*model.QuotaData)
	quotaDataCacheLock sync.Mutex
)

// ResetQuotaDataCache clears the in-memory histogram cache (test isolation).
func ResetQuotaDataCache() {
	quotaDataCacheLock.Lock()
	defer quotaDataCacheLock.Unlock()
	quotaDataCache = make(map[string]*model.QuotaData)
}

func quotaDataCacheKey(qd *model.QuotaData) string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%d\x00%s\x00%d\x00%d\x00%s",
		qd.UserID, qd.Username, qd.ModelName, qd.CreatedAt, qd.UseGroup,
		qd.TokenID, qd.ChannelID, qd.NodeName)
}

// LogQuotaData aggregates one consumption into the per-hour histogram cache
// (hour-bucketed; the flush loop persists it to quota_data).
func LogQuotaData(userId int, username, modelName, group string, tokenId, channelId int,
	quota, tokenUsed int, createdAt int64) {
	// Bucket to the hour (reference: only precise to the hour).
	createdAt -= createdAt % 3600
	qd := &model.QuotaData{
		UserID:    userId,
		Username:  username,
		ModelName: modelName,
		CreatedAt: createdAt,
		UseGroup:  group,
		TokenID:   tokenId,
		ChannelID: channelId,
		NodeName:  NodeName(),
		Count:     1,
		Quota:     quota,
		TokenUsed: tokenUsed,
	}
	quotaDataCacheLock.Lock()
	defer quotaDataCacheLock.Unlock()
	key := quotaDataCacheKey(qd)
	if cached, ok := quotaDataCache[key]; ok {
		cached.Count += qd.Count
		cached.Quota += qd.Quota
		cached.TokenUsed += qd.TokenUsed
		return
	}
	quotaDataCache[key] = qd
}

// SaveQuotaDataCache flushes the in-memory histogram into the quota_data
// table: existing hour-rows are incremented, new rows are inserted, and the
// cache is reset (reference semantics).
func SaveQuotaDataCache() {
	quotaDataCacheLock.Lock()
	defer quotaDataCacheLock.Unlock()
	size := len(quotaDataCache)
	for _, qd := range quotaDataCache {
		where := "user_id = ? and username = ? and model_name = ? and created_at = ? and use_group = ? and token_id = ? and channel_id = ? and node_name = ?"
		args := []any{qd.UserID, qd.Username, qd.ModelName, qd.CreatedAt, qd.UseGroup, qd.TokenID, qd.ChannelID, qd.NodeName}
		var existing model.QuotaData
		err := model.DB.Table(model.QuotaData{}.TableName()).Where(where, args...).First(&existing).Error
		if err == nil {
			// Atomic increments: concurrent flushes (multi-node) and direct
			// writers must not lose counts.
			_ = model.DB.Table(model.QuotaData{}.TableName()).Where(where, args...).Updates(map[string]any{
				"count":      gormExprAdd("count", qd.Count),
				"quota":      gormExprAdd("quota", qd.Quota),
				"token_used": gormExprAdd("token_used", qd.TokenUsed),
			})
			continue
		}
		_ = model.DB.Table(model.QuotaData{}.TableName()).Create(qd)
	}
	quotaDataCache = make(map[string]*model.QuotaData)
	if size > 0 {
		common.SysLog(fmt.Sprintf("保存数据看板数据成功，共保存%d条数据", size))
	}
}

// StartQuotaDataFlusher launches the periodic histogram flush loop (reference
// UpdateQuotaData): flushes every DataExportInterval minutes when data export
// is enabled.
func StartQuotaDataFlusher() {
	go func() {
		for {
			if DataExportEnabled() {
				SaveQuotaDataCache()
			}
			time.Sleep(time.Duration(DataExportIntervalMinutes()) * time.Minute)
		}
	}()
}
