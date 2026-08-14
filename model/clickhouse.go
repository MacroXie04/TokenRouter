package model

import (
	"fmt"
	"strings"

	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
)

// clickhouseLogDDL creates the logs MergeTree table, partitioned by month and
// ordered by (created_at, id). ClickHouse does not support GORM AutoMigrate or
// soft-delete, so logs are written with a dedicated raw INSERT.
const clickhouseLogDDL = `
CREATE TABLE IF NOT EXISTS logs (
    id UInt64,
    user_id Int32,
    created_at Int64,
    type Int32,
    content String,
    username String,
    token_name String,
    model_name String,
    quota Int32,
    prompt_tokens Int32,
    completion_tokens Int32,
    use_time Int32,
    is_stream UInt8,
    channel_id Int32,
    channel_name String,
    token_id Int32,
    ` + "`group`" + ` String,
    ip String,
    request_id String,
    upstream_request_id String,
    other String
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(created_at))
ORDER BY (created_at, id)
`

// openClickHouseLog connects to a ClickHouse log database and creates the logs
// MergeTree table.
func openClickHouseLog(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(clickhouse.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.Exec(clickhouseLogDDL).Error; err != nil {
		return nil, fmt.Errorf("create clickhouse logs table: %w", err)
	}
	return db, nil
}

// UsingClickHouseLog reports whether the log database is ClickHouse.
func UsingClickHouseLog() bool {
	return isClickHouseDSN(common.GetEnv("LOG_SQL_DSN", ""))
}

// InsertClickHouseLog writes a Log via a raw INSERT (ClickHouse has no
// soft-delete and no auto-increment primary key).
func InsertClickHouseLog(l *Log) error {
	sql := `INSERT INTO logs (id, user_id, created_at, type, content, username, token_name, model_name, quota, prompt_tokens, completion_tokens, use_time, is_stream, channel_id, channel_name, token_id, ` + "`group`" + `, ip, request_id, upstream_request_id, other) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	isStream := uint8(0)
	if l.IsStream {
		isStream = 1
	}
	return LOG_DB.Exec(sql,
		l.Id, l.UserId, l.CreatedAt, l.Type, l.Content, l.Username, l.TokenName,
		l.ModelName, l.Quota, l.PromptTokens, l.CompletionTokens, l.UseTime,
		isStream, l.ChannelId, l.ChannelName, l.TokenId, l.Group, l.Ip,
		l.RequestId, l.UpstreamRequestId, l.Other,
	).Error
}

// isClickHouseDSN reports whether a DSN targets ClickHouse.
func isClickHouseDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "clickhouse://")
}
