package model

import (
	"database/sql"
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

// referenceSubscriptionPreConsumeSchema independently transcribes the pinned
// reference declaration. TokenRouter's UsageEpoch durability fence and its
// stricter non-null request identity are handled as explicit extensions.
type referenceSubscriptionPreConsumeSchema struct {
	Id                 int
	RequestId          string `gorm:"type:varchar(64);uniqueIndex"`
	UserId             int    `gorm:"index"`
	UserSubscriptionId int    `gorm:"index"`
	PreConsumed        int64  `gorm:"type:bigint;not null;default:0"`
	Status             string `gorm:"type:varchar(32);index"`
	CreatedAt          int64  `gorm:"bigint"`
	UpdatedAt          int64  `gorm:"bigint;index"`
}

func (referenceSubscriptionPreConsumeSchema) TableName() string {
	return "subscription_pre_consume_records"
}

// legacySubscriptionPreConsumeSchema is the target immediately before this
// slice. It retains the already-enforced request identity and UsageEpoch, while
// pre_consumed and the timestamp declarations exercise the real upgrade path.
type legacySubscriptionPreConsumeSchema struct {
	Id                 int    `gorm:"primaryKey"`
	RequestId          string `gorm:"type:varchar(64);not null;uniqueIndex"`
	UserId             int    `gorm:"index"`
	UserSubscriptionId int    `gorm:"index"`
	PreConsumed        int64
	UsageEpoch         int64  `gorm:"type:bigint;not null;default:0"`
	Status             string `gorm:"type:varchar(32);index"`
	CreatedAt          int64
	UpdatedAt          int64 `gorm:"index"`
}

func (legacySubscriptionPreConsumeSchema) TableName() string {
	return "subscription_pre_consume_records"
}

type expectedSubscriptionPreConsumeIndex struct {
	name    string
	columns []string
	unique  bool
}

var expectedReferenceSubscriptionPreConsumeIndexes = []expectedSubscriptionPreConsumeIndex{
	{name: "idx_subscription_pre_consume_records_request_id", columns: []string{"request_id"}, unique: true},
	{name: "idx_subscription_pre_consume_records_user_id", columns: []string{"user_id"}},
	{name: "idx_subscription_pre_consume_records_user_subscription_id", columns: []string{"user_subscription_id"}},
	{name: "idx_subscription_pre_consume_records_status", columns: []string{"status"}},
	{name: "idx_subscription_pre_consume_records_updated_at", columns: []string{"updated_at"}},
}

func TestReferenceSubscriptionPreConsumeSchemaPortableDialects(t *testing.T) {
	targetSchema, err := schema.Parse(&SubscriptionPreConsumeRecord{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceSchema, err := schema.Parse(&referenceSubscriptionPreConsumeSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	for _, referenceField := range referenceSchema.Fields {
		targetField := targetSchema.LookUpField(referenceField.Name)
		require.NotNil(t, targetField, "SubscriptionPreConsumeRecord.%s is missing", referenceField.Name)
		assert.Equal(t, referenceField.DBName, targetField.DBName, referenceField.Name+" column")
		assert.Equal(t, referenceField.PrimaryKey, targetField.PrimaryKey, referenceField.Name+" primary key")
		assert.Equal(t, referenceField.AutoIncrement, targetField.AutoIncrement, referenceField.Name+" auto increment")
		if referenceField.Name == "RequestId" {
			assert.False(t, referenceField.NotNull, "the reference permits a NULL request identity")
			assert.True(t, targetField.NotNull, "the target must retain its stricter request identity")
		} else {
			assert.Equal(t, referenceField.NotNull, targetField.NotNull, referenceField.Name+" nullability")
		}
		assert.Equal(t, referenceField.HasDefaultValue, targetField.HasDefaultValue, referenceField.Name+" default presence")
		assert.Equal(t, referenceField.DefaultValue, targetField.DefaultValue, referenceField.Name+" default")
		assert.Equal(t, referenceField.Size, targetField.Size, referenceField.Name+" size")
	}
	assert.Nil(t, referenceSchema.LookUpField("UsageEpoch"), "UsageEpoch must remain an explicit target extension")
	require.NotNil(t, targetSchema.LookUpField("UsageEpoch"), "UsageEpoch durability fence")
	assert.Equal(t, len(referenceSchema.Fields)+1, len(targetSchema.Fields))

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
					"SubscriptionPreConsumeRecord.%s %s type", referenceField.Name, dialect.Name())
			}
			assert.Equal(t,
				dialect.DataTypeOf(targetSchema.LookUpField("PreConsumed")),
				dialect.DataTypeOf(targetSchema.LookUpField("UsageEpoch")),
				"UsageEpoch must retain the ledger's portable bigint type",
			)
		})
	}

	referenceIndexes := referenceSchema.ParseIndexes()
	targetIndexes := targetSchema.ParseIndexes()
	assert.Len(t, targetIndexes, len(referenceIndexes), "UsageEpoch must not add an unproven lookup index")
	for _, referenceIndex := range referenceIndexes {
		targetIndex := targetSchema.LookIndex(referenceIndex.Name)
		require.NotNil(t, targetIndex, "missing SubscriptionPreConsumeRecord index %s", referenceIndex.Name)
		assert.Equal(t, referenceIndex.Class, targetIndex.Class, referenceIndex.Name+" class")
		require.Len(t, targetIndex.Fields, len(referenceIndex.Fields), referenceIndex.Name+" fields")
		for i := range referenceIndex.Fields {
			assert.Equal(t, referenceIndex.Fields[i].DBName, targetIndex.Fields[i].DBName,
				referenceIndex.Name+" column")
			assert.Equal(t, referenceIndex.Fields[i].Priority, targetIndex.Fields[i].Priority,
				referenceIndex.Name+" priority")
		}
	}

	preConsumed := targetSchema.LookUpField("PreConsumed")
	require.NotNil(t, preConsumed)
	assert.Equal(t, "bigint", strings.ToLower(preConsumed.TagSettings["TYPE"]))
	for _, fieldName := range []string{"CreatedAt", "UpdatedAt"} {
		field := targetSchema.LookUpField(fieldName)
		require.NotNil(t, field)
		_, declared := field.TagSettings["BIGINT"]
		assert.True(t, declared, "SubscriptionPreConsumeRecord.%s must retain the reference bigint tag", fieldName)
		assert.Empty(t, field.TagSettings["TYPE"],
			"SubscriptionPreConsumeRecord.%s must retain the exact reference tag form", fieldName)
	}
}

func TestReferenceSubscriptionPreConsumeSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceSubscriptionPreConsumeSchemaMatchesSQLite(t, db)
	assertReferenceSubscriptionPreConsumeSchema(t, db)
	requestID := insertReferenceSubscriptionPreConsumeWithDatabaseDefaults(t, db, "fresh")
	assertSubscriptionPreConsumeRequestIdentity(t, db, requestID)

	created := SubscriptionPreConsumeRecord{RequestId: "orm-preconsume-fresh", UserId: 71, UserSubscriptionId: 72}
	require.NoError(t, db.Create(&created).Error)
	assert.Zero(t, created.PreConsumed)
	assert.Zero(t, created.UsageEpoch)
	assert.Positive(t, created.CreatedAt)
	assert.Positive(t, created.UpdatedAt)

	columnsBefore := sqliteColumnSignatures(t, db, SubscriptionPreConsumeRecord{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, SubscriptionPreConsumeRecord{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, SubscriptionPreConsumeRecord{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, SubscriptionPreConsumeRecord{}.TableName()))
	assertReferenceSubscriptionPreConsumeSchemaMatchesSQLite(t, db)
}

func TestReferenceSubscriptionPreConsumeSchemaSQLitePreservesLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacySubscriptionPreConsumeSchema{}))
	legacy := makeLegacyReferenceSubscriptionPreConsume(81)
	require.NoError(t, db.Create(&legacy).Error)
	zero := makeLegacyReferenceSubscriptionPreConsume(82)
	zero.PreConsumed = 0
	require.NoError(t, db.Create(&zero).Error)

	require.NoError(t, migrateDB())
	assertReferenceSubscriptionPreConsumeSchemaMatchesSQLite(t, db)
	assertReferenceSubscriptionPreConsumeSchema(t, db)
	assertLegacyReferenceSubscriptionPreConsumePreserved(t, db, legacy)
	assertLegacyReferenceSubscriptionPreConsumePreserved(t, db, zero)
	insertReferenceSubscriptionPreConsumeWithDatabaseDefaults(t, db, "legacy")

	columnsBefore := sqliteColumnSignatures(t, db, SubscriptionPreConsumeRecord{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, SubscriptionPreConsumeRecord{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, SubscriptionPreConsumeRecord{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, SubscriptionPreConsumeRecord{}.TableName()))
	assertLegacyReferenceSubscriptionPreConsumePreserved(t, db, legacy)
	assertLegacyReferenceSubscriptionPreConsumePreserved(t, db, zero)
}

func TestReferenceSubscriptionPreConsumeSchemaPreflightRejectsNullFinancialState(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacySubscriptionPreConsumeSchema{}))
	legacy := makeLegacyReferenceSubscriptionPreConsume(91)
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, db.Exec(`UPDATE subscription_pre_consume_records SET pre_consumed = NULL WHERE id = ?`, legacy.Id).Error)

	err := migrateDB()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
	assert.Contains(t, err.Error(), "subscription_pre_consume_records.pre_consumed")
	var stored sql.NullInt64
	require.NoError(t, db.Table(SubscriptionPreConsumeRecord{}.TableName()).Select("pre_consumed").
		Where("id = ?", legacy.Id).Row().Scan(&stored))
	assert.False(t, stored.Valid, "a rejected migration must not rewrite financial NULL to zero")
	assert.False(t, db.Migrator().HasTable(&User{}), "the preflight must abort before aggregate schema mutation")

	require.NoError(t, db.Exec(`UPDATE subscription_pre_consume_records SET pre_consumed = 0 WHERE id = ?`, legacy.Id).Error)
	require.NoError(t, migrateDB())
	assertReferenceSubscriptionPreConsumeSchema(t, db)
}

func assertReferenceSubscriptionPreConsumeSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-preconsume.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&referenceSubscriptionPreConsumeSchema{}))

	assert.Equal(t,
		sqliteColumnSignatures(t, reference, SubscriptionPreConsumeRecord{}.TableName()),
		normalizeSubscriptionPreConsumeTargetColumns(
			sqliteColumnSignatures(t, db, SubscriptionPreConsumeRecord{}.TableName())),
		"SubscriptionPreConsumeRecord shared columns after explicit target hardening is normalized",
	)
	assert.Equal(t,
		sqliteIndexSignatures(t, reference, SubscriptionPreConsumeRecord{}.TableName()),
		sqliteIndexSignatures(t, db, SubscriptionPreConsumeRecord{}.TableName()),
		"SubscriptionPreConsumeRecord reference indexes",
	)
}

// assertReferenceSubscriptionPreConsumeSchema is shared by SQLite and the
// opt-in MySQL/PostgreSQL migration gate.
func assertReferenceSubscriptionPreConsumeSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, index := range expectedReferenceSubscriptionPreConsumeIndexes {
		assertSubscriptionPreConsumeIndex(t, db, index)
	}
	assertColumnDefault(t, db, SubscriptionPreConsumeRecord{}.TableName(), "pre_consumed", "0")
	assertColumnDefault(t, db, SubscriptionPreConsumeRecord{}.TableName(), "usage_epoch", "0")
	assertColumnNullable(t, db, SubscriptionPreConsumeRecord{}.TableName(), "request_id", false)
	assertColumnNullable(t, db, SubscriptionPreConsumeRecord{}.TableName(), "pre_consumed", false)
	assertColumnNullable(t, db, SubscriptionPreConsumeRecord{}.TableName(), "usage_epoch", false)
	for _, column := range []string{"user_id", "user_subscription_id", "status", "created_at", "updated_at"} {
		assertColumnNullable(t, db, SubscriptionPreConsumeRecord{}.TableName(), column, true)
	}

	switch db.Dialector.Name() {
	case "sqlite":
		for _, column := range []string{"pre_consumed", "usage_epoch"} {
			assertColumnDatabaseType(t, db, SubscriptionPreConsumeRecord{}.TableName(), column, "bigint")
		}
		for _, column := range []string{"created_at", "updated_at"} {
			assertColumnDatabaseType(t, db, SubscriptionPreConsumeRecord{}.TableName(), column, "integer")
		}
	case "mysql":
		for _, column := range []string{"pre_consumed", "usage_epoch", "created_at", "updated_at"} {
			assertColumnDatabaseType(t, db, SubscriptionPreConsumeRecord{}.TableName(), column, "bigint")
		}
		assertReferenceSubscriptionPreConsumeLengths(t, db)
	case "postgres":
		for _, column := range []string{"pre_consumed", "usage_epoch", "created_at", "updated_at"} {
			assertColumnDatabaseType(t, db, SubscriptionPreConsumeRecord{}.TableName(), column, "int8")
		}
		assertReferenceSubscriptionPreConsumeLengths(t, db)
	default:
		require.FailNow(t, "unsupported SubscriptionPreConsumeRecord schema test dialect", db.Dialector.Name())
	}
}

func assertReferenceSubscriptionPreConsumeLengths(t *testing.T, db *gorm.DB) {
	t.Helper()
	assertColumnLength(t, db, SubscriptionPreConsumeRecord{}.TableName(), "request_id", 64)
	assertColumnLength(t, db, SubscriptionPreConsumeRecord{}.TableName(), "status", 32)
}

func assertSubscriptionPreConsumeIndex(t *testing.T, db *gorm.DB, expected expectedSubscriptionPreConsumeIndex) {
	t.Helper()
	columns, unique := requirePortableIndex(t, db, &SubscriptionPreConsumeRecord{}, SubscriptionPreConsumeRecord{}.TableName(), expected.name)
	assert.Equal(t, expected.columns, columns, expected.name+" columns")
	assert.Equal(t, expected.unique, unique, expected.name+" uniqueness")
}

func insertReferenceSubscriptionPreConsumeWithDatabaseDefaults(t *testing.T, db *gorm.DB, marker string) string {
	t.Helper()
	requestID := "preconsume-default-" + marker
	require.NoError(t, db.Exec(`INSERT INTO subscription_pre_consume_records (request_id) VALUES (?)`, requestID).Error)

	var id int
	var request sql.NullString
	var userID, userSubscriptionID, preConsumed, usageEpoch sql.NullInt64
	var status sql.NullString
	var createdAt, updatedAt sql.NullInt64
	row := db.Table(SubscriptionPreConsumeRecord{}.TableName()).
		Select("id, request_id, user_id, user_subscription_id, pre_consumed, usage_epoch, status, created_at, updated_at").
		Where("request_id = ?", requestID).Row()
	require.NoError(t, row.Scan(&id, &request, &userID, &userSubscriptionID, &preConsumed,
		&usageEpoch, &status, &createdAt, &updatedAt))
	assert.Positive(t, id)
	require.True(t, request.Valid)
	assert.Equal(t, requestID, request.String)
	require.True(t, preConsumed.Valid)
	assert.Zero(t, preConsumed.Int64)
	require.True(t, usageEpoch.Valid)
	assert.Zero(t, usageEpoch.Int64)
	for name, valid := range map[string]bool{
		"user_id": userID.Valid, "user_subscription_id": userSubscriptionID.Valid,
		"status": status.Valid, "created_at": createdAt.Valid, "updated_at": updatedAt.Valid,
	} {
		assert.False(t, valid, "SubscriptionPreConsumeRecord.%s must retain the reference's absent database default", name)
	}
	return requestID
}

func assertSubscriptionPreConsumeRequestIdentity(t *testing.T, db *gorm.DB, requestID string) {
	t.Helper()
	assert.Error(t, db.Exec(`INSERT INTO subscription_pre_consume_records (request_id) VALUES (?)`, requestID).Error,
		"request identities must remain unique")
	assert.Error(t, db.Exec(`INSERT INTO subscription_pre_consume_records (request_id) VALUES (NULL)`).Error,
		"the target must retain its stricter non-null request identity")
	assert.ErrorIs(t, db.Create(&SubscriptionPreConsumeRecord{}).Error, ErrInvalidPersistentIdentifier,
		"ORM writes must reject an empty request identity")
}

func makeLegacyReferenceSubscriptionPreConsume(seed int) legacySubscriptionPreConsumeSchema {
	return legacySubscriptionPreConsumeSchema{
		RequestId: "legacy-preconsume-" + strings.Repeat("x", seed%7) + string(rune('a'+seed%26)),
		UserId:    seed, UserSubscriptionId: seed + 1, PreConsumed: int64(seed + 2),
		UsageEpoch: int64(seed + 3), Status: "consumed", CreatedAt: int64(seed + 4), UpdatedAt: int64(seed + 5),
	}
}

func assertLegacyReferenceSubscriptionPreConsumePreserved(
	t *testing.T,
	db *gorm.DB,
	legacy legacySubscriptionPreConsumeSchema,
) {
	t.Helper()
	var migrated SubscriptionPreConsumeRecord
	require.NoError(t, db.First(&migrated, legacy.Id).Error)
	assert.Equal(t, legacy.RequestId, migrated.RequestId)
	assert.Equal(t, legacy.UserId, migrated.UserId)
	assert.Equal(t, legacy.UserSubscriptionId, migrated.UserSubscriptionId)
	assert.Equal(t, legacy.PreConsumed, migrated.PreConsumed)
	assert.Equal(t, legacy.UsageEpoch, migrated.UsageEpoch)
	assert.Equal(t, legacy.Status, migrated.Status)
	assert.Equal(t, legacy.CreatedAt, migrated.CreatedAt)
	assert.Equal(t, legacy.UpdatedAt, migrated.UpdatedAt)
}

func normalizeSubscriptionPreConsumeTargetColumns(columns []sqliteColumnSignature) []sqliteColumnSignature {
	result := make([]sqliteColumnSignature, 0, len(columns)-1)
	for _, column := range columns {
		if column.Name == "usage_epoch" {
			continue
		}
		if column.Name == "request_id" {
			column.NotNull = 0
		}
		result = append(result, column)
	}
	return result
}

func exerciseReferenceSubscriptionPreConsumeSchemaLegacyExternal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Contains(t, []string{"mysql", "postgres"}, db.Dialector.Name())
	require.NoError(t, db.Migrator().DropTable(&SubscriptionPreConsumeRecord{}))
	require.NoError(t, db.AutoMigrate(&legacySubscriptionPreConsumeSchema{}))
	legacy := makeLegacyReferenceSubscriptionPreConsume(101)
	require.NoError(t, db.Create(&legacy).Error)
	zero := makeLegacyReferenceSubscriptionPreConsume(102)
	zero.PreConsumed = 0
	require.NoError(t, db.Create(&zero).Error)

	require.NoError(t, migrateDB())
	assertReferenceSubscriptionPreConsumeSchema(t, db)
	assertLegacyReferenceSubscriptionPreConsumePreserved(t, db, legacy)
	assertLegacyReferenceSubscriptionPreConsumePreserved(t, db, zero)
	requestID := insertReferenceSubscriptionPreConsumeWithDatabaseDefaults(t, db, "server")
	assertSubscriptionPreConsumeRequestIdentity(t, db, requestID)
	require.NoError(t, migrateDB())
	assertReferenceSubscriptionPreConsumeSchema(t, db)
	assertLegacyReferenceSubscriptionPreConsumePreserved(t, db, legacy)

	require.NoError(t, db.Migrator().DropTable(&SubscriptionPreConsumeRecord{}))
	require.NoError(t, db.AutoMigrate(&legacySubscriptionPreConsumeSchema{}))
	unsafe := makeLegacyReferenceSubscriptionPreConsume(103)
	require.NoError(t, db.Create(&unsafe).Error)
	require.NoError(t, db.Exec(`UPDATE subscription_pre_consume_records SET pre_consumed = NULL WHERE id = ?`, unsafe.Id).Error)
	err := migrateDB()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
	assert.Contains(t, err.Error(), "subscription_pre_consume_records.pre_consumed")
	var stored sql.NullInt64
	require.NoError(t, db.Table(SubscriptionPreConsumeRecord{}.TableName()).Select("pre_consumed").
		Where("id = ?", unsafe.Id).Row().Scan(&stored))
	assert.False(t, stored.Valid)
	require.NoError(t, db.Exec(`UPDATE subscription_pre_consume_records SET pre_consumed = 0 WHERE id = ?`, unsafe.Id).Error)
	require.NoError(t, migrateDB())
	assertReferenceSubscriptionPreConsumeSchema(t, db)
}
