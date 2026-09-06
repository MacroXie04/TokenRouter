package service

import (
	"errors"
	"fmt"
	"sort"
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
	maxDataExportInterval     = 24 * 60
)

// DataExportEnabled reports whether the per-hour usage histogram is recorded
// (reference option DataExportEnabled, default true).
func DataExportEnabled() bool {
	return setting.GetOptionBool(DataExportEnabledOption, defaultDataExportEnabled)
}

// DataExportIntervalMinutes is the histogram flush interval in minutes
// (reference option DataExportInterval, default 5).
func DataExportIntervalMinutes() int {
	interval := setting.GetOptionIntOrDefault(DataExportIntervalOption, defaultDataExportInterval)
	if interval < 1 || interval > maxDataExportInterval {
		return defaultDataExportInterval
	}
	return interval
}

// quotaDataCache aggregates per-hour usage histogram rows in memory between
// flushes (reference: CacheQuotaData + logQuotaDataCache aggregation).
var (
	quotaDataCache     = make(map[string]*model.QuotaData)
	quotaDataCacheLock sync.Mutex
)

// ErrQuotaDataOverflow reports an invalid or overflowing in-memory/durable
// histogram counter. Quota data uses the same persisted integer bounds as the
// rest of the accounting system.
var ErrQuotaDataOverflow = errors.New("quota data counter overflow")

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
	if err := LogQuotaDataChecked(userId, username, modelName, group, tokenId, channelId, quota, tokenUsed, createdAt); err != nil {
		common.SysError("cache quota data failed: " + err.Error())
	}
}

// LogQuotaDataChecked aggregates one consumption and reports invalid or
// overflowing counters instead of wrapping them in memory.
func LogQuotaDataChecked(userId int, username, modelName, group string, tokenId, channelId int,
	quota, tokenUsed int, createdAt int64) error {
	if userId <= 0 {
		return fmt.Errorf("%w: invalid user id %d", ErrQuotaDataOverflow, userId)
	}
	if err := validateQuotaAmount(quota); err != nil {
		return fmt.Errorf("%w: quota: %v", ErrQuotaDataOverflow, err)
	}
	if err := validateQuotaAmount(tokenUsed); err != nil {
		return fmt.Errorf("%w: token usage: %v", ErrQuotaDataOverflow, err)
	}
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
		newCount, countOK := common.AddQuotaWithinBounds(cached.Count, qd.Count)
		newQuota, quotaOK := common.AddQuotaWithinBounds(cached.Quota, qd.Quota)
		newTokenUsed, tokenOK := common.AddQuotaWithinBounds(cached.TokenUsed, qd.TokenUsed)
		if !countOK || !quotaOK || !tokenOK {
			return fmt.Errorf("%w for key %q", ErrQuotaDataOverflow, key)
		}
		cached.Count = newCount
		cached.Quota = newQuota
		cached.TokenUsed = newTokenUsed
		return nil
	}
	quotaDataCache[key] = qd
	return nil
}

// SaveQuotaDataCache flushes the in-memory histogram into the quota_data
// table. Failed entries remain cached for a later retry, and every persistence
// failure is reported instead of being silently discarded.
func SaveQuotaDataCache() {
	if err := SaveQuotaDataCacheChecked(); err != nil {
		common.SysError("save quota data cache failed: " + err.Error())
	}
}

// SaveQuotaDataCacheChecked persists every cached histogram entry it can.
// Successful entries are removed; failed entries stay in memory and their
// errors are joined so callers can observe partial flushes.
func SaveQuotaDataCacheChecked() error {
	quotaDataCacheLock.Lock()
	defer quotaDataCacheLock.Unlock()
	if len(quotaDataCache) == 0 {
		return nil
	}
	if model.DB == nil {
		return errors.New("quota data database is nil")
	}
	keys := make([]string, 0, len(quotaDataCache))
	for key := range quotaDataCache {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	flushed := 0
	flushErrors := make([]error, 0)
	for _, key := range keys {
		qd := quotaDataCache[key]
		if err := flushQuotaDataEntry(qd); err != nil {
			flushErrors = append(flushErrors, fmt.Errorf("quota data key %q: %w", key, err))
			continue
		}
		delete(quotaDataCache, key)
		flushed++
	}
	if flushed > 0 {
		common.SysLog(fmt.Sprintf("保存数据看板数据成功，共保存%d条数据", flushed))
	}
	return errors.Join(flushErrors...)
}

func flushQuotaDataEntry(qd *model.QuotaData) error {
	if qd == nil || qd.Count <= 0 || int64(qd.Count) > common.MaxQuota ||
		qd.Quota < 0 || int64(qd.Quota) > common.MaxQuota ||
		qd.TokenUsed < 0 || int64(qd.TokenUsed) > common.MaxQuota {
		return ErrQuotaDataOverflow
	}
	where := "user_id = ? and username = ? and model_name = ? and created_at = ? and use_group = ? and token_id = ? and channel_id = ? and node_name = ?"
	args := []any{qd.UserID, qd.Username, qd.ModelName, qd.CreatedAt, qd.UseGroup, qd.TokenID, qd.ChannelID, qd.NodeName}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var existing model.QuotaData
		err := subscriptionLockForUpdate(tx.Table(model.QuotaData{}.TableName())).Where(where, args...).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Table(model.QuotaData{}.TableName()).Create(qd).Error; err != nil {
				return fmt.Errorf("create histogram row: %w", err)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("load histogram row: %w", err)
		}
		if _, ok := common.AddQuotaWithinBounds(existing.Count, qd.Count); !ok {
			return ErrQuotaDataOverflow
		}
		if _, ok := common.AddQuotaWithinBounds(existing.Quota, qd.Quota); !ok {
			return ErrQuotaDataOverflow
		}
		if _, ok := common.AddQuotaWithinBounds(existing.TokenUsed, qd.TokenUsed); !ok {
			return ErrQuotaDataOverflow
		}
		result := tx.Table(model.QuotaData{}.TableName()).Where(where, args...).
			Where("count >= 0 and count <= ? and quota >= 0 and quota <= ? and token_used >= 0 and token_used <= ?",
				common.MaxQuota-int64(qd.Count), common.MaxQuota-int64(qd.Quota), common.MaxQuota-int64(qd.TokenUsed)).
			Updates(map[string]any{
				"count":      gormExprAdd("count", qd.Count),
				"quota":      gormExprAdd("quota", qd.Quota),
				"token_used": gormExprAdd("token_used", qd.TokenUsed),
			})
		if result.Error != nil {
			return fmt.Errorf("update histogram row: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrQuotaDataOverflow
		}
		return nil
	})
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
