package model

import (
	"context"

	"gorm.io/gorm"
)

// Log is a request/consumption log, written to the log database.
type Log struct {
	Id                int            `json:"id" gorm:"primaryKey"`
	UserId            int            `json:"user_id" gorm:"index;index:idx_user_id_id,priority:1"`
	CreatedAt         int64          `json:"created_at" gorm:"index:idx_created_at_id,priority:1;index:idx_created_at_type,priority:1"`
	Type              int            `json:"type" gorm:"index:idx_created_at_type,priority:2"`
	Content           string         `json:"content" gorm:"type:text"`
	Username          string         `json:"username" gorm:"index;index:index_username_model_name,priority:1;type:varchar(64)"`
	TokenName         string         `json:"token_name" gorm:"index;type:varchar(64)"`
	ModelName         string         `json:"model_name" gorm:"index:index_username_model_name,priority:2;type:varchar(128)"`
	Quota             int            `json:"quota"`
	PromptTokens      int            `json:"prompt_tokens"`
	CompletionTokens  int            `json:"completion_tokens"`
	UseTime           int            `json:"use_time"`
	IsStream          bool           `json:"is_stream"`
	ChannelId         int            `json:"channel_id" gorm:"index"`
	ChannelName       string         `json:"channel_name"`
	TokenId           int            `json:"token_id" gorm:"index"`
	Group             string         `json:"group" gorm:"index;type:varchar(64)"`
	Ip                string         `json:"ip" gorm:"index;type:varchar(64)"`
	RequestId         string         `json:"request_id" gorm:"type:varchar(64);index"`
	UpstreamRequestId string         `json:"upstream_request_id" gorm:"type:varchar(128);index"`
	Other             string         `json:"other" gorm:"type:text"`
	DeletedAt         gorm.DeletedAt `json:"-" gorm:"index"`
}

func (Log) TableName() string { return "logs" }

// QuotaData is a per-model usage histogram used for billing aggregation.
type QuotaData struct {
	Id        int    `json:"id" gorm:"primaryKey"`
	UserID    int    `json:"user_id" gorm:"index"`
	Username  string `json:"username" gorm:"index:idx_qdt_model_user_name,priority:2;type:varchar(64)"`
	ModelName string `json:"model_name" gorm:"index:idx_qdt_model_user_name,priority:1;type:varchar(128)"`
	CreatedAt int64  `json:"created_at" gorm:"index:idx_qdt_created_at"`
	UseGroup  string `json:"use_group" gorm:"index;type:varchar(64)"`
	TokenID   int    `json:"token_id" gorm:"index"`
	ChannelID int    `json:"channel_id" gorm:"index"`
	NodeName  string `json:"node_name" gorm:"index;type:varchar(128)"`
	TokenUsed int    `json:"token_used"`
	Count     int    `json:"count"`
	Quota     int    `json:"quota"`
}

func (QuotaData) TableName() string { return "quota_data" }

// PerfMetric aggregates relay performance metrics.
type PerfMetric struct {
	Id             int    `json:"id" gorm:"primaryKey"`
	ModelName      string `json:"model_name" gorm:"type:varchar(128);uniqueIndex:idx_perf_model_group_bucket,priority:1"`
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
	var total int64
	if err := LOG_DB.WithContext(ctx).Model(&Log{}).Where("created_at < ?", targetTimestamp).Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// DeleteOldLogBatch deletes up to limit log rows older than the target
// timestamp (ClickHouse: one synchronous mutation regardless of limit).
func DeleteOldLogBatch(ctx context.Context, targetTimestamp int64, limit int) (int64, error) {
	if limit <= 0 {
		limit = 100
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if UsingClickHouseLog() {
		total, err := CountOldLog(ctx, targetTimestamp)
		if err != nil {
			return 0, err
		}
		if total == 0 {
			return 0, nil
		}
		if err := LOG_DB.WithContext(ctx).Exec(
			"ALTER TABLE logs DELETE WHERE created_at < ? SETTINGS mutations_sync = 1",
			targetTimestamp,
		).Error; err != nil {
			return 0, err
		}
		return total, nil
	}
	var ids []int
	if err := LOG_DB.WithContext(ctx).Model(&Log{}).
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
	result := LOG_DB.WithContext(ctx).Where("id IN ?", ids).Delete(&Log{})
	return result.RowsAffected, result.Error
}
