package model

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestReferenceAlignedCoreSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceAlignedCoreSchema(t, db)

	// Defaults and nullable fields must also work for direct/imported rows,
	// rather than depending on service-layer constructors.
	require.NoError(t, db.Exec(`
		INSERT INTO abilities ("group", model, channel_id, enabled)
		VALUES (?, ?, ?, ?)`, "default", "schema-default-model", 41, true).Error)
	var ability Ability
	require.NoError(t, db.First(&ability, "`group` = ? AND model = ? AND channel_id = ?",
		"default", "schema-default-model", 41).Error)
	require.NotNil(t, ability.Priority)
	assert.Zero(t, *ability.Priority)
	assert.Zero(t, ability.Weight)
	assert.Nil(t, ability.Tag)

	require.NoError(t, db.Exec(`
		INSERT INTO redemptions (user_id, key, name, created_time, redeemed_time, used_user_id, expired_time)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, 1, "12345678901234567890123456789012", "schema defaults", 1, 0, 0, 0).Error)
	var redemption Redemption
	require.NoError(t, db.Where(map[string]any{"key": "12345678901234567890123456789012"}).First(&redemption).Error)
	assert.Equal(t, 1, redemption.Status)
	assert.Equal(t, 100, redemption.Quota)

	require.NoError(t, db.Exec(`INSERT INTO quota_data (user_id, created_at) VALUES (?, ?)`, 9, int64(3600)).Error)
	var quotaData QuotaData
	require.NoError(t, db.Where("user_id = ?", 9).First(&quotaData).Error)
	assert.Empty(t, quotaData.Username)
	assert.Empty(t, quotaData.ModelName)
	assert.Empty(t, quotaData.UseGroup)
	assert.Empty(t, quotaData.NodeName)
	assert.Zero(t, quotaData.TokenID)
	assert.Zero(t, quotaData.ChannelID)
	assert.Zero(t, quotaData.TokenUsed)
	assert.Zero(t, quotaData.Count)
	assert.Zero(t, quotaData.Quota)
}

func TestReferenceAlignedCoreSchemaPreservesLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacyAbilitySchema{}, &legacyRedemptionSchema{}, &legacyQuotaDataSchema{}))

	priority := int64(7)
	require.NoError(t, db.Create(&legacyAbilitySchema{
		Group: "legacy", Model: "legacy-model", ChannelId: 17, Enabled: true,
		Priority: &priority, Weight: 3, Tag: strings.Repeat("t", 80),
	}).Error)
	require.NoError(t, db.Create(&legacyRedemptionSchema{
		UserId: 3, Key: "abcdefghijklmnopqrstuvwxyz123456", Status: 0,
		Name: strings.Repeat("n", 80), Quota: 321, CreatedTime: 10,
	}).Error)
	require.NoError(t, db.Create(&legacyQuotaDataSchema{
		UserID: 4, Username: "legacy-user", ModelName: strings.Repeat("m", 64),
		CreatedAt: 7200, UseGroup: "legacy", TokenID: 5, ChannelID: 6,
		NodeName: strings.Repeat("n", 64), TokenUsed: 7, Count: 8, Quota: 9,
	}).Error)

	require.NoError(t, migrateDB())
	assertReferenceAlignedCoreSchema(t, db)

	var ability Ability
	require.NoError(t, db.First(&ability, "`group` = ? AND model = ? AND channel_id = ?", "legacy", "legacy-model", 17).Error)
	require.NotNil(t, ability.Tag)
	assert.Equal(t, strings.Repeat("t", 80), *ability.Tag)
	require.NotNil(t, ability.Priority)
	assert.Equal(t, priority, *ability.Priority)
	assert.Equal(t, uint(3), ability.Weight)

	var redemption Redemption
	require.NoError(t, db.Where(map[string]any{"key": "abcdefghijklmnopqrstuvwxyz123456"}).First(&redemption).Error)
	assert.Zero(t, redemption.Status, "adding a default must not rewrite a legacy terminal status")
	assert.Equal(t, strings.Repeat("n", 80), redemption.Name)
	assert.Equal(t, 321, redemption.Quota)

	var quotaData QuotaData
	require.NoError(t, db.Where("user_id = ?", 4).First(&quotaData).Error)
	assert.Equal(t, strings.Repeat("m", 64), quotaData.ModelName)
	assert.Equal(t, strings.Repeat("n", 64), quotaData.NodeName)
	assert.Equal(t, 7, quotaData.TokenUsed)
	assert.Equal(t, 8, quotaData.Count)
	assert.Equal(t, 9, quotaData.Quota)

	// Repeated startup remains idempotent after the legacy table rebuild.
	require.NoError(t, migrateDB())
}

func TestReferenceSchemaNarrowingCheckRejectsOversizedQuotaDimensions(t *testing.T) {
	for _, test := range []struct {
		name      string
		modelName string
		nodeName  string
		column    string
	}{
		{name: "model name", modelName: strings.Repeat("m", 65), nodeName: "node", column: "model_name"},
		{name: "node name", modelName: "model", nodeName: strings.Repeat("n", 65), column: "node_name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newReferenceSchemaTestDB(t)
			require.NoError(t, db.AutoMigrate(&legacyQuotaDataSchema{}))
			require.NoError(t, db.Create(&legacyQuotaDataSchema{
				UserID: 1, ModelName: test.modelName, NodeName: test.nodeName,
			}).Error)

			err := rejectOversizedLegacyQuotaDimension(db, test.column, referenceQuotaDimensionLimit)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
			assert.Contains(t, err.Error(), "quota_data."+test.column)

			var stored legacyQuotaDataSchema
			require.NoError(t, db.First(&stored).Error)
			assert.Equal(t, test.modelName, stored.ModelName)
			assert.Equal(t, test.nodeName, stored.NodeName)
			assert.False(t, db.Migrator().HasTable(&User{}), "the preflight itself must not mutate unrelated schema")

			// SQLite has no enforced VARCHAR width, so its startup migration can
			// safely retain an oversized legacy value rather than blocking or
			// truncating it. Server dialects exercise the fail-closed startup
			// branch in TestDatabaseMatrixExternalMigration.
			require.NoError(t, migrateDB())
			var migrated QuotaData
			require.NoError(t, db.First(&migrated).Error)
			assert.Equal(t, test.modelName, migrated.ModelName)
			assert.Equal(t, test.nodeName, migrated.NodeName)
		})
	}
}

func TestPostgresReferenceLogIndexDropUsesValidSearchPathSyntax(t *testing.T) {
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=gorm dbname=gorm sslmode=disable",
	}), &gorm.Config{DisableAutomaticPing: true, DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, `DROP INDEX IF EXISTS "idx_user_id_id"`,
		postgresDropIndexStatement(db, "idx_user_id_id"))
}

func requirePortableIndex(t *testing.T, db *gorm.DB, entity any, table, name string) ([]string, bool) {
	t.Helper()
	if db.Dialector.Name() == "postgres" {
		indexes, err := inspectPostgresReferenceIndexes(db, table)
		require.NoError(t, err)
		index, ok := indexes[strings.ToLower(name)]
		require.True(t, ok, "missing index metadata for %s", name)
		require.True(t, index.uniqueKnown, "index %s uniqueness is unknown", name)
		return index.columns, index.unique
	}
	indexes, err := db.Migrator().GetIndexes(entity)
	require.NoError(t, err)
	for _, index := range indexes {
		if index.Name() != name {
			continue
		}
		unique, known := index.Unique()
		require.True(t, known, "index %s uniqueness is unknown", name)
		return index.Columns(), unique
	}
	require.FailNow(t, "missing index metadata", name)
	return nil, false
}

func exerciseReferenceSchemaLegacyNarrowingExternal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Contains(t, []string{"mysql", "postgres"}, db.Dialector.Name())

	// First prove representative legacy rows for all three aligned entities
	// survive the real server ALTER/default-reconciliation path.
	require.NoError(t, db.Migrator().DropTable(&Ability{}, &Redemption{}, &QuotaData{}))
	require.NoError(t, db.AutoMigrate(&legacyAbilitySchema{}, &legacyRedemptionSchema{}, &legacyQuotaDataSchema{}))
	priority := int64(7)
	legacyAbility := legacyAbilitySchema{
		Group: "legacy-server", Model: "legacy-model", ChannelId: 43, Enabled: true,
		Priority: &priority, Weight: 3, Tag: strings.Repeat("t", 64),
	}
	require.NoError(t, db.Create(&legacyAbility).Error)
	legacyRedemption := legacyRedemptionSchema{
		UserId: 3, Key: "serverlegacyredemptionkey1234567", Status: 0,
		Name: strings.Repeat("r", 64), Quota: 321, CreatedTime: 10,
	}
	require.Len(t, legacyRedemption.Key, 32)
	require.NoError(t, db.Create(&legacyRedemption).Error)
	boundary := legacyQuotaDataSchema{
		UserID: 44, Username: "legacy-server", ModelName: strings.Repeat("m", 64),
		NodeName: strings.Repeat("n", 64), Count: 1,
	}
	require.NoError(t, db.Create(&boundary).Error)
	require.NoError(t, migrateDB())
	var migratedAbility Ability
	require.NoError(t, db.Where(map[string]any{
		"group": legacyAbility.Group, "model": legacyAbility.Model, "channel_id": legacyAbility.ChannelId,
	}).First(&migratedAbility).Error)
	require.NotNil(t, migratedAbility.Tag)
	assert.Equal(t, legacyAbility.Tag, *migratedAbility.Tag)
	require.NotNil(t, migratedAbility.Priority)
	assert.Equal(t, priority, *migratedAbility.Priority)
	assert.Equal(t, legacyAbility.Weight, migratedAbility.Weight)
	var migratedRedemption Redemption
	require.NoError(t, db.Where(map[string]any{"key": legacyRedemption.Key}).First(&migratedRedemption).Error)
	assert.Zero(t, migratedRedemption.Status)
	assert.Equal(t, legacyRedemption.Name, migratedRedemption.Name)
	assert.Equal(t, legacyRedemption.Quota, migratedRedemption.Quota)
	var migrated QuotaData
	require.NoError(t, db.Where("user_id = ?", boundary.UserID).First(&migrated).Error)
	assert.Equal(t, boundary.ModelName, migrated.ModelName)
	assert.Equal(t, boundary.NodeName, migrated.NodeName)
	assertReferenceAlignedCoreSchema(t, db)

	// Then prove each unsafe legacy width aborts the complete startup
	// migration before AutoMigrate can issue a narrowing ALTER.
	for _, test := range []struct {
		name      string
		modelName string
		nodeName  string
		column    string
	}{
		{name: "model name", modelName: strings.Repeat("m", 65), nodeName: "node", column: "model_name"},
		{name: "node name", modelName: "model", nodeName: strings.Repeat("n", 65), column: "node_name"},
	} {
		t.Run("external oversized "+test.name, func(t *testing.T) {
			require.NoError(t, db.Migrator().DropTable(&QuotaData{}))
			require.NoError(t, db.AutoMigrate(&legacyQuotaDataSchema{}))
			legacy := legacyQuotaDataSchema{UserID: 45, ModelName: test.modelName, NodeName: test.nodeName}
			require.NoError(t, db.Create(&legacy).Error)

			err := migrateDB()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
			assert.Contains(t, err.Error(), "quota_data."+test.column)
			var stored legacyQuotaDataSchema
			require.NoError(t, db.First(&stored, legacy.Id).Error)
			assert.Equal(t, test.modelName, stored.ModelName)
			assert.Equal(t, test.nodeName, stored.NodeName)

			require.NoError(t, db.Delete(&stored).Error)
			require.NoError(t, migrateDB())
			assertReferenceAlignedCoreSchema(t, db)
		})
	}
}

func newReferenceSchemaTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-schema.db")), &gorm.Config{})
	require.NoError(t, err)
	previousDB, previousLogDB := DB, LOG_DB
	DB, LOG_DB = db, db
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
	})
	return db
}

// assertReferenceAlignedCoreSchema is intentionally driver-neutral. The
// SQLite tests call it directly and the opt-in MySQL/PostgreSQL migration gate
// calls the same assertions against their real metadata.
func assertReferenceAlignedCoreSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, index := range []struct {
		model any
		name  string
	}{
		{&Ability{}, "idx_abilities_channel_id"},
		{&Ability{}, "idx_abilities_priority"},
		{&Ability{}, "idx_abilities_weight"},
		{&Ability{}, "idx_abilities_tag"},
		{&Redemption{}, "idx_redemptions_name"},
		{&QuotaData{}, "idx_qdt_model_user_name"},
		{&QuotaData{}, "idx_qdt_created_at"},
	} {
		assert.True(t, db.Migrator().HasIndex(index.model, index.name), "missing migrated index %s", index.name)
	}

	assertColumnNullable(t, db, "abilities", "tag", true)
	assertColumnDefault(t, db, "abilities", "priority", "0")
	assertColumnDefault(t, db, "abilities", "weight", "0")
	assertColumnDefault(t, db, "redemptions", "status", "1")
	assertColumnDefault(t, db, "redemptions", "quota", "100")
	for _, column := range []string{"username", "model_name", "use_group", "node_name"} {
		assertColumnDefault(t, db, "quota_data", column, "")
	}
	for _, column := range []string{"token_id", "channel_id", "token_used", "count", "quota"} {
		assertColumnDefault(t, db, "quota_data", column, "0")
	}
	if db.Dialector.Name() == "sqlite" {
		statement := &gorm.Statement{DB: db}
		require.NoError(t, statement.Parse(&QuotaData{}))
		for _, fieldName := range []string{"Username", "ModelName", "UseGroup", "NodeName"} {
			field := statement.Schema.LookUpField(fieldName)
			require.NotNil(t, field, "QuotaData.%s is missing", fieldName)
			assert.Equal(t, referenceQuotaDimensionLimit, field.Size, "QuotaData.%s declared size", fieldName)
		}
	} else {
		for _, column := range []string{"username", "model_name", "use_group", "node_name"} {
			assertColumnLength(t, db, "quota_data", column, referenceQuotaDimensionLimit)
		}
	}
	assertReferenceMetadataSchema(t, db)
}

func assertColumnNullable(t *testing.T, db *gorm.DB, table, name string, expected bool) {
	t.Helper()
	column := requireColumnType(t, db, table, name)
	nullable, known := column.Nullable()
	require.True(t, known, "%s.%s nullability is unknown", table, name)
	assert.Equal(t, expected, nullable, "%s.%s nullability", table, name)
}

func assertColumnDefault(t *testing.T, db *gorm.DB, table, name, expected string) {
	t.Helper()
	column := requireColumnType(t, db, table, name)
	value, ok := column.DefaultValue()
	require.True(t, ok, "%s.%s has no database default", table, name)
	assert.Equal(t, expected, normalizeDatabaseDefault(value), "%s.%s default", table, name)
}

func assertColumnLength(t *testing.T, db *gorm.DB, table, name string, expected int64) {
	t.Helper()
	column := requireColumnType(t, db, table, name)
	length, known := column.Length()
	require.True(t, known, "%s.%s length is unknown", table, name)
	assert.Equal(t, expected, length, "%s.%s length", table, name)
}

func requireColumnType(t *testing.T, db *gorm.DB, table, name string) gorm.ColumnType {
	t.Helper()
	columns, err := db.Migrator().ColumnTypes(table)
	require.NoError(t, err)
	for _, column := range columns {
		if strings.EqualFold(column.Name(), name) {
			return column
		}
	}
	require.FailNow(t, fmt.Sprintf("missing column %s.%s", table, name))
	return nil
}

func normalizeDatabaseDefault(value string) string {
	return normalizeReferenceSchemaDefault(value)
}

type legacyAbilitySchema struct {
	Group     string `gorm:"primaryKey;autoIncrement:false;type:varchar(64)"`
	Model     string `gorm:"primaryKey;autoIncrement:false;type:varchar(255)"`
	ChannelId int    `gorm:"primaryKey;autoIncrement:false"`
	Enabled   bool
	Priority  *int64 `gorm:"index"`
	Weight    uint   `gorm:"index"`
	Tag       string `gorm:"index;type:varchar(64)"`
}

func (legacyAbilitySchema) TableName() string { return "abilities" }

type legacyRedemptionSchema struct {
	Id           int `gorm:"primaryKey"`
	UserId       int
	Key          string `gorm:"type:char(32);uniqueIndex"`
	Status       int
	Name         string `gorm:"index;type:varchar(64)"`
	Quota        int    `gorm:"default:100"`
	CreatedTime  int64
	RedeemedTime int64
	UsedUserId   int
	ExpiredTime  int64
	DeletedAt    gorm.DeletedAt `gorm:"index"`
}

func (legacyRedemptionSchema) TableName() string { return "redemptions" }

type legacyQuotaDataSchema struct {
	Id        int    `gorm:"primaryKey"`
	UserID    int    `gorm:"index"`
	Username  string `gorm:"index:idx_qdt_model_user_name,priority:2;type:varchar(64)"`
	ModelName string `gorm:"index:idx_qdt_model_user_name,priority:1;type:varchar(255)"`
	CreatedAt int64  `gorm:"index:idx_qdt_created_at"`
	UseGroup  string `gorm:"index;type:varchar(64)"`
	TokenID   int    `gorm:"index"`
	ChannelID int    `gorm:"index"`
	NodeName  string `gorm:"index;type:varchar(128)"`
	TokenUsed int
	Count     int
	Quota     int
}

func (legacyQuotaDataSchema) TableName() string { return "quota_data" }
