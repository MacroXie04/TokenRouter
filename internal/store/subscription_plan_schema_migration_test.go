package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// referenceSubscriptionPlanSchema independently mirrors the pinned model.
// The reference uses a separate hand-built SQLite table; its two additional
// SQLite-only pointer defaults are represented by createReferenceSubscriptionPlanSQLite.
type referenceSubscriptionPlanSchema struct {
	Id                      int
	Title                   string  `gorm:"type:varchar(128);not null"`
	Subtitle                string  `gorm:"type:varchar(255);default:''"`
	PriceAmount             float64 `gorm:"type:decimal(10,6);not null;default:0"`
	Currency                string  `gorm:"type:varchar(8);not null;default:'USD'"`
	DurationUnit            string  `gorm:"type:varchar(16);not null;default:'month'"`
	DurationValue           int     `gorm:"type:int;not null;default:1"`
	CustomSeconds           int64   `gorm:"type:bigint;not null;default:0"`
	Enabled                 bool    `gorm:"default:true"`
	SortOrder               int     `gorm:"type:int;default:0"`
	AllowBalancePay         *bool
	AllowWalletOverflow     *bool
	StripePriceId           string `gorm:"type:varchar(128);default:''"`
	CreemProductId          string `gorm:"type:varchar(128);default:''"`
	WaffoPancakeProductId   string `gorm:"type:varchar(128);default:''"`
	MaxPurchasePerUser      int    `gorm:"type:int;default:0"`
	UpgradeGroup            string `gorm:"type:varchar(64);default:''"`
	DowngradeGroup          string `gorm:"type:varchar(64);default:''"`
	TotalAmount             int64  `gorm:"type:bigint;not null;default:0"`
	QuotaResetPeriod        string `gorm:"type:varchar(16);default:'never'"`
	QuotaResetCustomSeconds int64  `gorm:"type:bigint;default:0"`
	CreatedAt               int64  `gorm:"bigint"`
	UpdatedAt               int64  `gorm:"bigint"`
}

func (referenceSubscriptionPlanSchema) TableName() string { return "subscription_plans" }

// legacySubscriptionPlanSchema is the target shape immediately before this
// slice. It intentionally keeps nullable reference-required fields and the
// former implicit integer declarations.
type legacySubscriptionPlanSchema struct {
	Id                      int    `gorm:"primaryKey"`
	Title                   string `gorm:"type:varchar(128);not null"`
	Subtitle                string `gorm:"type:varchar(255)"`
	PriceAmount             string `gorm:"type:varchar(64);not null"`
	Currency                string `gorm:"type:varchar(8);default:USD"`
	DurationUnit            string `gorm:"type:varchar(16)"`
	DurationValue           int
	CustomSeconds           int64
	Enabled                 bool
	SortOrder               int
	AllowBalancePay         *bool
	AllowWalletOverflow     *bool
	StripePriceId           string `gorm:"type:varchar(128)"`
	CreemProductId          string `gorm:"type:varchar(128)"`
	WaffoPancakeProductId   string `gorm:"type:varchar(128)"`
	MaxPurchasePerUser      int
	UpgradeGroup            string `gorm:"type:varchar(64)"`
	DowngradeGroup          string `gorm:"type:varchar(64)"`
	TotalAmount             int64
	QuotaResetPeriod        string `gorm:"type:varchar(16)"`
	QuotaResetCustomSeconds int64
	CreatedAt               int64
	UpdatedAt               int64
}

func (legacySubscriptionPlanSchema) TableName() string { return "subscription_plans" }

type nullableLegacySubscriptionPlanSchema struct {
	Id            int `gorm:"primaryKey"`
	Title         *string
	PriceAmount   *string
	Currency      *string
	DurationUnit  *string
	DurationValue *int64
	CustomSeconds *int64
	TotalAmount   *int64
}

func (nullableLegacySubscriptionPlanSchema) TableName() string { return "subscription_plans" }

type wideLegacySubscriptionPlanSchema struct {
	Id                 int    `gorm:"primaryKey"`
	Title              string `gorm:"not null"`
	PriceAmount        string `gorm:"not null"`
	Currency           string `gorm:"not null"`
	DurationUnit       string `gorm:"not null"`
	DurationValue      int64
	CustomSeconds      int64 `gorm:"not null"`
	SortOrder          int64
	MaxPurchasePerUser int64
	TotalAmount        int64 `gorm:"not null"`
}

func (wideLegacySubscriptionPlanSchema) TableName() string { return "subscription_plans" }

func TestReferenceSubscriptionPlanSchemaPortableDialects(t *testing.T) {
	targetSchema, err := schema.Parse(&SubscriptionPlan{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceSchema, err := schema.Parse(&referenceSubscriptionPlanSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	require.Len(t, targetSchema.Fields, len(referenceSchema.Fields))

	for _, referenceField := range referenceSchema.Fields {
		targetField := targetSchema.LookUpField(referenceField.Name)
		require.NotNil(t, targetField, "SubscriptionPlan.%s is missing", referenceField.Name)
		assert.Equal(t, referenceField.DBName, targetField.DBName, referenceField.Name+" column")
		assert.Equal(t, referenceField.PrimaryKey, targetField.PrimaryKey, referenceField.Name+" primary key")
		assert.Equal(t, referenceField.AutoIncrement, targetField.AutoIncrement, referenceField.Name+" auto increment")
		assert.Equal(t, referenceField.NotNull, targetField.NotNull, referenceField.Name+" nullability")
		assert.Equal(t, referenceField.Unique, targetField.Unique, referenceField.Name+" uniqueness")
		switch referenceField.Name {
		case "PriceAmount":
			assert.True(t, referenceField.HasDefaultValue)
			assert.False(t, targetField.HasDefaultValue,
				"the model omits the SQLite-only default and server migrations install it explicitly")
			assert.Equal(t, referenceField.Precision, targetField.Precision, referenceField.Name+" precision")
			assert.Equal(t, referenceField.Scale, targetField.Scale, referenceField.Name+" scale")
		case "Enabled":
			assert.True(t, referenceField.HasDefaultValue)
			assert.False(t, targetField.HasDefaultValue,
				"the database default is migration-owned so explicit false creates remain false")
		default:
			assert.Equal(t, referenceField.HasDefaultValue, targetField.HasDefaultValue, referenceField.Name+" default presence")
			assert.Equal(t, referenceField.DefaultValue, targetField.DefaultValue, referenceField.Name+" default")
			assert.Equal(t, referenceField.Size, targetField.Size, referenceField.Name+" size")
			assert.Equal(t, referenceField.Precision, targetField.Precision, referenceField.Name+" precision")
			assert.Equal(t, referenceField.Scale, targetField.Scale, referenceField.Name+" scale")
		}
	}

	targetPriceType, ok := reflect.TypeOf(SubscriptionPlan{}).FieldByName("PriceAmount")
	require.True(t, ok)
	referencePriceType, ok := reflect.TypeOf(referenceSubscriptionPlanSchema{}).FieldByName("PriceAmount")
	require.True(t, ok)
	assert.Equal(t, reflect.String, targetPriceType.Type.Kind())
	assert.Equal(t, reflect.Float64, referencePriceType.Type.Kind())
	for column, expected := range map[string]string{
		"subtitle": "", "price_amount": "0", "currency": "USD", "duration_unit": "month",
		"duration_value": "1", "custom_seconds": "0", "enabled": "1", "sort_order": "0",
		"stripe_price_id": "", "creem_product_id": "", "waffo_pancake_product_id": "",
		"max_purchase_per_user": "0", "upgrade_group": "", "downgrade_group": "",
		"total_amount": "0", "quota_reset_period": "never", "quota_reset_custom_seconds": "0",
	} {
		assertReferenceSchemaDefaultSpec(t, "subscription_plans", column, expected)
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
					"SubscriptionPlan.%s %s type", referenceField.Name, dialect.Name())
			}
		})
	}
	assert.Empty(t, referenceSchema.ParseIndexes())
	assert.Empty(t, targetSchema.ParseIndexes())
	assert.Empty(t, referenceSchema.ParseUniqueConstraints())
	assert.Empty(t, targetSchema.ParseUniqueConstraints())
}

func TestReferenceSubscriptionPlanSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceSubscriptionPlanSchemaMatchesSQLite(t, db)
	assertReferenceSubscriptionPlanSchema(t, db)

	insertReferenceSubscriptionPlanWithDatabaseDefaults(t, db, "plan-schema-direct", "12.340000")
	assertSubscriptionPlanExplicitDisabledCreate(t, db, "plan-schema-disabled")

	columnsBefore := sqliteColumnSignatures(t, db, SubscriptionPlan{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, SubscriptionPlan{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, SubscriptionPlan{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, SubscriptionPlan{}.TableName()))
	assertReferenceSubscriptionPlanSchemaMatchesSQLite(t, db)
}

func TestReferenceSubscriptionPlanSchemaSQLitePreservesLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacySubscriptionPlanSchema{}))
	legacy := makeLegacySubscriptionPlan("plan-schema-legacy")
	require.NoError(t, db.Create(&legacy).Error)
	blankPrice := makeLegacySubscriptionPlan("plan-schema-blank-price")
	blankPrice.PriceAmount = "  "
	require.NoError(t, db.Create(&blankPrice).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO subscription_plans (title, price_amount, currency, duration_unit,
			duration_value, custom_seconds, total_amount)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"plan-schema-null", "4.250000", "EUR", "custom", 0, int64(3600), int64(0)).Error)

	require.NoError(t, migrateDB())
	assertReferenceSubscriptionPlanSchemaMatchesSQLite(t, db)
	assertReferenceSubscriptionPlanSchema(t, db)
	assertLegacySubscriptionPlanPreserved(t, db, legacy)
	var normalizedBlankPrice sql.NullString
	require.NoError(t, db.Table(SubscriptionPlan{}.TableName()).Select("price_amount").
		Where("id = ?", blankPrice.Id).Row().Scan(&normalizedBlankPrice))
	assertDecimalTextEqual(t, normalizedBlankPrice, "0")
	assertLegacySubscriptionPlanNullsPreserved(t, db, "plan-schema-null")
	insertReferenceSubscriptionPlanWithDatabaseDefaults(t, db, "plan-schema-after-legacy", "8.500000")
	assertSubscriptionPlanExplicitDisabledCreate(t, db, "plan-schema-disabled-after-legacy")

	columnsBefore := sqliteColumnSignatures(t, db, SubscriptionPlan{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, SubscriptionPlan{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, SubscriptionPlan{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, SubscriptionPlan{}.TableName()))
	assertLegacySubscriptionPlanPreserved(t, db, legacy)
	assertLegacySubscriptionPlanNullsPreserved(t, db, "plan-schema-null")
}

func TestReferenceSubscriptionPlanSchemaPreflightRejectsLossyPrices(t *testing.T) {
	for name, price := range map[string]string{
		"non decimal":       "not-a-number",
		"exponent":          "1e2",
		"fractional digits": "1.0000001",
		"positive overflow": "10000",
		"negative overflow": "-10000",
		"oversized input":   strings.Repeat("1", referenceSubscriptionPriceLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			db := newReferenceSchemaTestDB(t)
			require.NoError(t, db.AutoMigrate(&legacySubscriptionPlanSchema{}))
			legacy := makeLegacySubscriptionPlan("plan-schema-lossy-" + strings.ReplaceAll(name, " ", "-"))
			legacy.PriceAmount = price
			require.NoError(t, db.Create(&legacy).Error)
			columnsBefore := sqliteColumnSignatures(t, db, SubscriptionPlan{}.TableName())

			err := migrateDB()
			assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
			assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, SubscriptionPlan{}.TableName()),
				"failed preflight must not change the legacy table")
			var preserved string
			require.NoError(t, db.Table(SubscriptionPlan{}.TableName()).Select("price_amount").
				Where("id = ?", legacy.Id).Scan(&preserved).Error)
			assert.Equal(t, price, preserved)

			require.NoError(t, db.Table(SubscriptionPlan{}.TableName()).Where("id = ?", legacy.Id).
				Update("price_amount", "1.250000").Error)
			require.NoError(t, migrateDB())
			assertReferenceSubscriptionPlanSchema(t, db)
		})
	}
}

func TestReferenceSubscriptionPlanSchemaPreflightRejectsNullRequiredState(t *testing.T) {
	for _, column := range []string{
		"title", "price_amount", "currency", "duration_unit", "duration_value", "custom_seconds", "total_amount",
	} {
		t.Run(column, func(t *testing.T) {
			db := newReferenceSchemaTestDB(t)
			require.NoError(t, db.AutoMigrate(&nullableLegacySubscriptionPlanSchema{}))
			legacy := makeNullableLegacySubscriptionPlan()
			require.NoError(t, db.Create(&legacy).Error)
			require.NoError(t, db.Table(SubscriptionPlan{}.TableName()).Where("id = ?", legacy.Id).
				Update(column, nil).Error)

			err := migrateDB()
			assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
			var nullRows int64
			require.NoError(t, db.Table(SubscriptionPlan{}.TableName()).Where(quoteReferenceSchemaIdentifier(db, column)+" IS NULL").
				Count(&nullRows).Error)
			assert.EqualValues(t, 1, nullRows, "failed migration must not rewrite ambiguous plan state")

			require.NoError(t, db.Table(SubscriptionPlan{}.TableName()).Where("id = ?", legacy.Id).
				Update(column, requiredSubscriptionPlanRepairValue(column)).Error)
			require.NoError(t, migrateDB())
			assertReferenceSubscriptionPlanSchema(t, db)
		})
	}

	t.Run("missing price on populated table", func(t *testing.T) {
		db := newReferenceSchemaTestDB(t)
		require.NoError(t, db.Exec(`CREATE TABLE subscription_plans (id integer primary key, title text)`).Error)
		require.NoError(t, db.Exec(`INSERT INTO subscription_plans (id, title) VALUES (1, 'legacy')`).Error)
		err := migrateDB()
		assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
		var title string
		require.NoError(t, db.Table(SubscriptionPlan{}.TableName()).Select("title").Where("id = 1").Scan(&title).Error)
		assert.Equal(t, "legacy", title)
	})
}

func TestReferenceSubscriptionPlanSchemaPreflightRejectsUnsafeIntNarrowing(t *testing.T) {
	for _, test := range []struct {
		column string
		value  int64
	}{
		{column: "duration_value", value: referenceIntMax + 1},
		{column: "duration_value", value: referenceIntMin - 1},
		{column: "sort_order", value: referenceIntMax + 1},
		{column: "sort_order", value: referenceIntMin - 1},
		{column: "max_purchase_per_user", value: referenceIntMax + 1},
		{column: "max_purchase_per_user", value: referenceIntMin - 1},
	} {
		t.Run(fmt.Sprintf("%s/%d", test.column, test.value), func(t *testing.T) {
			db := newReferenceSchemaTestDB(t)
			require.NoError(t, db.AutoMigrate(&wideLegacySubscriptionPlanSchema{}))
			legacy := makeWideLegacySubscriptionPlan()
			require.NoError(t, db.Create(&legacy).Error)
			require.NoError(t, db.Table(SubscriptionPlan{}.TableName()).Where("id = ?", legacy.Id).
				Update(test.column, test.value).Error)

			err := rejectSubscriptionPlanIntOutsideReferenceRange(db, test.column)
			assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
			var stored int64
			require.NoError(t, db.Table(SubscriptionPlan{}.TableName()).Select(test.column).
				Where("id = ?", legacy.Id).Scan(&stored).Error)
			assert.Equal(t, test.value, stored, "preflight must not clamp or rewrite an oversized value")
		})
	}
}

func assertReferenceSubscriptionPlanSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-subscription-plan.db")), &gorm.Config{})
	require.NoError(t, err)
	createReferenceSubscriptionPlanSQLite(t, reference)

	referenceColumns := sqliteColumnSignatures(t, reference, SubscriptionPlan{}.TableName())
	targetColumns := sqliteColumnSignatures(t, db, SubscriptionPlan{}.TableName())
	assert.Equal(t, referenceColumns, targetColumns)
	assert.Equal(t, sqliteIndexSignatures(t, reference, SubscriptionPlan{}.TableName()),
		sqliteIndexSignatures(t, db, SubscriptionPlan{}.TableName()))
}

func createReferenceSubscriptionPlanSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE subscription_plans (
		id integer,
		title varchar(128) NOT NULL,
		subtitle varchar(255) DEFAULT '',
		price_amount decimal(10,6) NOT NULL,
		currency varchar(8) NOT NULL DEFAULT 'USD',
		duration_unit varchar(16) NOT NULL DEFAULT 'month',
		duration_value integer NOT NULL DEFAULT 1,
		custom_seconds bigint NOT NULL DEFAULT 0,
		enabled numeric DEFAULT 1,
		sort_order integer DEFAULT 0,
		allow_balance_pay numeric DEFAULT 1,
		allow_wallet_overflow numeric DEFAULT 1,
		stripe_price_id varchar(128) DEFAULT '',
		creem_product_id varchar(128) DEFAULT '',
		waffo_pancake_product_id varchar(128) DEFAULT '',
		max_purchase_per_user integer DEFAULT 0,
		upgrade_group varchar(64) DEFAULT '',
		downgrade_group varchar(64) DEFAULT '',
		total_amount bigint NOT NULL DEFAULT 0,
		quota_reset_period varchar(16) DEFAULT 'never',
		quota_reset_custom_seconds bigint DEFAULT 0,
		created_at bigint,
		updated_at bigint,
		PRIMARY KEY (id)
	)`).Error)
}

// assertReferenceSubscriptionPlanSchema is shared with the opt-in live
// MySQL/PostgreSQL migration gate.
func assertReferenceSubscriptionPlanSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, column := range []string{
		"title", "price_amount", "currency", "duration_unit", "duration_value", "custom_seconds", "total_amount",
	} {
		assertColumnNullable(t, db, SubscriptionPlan{}.TableName(), column, false)
	}
	for _, column := range []string{
		"subtitle", "enabled", "sort_order", "allow_balance_pay", "allow_wallet_overflow",
		"stripe_price_id", "creem_product_id", "waffo_pancake_product_id", "max_purchase_per_user",
		"upgrade_group", "downgrade_group", "quota_reset_period", "quota_reset_custom_seconds",
		"created_at", "updated_at",
	} {
		assertColumnNullable(t, db, SubscriptionPlan{}.TableName(), column, true)
	}
	for column, expected := range map[string]string{
		"subtitle": "", "currency": "USD", "duration_unit": "month", "duration_value": "1",
		"custom_seconds": "0", "enabled": "1", "sort_order": "0", "stripe_price_id": "",
		"creem_product_id": "", "waffo_pancake_product_id": "", "max_purchase_per_user": "0",
		"upgrade_group": "", "downgrade_group": "", "total_amount": "0",
		"quota_reset_period": "never", "quota_reset_custom_seconds": "0",
	} {
		assertColumnDefault(t, db, SubscriptionPlan{}.TableName(), column, expected)
	}
	for _, column := range []string{"title", "created_at", "updated_at"} {
		_, hasDefault := requireColumnType(t, db, SubscriptionPlan{}.TableName(), column).DefaultValue()
		assert.False(t, hasDefault, "SubscriptionPlan.%s must not gain a database default", column)
	}

	switch db.Dialector.Name() {
	case "sqlite":
		_, priceHasDefault := requireColumnType(t, db, SubscriptionPlan{}.TableName(), "price_amount").DefaultValue()
		assert.False(t, priceHasDefault, "the pinned SQLite price column has no default")
		assertColumnDefault(t, db, SubscriptionPlan{}.TableName(), "allow_balance_pay", "1")
		assertColumnDefault(t, db, SubscriptionPlan{}.TableName(), "allow_wallet_overflow", "1")
		assertColumnDatabaseType(t, db, SubscriptionPlan{}.TableName(), "price_amount", "decimal")
	case "mysql", "postgres":
		priceDefault := "0"
		if db.Dialector.Name() == "mysql" {
			priceDefault = "0.000000"
		}
		assertColumnDefault(t, db, SubscriptionPlan{}.TableName(), "price_amount", priceDefault)
		for _, column := range []string{"allow_balance_pay", "allow_wallet_overflow"} {
			_, hasDefault := requireColumnType(t, db, SubscriptionPlan{}.TableName(), column).DefaultValue()
			assert.False(t, hasDefault, "the pinned server schema has no default for %s", column)
		}
		for column, length := range map[string]int64{
			"title": 128, "subtitle": 255, "currency": 8,
			"duration_unit": 16, "stripe_price_id": 128, "creem_product_id": 128,
			"waffo_pancake_product_id": 128, "upgrade_group": 64, "downgrade_group": 64,
			"quota_reset_period": 16,
		} {
			assertColumnLength(t, db, SubscriptionPlan{}.TableName(), column, length)
		}
		bigintType, intType, boolType := "bigint", "int", "tinyint"
		if db.Dialector.Name() == "postgres" {
			bigintType, intType, boolType = "int8", "int4", "bool"
		}
		for _, column := range []string{"custom_seconds", "total_amount", "quota_reset_custom_seconds", "created_at", "updated_at"} {
			assertColumnDatabaseType(t, db, SubscriptionPlan{}.TableName(), column, bigintType)
		}
		for _, column := range []string{"duration_value", "sort_order", "max_purchase_per_user"} {
			assertColumnDatabaseType(t, db, SubscriptionPlan{}.TableName(), column, intType)
		}
		for _, column := range []string{"enabled", "allow_balance_pay", "allow_wallet_overflow"} {
			assertColumnDatabaseType(t, db, SubscriptionPlan{}.TableName(), column, boolType)
		}
	default:
		require.FailNow(t, "unsupported SubscriptionPlan schema test dialect", db.Dialector.Name())
	}
	if db.Dialector.Name() != "sqlite" {
		priceColumn := requireColumnType(t, db, SubscriptionPlan{}.TableName(), "price_amount")
		precision, scale, known := priceColumn.DecimalSize()
		require.True(t, known, "subscription_plans.price_amount decimal size is unknown")
		assert.EqualValues(t, 10, precision)
		assert.EqualValues(t, 6, scale)
	}
	indexes, err := db.Migrator().GetIndexes(&SubscriptionPlan{})
	require.NoError(t, err)
	for _, index := range indexes {
		primary, known := index.PrimaryKey()
		assert.True(t, known && primary, "unexpected SubscriptionPlan secondary index %s", index.Name())
	}
}

func insertReferenceSubscriptionPlanWithDatabaseDefaults(t *testing.T, db *gorm.DB, title, price string) int {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO subscription_plans (title, price_amount) VALUES (?, ?)`, title, price).Error)

	var id int
	var subtitle, storedPrice, currency, durationUnit sql.NullString
	var stripePrice, creemProduct, waffoProduct, upgradeGroup, downgradeGroup, resetPeriod sql.NullString
	var durationValue, customSeconds, sortOrder, maxPurchase, totalAmount, resetCustom sql.NullInt64
	var enabled, allowBalance, allowOverflow sql.NullBool
	var createdAt, updatedAt sql.NullInt64
	row := db.Table(SubscriptionPlan{}.TableName()).Select(`id, subtitle, price_amount, currency,
		duration_unit, duration_value, custom_seconds, enabled, sort_order, allow_balance_pay,
		allow_wallet_overflow, stripe_price_id, creem_product_id, waffo_pancake_product_id,
		max_purchase_per_user, upgrade_group, downgrade_group, total_amount,
		quota_reset_period, quota_reset_custom_seconds, created_at, updated_at`).
		Where("title = ?", title).Row()
	require.NoError(t, row.Scan(&id, &subtitle, &storedPrice, &currency, &durationUnit,
		&durationValue, &customSeconds, &enabled, &sortOrder, &allowBalance, &allowOverflow,
		&stripePrice, &creemProduct, &waffoProduct, &maxPurchase, &upgradeGroup, &downgradeGroup,
		&totalAmount, &resetPeriod, &resetCustom, &createdAt, &updatedAt))
	assert.Positive(t, id)
	assertNullString(t, subtitle, "")
	assertDecimalTextEqual(t, storedPrice, price)
	assertNullString(t, currency, "USD")
	assertNullString(t, durationUnit, "month")
	assertNullInt64(t, durationValue, 1)
	assertNullInt64(t, customSeconds, 0)
	require.True(t, enabled.Valid)
	assert.True(t, enabled.Bool)
	assertNullInt64(t, sortOrder, 0)
	for name, value := range map[string]sql.NullBool{
		"allow_balance_pay": allowBalance, "allow_wallet_overflow": allowOverflow,
	} {
		if db.Dialector.Name() == "sqlite" {
			require.True(t, value.Valid, name+" SQLite default")
			assert.True(t, value.Bool, name+" SQLite default")
		} else {
			assert.False(t, value.Valid, name+" has no reference server default")
		}
	}
	for name, value := range map[string]sql.NullString{
		"stripe_price_id": stripePrice, "creem_product_id": creemProduct,
		"waffo_pancake_product_id": waffoProduct, "upgrade_group": upgradeGroup,
		"downgrade_group": downgradeGroup,
	} {
		require.True(t, value.Valid, name+" default")
		assert.Empty(t, value.String, name+" default")
	}
	assertNullInt64(t, maxPurchase, 0)
	assertNullInt64(t, totalAmount, 0)
	assertNullString(t, resetPeriod, "never")
	assertNullInt64(t, resetCustom, 0)
	assert.False(t, createdAt.Valid)
	assert.False(t, updatedAt.Valid)
	return id
}

func assertSubscriptionPlanExplicitDisabledCreate(t *testing.T, db *gorm.DB, title string) {
	t.Helper()
	plan := SubscriptionPlan{Title: title, PriceAmount: "1.000000", Enabled: false}
	require.NoError(t, db.Create(&plan).Error)
	var enabled bool
	var allowBalance, allowOverflow sql.NullBool
	row := db.Table(SubscriptionPlan{}.TableName()).Select("enabled, allow_balance_pay, allow_wallet_overflow").
		Where("id = ?", plan.Id).Row()
	require.NoError(t, row.Scan(&enabled, &allowBalance, &allowOverflow))
	assert.False(t, enabled, "migration-owned database default must not override explicit false")
	assert.False(t, allowBalance.Valid, "nil remains an inheritance marker for ORM creates")
	assert.False(t, allowOverflow.Valid, "nil remains an inheritance marker for ORM creates")
}

func makeLegacySubscriptionPlan(title string) legacySubscriptionPlanSchema {
	allowBalance := false
	allowOverflow := false
	return legacySubscriptionPlanSchema{
		Title: title, Subtitle: "legacy subtitle", PriceAmount: "9.990000", Currency: "EUR",
		DurationUnit: "custom", DurationValue: 0, CustomSeconds: 7200, Enabled: false,
		SortOrder: -7, AllowBalancePay: &allowBalance, AllowWalletOverflow: &allowOverflow,
		StripePriceId: "price_legacy", CreemProductId: "prod_legacy",
		WaffoPancakeProductId: "waffo_legacy", MaxPurchasePerUser: 3,
		UpgradeGroup: "vip", DowngradeGroup: "default", TotalAmount: 123456,
		QuotaResetPeriod: "custom", QuotaResetCustomSeconds: 1800,
		CreatedAt: 101, UpdatedAt: 102,
	}
}

func assertLegacySubscriptionPlanPreserved(t *testing.T, db *gorm.DB, legacy legacySubscriptionPlanSchema) {
	t.Helper()
	var migrated legacySubscriptionPlanSchema
	require.NoError(t, db.First(&migrated, legacy.Id).Error)
	assertDecimalTextEqual(t, sql.NullString{String: migrated.PriceAmount, Valid: true}, legacy.PriceAmount)
	legacy.PriceAmount = migrated.PriceAmount
	assert.Equal(t, legacy, migrated, "schema alignment must not rewrite explicit plan configuration")
}

func assertLegacySubscriptionPlanNullsPreserved(t *testing.T, db *gorm.DB, title string) {
	t.Helper()
	var subtitle, resetPeriod, stripePrice, creemProduct, waffoProduct, upgradeGroup, downgradeGroup sql.NullString
	var enabled, allowBalance, allowOverflow sql.NullBool
	var sortOrder, maxPurchase, resetCustom, createdAt, updatedAt sql.NullInt64
	row := db.Table(SubscriptionPlan{}.TableName()).Select(`subtitle, enabled, sort_order,
		allow_balance_pay, allow_wallet_overflow, stripe_price_id, creem_product_id,
		waffo_pancake_product_id, max_purchase_per_user, upgrade_group, downgrade_group,
		quota_reset_period, quota_reset_custom_seconds, created_at, updated_at`).
		Where("title = ?", title).Row()
	require.NoError(t, row.Scan(&subtitle, &enabled, &sortOrder, &allowBalance, &allowOverflow,
		&stripePrice, &creemProduct, &waffoProduct, &maxPurchase, &upgradeGroup, &downgradeGroup,
		&resetPeriod, &resetCustom, &createdAt, &updatedAt))
	for name, value := range map[string]sql.NullString{
		"subtitle": subtitle, "stripe_price_id": stripePrice, "creem_product_id": creemProduct,
		"waffo_pancake_product_id": waffoProduct, "upgrade_group": upgradeGroup,
		"downgrade_group": downgradeGroup, "quota_reset_period": resetPeriod,
	} {
		assert.False(t, value.Valid, "adding a default must not rewrite legacy NULL in "+name)
	}
	for name, value := range map[string]sql.NullBool{
		"enabled": enabled, "allow_balance_pay": allowBalance, "allow_wallet_overflow": allowOverflow,
	} {
		assert.False(t, value.Valid, "adding a default must not rewrite legacy NULL in "+name)
	}
	for name, value := range map[string]sql.NullInt64{
		"sort_order": sortOrder, "max_purchase_per_user": maxPurchase,
		"quota_reset_custom_seconds": resetCustom, "created_at": createdAt, "updated_at": updatedAt,
	} {
		assert.False(t, value.Valid, "adding a default must not rewrite legacy NULL in "+name)
	}
}

func makeNullableLegacySubscriptionPlan() nullableLegacySubscriptionPlanSchema {
	title, price, currency, unit := "legacy", "1.000000", "USD", "month"
	duration, custom, total := int64(1), int64(0), int64(0)
	return nullableLegacySubscriptionPlanSchema{
		Title: &title, PriceAmount: &price, Currency: &currency, DurationUnit: &unit,
		DurationValue: &duration, CustomSeconds: &custom, TotalAmount: &total,
	}
}

func requiredSubscriptionPlanRepairValue(column string) any {
	switch column {
	case "title":
		return "legacy"
	case "price_amount":
		return "1.000000"
	case "currency":
		return "USD"
	case "duration_unit":
		return "month"
	case "duration_value":
		return int64(1)
	case "custom_seconds", "total_amount":
		return int64(0)
	default:
		panic("unsupported required SubscriptionPlan field: " + column)
	}
}

func makeWideLegacySubscriptionPlan() wideLegacySubscriptionPlanSchema {
	return wideLegacySubscriptionPlanSchema{
		Title: "wide legacy", PriceAmount: "1.000000", Currency: "USD", DurationUnit: "month",
		DurationValue: 1, CustomSeconds: 0, SortOrder: 0, MaxPurchasePerUser: 0, TotalAmount: 0,
	}
}

func assertReferenceSchemaDefaultSpec(t *testing.T, table, column, wanted string) {
	t.Helper()
	for _, spec := range referenceSchemaDefaults {
		if spec.table == table && spec.column == column {
			assert.Equal(t, wanted, spec.wanted)
			return
		}
	}
	require.FailNow(t, "missing reference default migration spec", table+"."+column)
}

func compactSchemaType(value string) string {
	return strings.ToLower(strings.ReplaceAll(value, " ", ""))
}

func assertNullString(t *testing.T, value sql.NullString, expected string) {
	t.Helper()
	require.True(t, value.Valid)
	assert.Equal(t, expected, value.String)
}

func assertDecimalTextEqual(t *testing.T, value sql.NullString, expected string) {
	t.Helper()
	require.True(t, value.Valid)
	want, err := decimal.NewFromString(expected)
	require.NoError(t, err)
	got, err := decimal.NewFromString(value.String)
	require.NoError(t, err)
	assert.True(t, want.Equal(got), "decimal %q must equal %q", value.String, expected)
}

func assertNullInt64(t *testing.T, value sql.NullInt64, expected int64) {
	t.Helper()
	require.True(t, value.Valid)
	assert.Equal(t, expected, value.Int64)
}

func exerciseReferenceSubscriptionPlanSchemaLegacyExternal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Contains(t, []string{"mysql", "postgres"}, db.Dialector.Name())
	require.NoError(t, db.Migrator().DropTable(&SubscriptionPlan{}))
	require.NoError(t, db.AutoMigrate(&legacySubscriptionPlanSchema{}))
	legacy := makeLegacySubscriptionPlan("plan-schema-server-legacy")
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO subscription_plans (title, price_amount, currency, duration_unit,
			duration_value, custom_seconds, total_amount)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"plan-schema-server-null", "4.250000", "EUR", "custom", 0, int64(3600), int64(0)).Error)

	require.NoError(t, migrateDB())
	assertReferenceSubscriptionPlanSchema(t, db)
	assertLegacySubscriptionPlanPreserved(t, db, legacy)
	assertLegacySubscriptionPlanNullsPreserved(t, db, "plan-schema-server-null")
	insertReferenceSubscriptionPlanWithDatabaseDefaults(t, db, "plan-schema-server-direct", "8.500000")
	assertSubscriptionPlanExplicitDisabledCreate(t, db, "plan-schema-server-disabled")
	require.NoError(t, migrateDB())
	assertReferenceSubscriptionPlanSchema(t, db)
	assertLegacySubscriptionPlanPreserved(t, db, legacy)

	// A server BIGINT value outside the reference INT domain must abort before
	// AutoMigrate can narrow it. Restore a fresh schema after proving the gate.
	require.NoError(t, db.Migrator().DropTable(&SubscriptionPlan{}))
	require.NoError(t, db.AutoMigrate(&wideLegacySubscriptionPlanSchema{}))
	wide := makeWideLegacySubscriptionPlan()
	wide.DurationValue = referenceIntMax + 1
	require.NoError(t, db.Create(&wide).Error)
	err := migrateDB()
	assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
	var preservedWide int64
	require.NoError(t, db.Table(SubscriptionPlan{}.TableName()).Select("duration_value").
		Where("id = ?", wide.Id).Scan(&preservedWide).Error)
	assert.Equal(t, wide.DurationValue, preservedWide)

	require.NoError(t, db.Migrator().DropTable(&SubscriptionPlan{}))
	require.NoError(t, db.AutoMigrate(&nullableLegacySubscriptionPlanSchema{}))
	nullable := makeNullableLegacySubscriptionPlan()
	nullable.Currency = nil
	require.NoError(t, db.Create(&nullable).Error)
	err = migrateDB()
	assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
	var nullCurrency sql.NullString
	require.NoError(t, db.Table(SubscriptionPlan{}.TableName()).Select("currency").
		Where("id = ?", nullable.Id).Row().Scan(&nullCurrency))
	assert.False(t, nullCurrency.Valid)

	require.NoError(t, db.Migrator().DropTable(&SubscriptionPlan{}))
	require.NoError(t, migrateDB())
	assertReferenceSubscriptionPlanSchema(t, db)
}
