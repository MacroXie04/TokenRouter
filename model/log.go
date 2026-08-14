package model

import "gorm.io/gorm"

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
	Id              int    `json:"id" gorm:"primaryKey"`
	ModelName       string `json:"model_name" gorm:"type:varchar(128);uniqueIndex:idx_perf_model_group_bucket,priority:1"`
	Group           string `json:"group" gorm:"type:varchar(64);uniqueIndex:idx_perf_model_group_bucket,priority:2"`
	BucketTs        int64  `json:"bucket_ts" gorm:"uniqueIndex:idx_perf_model_group_bucket,priority:3;index:idx_perf_bucket_ts"`
	RequestCount    int64  `json:"request_count"`
	SuccessCount    int64  `json:"success_count"`
	TotalLatencyMs  int64  `json:"total_latency_ms"`
	TtftSumMs       int64  `json:"ttft_sum_ms"`
	TtftCount       int64  `json:"ttft_count"`
	OutputTokens    int64  `json:"output_tokens"`
	GenerationMs    int64  `json:"generation_ms"`
}

func (PerfMetric) TableName() string { return "perf_metrics" }
