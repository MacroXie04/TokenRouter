package store

import (
	"context"
	"fmt"
	clickhouseclient "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/url"
	"strings"
)

// clickhouseLogDDL creates the logs MergeTree table, partitioned by month and
// ordered by (created_at, id). ClickHouse does not support GORM AutoMigrate or
// soft-delete, so logs are written with a dedicated raw INSERT.
const clickhouseLogDDL = `
CREATE TABLE IF NOT EXISTS logs (
    id UInt64 DEFAULT 0,
    audit_event_id Nullable(String),
    user_id Int32 DEFAULT 0,
    created_at Int64 DEFAULT 0,
    type Int32 DEFAULT 0,
    content String DEFAULT '',
    username String DEFAULT '',
    token_name String DEFAULT '',
    model_name String DEFAULT '',
    quota Int32 DEFAULT 0,
    prompt_tokens Int32 DEFAULT 0,
    completion_tokens Int32 DEFAULT 0,
    use_time Int32 DEFAULT 0,
    is_stream UInt8 DEFAULT 0,
    channel_id Int32 DEFAULT 0,
    channel_name String DEFAULT '',
    token_id Int32 DEFAULT 0,
    ` + "`group`" + ` String DEFAULT '',
    ip String DEFAULT '',
    request_id String DEFAULT '',
    upstream_request_id String DEFAULT '',
    other String DEFAULT ''
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(created_at))
ORDER BY (created_at, id)
SETTINGS non_replicated_deduplication_window = 100000
`

const clickhouseLogAuditEventMigrationDDL = `
ALTER TABLE logs ADD COLUMN IF NOT EXISTS audit_event_id Nullable(String)
`

const clickhouseLogChannelNameMigrationDDL = `
ALTER TABLE logs ADD COLUMN IF NOT EXISTS channel_name String DEFAULT ''
`

// Existing target and reference ClickHouse layouts use the same types for
// these shared columns. Re-declaring those types with reference defaults is a
// metadata-only migration and does not rewrite legacy values. id is handled
// separately after inspecting whether the existing table uses the target's
// UInt64 audit hash or the reference's Int64 declaration.
const clickhouseLogReferenceDefaultsMigrationDDL = `
ALTER TABLE logs
    MODIFY COLUMN user_id Int32 DEFAULT 0,
    MODIFY COLUMN created_at Int64 DEFAULT 0,
    MODIFY COLUMN type Int32 DEFAULT 0,
    MODIFY COLUMN content String DEFAULT '',
    MODIFY COLUMN username String DEFAULT '',
    MODIFY COLUMN token_name String DEFAULT '',
    MODIFY COLUMN model_name String DEFAULT '',
    MODIFY COLUMN quota Int32 DEFAULT 0,
    MODIFY COLUMN prompt_tokens Int32 DEFAULT 0,
    MODIFY COLUMN completion_tokens Int32 DEFAULT 0,
    MODIFY COLUMN use_time Int32 DEFAULT 0,
    MODIFY COLUMN is_stream UInt8 DEFAULT 0,
    MODIFY COLUMN channel_id Int32 DEFAULT 0,
    MODIFY COLUMN channel_name String DEFAULT '',
    MODIFY COLUMN token_id Int32 DEFAULT 0,
    MODIFY COLUMN ` + "`group`" + ` String DEFAULT '',
    MODIFY COLUMN ip String DEFAULT '',
    MODIFY COLUMN request_id String DEFAULT '',
    MODIFY COLUMN upstream_request_id String DEFAULT '',
    MODIFY COLUMN other String DEFAULT ''
`

const clickhouseLogDeduplicationMigrationDDL = `
ALTER TABLE logs MODIFY SETTING non_replicated_deduplication_window = 100000
`

const maxClickHouseLogTTLDays = 36500

// openClickHouseLog connects to a ClickHouse log database and creates the logs
// MergeTree table. LOG_SQL_CLICKHOUSE_TTL_DAYS is intentionally parsed as an
// integer before it is interpolated into DDL, so configuration cannot become a
// SQL fragment.
func openClickHouseLog(dsn string, cfg *gorm.Config) (*gorm.DB, error) {
	safeConfig, err := clickHouseGORMConfig(cfg)
	if err != nil {
		return nil, err
	}
	safeDSN, err := sanitizeClickHouseDSN(dsn)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(clickhouse.Open(safeDSN), safeConfig)
	if err != nil {
		return nil, err
	}
	ttlDays := clickHouseLogTTLDays()
	if err := db.Exec(clickHouseLogCreateTableSQL(ttlDays)).Error; err != nil {
		return nil, fmt.Errorf("create clickhouse logs table: %w", err)
	}
	if err := db.Exec(clickhouseLogAuditEventMigrationDDL).Error; err != nil {
		return nil, fmt.Errorf("migrate clickhouse logs table: %w", err)
	}
	if err := db.Exec(clickhouseLogChannelNameMigrationDDL).Error; err != nil {
		return nil, fmt.Errorf("migrate clickhouse log channel name: %w", err)
	}
	if err := ensureClickHouseLogIDDefault(db); err != nil {
		return nil, err
	}
	if err := db.Exec(clickhouseLogReferenceDefaultsMigrationDDL).Error; err != nil {
		return nil, fmt.Errorf("migrate clickhouse log defaults: %w", err)
	}
	if err := db.Exec(clickhouseLogDeduplicationMigrationDDL).Error; err != nil {
		return nil, fmt.Errorf("enable clickhouse log insert deduplication: %w", err)
	}
	if err := syncClickHouseLogTTL(db, ttlDays); err != nil {
		return nil, err
	}
	return db, nil
}

func clickHouseLogTTLDays() int {
	ttlDays := env.GetEnvInt("LOG_SQL_CLICKHOUSE_TTL_DAYS", 0)
	if ttlDays < 0 || ttlDays > maxClickHouseLogTTLDays {
		return 0
	}
	return ttlDays
}

func clickHouseLogTTLExpression(ttlDays int) string {
	if ttlDays <= 0 || ttlDays > maxClickHouseLogTTLDays {
		return ""
	}
	return fmt.Sprintf("toDateTime(created_at) + INTERVAL %d DAY DELETE", ttlDays)
}

func clickHouseLogTTLClause(ttlDays int) string {
	expression := clickHouseLogTTLExpression(ttlDays)
	if expression == "" {
		return ""
	}
	return "\nTTL " + expression
}

func clickHouseLogCreateTableSQL(ttlDays int) string {
	clause := clickHouseLogTTLClause(ttlDays)
	if clause == "" {
		return clickhouseLogDDL
	}
	return strings.Replace(clickhouseLogDDL, "\nSETTINGS ", clause+"\nSETTINGS ", 1)
}

func syncClickHouseLogTTL(db *gorm.DB, ttlDays int) error {
	expression := clickHouseLogTTLExpression(ttlDays)
	if expression != "" {
		if err := db.Exec("ALTER TABLE logs MODIFY TTL " + expression).Error; err != nil {
			return fmt.Errorf("configure clickhouse log TTL: %w", err)
		}
		return nil
	}

	hasTTL, err := clickHouseLogTableHasTTL(db)
	if err != nil {
		return err
	}
	if !hasTTL {
		return nil
	}
	if err := db.Exec("ALTER TABLE logs REMOVE TTL").Error; err != nil {
		return fmt.Errorf("remove clickhouse log TTL: %w", err)
	}
	return nil
}

func clickHouseLogTableHasTTL(db *gorm.DB) (bool, error) {
	var createTableSQL string
	if err := db.Raw("SHOW CREATE TABLE logs").Scan(&createTableSQL).Error; err != nil {
		return false, fmt.Errorf("inspect clickhouse log TTL: %w", err)
	}
	return clickHouseCreateTableHasTTL(createTableSQL), nil
}

func clickHouseCreateTableHasTTL(createTableSQL string) bool {
	upperSQL := strings.ToUpper(createTableSQL)
	return strings.Contains(upperSQL, "\nTTL ") || strings.Contains(upperSQL, " TTL ")
}

func clickHouseCreateTableHasTTLForDays(createTableSQL string, ttlDays int) bool {
	if ttlDays <= 0 || ttlDays > maxClickHouseLogTTLDays || !clickHouseCreateTableHasTTL(createTableSQL) {
		return false
	}
	lowerSQL := strings.ToLower(createTableSQL)
	intervalLiteral := fmt.Sprintf("ttl todatetime(created_at) + interval %d day", ttlDays)
	canonicalInterval := fmt.Sprintf("ttl todatetime(created_at) + tointervalday(%d)", ttlDays)
	return strings.Contains(lowerSQL, intervalLiteral) || strings.Contains(lowerSQL, canonicalInterval)
}

func ensureClickHouseLogIDDefault(db *gorm.DB) error {
	var columnType string
	if err := db.Raw(`SELECT type FROM system.columns
WHERE database = currentDatabase() AND table = 'logs' AND name = 'id'`).Scan(&columnType).Error; err != nil {
		return fmt.Errorf("inspect clickhouse logs.id type: %w", err)
	}
	query, err := clickHouseLogIDDefaultMigrationDDL(columnType)
	if err != nil {
		return err
	}
	if err := db.Exec(query).Error; err != nil {
		return fmt.Errorf("migrate clickhouse logs.id default: %w", err)
	}
	return nil
}

func clickHouseLogIDDefaultMigrationDDL(columnType string) (string, error) {
	switch strings.TrimSpace(columnType) {
	case "Int64", "UInt64":
		return "ALTER TABLE logs MODIFY COLUMN id " + strings.TrimSpace(columnType) + " DEFAULT 0", nil
	default:
		return "", fmt.Errorf("%w: clickhouse logs.id has unsupported type %q",
			ErrUnsafeReferenceSchemaMigration, columnType)
	}
}

// sanitizeClickHouseDSN disables the driver's independent debug logger. The
// clickhouse-go debug path writes fully bound SQL directly to stdout, bypassing
// both GORM's silent logger and the application's central secret redactor.
func sanitizeClickHouseDSN(dsn string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse clickhouse log DSN: %w", err)
	}
	if parsed.Scheme != "clickhouse" {
		return "", fmt.Errorf("invalid clickhouse log DSN scheme")
	}
	query := parsed.Query()
	for key := range query {
		if strings.EqualFold(key, "debug") {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func clickHouseGORMConfig(cfg *gorm.Config) (*gorm.Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("clickhouse GORM config is nil")
	}
	cloned := *cfg
	if cloned.Logger == nil {
		cloned.Logger = logger.Default.LogMode(logger.Silent)
	} else {
		cloned.Logger = cloned.Logger.LogMode(logger.Silent)
	}
	return &cloned, nil
}

// UsingClickHouseLog reports whether the log database is ClickHouse.
func UsingClickHouseLog() bool {
	if LOG_DB != nil && LOG_DB.Dialector != nil {
		return LOG_DB.Dialector.Name() == "clickhouse"
	}
	return isClickHouseDSN(env.GetEnv("LOG_SQL_DSN", ""))
}

// InsertClickHouseLog writes a Log via a raw INSERT (ClickHouse has no
// soft-delete and no auto-increment primary key).
func InsertClickHouseLog(l *Log) error {
	return InsertClickHouseLogContext(context.Background(), l)
}

// InsertClickHouseLogContext writes a Log through a bounded caller context.
// AuditEventId is persisted as the replay idempotency key used by the primary
// database outbox.
func InsertClickHouseLogContext(ctx context.Context, l *Log) error {
	if l == nil {
		return fmt.Errorf("clickhouse log is nil")
	}
	if settings := clickHouseAuditInsertSettings(l.AuditEventId); settings != nil {
		// ClickHouse's retry-safe insert protocol requires both a table-level
		// deduplication window and a stable query-level token. The audit event ID
		// is immutable across outbox retries, so an acknowledgement lost after a
		// committed INSERT does not create a second block on retry.
		ctx = clickhouseclient.Context(ctx, clickhouseclient.WithSettings(settings))
	}
	sql := `INSERT INTO logs (id, audit_event_id, user_id, created_at, type, content, username, token_name, model_name, quota, prompt_tokens, completion_tokens, use_time, is_stream, channel_id, channel_name, token_id, ` + "`group`" + `, ip, request_id, upstream_request_id, other) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	isStream := uint8(0)
	if l.IsStream {
		isStream = 1
	}
	var auditEventID any
	if l.AuditEventId != nil {
		auditEventID = *l.AuditEventId
	}
	return LOG_DB.WithContext(ctx).Exec(sql,
		l.Id, auditEventID, l.UserId, l.CreatedAt, l.Type, l.Content, l.Username, l.TokenName,
		l.ModelName, l.Quota, l.PromptTokens, l.CompletionTokens, l.UseTime,
		isStream, l.ChannelId, l.ChannelName, l.TokenId, l.Group, l.Ip,
		l.RequestId, l.UpstreamRequestId, l.Other,
	).Error
}

func clickHouseAuditInsertSettings(eventID *string) clickhouseclient.Settings {
	if eventID == nil || strings.TrimSpace(*eventID) == "" {
		return nil
	}
	return clickhouseclient.Settings{
		"insert_deduplicate":         1,
		"insert_deduplication_token": *eventID,
	}
}

// isClickHouseDSN reports whether a DSN targets ClickHouse.
func isClickHouseDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "clickhouse://")
}
