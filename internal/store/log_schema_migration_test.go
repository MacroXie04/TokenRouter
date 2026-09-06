package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// referenceLogSchema independently transcribes the pinned reference Log
// model. It intentionally excludes TokenRouter's audit-event key and soft
// delete extensions, while retaining the reference's read-only channel name.
type referenceLogSchema struct {
	Id                int   `gorm:"index:idx_created_at_id,priority:2;index:idx_user_id_id,priority:2"`
	UserId            int   `gorm:"index;index:idx_user_id_id,priority:1"`
	CreatedAt         int64 `gorm:"bigint;index:idx_created_at_id,priority:1;index:idx_created_at_type"`
	Type              int   `gorm:"index:idx_created_at_type"`
	Content           string
	Username          string `gorm:"index;index:index_username_model_name,priority:2;default:''"`
	TokenName         string `gorm:"index;default:''"`
	ModelName         string `gorm:"index;index:index_username_model_name,priority:1;default:''"`
	Quota             int    `gorm:"default:0"`
	PromptTokens      int    `gorm:"default:0"`
	CompletionTokens  int    `gorm:"default:0"`
	UseTime           int    `gorm:"default:0"`
	IsStream          bool
	ChannelId         int    `gorm:"index"`
	ChannelName       string `gorm:"->"`
	TokenId           int    `gorm:"default:0;index"`
	Group             string `gorm:"index"`
	Ip                string `gorm:"index;default:''"`
	RequestId         string `gorm:"type:varchar(64);index:idx_logs_request_id;default:''"`
	UpstreamRequestId string `gorm:"type:varchar(128);index:idx_logs_upstream_request_id;default:''"`
	Other             string
}

func (referenceLogSchema) TableName() string { return "logs" }

// legacyLogSchema is the exact TokenRouter declaration immediately before
// this alignment. Its deliberately incomplete/order-reversed composite
// indexes exercise the explicit index repair path.
type legacyLogSchema struct {
	Id                int     `gorm:"primaryKey"`
	AuditEventId      *string `gorm:"type:varchar(64);uniqueIndex:ux_logs_audit_event_id"`
	UserId            int     `gorm:"index;index:idx_user_id_id,priority:1"`
	CreatedAt         int64   `gorm:"index:idx_created_at_id,priority:1;index:idx_created_at_type,priority:1"`
	Type              int     `gorm:"index:idx_created_at_type,priority:2"`
	Content           string  `gorm:"type:text"`
	Username          string  `gorm:"index;index:index_username_model_name,priority:1;type:varchar(64)"`
	TokenName         string  `gorm:"index;type:varchar(64)"`
	ModelName         string  `gorm:"index:index_username_model_name,priority:2;type:varchar(255)"`
	Quota             int
	PromptTokens      int
	CompletionTokens  int
	UseTime           int
	IsStream          bool
	ChannelId         int `gorm:"index"`
	ChannelName       string
	TokenId           int            `gorm:"index"`
	Group             string         `gorm:"index;type:varchar(64)"`
	Ip                string         `gorm:"index;type:varchar(64)"`
	RequestId         string         `gorm:"type:varchar(64);index"`
	UpstreamRequestId string         `gorm:"type:varchar(128);index"`
	Other             string         `gorm:"type:text"`
	DeletedAt         gorm.DeletedAt `gorm:"index"`
}

func (legacyLogSchema) TableName() string { return "logs" }

type expectedLogIndex struct {
	name    string
	columns []string
}

var expectedReferenceLogIndexes = []expectedLogIndex{
	{name: "idx_logs_user_id", columns: []string{"user_id"}},
	{name: "idx_user_id_id", columns: []string{"user_id", "id"}},
	{name: "idx_created_at_id", columns: []string{"created_at", "id"}},
	{name: "idx_created_at_type", columns: []string{"created_at", "type"}},
	{name: "idx_logs_username", columns: []string{"username"}},
	{name: "index_username_model_name", columns: []string{"model_name", "username"}},
	{name: "idx_logs_token_name", columns: []string{"token_name"}},
	{name: "idx_logs_model_name", columns: []string{"model_name"}},
	{name: "idx_logs_channel_id", columns: []string{"channel_id"}},
	{name: "idx_logs_token_id", columns: []string{"token_id"}},
	{name: "idx_logs_group", columns: []string{"group"}},
	{name: "idx_logs_ip", columns: []string{"ip"}},
	{name: "idx_logs_request_id", columns: []string{"request_id"}},
	{name: "idx_logs_upstream_request_id", columns: []string{"upstream_request_id"}},
}

func TestReferenceLogSchemaPortableDialects(t *testing.T) {
	targetSchema, err := schema.Parse(&Log{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceSchema, err := schema.Parse(&referenceLogSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	legacySchema, err := schema.Parse(&legacyLogSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	for _, referenceField := range referenceSchema.Fields {
		targetField := targetSchema.LookUpField(referenceField.Name)
		require.NotNil(t, targetField, "Log.%s is missing", referenceField.Name)
		assert.Equal(t, referenceField.DBName, targetField.DBName, referenceField.Name+" column")
		assert.Equal(t, referenceField.PrimaryKey, targetField.PrimaryKey, referenceField.Name+" primary key")
		assert.Equal(t, referenceField.AutoIncrement, targetField.AutoIncrement, referenceField.Name+" auto increment")
		assert.Equal(t, referenceField.NotNull, targetField.NotNull, referenceField.Name+" nullability")
		assert.Equal(t, referenceField.HasDefaultValue, targetField.HasDefaultValue, referenceField.Name+" default presence")
		assert.Equal(t, referenceField.DefaultValue, targetField.DefaultValue, referenceField.Name+" default")
		assert.Equal(t, referenceField.Size, targetField.Size, referenceField.Name+" size")
	}

	mysqlDB, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user@tcp(127.0.0.1:1)/db?charset=utf8mb4&parseTime=true",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true, DryRun: true})
	require.NoError(t, err)
	postgresDB, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=gorm dbname=gorm sslmode=disable",
	}), &gorm.Config{DisableAutomaticPing: true, DryRun: true})
	require.NoError(t, err)

	for _, dialect := range []gorm.Dialector{mysqlDB.Dialector, postgresDB.Dialector} {
		t.Run(dialect.Name(), func(t *testing.T) {
			for _, referenceField := range referenceSchema.Fields {
				targetField := targetSchema.LookUpField(referenceField.Name)
				require.NotNil(t, targetField)
				assert.Equal(t, dialect.DataTypeOf(referenceField), dialect.DataTypeOf(targetField),
					"Log.%s %s type", referenceField.Name, dialect.Name())
			}
			legacyModelName := legacySchema.LookUpField("ModelName")
			targetModelName := targetSchema.LookUpField("ModelName")
			require.NotNil(t, legacyModelName)
			require.NotNil(t, targetModelName)
			if dialect.Name() == "mysql" {
				assert.Equal(t, "varchar(255)", strings.ToLower(dialect.DataTypeOf(legacyModelName)))
				assert.Equal(t, "varchar(191)", strings.ToLower(dialect.DataTypeOf(targetModelName)))
			} else {
				assert.Equal(t, "varchar(255)", strings.ToLower(dialect.DataTypeOf(legacyModelName)))
				assert.Equal(t, "text", strings.ToLower(dialect.DataTypeOf(targetModelName)))
			}
		})
	}

	referenceIndexes := referenceSchema.ParseIndexes()
	targetIndexes := targetSchema.ParseIndexes()
	assert.Len(t, targetIndexes, len(referenceIndexes)+2,
		"audit-event uniqueness and soft-delete lookup must be the only target-only Log indexes")
	for _, referenceIndex := range referenceIndexes {
		targetIndex := targetSchema.LookIndex(referenceIndex.Name)
		require.NotNil(t, targetIndex, "missing Log index %s", referenceIndex.Name)
		assert.Equal(t, referenceIndex.Class, targetIndex.Class, referenceIndex.Name+" class")
		require.Len(t, targetIndex.Fields, len(referenceIndex.Fields), referenceIndex.Name+" fields")
		for i := range referenceIndex.Fields {
			assert.Equal(t, referenceIndex.Fields[i].DBName, targetIndex.Fields[i].DBName,
				referenceIndex.Name+" column")
			assert.Equal(t, referenceIndex.Fields[i].Priority, targetIndex.Fields[i].Priority,
				referenceIndex.Name+" priority")
		}
	}
	require.NotNil(t, targetSchema.LookIndex("ux_logs_audit_event_id"))
	require.NotNil(t, targetSchema.LookIndex("idx_logs_deleted_at"))

	channelName := targetSchema.LookUpField("ChannelName")
	referenceChannelName := referenceSchema.LookUpField("ChannelName")
	require.NotNil(t, channelName)
	require.NotNil(t, referenceChannelName)
	assert.True(t, channelName.Creatable)
	assert.True(t, channelName.Updatable)
	assert.False(t, referenceChannelName.Creatable)
	assert.False(t, referenceChannelName.Updatable)
	logType := reflect.TypeOf(Log{})
	channelID, _ := logType.FieldByName("ChannelId")
	requestID, _ := logType.FieldByName("RequestId")
	upstreamRequestID, _ := logType.FieldByName("UpstreamRequestId")
	assert.Equal(t, "channel_id", channelID.Tag.Get("json"), "the established target API field name must not change")
	assert.Equal(t, "request_id", requestID.Tag.Get("json"), "the established target response shape must not gain omitempty")
	assert.Equal(t, "upstream_request_id", upstreamRequestID.Tag.Get("json"), "the established target response shape must not gain omitempty")
}

func TestReferenceLogSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceLogSchemaMatchesSQLite(t, db)
	assertReferenceLogSchema(t, db)
	insertReferenceLogWithDatabaseDefaults(t, db, "fresh-direct-default")

	auditID := "audit-log-schema-writable-channel"
	created := Log{
		AuditEventId: &auditID, UserId: 71, CreatedAt: 72, Type: 4,
		Content: "writable-channel-name", ChannelId: 73, ChannelName: "retained upstream",
		Group: "default", IsStream: true,
	}
	require.NoError(t, db.Create(&created).Error)
	var stored Log
	require.NoError(t, db.Unscoped().First(&stored, created.Id).Error)
	assert.Equal(t, created.ChannelName, stored.ChannelName,
		"TokenRouter's established audit display value must remain writable")
	duplicate := created
	duplicate.Id = 0
	assert.Error(t, db.Create(&duplicate).Error, "audit-event retries must remain uniquely constrained")
	require.NoError(t, db.Delete(&stored).Error)
	assert.ErrorIs(t, db.First(&Log{}, stored.Id).Error, gorm.ErrRecordNotFound)
	require.NoError(t, db.Unscoped().First(&stored, stored.Id).Error,
		"the target's soft-delete audit extension must remain recoverable")

	columnsBefore := sqliteColumnSignatures(t, db, Log{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, Log{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, Log{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, Log{}.TableName()))
	assertReferenceLogSchemaMatchesSQLite(t, db)
}

func TestReferenceLogSchemaSQLitePreservesLegacyRows(t *testing.T) {
	primary := newReferenceSchemaTestDB(t)
	sink, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy-log-sink.db")), &gorm.Config{})
	require.NoError(t, err)
	LOG_DB = sink
	require.NoError(t, sink.AutoMigrate(&legacyLogSchema{}))

	legacy := makeLegacyReferenceLog("legacy-log", strings.Repeat("m", referenceLogMySQLModelNameLimit))
	legacy.DeletedAt = gorm.DeletedAt{Time: time.Unix(1_700_000_000, 0).UTC(), Valid: true}
	require.NoError(t, sink.Create(&legacy).Error)
	nullable := makeLegacyReferenceLog("nullable-log", "temporary-model")
	nullable.DeletedAt = gorm.DeletedAt{}
	require.NoError(t, sink.Create(&nullable).Error)
	require.NoError(t, setLegacyLogDefaultColumnsNull(sink, nullable.Id))

	require.NoError(t, migrateDB())
	assertReferenceLogSchemaMatchesSQLite(t, sink)
	assertReferenceLogSchema(t, sink)
	assertLegacyReferenceLogPreserved(t, sink, legacy)
	assertLegacyLogNullDefaultsPreserved(t, sink, nullable.Id)
	insertReferenceLogWithDatabaseDefaults(t, sink, "legacy-direct-default")

	columnsBefore := sqliteColumnSignatures(t, sink, Log{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, sink, Log{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, sink, Log{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, sink, Log{}.TableName()))
	assertLegacyReferenceLogPreserved(t, sink, legacy)
	assertLegacyLogNullDefaultsPreserved(t, sink, nullable.Id)
	assert.NoError(t, primary.Session(&gorm.Session{}).Exec("SELECT 1").Error)
}

func TestReferenceLogSchemaPreflightRejectsUnsafeLegacyModelName(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacyLogSchema{}))
	legacy := makeLegacyReferenceLog("unsafe-log", strings.Repeat("m", referenceLogMySQLModelNameLimit+1))
	require.NoError(t, db.Create(&legacy).Error)

	err := rejectOversizedLegacyLogModelName(db)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
	assert.Contains(t, err.Error(), "logs.model_name")
	var stored string
	require.NoError(t, db.Table(Log{}.TableName()).Select("model_name").Where("id = ?", legacy.Id).Scan(&stored).Error)
	assert.Equal(t, legacy.ModelName, stored, "a rejected migration must not truncate the legacy model name")

	require.NoError(t, db.Unscoped().Delete(&legacyLogSchema{}, legacy.Id).Error)
	safe := makeLegacyReferenceLog("safe-log", strings.Repeat("m", referenceLogMySQLModelNameLimit))
	require.NoError(t, db.Create(&safe).Error)
	assert.NoError(t, rejectOversizedLegacyLogModelName(db))
}

func assertReferenceLogSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-log.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&referenceLogSchema{}))

	referenceColumns := sqliteColumnSignatures(t, reference, Log{}.TableName())
	targetColumns := sqliteColumnSignatures(t, db, Log{}.TableName())
	targetColumns = filterLogColumnSignatures(targetColumns, "audit_event_id", "deleted_at")
	assert.Equal(t, referenceColumns, targetColumns, "Log columns after target-only extension normalization")

	referenceIndexes := sqliteIndexSignatures(t, reference, Log{}.TableName())
	targetIndexes := sqliteIndexSignatures(t, db, Log{}.TableName())
	targetIndexes = filterLogIndexSignatures(targetIndexes, "ux_logs_audit_event_id", "idx_logs_deleted_at")
	assert.Equal(t, referenceIndexes, targetIndexes, "Log indexes after target-only extension normalization")
}

// assertReferenceLogSchema is shared with the opt-in MySQL/PostgreSQL gate.
func assertReferenceLogSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, index := range expectedReferenceLogIndexes {
		assertLogIndex(t, db, index.name, index.columns, false)
	}
	assertLogIndex(t, db, "ux_logs_audit_event_id", []string{"audit_event_id"}, true)
	assertLogIndex(t, db, "idx_logs_deleted_at", []string{"deleted_at"}, false)

	for column, expected := range map[string]string{
		"username": "", "token_name": "", "model_name": "", "quota": "0",
		"prompt_tokens": "0", "completion_tokens": "0", "use_time": "0",
		"token_id": "0", "ip": "", "request_id": "", "upstream_request_id": "",
	} {
		assertColumnDefault(t, db, Log{}.TableName(), column, expected)
		assertColumnNullable(t, db, Log{}.TableName(), column, true)
	}

	statement := &gorm.Statement{DB: db}
	require.NoError(t, statement.Parse(&Log{}))
	for _, fieldName := range []string{"Content", "Username", "TokenName", "ModelName", "Group", "Ip", "Other"} {
		field := statement.Schema.LookUpField(fieldName)
		require.NotNil(t, field, "Log.%s is missing", fieldName)
		assert.Empty(t, field.TagSettings["TYPE"], "Log.%s must use the reference dialect-native string type", fieldName)
	}
	createdAt := statement.Schema.LookUpField("CreatedAt")
	require.NotNil(t, createdAt)
	_, declaredBigint := createdAt.TagSettings["BIGINT"]
	assert.True(t, declaredBigint, "Log.CreatedAt must retain the reference's explicit bigint setting")

	switch db.Dialector.Name() {
	case "sqlite":
		for _, column := range []string{"content", "username", "token_name", "model_name", "channel_name", "group", "ip", "other"} {
			assertColumnDatabaseType(t, db, Log{}.TableName(), column, "text")
		}
		assertColumnDatabaseType(t, db, Log{}.TableName(), "created_at", "integer")
	case "mysql":
		for _, column := range []string{"username", "token_name", "model_name", "group", "ip"} {
			assertColumnLength(t, db, Log{}.TableName(), column, referenceLogMySQLModelNameLimit)
		}
		assertColumnLength(t, db, Log{}.TableName(), "request_id", 64)
		assertColumnLength(t, db, Log{}.TableName(), "upstream_request_id", 128)
		assertColumnDatabaseType(t, db, Log{}.TableName(), "created_at", "bigint")
	case "postgres":
		for _, column := range []string{"content", "username", "token_name", "model_name", "channel_name", "group", "ip", "other"} {
			assertColumnDatabaseType(t, db, Log{}.TableName(), column, "text")
		}
		assertColumnLength(t, db, Log{}.TableName(), "request_id", 64)
		assertColumnLength(t, db, Log{}.TableName(), "upstream_request_id", 128)
		assertColumnDatabaseType(t, db, Log{}.TableName(), "created_at", "int8")
	default:
		require.FailNow(t, "unsupported Log schema test dialect", db.Dialector.Name())
	}
}

func exerciseReferenceLogSchemaLegacyExternal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Contains(t, []string{"mysql", "postgres"}, db.Dialector.Name())
	require.NoError(t, db.Migrator().DropTable(&Log{}))
	require.NoError(t, db.AutoMigrate(&legacyLogSchema{}))
	modelNameLength := referenceLogMySQLModelNameLimit
	if db.Dialector.Name() == "postgres" {
		modelNameLength = 255
	}
	legacy := makeLegacyReferenceLog("server-legacy-log", strings.Repeat("m", modelNameLength))
	require.NoError(t, db.Create(&legacy).Error)
	nullable := makeLegacyReferenceLog("server-nullable-log", "temporary-model")
	require.NoError(t, db.Create(&nullable).Error)
	require.NoError(t, setLegacyLogDefaultColumnsNull(db, nullable.Id))

	require.NoError(t, migrateDB())
	assertReferenceLogSchema(t, db)
	assertLegacyReferenceLogPreserved(t, db, legacy)
	assertLegacyLogNullDefaultsPreserved(t, db, nullable.Id)
	insertReferenceLogWithDatabaseDefaults(t, db, "server-direct-default")
	require.NoError(t, migrateDB())
	assertReferenceLogSchema(t, db)
	assertLegacyReferenceLogPreserved(t, db, legacy)
	assertLegacyLogNullDefaultsPreserved(t, db, nullable.Id)

	if db.Dialector.Name() != "mysql" {
		return
	}
	require.NoError(t, db.Migrator().DropTable(&Log{}))
	require.NoError(t, db.AutoMigrate(&legacyLogSchema{}))
	unsafe := makeLegacyReferenceLog("server-unsafe-log", strings.Repeat("m", referenceLogMySQLModelNameLimit+1))
	require.NoError(t, db.Create(&unsafe).Error)
	err := migrateDB()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
	assert.Contains(t, err.Error(), "logs.model_name")
	var stored string
	require.NoError(t, db.Table(Log{}.TableName()).Select("model_name").Where("id = ?", unsafe.Id).Scan(&stored).Error)
	assert.Equal(t, unsafe.ModelName, stored)
	require.NoError(t, db.Unscoped().Delete(&legacyLogSchema{}, unsafe.Id).Error)
	require.NoError(t, migrateDB())
	assertReferenceLogSchema(t, db)
}

func makeLegacyReferenceLog(content, modelName string) legacyLogSchema {
	auditID := "audit-" + content
	return legacyLogSchema{
		AuditEventId: &auditID, UserId: 81, CreatedAt: 82, Type: 4,
		Content: content, Username: strings.Repeat("u", 64), TokenName: strings.Repeat("t", 64),
		ModelName: modelName, Quota: 83, PromptTokens: 84, CompletionTokens: 85,
		UseTime: 86, IsStream: true, ChannelId: 87, ChannelName: "legacy channel",
		TokenId: 88, Group: strings.Repeat("g", 64), Ip: strings.Repeat("i", 64),
		RequestId: "request-" + content, UpstreamRequestId: "upstream-" + content,
		Other: `{"legacy":true}`,
	}
}

func insertReferenceLogWithDatabaseDefaults(t *testing.T, db *gorm.DB, content string) Log {
	t.Helper()
	groupColumn := quoteReferenceSchemaIdentifier(db, "group")
	query := fmt.Sprintf(`INSERT INTO logs
		(user_id, created_at, type, content, is_stream, channel_id, channel_name, %s, other)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, groupColumn)
	require.NoError(t, db.Exec(query, 91, int64(92), 4, content, false, 93, "direct channel", "default", "{}").Error)
	var direct Log
	require.NoError(t, db.Unscoped().Where("content = ?", content).First(&direct).Error)
	assert.Empty(t, direct.Username)
	assert.Empty(t, direct.TokenName)
	assert.Empty(t, direct.ModelName)
	assert.Zero(t, direct.Quota)
	assert.Zero(t, direct.PromptTokens)
	assert.Zero(t, direct.CompletionTokens)
	assert.Zero(t, direct.UseTime)
	assert.Zero(t, direct.TokenId)
	assert.Empty(t, direct.Ip)
	assert.Empty(t, direct.RequestId)
	assert.Empty(t, direct.UpstreamRequestId)
	assert.Equal(t, "direct channel", direct.ChannelName)
	return direct
}

func setLegacyLogDefaultColumnsNull(db *gorm.DB, id int) error {
	return db.Exec(`UPDATE logs SET
		username = NULL, token_name = NULL, model_name = NULL, quota = NULL,
		prompt_tokens = NULL, completion_tokens = NULL, use_time = NULL,
		token_id = NULL, ip = NULL, request_id = NULL, upstream_request_id = NULL
		WHERE id = ?`, id).Error
}

func assertLegacyReferenceLogPreserved(t *testing.T, db *gorm.DB, legacy legacyLogSchema) {
	t.Helper()
	var migrated Log
	require.NoError(t, db.Unscoped().First(&migrated, legacy.Id).Error)
	assert.Equal(t, legacy.AuditEventId, migrated.AuditEventId)
	assert.Equal(t, legacy.UserId, migrated.UserId)
	assert.Equal(t, legacy.CreatedAt, migrated.CreatedAt)
	assert.Equal(t, legacy.Type, migrated.Type)
	assert.Equal(t, legacy.Content, migrated.Content)
	assert.Equal(t, legacy.Username, migrated.Username)
	assert.Equal(t, legacy.TokenName, migrated.TokenName)
	assert.Equal(t, legacy.ModelName, migrated.ModelName)
	assert.Equal(t, legacy.Quota, migrated.Quota)
	assert.Equal(t, legacy.PromptTokens, migrated.PromptTokens)
	assert.Equal(t, legacy.CompletionTokens, migrated.CompletionTokens)
	assert.Equal(t, legacy.UseTime, migrated.UseTime)
	assert.Equal(t, legacy.IsStream, migrated.IsStream)
	assert.Equal(t, legacy.ChannelId, migrated.ChannelId)
	assert.Equal(t, legacy.ChannelName, migrated.ChannelName)
	assert.Equal(t, legacy.TokenId, migrated.TokenId)
	assert.Equal(t, legacy.Group, migrated.Group)
	assert.Equal(t, legacy.Ip, migrated.Ip)
	assert.Equal(t, legacy.RequestId, migrated.RequestId)
	assert.Equal(t, legacy.UpstreamRequestId, migrated.UpstreamRequestId)
	assert.Equal(t, legacy.Other, migrated.Other)
	assert.Equal(t, legacy.DeletedAt.Valid, migrated.DeletedAt.Valid)
	if legacy.DeletedAt.Valid {
		assert.WithinDuration(t, legacy.DeletedAt.Time, migrated.DeletedAt.Time, time.Second)
		assert.ErrorIs(t, db.First(&Log{}, legacy.Id).Error, gorm.ErrRecordNotFound)
	}
}

func assertLegacyLogNullDefaultsPreserved(t *testing.T, db *gorm.DB, id int) {
	t.Helper()
	var username, tokenName, modelName, ip, requestID, upstreamRequestID sql.NullString
	var quota, promptTokens, completionTokens, useTime, tokenID sql.NullInt64
	row := db.Table(Log{}.TableName()).Select(`username, token_name, model_name, quota,
		prompt_tokens, completion_tokens, use_time, token_id, ip, request_id, upstream_request_id`).
		Where("id = ?", id).Row()
	require.NoError(t, row.Scan(&username, &tokenName, &modelName, &quota, &promptTokens,
		&completionTokens, &useTime, &tokenID, &ip, &requestID, &upstreamRequestID))
	for name, valid := range map[string]bool{
		"username": username.Valid, "token_name": tokenName.Valid, "model_name": modelName.Valid,
		"quota": quota.Valid, "prompt_tokens": promptTokens.Valid, "completion_tokens": completionTokens.Valid,
		"use_time": useTime.Valid, "token_id": tokenID.Valid, "ip": ip.Valid,
		"request_id": requestID.Valid, "upstream_request_id": upstreamRequestID.Valid,
	} {
		assert.False(t, valid, "adding the %s default must not rewrite legacy NULL", name)
	}
}

func assertLogIndex(t *testing.T, db *gorm.DB, name string, columns []string, unique bool) {
	t.Helper()
	actualColumns, actualUnique := requirePortableIndex(t, db, &Log{}, Log{}.TableName(), name)
	assert.Equal(t, columns, actualColumns, "index %s column order", name)
	assert.Equal(t, unique, actualUnique, "index %s uniqueness", name)
}

func filterLogColumnSignatures(columns []sqliteColumnSignature, excluded ...string) []sqliteColumnSignature {
	exclusions := make(map[string]bool, len(excluded))
	for _, name := range excluded {
		exclusions[name] = true
	}
	result := make([]sqliteColumnSignature, 0, len(columns))
	for _, column := range columns {
		if !exclusions[column.Name] {
			result = append(result, column)
		}
	}
	return result
}

func filterLogIndexSignatures(indexes []sqliteIndexSignature, excluded ...string) []sqliteIndexSignature {
	exclusions := make(map[string]bool, len(excluded))
	for _, name := range excluded {
		exclusions[name] = true
	}
	result := make([]sqliteIndexSignature, 0, len(indexes))
	for _, index := range indexes {
		if !exclusions[index.Name] {
			result = append(result, index)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
