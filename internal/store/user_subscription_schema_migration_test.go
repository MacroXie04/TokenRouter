package store

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

// referenceUserSubscriptionSchema independently transcribes the pinned
// reference declaration. TokenRouter's usage-epoch and immutable-entitlement
// fields are intentionally absent and normalized explicitly by the tests.
type referenceUserSubscriptionSchema struct {
	Id          int
	UserId      int    `gorm:"index;index:idx_user_sub_active,priority:1"`
	PlanId      int    `gorm:"index"`
	AmountTotal int64  `gorm:"type:bigint;not null;default:0"`
	AmountUsed  int64  `gorm:"type:bigint;not null;default:0"`
	StartTime   int64  `gorm:"bigint"`
	EndTime     int64  `gorm:"bigint;index;index:idx_user_sub_active,priority:3"`
	Status      string `gorm:"type:varchar(32);index;index:idx_user_sub_active,priority:2"`
	Source      string `gorm:"type:varchar(32);default:'order'"`

	LastResetTime int64 `gorm:"type:bigint;default:0"`
	NextResetTime int64 `gorm:"type:bigint;default:0;index"`

	UpgradeGroup   string `gorm:"type:varchar(64);default:''"`
	PrevUserGroup  string `gorm:"type:varchar(64);default:''"`
	DowngradeGroup string `gorm:"type:varchar(64);default:''"`

	AllowWalletOverflow bool
	CreatedAt           int64 `gorm:"bigint"`
	UpdatedAt           int64 `gorm:"bigint"`
}

func (referenceUserSubscriptionSchema) TableName() string { return "user_subscriptions" }

// legacyUserSubscriptionSchema captures the target immediately before this
// alignment while retaining every target-only durability field. Its quota
// amounts are nullable and its standalone user lookup index/defaults are
// absent, which exercises the real upgrade rather than a fresh create.
type legacyUserSubscriptionSchema struct {
	Id          int `gorm:"primaryKey"`
	UserId      int `gorm:"index:idx_user_sub_active,priority:1"`
	PlanId      int `gorm:"index"`
	AmountTotal int64
	AmountUsed  int64
	UsageEpoch  int64 `gorm:"type:bigint;not null;default:0"`
	StartTime   int64
	EndTime     int64  `gorm:"index;index:idx_user_sub_active,priority:3"`
	Status      string `gorm:"type:varchar(32);index;index:idx_user_sub_active,priority:2"`
	Source      string `gorm:"type:varchar(32)"`

	LastResetTime int64
	NextResetTime int64 `gorm:"index"`

	UpgradeGroup   string `gorm:"type:varchar(64)"`
	PrevUserGroup  string `gorm:"type:varchar(64)"`
	DowngradeGroup string `gorm:"type:varchar(64)"`
	GroupBaseline  string `gorm:"type:varchar(64);not null;default:''"`

	EntitlementVersion              int    `gorm:"not null;default:0"`
	EntitlementMigrationState       string `gorm:"type:varchar(16);not null;default:'pending';index"`
	QuotaResetPeriodSnapshot        string `gorm:"type:varchar(16);not null;default:''"`
	QuotaResetCustomSecondsSnapshot int64  `gorm:"type:bigint;not null;default:0"`
	AllowWalletOverflow             bool
	CreatedAt                       int64
	UpdatedAt                       int64
}

func (legacyUserSubscriptionSchema) TableName() string { return "user_subscriptions" }

var userSubscriptionTargetOnlyFields = []string{
	"UsageEpoch",
	"GroupBaseline",
	"EntitlementVersion",
	"EntitlementMigrationState",
	"QuotaResetPeriodSnapshot",
	"QuotaResetCustomSecondsSnapshot",
}

var userSubscriptionTargetOnlyColumns = []string{
	"usage_epoch",
	"group_baseline",
	"entitlement_version",
	"entitlement_migration_state",
	"quota_reset_period_snapshot",
	"quota_reset_custom_seconds_snapshot",
}

type expectedUserSubscriptionIndex struct {
	name    string
	columns []string
}

var expectedReferenceUserSubscriptionIndexes = []expectedUserSubscriptionIndex{
	{name: "idx_user_subscriptions_user_id", columns: []string{"user_id"}},
	{name: "idx_user_sub_active", columns: []string{"user_id", "status", "end_time"}},
	{name: "idx_user_subscriptions_plan_id", columns: []string{"plan_id"}},
	{name: "idx_user_subscriptions_end_time", columns: []string{"end_time"}},
	{name: "idx_user_subscriptions_status", columns: []string{"status"}},
	{name: "idx_user_subscriptions_next_reset_time", columns: []string{"next_reset_time"}},
}

const targetUserSubscriptionEntitlementIndex = "idx_user_subscriptions_entitlement_migration_state"

func TestReferenceUserSubscriptionSchemaPortableDialects(t *testing.T) {
	targetSchema, err := schema.Parse(&UserSubscription{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceSchema, err := schema.Parse(&referenceUserSubscriptionSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	for _, referenceField := range referenceSchema.Fields {
		targetField := targetSchema.LookUpField(referenceField.Name)
		require.NotNil(t, targetField, "UserSubscription.%s is missing", referenceField.Name)
		assert.Equal(t, referenceField.DBName, targetField.DBName, referenceField.Name+" column")
		assert.Equal(t, referenceField.PrimaryKey, targetField.PrimaryKey, referenceField.Name+" primary key")
		assert.Equal(t, referenceField.AutoIncrement, targetField.AutoIncrement, referenceField.Name+" auto increment")
		assert.Equal(t, referenceField.NotNull, targetField.NotNull, referenceField.Name+" nullability")
		assert.Equal(t, referenceField.HasDefaultValue, targetField.HasDefaultValue, referenceField.Name+" default presence")
		assert.Equal(t, referenceField.DefaultValue, targetField.DefaultValue, referenceField.Name+" default")
		assert.Equal(t, referenceField.Size, targetField.Size, referenceField.Name+" size")
	}
	for _, fieldName := range userSubscriptionTargetOnlyFields {
		assert.Nil(t, referenceSchema.LookUpField(fieldName), fieldName+" must remain an explicit target extension")
		require.NotNil(t, targetSchema.LookUpField(fieldName), fieldName+" target durability field")
	}
	assert.Equal(t, len(referenceSchema.Fields)+len(userSubscriptionTargetOnlyFields), len(targetSchema.Fields))

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
					"UserSubscription.%s %s type", referenceField.Name, dialect.Name())
			}
		})
	}

	referenceIndexes := referenceSchema.ParseIndexes()
	targetIndexes := targetSchema.ParseIndexes()
	assert.Len(t, targetIndexes, len(referenceIndexes)+1,
		"the entitlement migration lookup must be the only target-only index")
	for _, referenceIndex := range referenceIndexes {
		targetIndex := targetSchema.LookIndex(referenceIndex.Name)
		require.NotNil(t, targetIndex, "missing UserSubscription index %s", referenceIndex.Name)
		assert.Equal(t, referenceIndex.Class, targetIndex.Class, referenceIndex.Name+" class")
		require.Len(t, targetIndex.Fields, len(referenceIndex.Fields), referenceIndex.Name+" fields")
		for i := range referenceIndex.Fields {
			assert.Equal(t, referenceIndex.Fields[i].DBName, targetIndex.Fields[i].DBName,
				referenceIndex.Name+" column")
			assert.Equal(t, referenceIndex.Fields[i].Priority, targetIndex.Fields[i].Priority,
				referenceIndex.Name+" priority")
		}
	}
	require.NotNil(t, targetSchema.LookIndex(targetUserSubscriptionEntitlementIndex))

	for _, fieldName := range []string{"StartTime", "EndTime", "CreatedAt", "UpdatedAt"} {
		field := targetSchema.LookUpField(fieldName)
		require.NotNil(t, field)
		_, declared := field.TagSettings["BIGINT"]
		assert.True(t, declared, "UserSubscription.%s must retain the reference bigint tag", fieldName)
		assert.Empty(t, field.TagSettings["TYPE"], "UserSubscription.%s must retain the exact reference tag form", fieldName)
	}
}

func TestReferenceUserSubscriptionSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceUserSubscriptionSchemaMatchesSQLite(t, db)
	assertReferenceUserSubscriptionSchema(t, db)
	insertReferenceUserSubscriptionWithDatabaseDefaults(t, db, "fresh")

	created := UserSubscription{UserId: 71, PlanId: 72, Status: "active"}
	require.NoError(t, db.Create(&created).Error)
	assert.Equal(t, int64(0), created.AmountTotal)
	assert.Equal(t, int64(0), created.AmountUsed)
	assert.Equal(t, "order", created.Source)
	assert.Equal(t, "pending", created.EntitlementMigrationState)

	columnsBefore := sqliteColumnSignatures(t, db, UserSubscription{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, UserSubscription{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, UserSubscription{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, UserSubscription{}.TableName()))
	assertReferenceUserSubscriptionSchemaMatchesSQLite(t, db)
}

func TestReferenceUserSubscriptionSchemaSQLitePreservesLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacyUserSubscriptionSchema{}))
	legacy := makeLegacyReferenceUserSubscription(81)
	require.NoError(t, db.Create(&legacy).Error)
	nullable := makeLegacyReferenceUserSubscription(82)
	nullable.UserId = 182
	require.NoError(t, db.Create(&nullable).Error)
	require.NoError(t, setLegacyUserSubscriptionDefaultColumnsNull(db, nullable.Id))

	require.NoError(t, migrateDB())
	assertReferenceUserSubscriptionSchemaMatchesSQLite(t, db)
	assertReferenceUserSubscriptionSchema(t, db)
	assertLegacyReferenceUserSubscriptionPreserved(t, db, legacy)
	assertLegacyUserSubscriptionNullDefaultsPreserved(t, db, nullable.Id)
	insertReferenceUserSubscriptionWithDatabaseDefaults(t, db, "legacy")

	columnsBefore := sqliteColumnSignatures(t, db, UserSubscription{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, UserSubscription{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, UserSubscription{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, UserSubscription{}.TableName()))
	assertLegacyReferenceUserSubscriptionPreserved(t, db, legacy)
	assertLegacyUserSubscriptionNullDefaultsPreserved(t, db, nullable.Id)
}

func TestReferenceUserSubscriptionSchemaPreflightRejectsNullFinancialState(t *testing.T) {
	for _, column := range []string{"amount_total", "amount_used"} {
		t.Run(column, func(t *testing.T) {
			db := newReferenceSchemaTestDB(t)
			require.NoError(t, db.AutoMigrate(&legacyUserSubscriptionSchema{}))
			legacy := makeLegacyReferenceUserSubscription(91)
			require.NoError(t, db.Create(&legacy).Error)
			quotedColumn := quoteReferenceSchemaIdentifier(db, column)
			require.NoError(t, db.Exec("UPDATE user_subscriptions SET "+quotedColumn+" = NULL WHERE id = ?", legacy.Id).Error)

			err := migrateDB()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
			assert.Contains(t, err.Error(), "user_subscriptions."+column)
			var stored sql.NullInt64
			require.NoError(t, db.Table(UserSubscription{}.TableName()).Select(quotedColumn).
				Where("id = ?", legacy.Id).Row().Scan(&stored))
			assert.False(t, stored.Valid, "a rejected migration must not rewrite financial NULL to zero")
			assert.False(t, db.Migrator().HasIndex(&UserSubscription{}, "idx_user_subscriptions_user_id"),
				"the preflight must abort before schema mutation")

			require.NoError(t, db.Exec("UPDATE user_subscriptions SET "+quotedColumn+" = 0 WHERE id = ?", legacy.Id).Error)
			require.NoError(t, migrateDB())
			assertReferenceUserSubscriptionSchema(t, db)
		})
	}
}

func assertReferenceUserSubscriptionSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-user-subscription.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&referenceUserSubscriptionSchema{}))

	assert.Equal(t,
		sqliteColumnSignatures(t, reference, UserSubscription{}.TableName()),
		filterUserSubscriptionColumnSignatures(sqliteColumnSignatures(t, db, UserSubscription{}.TableName()),
			userSubscriptionTargetOnlyColumns...),
		"UserSubscription shared columns after target durability extensions are removed",
	)
	assert.Equal(t,
		sqliteIndexSignatures(t, reference, UserSubscription{}.TableName()),
		filterUserSubscriptionIndexSignatures(sqliteIndexSignatures(t, db, UserSubscription{}.TableName()),
			targetUserSubscriptionEntitlementIndex),
		"UserSubscription reference indexes after the target durability lookup is removed",
	)
}

// assertReferenceUserSubscriptionSchema is shared by SQLite and the opt-in
// MySQL/PostgreSQL migration gate.
func assertReferenceUserSubscriptionSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, index := range expectedReferenceUserSubscriptionIndexes {
		assertUserSubscriptionIndex(t, db, index.name, index.columns)
	}
	assertUserSubscriptionIndex(t, db, targetUserSubscriptionEntitlementIndex,
		[]string{"entitlement_migration_state"})

	for column, expected := range map[string]string{
		"amount_total": "0", "amount_used": "0", "source": "order",
		"last_reset_time": "0", "next_reset_time": "0", "upgrade_group": "",
		"prev_user_group": "", "downgrade_group": "",
	} {
		assertColumnDefault(t, db, UserSubscription{}.TableName(), column, expected)
	}
	assertColumnNullable(t, db, UserSubscription{}.TableName(), "amount_total", false)
	assertColumnNullable(t, db, UserSubscription{}.TableName(), "amount_used", false)
	for _, column := range []string{
		"source", "last_reset_time", "next_reset_time", "upgrade_group", "prev_user_group", "downgrade_group",
	} {
		assertColumnNullable(t, db, UserSubscription{}.TableName(), column, true)
	}
	for column, expected := range map[string]string{
		"usage_epoch": "0", "group_baseline": "", "entitlement_version": "0",
		"entitlement_migration_state": "pending", "quota_reset_period_snapshot": "",
		"quota_reset_custom_seconds_snapshot": "0",
	} {
		assertColumnDefault(t, db, UserSubscription{}.TableName(), column, expected)
		assertColumnNullable(t, db, UserSubscription{}.TableName(), column, false)
	}

	switch db.Dialector.Name() {
	case "sqlite":
		for _, column := range []string{"amount_total", "amount_used", "last_reset_time", "next_reset_time"} {
			assertColumnDatabaseType(t, db, UserSubscription{}.TableName(), column, "bigint")
		}
		for _, column := range []string{"start_time", "end_time", "created_at", "updated_at"} {
			assertColumnDatabaseType(t, db, UserSubscription{}.TableName(), column, "integer")
		}
	case "mysql":
		for _, column := range []string{
			"amount_total", "amount_used", "start_time", "end_time", "last_reset_time",
			"next_reset_time", "created_at", "updated_at",
		} {
			assertColumnDatabaseType(t, db, UserSubscription{}.TableName(), column, "bigint")
		}
		assertReferenceUserSubscriptionLengths(t, db)
	case "postgres":
		for _, column := range []string{
			"amount_total", "amount_used", "start_time", "end_time", "last_reset_time",
			"next_reset_time", "created_at", "updated_at",
		} {
			assertColumnDatabaseType(t, db, UserSubscription{}.TableName(), column, "int8")
		}
		assertReferenceUserSubscriptionLengths(t, db)
	default:
		require.FailNow(t, "unsupported UserSubscription schema test dialect", db.Dialector.Name())
	}
}

func assertReferenceUserSubscriptionLengths(t *testing.T, db *gorm.DB) {
	t.Helper()
	for column, length := range map[string]int64{
		"status": 32, "source": 32, "upgrade_group": 64,
		"prev_user_group": 64, "downgrade_group": 64,
	} {
		assertColumnLength(t, db, UserSubscription{}.TableName(), column, length)
	}
}

func assertUserSubscriptionIndex(t *testing.T, db *gorm.DB, name string, columns []string) {
	t.Helper()
	actualColumns, unique := requirePortableIndex(t, db, &UserSubscription{}, UserSubscription{}.TableName(), name)
	assert.Equal(t, columns, actualColumns, name+" columns")
	assert.False(t, unique, name+" must remain an ordinary index")
}

func insertReferenceUserSubscriptionWithDatabaseDefaults(t *testing.T, db *gorm.DB, marker string) int {
	t.Helper()
	userID := 200 + len(marker)
	planID := 300 + len(marker)
	require.NoError(t, db.Exec("INSERT INTO user_subscriptions (user_id, plan_id) VALUES (?, ?)", userID, planID).Error)

	var id int
	var amountTotal, amountUsed, lastResetTime, nextResetTime sql.NullInt64
	var source, upgradeGroup, prevUserGroup, downgradeGroup sql.NullString
	var usageEpoch, entitlementVersion, quotaResetCustomSeconds sql.NullInt64
	var groupBaseline, entitlementState, quotaResetPeriod sql.NullString
	var startTime, endTime, createdAt, updatedAt sql.NullInt64
	var statusText sql.NullString
	var allowWalletOverflow sql.NullBool
	columns := []string{
		"id", "amount_total", "amount_used", "source", "last_reset_time", "next_reset_time",
		"upgrade_group", "prev_user_group", "downgrade_group", "usage_epoch", "group_baseline",
		"entitlement_version", "entitlement_migration_state", "quota_reset_period_snapshot",
		"quota_reset_custom_seconds_snapshot", "start_time", "end_time", "status",
		"allow_wallet_overflow", "created_at", "updated_at",
	}
	quoted := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted = append(quoted, quoteReferenceSchemaIdentifier(db, column))
	}
	row := db.Table(UserSubscription{}.TableName()).Select(strings.Join(quoted, ", ")).
		Where("user_id = ? AND plan_id = ?", userID, planID).Row()
	require.NoError(t, row.Scan(&id, &amountTotal, &amountUsed, &source, &lastResetTime, &nextResetTime,
		&upgradeGroup, &prevUserGroup, &downgradeGroup, &usageEpoch, &groupBaseline,
		&entitlementVersion, &entitlementState, &quotaResetPeriod, &quotaResetCustomSeconds,
		&startTime, &endTime, &statusText, &allowWalletOverflow, &createdAt, &updatedAt))
	assert.Positive(t, id)
	require.True(t, amountTotal.Valid)
	require.True(t, amountUsed.Valid)
	assert.Zero(t, amountTotal.Int64)
	assert.Zero(t, amountUsed.Int64)
	require.True(t, source.Valid)
	assert.Equal(t, "order", source.String)
	for name, value := range map[string]sql.NullInt64{
		"last_reset_time": lastResetTime, "next_reset_time": nextResetTime,
		"usage_epoch": usageEpoch, "entitlement_version": entitlementVersion,
		"quota_reset_custom_seconds_snapshot": quotaResetCustomSeconds,
	} {
		require.True(t, value.Valid, name+" default")
		assert.Zero(t, value.Int64, name+" default")
	}
	for name, value := range map[string]sql.NullString{
		"upgrade_group": upgradeGroup, "prev_user_group": prevUserGroup,
		"downgrade_group": downgradeGroup, "group_baseline": groupBaseline,
		"quota_reset_period_snapshot": quotaResetPeriod,
	} {
		require.True(t, value.Valid, name+" default")
		assert.Empty(t, value.String, name+" default")
	}
	require.True(t, entitlementState.Valid)
	assert.Equal(t, "pending", entitlementState.String)
	for name, valid := range map[string]bool{
		"start_time": startTime.Valid, "end_time": endTime.Valid, "status": statusText.Valid,
		"allow_wallet_overflow": allowWalletOverflow.Valid, "created_at": createdAt.Valid,
		"updated_at": updatedAt.Valid,
	} {
		assert.False(t, valid, "UserSubscription.%s must retain the reference's absent database default", name)
	}
	return id
}

func makeLegacyReferenceUserSubscription(seed int) legacyUserSubscriptionSchema {
	return legacyUserSubscriptionSchema{
		UserId: seed, PlanId: seed + 1, AmountTotal: int64(seed + 2), AmountUsed: int64(seed + 3),
		UsageEpoch: int64(seed + 4), StartTime: int64(seed + 5), EndTime: int64(seed + 6),
		Status: "active", Source: "admin", LastResetTime: int64(seed + 7), NextResetTime: int64(seed + 8),
		UpgradeGroup: "vip", PrevUserGroup: "default", DowngradeGroup: "basic", GroupBaseline: "default",
		EntitlementVersion: 2, EntitlementMigrationState: "review",
		QuotaResetPeriodSnapshot: "monthly", QuotaResetCustomSecondsSnapshot: int64(seed + 9),
		AllowWalletOverflow: true, CreatedAt: int64(seed + 10), UpdatedAt: int64(seed + 11),
	}
}

func assertLegacyReferenceUserSubscriptionPreserved(t *testing.T, db *gorm.DB, legacy legacyUserSubscriptionSchema) {
	t.Helper()
	var migrated UserSubscription
	require.NoError(t, db.First(&migrated, legacy.Id).Error)
	assert.Equal(t, legacy.UserId, migrated.UserId)
	assert.Equal(t, legacy.PlanId, migrated.PlanId)
	assert.Equal(t, legacy.AmountTotal, migrated.AmountTotal)
	assert.Equal(t, legacy.AmountUsed, migrated.AmountUsed)
	assert.Equal(t, legacy.UsageEpoch, migrated.UsageEpoch)
	assert.Equal(t, legacy.StartTime, migrated.StartTime)
	assert.Equal(t, legacy.EndTime, migrated.EndTime)
	assert.Equal(t, legacy.Status, migrated.Status)
	assert.Equal(t, legacy.Source, migrated.Source)
	assert.Equal(t, legacy.LastResetTime, migrated.LastResetTime)
	assert.Equal(t, legacy.NextResetTime, migrated.NextResetTime)
	assert.Equal(t, legacy.UpgradeGroup, migrated.UpgradeGroup)
	assert.Equal(t, legacy.PrevUserGroup, migrated.PrevUserGroup)
	assert.Equal(t, legacy.DowngradeGroup, migrated.DowngradeGroup)
	assert.Equal(t, legacy.GroupBaseline, migrated.GroupBaseline)
	assert.Equal(t, legacy.EntitlementVersion, migrated.EntitlementVersion)
	assert.Equal(t, legacy.EntitlementMigrationState, migrated.EntitlementMigrationState)
	assert.Equal(t, legacy.QuotaResetPeriodSnapshot, migrated.QuotaResetPeriodSnapshot)
	assert.Equal(t, legacy.QuotaResetCustomSecondsSnapshot, migrated.QuotaResetCustomSecondsSnapshot)
	assert.Equal(t, legacy.AllowWalletOverflow, migrated.AllowWalletOverflow)
	assert.Equal(t, legacy.CreatedAt, migrated.CreatedAt)
	assert.Equal(t, legacy.UpdatedAt, migrated.UpdatedAt)
}

func setLegacyUserSubscriptionDefaultColumnsNull(db *gorm.DB, id int) error {
	return db.Exec(`UPDATE user_subscriptions SET source = NULL, last_reset_time = NULL,
		next_reset_time = NULL, upgrade_group = NULL, prev_user_group = NULL,
		downgrade_group = NULL WHERE id = ?`, id).Error
}

func assertLegacyUserSubscriptionNullDefaultsPreserved(t *testing.T, db *gorm.DB, id int) {
	t.Helper()
	var source, upgradeGroup, prevUserGroup, downgradeGroup sql.NullString
	var lastResetTime, nextResetTime sql.NullInt64
	row := db.Table(UserSubscription{}.TableName()).Select(`source, last_reset_time, next_reset_time,
		upgrade_group, prev_user_group, downgrade_group`).Where("id = ?", id).Row()
	require.NoError(t, row.Scan(&source, &lastResetTime, &nextResetTime,
		&upgradeGroup, &prevUserGroup, &downgradeGroup))
	for name, valid := range map[string]bool{
		"source": source.Valid, "last_reset_time": lastResetTime.Valid,
		"next_reset_time": nextResetTime.Valid, "upgrade_group": upgradeGroup.Valid,
		"prev_user_group": prevUserGroup.Valid, "downgrade_group": downgradeGroup.Valid,
	} {
		assert.False(t, valid, "adding the %s default must not rewrite legacy NULL", name)
	}
}

func filterUserSubscriptionColumnSignatures(columns []sqliteColumnSignature, excluded ...string) []sqliteColumnSignature {
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

func filterUserSubscriptionIndexSignatures(indexes []sqliteIndexSignature, excluded ...string) []sqliteIndexSignature {
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
	return result
}

func exerciseReferenceUserSubscriptionSchemaLegacyExternal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Contains(t, []string{"mysql", "postgres"}, db.Dialector.Name())
	require.NoError(t, db.Migrator().DropTable(&UserSubscription{}))
	require.NoError(t, db.AutoMigrate(&legacyUserSubscriptionSchema{}))
	legacy := makeLegacyReferenceUserSubscription(101)
	require.NoError(t, db.Create(&legacy).Error)
	nullable := makeLegacyReferenceUserSubscription(102)
	nullable.UserId = 202
	require.NoError(t, db.Create(&nullable).Error)
	require.NoError(t, setLegacyUserSubscriptionDefaultColumnsNull(db, nullable.Id))

	require.NoError(t, migrateDB())
	assertReferenceUserSubscriptionSchema(t, db)
	assertLegacyReferenceUserSubscriptionPreserved(t, db, legacy)
	assertLegacyUserSubscriptionNullDefaultsPreserved(t, db, nullable.Id)
	insertReferenceUserSubscriptionWithDatabaseDefaults(t, db, "server")
	require.NoError(t, migrateDB())
	assertReferenceUserSubscriptionSchema(t, db)
	assertLegacyReferenceUserSubscriptionPreserved(t, db, legacy)

	require.NoError(t, db.Migrator().DropTable(&UserSubscription{}))
	require.NoError(t, db.AutoMigrate(&legacyUserSubscriptionSchema{}))
	unsafe := makeLegacyReferenceUserSubscription(103)
	require.NoError(t, db.Create(&unsafe).Error)
	require.NoError(t, db.Exec("UPDATE user_subscriptions SET amount_used = NULL WHERE id = ?", unsafe.Id).Error)
	err := migrateDB()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
	assert.Contains(t, err.Error(), "user_subscriptions.amount_used")
	var stored sql.NullInt64
	require.NoError(t, db.Table(UserSubscription{}.TableName()).Select("amount_used").
		Where("id = ?", unsafe.Id).Row().Scan(&stored))
	assert.False(t, stored.Valid)
	require.NoError(t, db.Exec("UPDATE user_subscriptions SET amount_used = 0 WHERE id = ?", unsafe.Id).Error)
	require.NoError(t, migrateDB())
	assertReferenceUserSubscriptionSchema(t, db)
}
