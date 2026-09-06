package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

const (
	defaultOldLogDeleteBatchSize = 100
	maxOldLogDeleteBatchSize     = 1_000
	clickHouseCountOldLogSQL     = "SELECT count() FROM logs WHERE created_at < ?"
	clickHouseDeleteOldLogSQL    = "ALTER TABLE logs DELETE WHERE created_at < ? SETTINGS mutations_sync = 1"
)

// Log is a request/consumption log, written to the log database.
type Log struct {
	Id                int            `json:"id" gorm:"primaryKey;index:idx_created_at_id,priority:2;index:idx_user_id_id,priority:2"`
	AuditEventId      *string        `json:"-" gorm:"type:varchar(64);uniqueIndex:ux_logs_audit_event_id"`
	UserId            int            `json:"user_id" gorm:"index;index:idx_user_id_id,priority:1"`
	CreatedAt         int64          `json:"created_at" gorm:"bigint;index:idx_created_at_id,priority:1;index:idx_created_at_type"`
	Type              int            `json:"type" gorm:"index:idx_created_at_type"`
	Content           string         `json:"content"`
	Username          string         `json:"username" gorm:"index;index:index_username_model_name,priority:2;default:''"`
	TokenName         string         `json:"token_name" gorm:"index;default:''"`
	ModelName         string         `json:"model_name" gorm:"index;index:index_username_model_name,priority:1;default:''"`
	Quota             int            `json:"quota" gorm:"default:0"`
	PromptTokens      int            `json:"prompt_tokens" gorm:"default:0"`
	CompletionTokens  int            `json:"completion_tokens" gorm:"default:0"`
	UseTime           int            `json:"use_time" gorm:"default:0"`
	IsStream          bool           `json:"is_stream"`
	ChannelId         int            `json:"channel_id" gorm:"index"`
	ChannelName       string         `json:"channel_name"`
	TokenId           int            `json:"token_id" gorm:"default:0;index"`
	Group             string         `json:"group" gorm:"index"`
	Ip                string         `json:"ip" gorm:"index;default:''"`
	RequestId         string         `json:"request_id" gorm:"type:varchar(64);index:idx_logs_request_id;default:''"`
	UpstreamRequestId string         `json:"upstream_request_id" gorm:"type:varchar(128);index:idx_logs_upstream_request_id;default:''"`
	Other             string         `json:"other"`
	DeletedAt         gorm.DeletedAt `json:"-" gorm:"index"`
}

func (Log) TableName() string { return "logs" }

// QuotaData is a per-model usage histogram used for billing aggregation.
type QuotaData struct {
	Id        int    `json:"id" gorm:"primaryKey"`
	UserID    int    `json:"user_id" gorm:"index"`
	Username  string `json:"username" gorm:"index:idx_qdt_model_user_name,priority:2;size:64;default:''"`
	ModelName string `json:"model_name" gorm:"index:idx_qdt_model_user_name,priority:1;size:64;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;index:idx_qdt_created_at,priority:2"`
	UseGroup  string `json:"use_group" gorm:"index;size:64;default:''"`
	TokenID   int    `json:"token_id" gorm:"index;default:0"`
	ChannelID int    `json:"channel_id" gorm:"index;default:0"`
	NodeName  string `json:"node_name" gorm:"index;size:64;default:''"`
	TokenUsed int    `json:"token_used" gorm:"default:0"`
	Count     int    `json:"count" gorm:"default:0"`
	Quota     int    `json:"quota" gorm:"default:0"`
}

func (QuotaData) TableName() string { return "quota_data" }

// PerfMetric aggregates relay performance metrics.
type PerfMetric struct {
	Id             int    `json:"id" gorm:"primaryKey"`
	ModelName      string `json:"model_name" gorm:"type:varchar(255);uniqueIndex:idx_perf_model_group_bucket,priority:1"`
	Group          string `json:"group" gorm:"type:varchar(64);uniqueIndex:idx_perf_model_group_bucket,priority:2"`
	BucketTs       int64  `json:"bucket_ts" gorm:"uniqueIndex:idx_perf_model_group_bucket,priority:3;index:idx_perf_bucket_ts"`
	RequestCount   int64  `json:"-"`
	SuccessCount   int64  `json:"-"`
	TotalLatencyMs int64  `json:"-"`
	TtftSumMs      int64  `json:"-"`
	TtftCount      int64  `json:"-"`
	OutputTokens   int64  `json:"-"`
	GenerationMs   int64  `json:"-"`
}

func (PerfMetric) TableName() string { return "perf_metrics" }

// CountOldLog counts log rows older than the target timestamp.
func CountOldLog(ctx context.Context, targetTimestamp int64) (int64, error) {
	if ctx == nil {
		return 0, errors.New("log cleanup context is nil")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if LOG_DB == nil {
		return 0, errors.New("log database is nil")
	}
	var total int64
	if UsingClickHouseLog() {
		// ClickHouse's logs schema deliberately has no GORM soft-delete column.
		// Keep this raw so GORM cannot append a deleted_at predicate.
		if err := LOG_DB.WithContext(ctx).Raw(clickHouseCountOldLogSQL, targetTimestamp).Scan(&total).Error; err != nil {
			return 0, err
		}
		return total, nil
	}
	// Include tombstones left by older cleanup builds so the physical-delete
	// pass can reclaim those rows instead of permanently hiding them.
	if err := LOG_DB.WithContext(ctx).Unscoped().Model(&Log{}).
		Where("created_at < ?", targetTimestamp).Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// DeleteOldLogBatch deletes up to limit log rows older than the target
// timestamp (ClickHouse: one synchronous mutation regardless of limit).
func DeleteOldLogBatch(ctx context.Context, targetTimestamp int64, limit int) (int64, error) {
	if ctx == nil {
		return 0, errors.New("log cleanup context is nil")
	}
	if limit <= 0 {
		limit = defaultOldLogDeleteBatchSize
	}
	if limit > maxOldLogDeleteBatchSize {
		return 0, fmt.Errorf("log cleanup batch size exceeds maximum %d", maxOldLogDeleteBatchSize)
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if LOG_DB == nil {
		return 0, errors.New("log database is nil")
	}
	if UsingClickHouseLog() {
		total, err := CountOldLog(ctx, targetTimestamp)
		if err != nil {
			return 0, err
		}
		if total == 0 {
			return 0, nil
		}
		if err := LOG_DB.WithContext(ctx).Exec(clickHouseDeleteOldLogSQL, targetTimestamp).Error; err != nil {
			return 0, err
		}
		return total, nil
	}
	var ids []int
	if err := LOG_DB.WithContext(ctx).Unscoped().Model(&Log{}).
		Select("id").
		Where("created_at < ?", targetTimestamp).
		Order("id asc").
		Limit(limit).
		Scan(&ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := LOG_DB.WithContext(ctx).Unscoped().Where("id IN ?", ids).Delete(&Log{})
	return result.RowsAffected, result.Error
}
