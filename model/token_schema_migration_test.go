package model

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

const referenceTokenIndexedStringLimit = int64(191)

// referenceTokenSchema is an independently transcribed database-only mirror
// of the audited Token contract. Keeping it separate from Token makes SQLite
// compare generated DDL rather than merely rechecking Token's own tags.
type referenceTokenSchema struct {
	Id                 int
	UserId             int    `gorm:"index"`
	Key                string `gorm:"type:varchar(128);uniqueIndex"`
	Status             int    `gorm:"default:1"`
	Name               string `gorm:"index"`
	CreatedTime        int64  `gorm:"bigint"`
	AccessedTime       int64  `gorm:"bigint"`
	ExpiredTime        int64  `gorm:"bigint;default:-1"`
	RemainQuota        int    `gorm:"default:0"`
	UnlimitedQuota     bool
	ModelLimitsEnabled bool
	ModelLimits        string  `gorm:"type:text"`
	AllowIps           *string `gorm:"default:''"`
	UsedQuota          int     `gorm:"default:0"`
	Group              string  `gorm:"default:''"`
	CrossGroupRetry    bool
	AutoGroups         string         `gorm:"type:text"`
	DeletedAt          gorm.DeletedAt `gorm:"index"`
}

func (referenceTokenSchema) TableName() string { return "tokens" }

// legacyTokenSchema captures the TokenRouter schema before the reference
// alignment, including the historical varchar(1024) model-limits column.
// Every changed string column is widened and all newly declared defaults are
// added without changing stored row values.
type legacyTokenSchema struct {
	Id                 int    `gorm:"primaryKey"`
	UserId             int    `gorm:"index"`
	Key                string `gorm:"type:varchar(128);uniqueIndex"`
	Status             int
	Name               string `gorm:"index;type:varchar(64)"`
	CreatedTime        int64
	AccessedTime       int64
	ExpiredTime        int64
	RemainQuota        int
	UnlimitedQuota     bool
	ModelLimitsEnabled bool
	ModelLimits        string `gorm:"type:varchar(1024)"`
	AllowIps           string `gorm:"type:varchar(128)"`
	UsedQuota          int
	Group              string `gorm:"type:varchar(64)"`
	CrossGroupRetry    bool
	AutoGroups         string         `gorm:"type:text"`
	DeletedAt          gorm.DeletedAt `gorm:"index"`
}

func (legacyTokenSchema) TableName() string { return "tokens" }

func TestReferenceTokenSchemaPortableDialects(t *testing.T) {
	targetSchema, err := schema.Parse(&Token{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceSchema, err := schema.Parse(&referenceTokenSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	mysqlDB, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user@tcp(127.0.0.1:1)/db?charset=utf8mb4&parseTime=true",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true, DryRun: true})
	require.NoError(t, err)
	postgresDB, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=gorm dbname=gorm sslmode=disable",
	}), &gorm.Config{DisableAutomaticPing: true, DryRun: true})
	require.NoError(t, err)

	for _, referenceField := range referenceSchema.Fields {
		targetField := targetSchema.LookUpField(referenceField.Name)
		require.NotNil(t, targetField, "Token.%s is missing", referenceField.Name)
		assert.Equal(t, referenceField.DBName, targetField.DBName, referenceField.Name+" column")
		assert.Equal(t, referenceField.PrimaryKey, targetField.PrimaryKey, referenceField.Name+" primary key")
		assert.Equal(t, referenceField.AutoIncrement, targetField.AutoIncrement, referenceField.Name+" auto increment")
		assert.Equal(t, referenceField.NotNull, targetField.NotNull, referenceField.Name+" nullability")
		assert.Equal(t, referenceField.HasDefaultValue, targetField.HasDefaultValue, referenceField.Name+" default presence")
		assert.Equal(t, referenceField.DefaultValue, targetField.DefaultValue, referenceField.Name+" default")
		assert.Equal(t, referenceField.Size, targetField.Size, referenceField.Name+" size")
	}

	for _, dialect := range []gorm.Dialector{
		mysqlDB.Dialector,
		postgresDB.Dialector,
	} {
		t.Run(dialect.Name(), func(t *testing.T) {
			for _, referenceField := range referenceSchema.Fields {
				targetField := targetSchema.LookUpField(referenceField.Name)
				assert.Equal(t, dialect.DataTypeOf(referenceField), dialect.DataTypeOf(targetField),
					"Token.%s %s type", referenceField.Name, dialect.Name())
			}
		})
	}

	for _, referenceIndex := range referenceSchema.ParseIndexes() {
		targetIndex := targetSchema.LookIndex(referenceIndex.Name)
		require.NotNil(t, targetIndex, "missing Token index %s", referenceIndex.Name)
		assert.Equal(t, referenceIndex.Class, targetIndex.Class, referenceIndex.Name+" class")
		require.Len(t, targetIndex.Fields, len(referenceIndex.Fields), referenceIndex.Name+" fields")
		for i := range referenceIndex.Fields {
			assert.Equal(t, referenceIndex.Fields[i].DBName, targetIndex.Fields[i].DBName, referenceIndex.Name+" column")
			assert.Equal(t, referenceIndex.Fields[i].Priority, targetIndex.Fields[i].Priority, referenceIndex.Name+" priority")
		}
	}
}

func TestReferenceTokenSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceTokenSchemaMatchesSQLite(t, db)
	assertReferenceTokenSchema(t, db)

	longName := strings.Repeat("n", int(referenceTokenIndexedStringLimit))
	require.NoError(t, db.Exec(`
		INSERT INTO tokens
			(user_id, "key", name, created_time, accessed_time, unlimited_quota,
			 model_limits_enabled, model_limits, cross_group_retry, auto_groups)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		71, "sk-token-schema-direct-default", longName, int64(11), int64(12), false,
		false, "", false, "").Error)

	var direct Token
	require.NoError(t, db.Where(map[string]any{"key": "sk-token-schema-direct-default"}).First(&direct).Error)
	assert.Equal(t, 1, direct.Status)
	assert.EqualValues(t, -1, direct.ExpiredTime)
	assert.Zero(t, direct.RemainQuota)
	assert.Zero(t, direct.UsedQuota)
	assert.Equal(t, longName, direct.Name)
	assert.Empty(t, direct.Group)
	if assert.NotNil(t, direct.AllowIps) {
		assert.Empty(t, *direct.AllowIps)
	}

	require.NoError(t, db.Exec(`
		INSERT INTO tokens
			(user_id, "key", name, created_time, accessed_time, unlimited_quota,
			 model_limits_enabled, model_limits, allow_ips, "group", cross_group_retry, auto_groups)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?)`,
		72, "sk-token-schema-direct-null", "nullable IPs", int64(21), int64(22), false,
		false, "", "default", false, "").Error)
	var nullable Token
	require.NoError(t, db.Where(map[string]any{"key": "sk-token-schema-direct-null"}).First(&nullable).Error)
	assert.Nil(t, nullable.AllowIps)
	assert.Equal(t, "default", nullable.Group)

	columnsBefore := sqliteColumnSignatures(t, db, Token{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, Token{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, Token{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, Token{}.TableName()))
	assertReferenceTokenSchemaMatchesSQLite(t, db)
}

func TestReferenceTokenSchemaSQLitePreservesLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacyTokenSchema{}))

	legacy := legacyTokenSchema{
		UserId: 81, Key: "sk-token-schema-legacy", Status: 0,
		Name: strings.Repeat("w", int(referenceTokenIndexedStringLimit)), CreatedTime: 31, AccessedTime: 32,
		ExpiredTime: 0, RemainQuota: 321, UnlimitedQuota: false,
		ModelLimitsEnabled: true, ModelLimits: "model-a,model-b",
		AllowIps: strings.Repeat("i", 128), UsedQuota: 17,
		Group: strings.Repeat("g", 64), CrossGroupRetry: true, AutoGroups: `["g"]`,
	}
	require.NoError(t, db.Create(&legacy).Error)
	nullable := legacyTokenSchema{
		UserId: 82, Key: "sk-token-schema-legacy-null", Status: 4,
		Name: "legacy null IP", CreatedTime: 41, AccessedTime: 42,
		ExpiredTime: 43, RemainQuota: 44, AllowIps: "temporary", UsedQuota: 45,
		Group: "default",
	}
	require.NoError(t, db.Create(&nullable).Error)
	require.NoError(t, db.Exec("UPDATE tokens SET allow_ips = NULL WHERE id = ?", nullable.Id).Error)

	require.NoError(t, migrateDB())
	assertReferenceTokenSchemaMatchesSQLite(t, db)
	assertReferenceTokenSchema(t, db)
	assertLegacyTokenRowsPreserved(t, db, legacy, nullable)

	columnsBefore := sqliteColumnSignatures(t, db, Token{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, Token{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, Token{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, Token{}.TableName()))
	assertLegacyTokenRowsPreserved(t, db, legacy, nullable)
}

func assertReferenceTokenSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-token.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&referenceTokenSchema{}))
	assert.Equal(t,
		sqliteColumnSignatures(t, reference, Token{}.TableName()),
		sqliteColumnSignatures(t, db, Token{}.TableName()),
		"tokens columns",
	)
	assert.Equal(t,
		sqliteIndexSignatures(t, reference, Token{}.TableName()),
		sqliteIndexSignatures(t, db, Token{}.TableName()),
		"tokens indexes",
	)
}

// assertReferenceTokenSchema is shared with the opt-in MySQL/PostgreSQL gate,
// so the same portable metadata contract is compiled and exercised locally.
func assertReferenceTokenSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, index := range []struct {
		name   string
		unique bool
	}{
		{name: "idx_tokens_user_id"},
		{name: "idx_tokens_key", unique: true},
		{name: "idx_tokens_name"},
		{name: "idx_tokens_deleted_at"},
	} {
		assert.True(t, db.Migrator().HasIndex(&Token{}, index.name), "missing migrated index %s", index.name)
		assertIndexUnique(t, db, &Token{}, index.name, index.unique)
	}
	for column, expected := range map[string]string{
		"status":       "1",
		"expired_time": "-1",
		"remain_quota": "0",
		"allow_ips":    "",
		"used_quota":   "0",
		"group":        "",
	} {
		assertColumnDefault(t, db, Token{}.TableName(), column, expected)
		assertColumnNullable(t, db, Token{}.TableName(), column, true)
	}

	statement := &gorm.Statement{DB: db}
	require.NoError(t, statement.Parse(&Token{}))
	for _, fieldName := range []string{"Name", "AllowIps", "Group"} {
		field := statement.Schema.LookUpField(fieldName)
		require.NotNil(t, field, "Token.%s is missing", fieldName)
		assert.Empty(t, field.TagSettings["TYPE"], "Token.%s must use the reference's dialect-native string type", fieldName)
	}
	for _, fieldName := range []string{"CreatedTime", "AccessedTime", "ExpiredTime"} {
		field := statement.Schema.LookUpField(fieldName)
		require.NotNil(t, field, "Token.%s is missing", fieldName)
		_, declared := field.TagSettings["BIGINT"]
		assert.True(t, declared, "Token.%s must retain the reference's explicit bigint setting", fieldName)
		assert.Empty(t, field.TagSettings["TYPE"], "Token.%s must retain the reference tag semantics", fieldName)
	}

	switch db.Dialector.Name() {
	case "sqlite":
		for _, column := range []string{"name", "model_limits", "allow_ips", "group"} {
			assertColumnDatabaseType(t, db, Token{}.TableName(), column, "text")
		}
		for _, column := range []string{"created_time", "accessed_time", "expired_time"} {
			assertColumnDatabaseType(t, db, Token{}.TableName(), column, "integer")
		}
	case "mysql":
		assertColumnLength(t, db, Token{}.TableName(), "key", 128)
		assertColumnDatabaseType(t, db, Token{}.TableName(), "model_limits", "text")
		for _, column := range []string{"name", "allow_ips", "group"} {
			assertColumnLength(t, db, Token{}.TableName(), column, referenceTokenIndexedStringLimit)
		}
	case "postgres":
		assertColumnLength(t, db, Token{}.TableName(), "key", 128)
		for _, column := range []string{"name", "model_limits", "allow_ips", "group"} {
			assertColumnDatabaseType(t, db, Token{}.TableName(), column, "text")
		}
	default:
		require.FailNow(t, "unsupported token schema test dialect", db.Dialector.Name())
	}
}

func assertLegacyTokenRowsPreserved(t *testing.T, db *gorm.DB, legacy, nullable legacyTokenSchema) {
	t.Helper()
	var migrated Token
	require.NoError(t, db.Unscoped().First(&migrated, legacy.Id).Error)
	assert.Equal(t, legacy.UserId, migrated.UserId)
	assert.Equal(t, legacy.Key, migrated.Key)
	assert.Equal(t, legacy.Status, migrated.Status, "adding a default must not enable a legacy token")
	assert.Equal(t, legacy.Name, migrated.Name)
	assert.Equal(t, legacy.CreatedTime, migrated.CreatedTime)
	assert.Equal(t, legacy.AccessedTime, migrated.AccessedTime)
	assert.Equal(t, legacy.ExpiredTime, migrated.ExpiredTime)
	assert.Equal(t, legacy.RemainQuota, migrated.RemainQuota)
	assert.Equal(t, legacy.UnlimitedQuota, migrated.UnlimitedQuota)
	assert.Equal(t, legacy.ModelLimitsEnabled, migrated.ModelLimitsEnabled)
	assert.Equal(t, legacy.ModelLimits, migrated.ModelLimits)
	assert.Equal(t, legacy.UsedQuota, migrated.UsedQuota)
	assert.Equal(t, legacy.Group, migrated.Group)
	assert.Equal(t, legacy.CrossGroupRetry, migrated.CrossGroupRetry)
	assert.Equal(t, legacy.AutoGroups, migrated.AutoGroups)
	assert.Equal(t, legacy.DeletedAt.Valid, migrated.DeletedAt.Valid)
	if assert.NotNil(t, migrated.AllowIps) {
		assert.Equal(t, legacy.AllowIps, *migrated.AllowIps)
	}

	var migratedNullable Token
	require.NoError(t, db.Unscoped().First(&migratedNullable, nullable.Id).Error)
	assert.Equal(t, nullable.UserId, migratedNullable.UserId)
	assert.Equal(t, nullable.Key, migratedNullable.Key)
	assert.Equal(t, nullable.Status, migratedNullable.Status)
	assert.Equal(t, nullable.Name, migratedNullable.Name)
	assert.Equal(t, nullable.CreatedTime, migratedNullable.CreatedTime)
	assert.Equal(t, nullable.AccessedTime, migratedNullable.AccessedTime)
	assert.Equal(t, nullable.ExpiredTime, migratedNullable.ExpiredTime)
	assert.Equal(t, nullable.RemainQuota, migratedNullable.RemainQuota)
	assert.Equal(t, nullable.UsedQuota, migratedNullable.UsedQuota)
	assert.Equal(t, nullable.Group, migratedNullable.Group)
	assert.Nil(t, migratedNullable.AllowIps, "an explicit legacy NULL must not be rewritten to the new default")
}

func exerciseReferenceTokenSchemaLegacyExternal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Contains(t, []string{"mysql", "postgres"}, db.Dialector.Name())
	require.NoError(t, db.Migrator().DropTable(&Token{}))
	require.NoError(t, db.AutoMigrate(&legacyTokenSchema{}))

	legacy := legacyTokenSchema{
		UserId: 91, Key: "sk-token-schema-server-legacy", Status: 0,
		Name: strings.Repeat("n", 64), CreatedTime: 51, AccessedTime: 52,
		ExpiredTime: 0, RemainQuota: 53, ModelLimitsEnabled: true,
		ModelLimits: "model-a", AllowIps: strings.Repeat("i", 128), UsedQuota: 54,
		Group: strings.Repeat("g", 64), CrossGroupRetry: true, AutoGroups: `["g"]`,
	}
	require.NoError(t, db.Create(&legacy).Error)
	nullable := legacyTokenSchema{
		UserId: 92, Key: "sk-token-schema-server-null", Status: 4,
		Name: "server null IP", CreatedTime: 61, AccessedTime: 62,
		ExpiredTime: 63, RemainQuota: 64, AllowIps: "temporary", UsedQuota: 65,
		Group: "default",
	}
	require.NoError(t, db.Create(&nullable).Error)
	require.NoError(t, db.Exec("UPDATE tokens SET allow_ips = NULL WHERE id = ?", nullable.Id).Error)

	require.NoError(t, migrateDB())
	assertReferenceTokenSchema(t, db)
	assertLegacyTokenRowsPreserved(t, db, legacy, nullable)

	// A second server startup must neither repeat unsafe DDL nor rewrite rows.
	require.NoError(t, migrateDB())
	assertReferenceTokenSchema(t, db)
	assertLegacyTokenRowsPreserved(t, db, legacy, nullable)
}
