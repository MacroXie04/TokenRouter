package store

import (
	"context"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type logModeProbe struct {
	requested logger.LogLevel
}

func TestClickHouseLogDSNDisablesDriverDebugLogging(t *testing.T) {
	sanitized, err := sanitizeClickHouseDSN("clickhouse://logger:secret@example.test/logs?debug=true&compress=lz4&Debug=1")
	require.NoError(t, err)
	parsed, err := url.Parse(sanitized)
	require.NoError(t, err)
	for key := range parsed.Query() {
		assert.NotEqual(t, "debug", strings.ToLower(key))
	}
	assert.Equal(t, "lz4", parsed.Query().Get("compress"))
	assert.Equal(t, "logger", parsed.User.Username())
	password, present := parsed.User.Password()
	assert.True(t, present)
	assert.Equal(t, "secret", password)

	_, err = sanitizeClickHouseDSN("postgres://example.test/logs?debug=true")
	assert.Error(t, err)
	_, err = sanitizeClickHouseDSN("clickhouse://example.test/%zz?debug=true")
	assert.Error(t, err)
}

func (probe *logModeProbe) LogMode(level logger.LogLevel) logger.Interface {
	return &logModeProbe{requested: level}
}
func (*logModeProbe) Info(context.Context, string, ...any)  {}
func (*logModeProbe) Warn(context.Context, string, ...any)  {}
func (*logModeProbe) Error(context.Context, string, ...any) {}
func (*logModeProbe) Trace(context.Context, time.Time, func() (string, int64), error) {
}

func TestClickHouseLogConfigForcesSilentSQLLogging(t *testing.T) {
	config, err := clickHouseGORMConfig(&gorm.Config{Logger: &logModeProbe{requested: logger.Info}})
	require.NoError(t, err)
	configured, ok := config.Logger.(*logModeProbe)
	require.True(t, ok)
	assert.Equal(t, logger.Silent, configured.requested)

	_, err = clickHouseGORMConfig(nil)
	assert.Error(t, err)
}

func TestClickHouseLogSchemaCarriesAuditDeliveryIdempotencyKey(t *testing.T) {
	assert.Contains(t, clickhouseLogDDL, "audit_event_id Nullable(String)")
	assert.Contains(t, clickhouseLogAuditEventMigrationDDL, "ADD COLUMN IF NOT EXISTS audit_event_id")
	assert.Contains(t, clickhouseLogChannelNameMigrationDDL, "ADD COLUMN IF NOT EXISTS channel_name String DEFAULT ''")
	assert.Contains(t, clickhouseLogDDL, "non_replicated_deduplication_window = 100000")
	assert.Contains(t, clickhouseLogDeduplicationMigrationDDL, "MODIFY SETTING non_replicated_deduplication_window = 100000")

	eventID := "audit-event-1"
	settings := clickHouseAuditInsertSettings(&eventID)
	assert.Equal(t, 1, settings["insert_deduplicate"])
	assert.Equal(t, eventID, settings["insert_deduplication_token"])
	assert.Nil(t, clickHouseAuditInsertSettings(nil))
	empty := "  "
	assert.Nil(t, clickHouseAuditInsertSettings(&empty), "blank tokens must not collapse unrelated legacy inserts")
}

func TestClickHouseLogSchemaCarriesReferenceDefaults(t *testing.T) {
	for _, declaration := range []string{
		"id UInt64 DEFAULT 0",
		"user_id Int32 DEFAULT 0",
		"created_at Int64 DEFAULT 0",
		"type Int32 DEFAULT 0",
		"content String DEFAULT ''",
		"username String DEFAULT ''",
		"token_name String DEFAULT ''",
		"model_name String DEFAULT ''",
		"quota Int32 DEFAULT 0",
		"prompt_tokens Int32 DEFAULT 0",
		"completion_tokens Int32 DEFAULT 0",
		"use_time Int32 DEFAULT 0",
		"is_stream UInt8 DEFAULT 0",
		"channel_id Int32 DEFAULT 0",
		"channel_name String DEFAULT ''",
		"token_id Int32 DEFAULT 0",
		"`group` String DEFAULT ''",
		"ip String DEFAULT ''",
		"request_id String DEFAULT ''",
		"upstream_request_id String DEFAULT ''",
		"other String DEFAULT ''",
	} {
		assert.Contains(t, clickhouseLogDDL, declaration)
	}
	for _, declaration := range []string{
		"MODIFY COLUMN user_id Int32 DEFAULT 0",
		"MODIFY COLUMN created_at Int64 DEFAULT 0",
		"MODIFY COLUMN type Int32 DEFAULT 0",
		"MODIFY COLUMN content String DEFAULT ''",
		"MODIFY COLUMN username String DEFAULT ''",
		"MODIFY COLUMN token_name String DEFAULT ''",
		"MODIFY COLUMN model_name String DEFAULT ''",
		"MODIFY COLUMN quota Int32 DEFAULT 0",
		"MODIFY COLUMN prompt_tokens Int32 DEFAULT 0",
		"MODIFY COLUMN completion_tokens Int32 DEFAULT 0",
		"MODIFY COLUMN use_time Int32 DEFAULT 0",
		"MODIFY COLUMN is_stream UInt8 DEFAULT 0",
		"MODIFY COLUMN channel_id Int32 DEFAULT 0",
		"MODIFY COLUMN channel_name String DEFAULT ''",
		"MODIFY COLUMN token_id Int32 DEFAULT 0",
		"MODIFY COLUMN `group` String DEFAULT ''",
		"MODIFY COLUMN ip String DEFAULT ''",
		"MODIFY COLUMN request_id String DEFAULT ''",
		"MODIFY COLUMN upstream_request_id String DEFAULT ''",
		"MODIFY COLUMN other String DEFAULT ''",
	} {
		assert.Contains(t, clickhouseLogReferenceDefaultsMigrationDDL, declaration)
	}
	assert.NotContains(t, clickhouseLogReferenceDefaultsMigrationDDL, "MODIFY COLUMN id",
		"legacy Int64/UInt64 identifiers must not be narrowed or reinterpreted")
	intDDL, err := clickHouseLogIDDefaultMigrationDDL("Int64")
	require.NoError(t, err)
	assert.Equal(t, "ALTER TABLE logs MODIFY COLUMN id Int64 DEFAULT 0", intDDL)
	uintDDL, err := clickHouseLogIDDefaultMigrationDDL("UInt64")
	require.NoError(t, err)
	assert.Equal(t, "ALTER TABLE logs MODIFY COLUMN id UInt64 DEFAULT 0", uintDDL)
	_, err = clickHouseLogIDDefaultMigrationDDL("UInt32")
	assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
}

func TestClickHouseLogTTLConfiguration(t *testing.T) {
	assert.Equal(t, "", clickHouseLogTTLExpression(0))
	assert.Equal(t, "", clickHouseLogTTLExpression(-5))
	assert.Equal(t, "", clickHouseLogTTLExpression(maxClickHouseLogTTLDays+1))
	assert.Equal(t, "toDateTime(created_at) + INTERVAL 30 DAY DELETE", clickHouseLogTTLExpression(30))
	assert.Equal(t, "", clickHouseLogTTLClause(0))
	assert.Equal(t, "\nTTL toDateTime(created_at) + INTERVAL 7 DAY DELETE", clickHouseLogTTLClause(7))

	withoutTTL := clickHouseLogCreateTableSQL(0)
	assert.Equal(t, clickhouseLogDDL, withoutTTL)
	assert.NotContains(t, withoutTTL, "\nTTL ")

	withTTL := clickHouseLogCreateTableSQL(30)
	assert.Contains(t, withTTL, "ORDER BY (created_at, id)\nTTL toDateTime(created_at) + INTERVAL 30 DAY DELETE\nSETTINGS")
	assert.True(t, clickHouseCreateTableHasTTL(withTTL))
	assert.True(t, clickHouseCreateTableHasTTLForDays(withTTL, 30))
	assert.True(t, clickHouseCreateTableHasTTLForDays(
		"CREATE TABLE logs (...) TTL toDateTime(created_at) + toIntervalDay(30)", 30))
	assert.False(t, clickHouseCreateTableHasTTLForDays(withTTL, 7))
	assert.True(t, clickHouseCreateTableHasTTL("CREATE TABLE logs (...) TTL toDateTime(created_at)"))
	assert.False(t, clickHouseCreateTableHasTTL(withoutTTL))

	t.Setenv("LOG_SQL_CLICKHOUSE_TTL_DAYS", "12")
	assert.Equal(t, 12, clickHouseLogTTLDays())
	t.Setenv("LOG_SQL_CLICKHOUSE_TTL_DAYS", "-1")
	assert.Zero(t, clickHouseLogTTLDays())
	t.Setenv("LOG_SQL_CLICKHOUSE_TTL_DAYS", "not-an-integer")
	assert.Zero(t, clickHouseLogTTLDays())
	t.Setenv("LOG_SQL_CLICKHOUSE_TTL_DAYS", "36501")
	assert.Zero(t, clickHouseLogTTLDays())
}

func TestInsertClickHouseLogRejectsNilRecord(t *testing.T) {
	assert.Error(t, InsertClickHouseLogContext(context.Background(), nil))
}

func TestUsingClickHouseLogFollowsConnectedSinkAfterFallback(t *testing.T) {
	t.Setenv("LOG_SQL_DSN", "clickhouse://unavailable.example/logs")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "fallback.db")), &gorm.Config{})
	require.NoError(t, err)
	previous := LOG_DB
	LOG_DB = db
	t.Cleanup(func() { LOG_DB = previous })
	assert.False(t, UsingClickHouseLog(), "a relational fallback must not receive ClickHouse SQL")
}

// TestClickHouseExternalLogLifecycle verifies the real ClickHouse DDL, raw log
// insert, and stable audit-event deduplication token. Ordinary unit runs skip
// this gate instead of presenting a SQLite substitute as ClickHouse evidence.
func TestClickHouseExternalLogLifecycle(t *testing.T) {
	dsn := os.Getenv("TOKENROUTER_TEST_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("set TOKENROUTER_TEST_CLICKHOUSE_DSN to an isolated ClickHouse database")
	}

	db, err := openClickHouseLog(dsn, &gorm.Config{})
	require.NoError(t, err)
	previous := LOG_DB
	LOG_DB = db
	t.Cleanup(func() {
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
		LOG_DB = previous
	})

	var createTableSQL string
	require.NoError(t, db.Raw("SHOW CREATE TABLE logs").Scan(&createTableSQL).Error)
	ttlExpression := clickHouseLogTTLExpression(clickHouseLogTTLDays())
	if ttlExpression == "" {
		assert.False(t, clickHouseCreateTableHasTTL(createTableSQL))
	} else {
		assert.True(t, clickHouseCreateTableHasTTLForDays(createTableSQL, clickHouseLogTTLDays()),
			"ClickHouse must retain the configured TTL even when SHOW CREATE canonicalizes the interval expression")
	}

	eventID := "clickhouse-external-" + cryptoutil.BestEffortRandomAlphanumeric(16)
	record := &Log{
		Id:               int(wallclock.NowTimestamp()),
		AuditEventId:     &eventID,
		UserId:           42,
		CreatedAt:        wallclock.NowTimestamp(),
		Type:             1,
		Content:          "external audit lifecycle",
		Username:         "clickhouse-test",
		TokenName:        "test-token",
		ModelName:        "test-model",
		Quota:            7,
		PromptTokens:     3,
		CompletionTokens: 4,
		RequestId:        "request-" + eventID,
	}
	require.NoError(t, InsertClickHouseLog(record))
	// Retrying the exact immutable outbox event must not duplicate the block.
	require.NoError(t, InsertClickHouseLog(record))
	// Reapplying metadata-only defaults must be idempotent and must not rewrite
	// the already-persisted audit row.
	require.NoError(t, ensureClickHouseLogIDDefault(db))
	require.NoError(t, ensureClickHouseLogIDDefault(db))
	require.NoError(t, db.Exec(clickhouseLogReferenceDefaultsMigrationDDL).Error)
	require.NoError(t, db.Exec(clickhouseLogReferenceDefaultsMigrationDDL).Error)

	var count int64
	require.NoError(t, db.Raw("SELECT count() FROM logs WHERE audit_event_id = ?", eventID).Scan(&count).Error)
	assert.EqualValues(t, 1, count)

	var content string
	require.NoError(t, db.Raw("SELECT any(content) FROM logs WHERE audit_event_id = ?", eventID).Scan(&content).Error)
	assert.Equal(t, record.Content, content)

	defaultEventID := "clickhouse-defaults-" + cryptoutil.BestEffortRandomAlphanumeric(16)
	require.NoError(t, db.Exec("INSERT INTO logs (audit_event_id) VALUES (?)", defaultEventID).Error)
	var defaults struct {
		Id                uint64
		UserId            int32
		CreatedAt         int64
		Type              int32
		Content           string
		Username          string
		TokenName         string
		ModelName         string
		Quota             int32
		PromptTokens      int32
		CompletionTokens  int32
		UseTime           int32
		IsStream          uint8
		ChannelId         int32
		ChannelName       string
		TokenId           int32
		UseGroup          string
		Ip                string
		RequestId         string
		UpstreamRequestId string
		Other             string
	}
	require.NoError(t, db.Raw(`SELECT id, user_id, created_at, type, content, username, token_name, model_name,
		quota, prompt_tokens, completion_tokens, use_time, is_stream, channel_id, channel_name,
		token_id, `+"`group`"+` AS use_group, ip, request_id, upstream_request_id, other
		FROM logs WHERE audit_event_id = ? LIMIT 1`, defaultEventID).Scan(&defaults).Error)
	assert.Zero(t, defaults.Id)
	assert.Zero(t, defaults.UserId)
	assert.Zero(t, defaults.CreatedAt)
	assert.Zero(t, defaults.Type)
	assert.Empty(t, defaults.Content)
	assert.Empty(t, defaults.Username)
	assert.Empty(t, defaults.TokenName)
	assert.Empty(t, defaults.ModelName)
	assert.Zero(t, defaults.Quota)
	assert.Zero(t, defaults.PromptTokens)
	assert.Zero(t, defaults.CompletionTokens)
	assert.Zero(t, defaults.UseTime)
	assert.Zero(t, defaults.IsStream)
	assert.Zero(t, defaults.ChannelId)
	assert.Empty(t, defaults.ChannelName)
	assert.Zero(t, defaults.TokenId)
	assert.Empty(t, defaults.UseGroup)
	assert.Empty(t, defaults.Ip)
	assert.Empty(t, defaults.RequestId)
	assert.Empty(t, defaults.UpstreamRequestId)
	assert.Empty(t, defaults.Other)
}

// TestClickHouseLogRetentionExternalDatabaseLifecycle exercises the real
// schema-compatible count and synchronous mutation path. ClickHouse has no
// deleted_at column and performs one bounded scheduler operation even though
// the server mutation removes every row before the cutoff in that operation.
func TestClickHouseLogRetentionExternalDatabaseLifecycle(t *testing.T) {
	dsn := os.Getenv("TOKENROUTER_TEST_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("set TOKENROUTER_TEST_CLICKHOUSE_DSN to an isolated ClickHouse database")
	}
	if os.Getenv("TOKENROUTER_TEST_ALLOW_SCHEMA_RESET") != "1" {
		t.Fatal("set TOKENROUTER_TEST_ALLOW_SCHEMA_RESET=1 only for an isolated disposable database")
	}

	db, err := openClickHouseLog(dsn, &gorm.Config{})
	require.NoError(t, err)
	previous := LOG_DB
	LOG_DB = db
	t.Cleanup(func() {
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
		LOG_DB = previous
	})

	// The exact ClickHouse selectors share one disposable database. Truncating
	// only its logs table keeps the global CountOldLog assertion independent of
	// test registration order and is guarded by the explicit schema-reset opt-in.
	require.NoError(t, db.Exec("TRUNCATE TABLE logs").Error)
	suffix := cryptoutil.BestEffortRandomAlphanumeric(16)
	oldOneEventID := "clickhouse-retention-old-one-" + suffix
	oldTwoEventID := "clickhouse-retention-old-two-" + suffix
	freshEventID := "clickhouse-retention-fresh-" + suffix
	now := wallclock.NowTimestamp()
	cutoff := now - 60
	records := []*Log{
		{Id: 101, AuditEventId: clickHouseStringPointer(oldOneEventID), CreatedAt: now - 120, Content: "old-one"},
		{Id: 102, AuditEventId: clickHouseStringPointer(oldTwoEventID), CreatedAt: now - 90, Content: "old-two"},
		{Id: 103, AuditEventId: clickHouseStringPointer(freshEventID), CreatedAt: now, Content: "fresh"},
	}
	for _, record := range records {
		require.NoError(t, InsertClickHouseLog(record))
	}

	total, err := CountOldLog(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	deleted, err := DeleteOldLogBatch(context.Background(), cutoff, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted,
		"one ClickHouse mutation removes all matching rows and reports the pre-mutation count")
	total, err = CountOldLog(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Zero(t, total)

	var fresh int64
	require.NoError(t, db.Raw("SELECT count() FROM logs WHERE audit_event_id = ?", freshEventID).Scan(&fresh).Error)
	assert.Equal(t, int64(1), fresh, "rows on or after the cutoff must remain")
}

func clickHouseStringPointer(value string) *string { return &value }
