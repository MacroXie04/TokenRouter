package store

import (
	"database/sql"
	"path/filepath"
	"sort"
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

// referenceTopUpSchema is an independently transcribed database-only mirror
// of the pinned TopUp entity. Target-only payment durability fields are
// deliberately absent so common schema can be compared without treating
// stronger reconciliation state as reference behavior.
type referenceTopUpSchema struct {
	Id              int
	UserId          int `gorm:"index"`
	Amount          int64
	Money           float64
	TradeNo         string `gorm:"unique;type:varchar(255);index"`
	PaymentMethod   string `gorm:"type:varchar(50)"`
	PaymentProvider string `gorm:"type:varchar(50);default:''"`
	CreateTime      int64
	CompleteTime    int64
	Status          string
}

func (referenceTopUpSchema) TableName() string { return "top_ups" }

// legacyTopUpSchema captures the target immediately before reference
// alignment. It includes every target durability field so migration tests
// prove the index/default/type repair cannot discard reconciliation state.
type legacyTopUpSchema struct {
	Id                           int `gorm:"primaryKey"`
	UserId                       int `gorm:"index"`
	Amount                       int64
	CreditQuota                  int64 `gorm:"type:bigint;not null;default:0"`
	CreditQuotaVersion           int   `gorm:"not null;default:0"`
	Money                        float64
	TradeNo                      string  `gorm:"type:varchar(255);not null;uniqueIndex"`
	PaymentMethod                string  `gorm:"type:varchar(50)"`
	PaymentProvider              string  `gorm:"type:varchar(50)"`
	ProviderAmountMinor          int64   `gorm:"type:bigint;not null;default:0"`
	ProviderCurrency             string  `gorm:"type:varchar(8);not null;default:''"`
	ProviderBindingVersion       int     `gorm:"not null;default:0"`
	ProviderSessionId            *string `gorm:"type:varchar(255);uniqueIndex:idx_topups_provider_session_id"`
	ProviderOrderType            string  `gorm:"type:varchar(32);not null;default:''"`
	ProviderMode                 string  `gorm:"type:varchar(32);not null;default:''"`
	ReconciliationState          string  `gorm:"type:varchar(64);not null;default:''"`
	ReconciliationDetail         string  `gorm:"type:text"`
	CheckoutRequest              string  `gorm:"type:text"`
	CheckoutFingerprint          string  `gorm:"type:varchar(64);not null;default:''"`
	ProviderCreateIdempotencyKey string  `gorm:"type:varchar(255);not null;default:''"`
	ProviderExpiresAt            int64   `gorm:"type:bigint;not null;default:0;index"`
	ReconciliationNextAt         int64   `gorm:"type:bigint;not null;default:0;index:idx_topup_reconcile,priority:2"`
	ReconciliationAttempts       int     `gorm:"not null;default:0"`
	ReconciliationLeaseOwner     string  `gorm:"type:varchar(128);not null;default:'';index"`
	ReconciliationLeaseExpiresAt int64   `gorm:"type:bigint;not null;default:0;index"`
	CreateTime                   int64
	CompleteTime                 int64
	Status                       string `gorm:"type:varchar(50)"`
}

func (legacyTopUpSchema) TableName() string { return "top_ups" }

var topUpTargetOnlyFields = []string{
	"CreditQuota", "CreditQuotaVersion", "ProviderAmountMinor", "ProviderCurrency",
	"ProviderBindingVersion", "ProviderSessionId", "ProviderOrderType", "ProviderMode",
	"ReconciliationState", "ReconciliationDetail", "CheckoutRequest", "CheckoutFingerprint",
	"ProviderCreateIdempotencyKey", "ProviderExpiresAt", "ReconciliationNextAt",
	"ReconciliationAttempts", "ReconciliationLeaseOwner", "ReconciliationLeaseExpiresAt",
}

var topUpTargetOnlyColumns = map[string]struct{}{
	"credit_quota": {}, "credit_quota_version": {}, "provider_amount_minor": {},
	"provider_currency": {}, "provider_binding_version": {}, "provider_session_id": {},
	"provider_order_type": {}, "provider_mode": {}, "reconciliation_state": {},
	"reconciliation_detail": {}, "checkout_request": {}, "checkout_fingerprint": {},
	"provider_create_idempotency_key": {}, "provider_expires_at": {},
	"reconciliation_next_at": {}, "reconciliation_attempts": {},
	"reconciliation_lease_owner": {}, "reconciliation_lease_expires_at": {},
}

var topUpTargetOnlyIndexes = map[string]struct{}{
	"idx_topups_provider_session_id":              {},
	"idx_top_ups_provider_expires_at":             {},
	"idx_topup_reconcile":                         {},
	"idx_top_ups_reconciliation_lease_owner":      {},
	"idx_top_ups_reconciliation_lease_expires_at": {},
}

func TestReferenceTopUpSchemaPortableDialects(t *testing.T) {
	targetSchema, err := schema.Parse(&TopUp{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceSchema, err := schema.Parse(&referenceTopUpSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	for _, referenceField := range referenceSchema.Fields {
		targetField := targetSchema.LookUpField(referenceField.Name)
		require.NotNil(t, targetField, "TopUp.%s is missing", referenceField.Name)
		assert.Equal(t, referenceField.DBName, targetField.DBName, referenceField.Name+" column")
		assert.Equal(t, referenceField.PrimaryKey, targetField.PrimaryKey, referenceField.Name+" primary key")
		assert.Equal(t, referenceField.AutoIncrement, targetField.AutoIncrement, referenceField.Name+" auto increment")
		if referenceField.Name == "TradeNo" {
			assert.False(t, referenceField.NotNull, "the reference trade number is nullable")
			assert.True(t, targetField.NotNull, "the target must retain fail-closed trade-number nullability")
		} else {
			assert.Equal(t, referenceField.NotNull, targetField.NotNull, referenceField.Name+" nullability")
		}
		assert.Equal(t, referenceField.Unique, targetField.Unique, referenceField.Name+" column uniqueness")
		assert.Equal(t, referenceField.HasDefaultValue, targetField.HasDefaultValue, referenceField.Name+" default presence")
		assert.Equal(t, referenceField.DefaultValue, targetField.DefaultValue, referenceField.Name+" default")
		assert.Equal(t, referenceField.Size, targetField.Size, referenceField.Name+" size")
	}
	for _, fieldName := range topUpTargetOnlyFields {
		assert.Nil(t, referenceSchema.LookUpField(fieldName), fieldName+" must remain a target extension")
		require.NotNil(t, targetSchema.LookUpField(fieldName), fieldName+" target durability field")
	}
	assert.Equal(t, len(referenceSchema.Fields)+len(topUpTargetOnlyFields), len(targetSchema.Fields))

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
					"TopUp.%s %s type", referenceField.Name, dialect.Name())
			}
		})
	}

	referenceIndexes := referenceSchema.ParseIndexes()
	targetIndexes := targetSchema.ParseIndexes()
	for _, referenceIndex := range referenceIndexes {
		targetIndex := targetSchema.LookIndex(referenceIndex.Name)
		require.NotNil(t, targetIndex, "missing TopUp index %s", referenceIndex.Name)
		assert.Equal(t, referenceIndex.Class, targetIndex.Class, referenceIndex.Name+" class")
		require.Len(t, targetIndex.Fields, len(referenceIndex.Fields), referenceIndex.Name+" fields")
		for i := range referenceIndex.Fields {
			assert.Equal(t, referenceIndex.Fields[i].DBName, targetIndex.Fields[i].DBName,
				referenceIndex.Name+" column")
			assert.Equal(t, referenceIndex.Fields[i].Priority, targetIndex.Fields[i].Priority,
				referenceIndex.Name+" priority")
		}
	}
	for _, index := range targetIndexes {
		if referenceSchema.LookIndex(index.Name) != nil {
			continue
		}
		_, ok := topUpTargetOnlyIndexes[index.Name]
		assert.True(t, ok, "unexpected target-only TopUp index %s", index.Name)
	}
	assert.Len(t, targetIndexes, len(referenceIndexes)+len(topUpTargetOnlyIndexes))

	referenceConstraints := referenceSchema.ParseUniqueConstraints()
	targetConstraints := targetSchema.ParseUniqueConstraints()
	require.Len(t, referenceConstraints, 1)
	require.Len(t, targetConstraints, 1)
	require.Contains(t, referenceConstraints, "uni_top_ups_trade_no")
	require.Contains(t, targetConstraints, "uni_top_ups_trade_no")
}

func TestReferenceTopUpSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceTopUpSchemaMatchesSQLite(t, db)
	assertReferenceTopUpSchema(t, db)

	insertReferenceTopUpWithDatabaseDefaults(t, db, "topup-schema-direct")
	assert.Error(t, db.Exec(`INSERT INTO top_ups (trade_no) VALUES (NULL)`).Error,
		"the target must not relax the trade-number security constraint")
	assert.Error(t, db.Create(&TopUp{}).Error, "the ORM hook must reject an empty trade number")
	assert.Error(t, db.Exec(`INSERT INTO top_ups (trade_no) VALUES (?)`, "topup-schema-direct").Error,
		"trade-number uniqueness must survive the index-shape migration")
	assertTopUpStatusWidened(t, db, "topup-schema-direct")

	columnsBefore := sqliteColumnSignatures(t, db, TopUp{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, TopUp{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, TopUp{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, TopUp{}.TableName()))
	assertReferenceTopUpSchemaMatchesSQLite(t, db)
}

func TestReferenceTopUpSchemaSQLitePreservesLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacyTopUpSchema{}))
	legacy := makeLegacyTopUp("topup-schema-legacy")
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO top_ups (user_id, amount, money, trade_no, payment_method,
			payment_provider, create_time, complete_time, status)
		VALUES (?, ?, ?, ?, ?, NULL, ?, ?, NULL)`,
		82, int64(83), 84.5, "topup-schema-legacy-null", "stripe", int64(85), int64(86)).Error)

	require.NoError(t, migrateDB())
	assertReferenceTopUpSchemaMatchesSQLite(t, db)
	assertReferenceTopUpSchema(t, db)
	assertLegacyTopUpPreserved(t, db, legacy)
	assertLegacyTopUpNullsPreserved(t, db, "topup-schema-legacy-null")
	insertReferenceTopUpWithDatabaseDefaults(t, db, "topup-schema-after-legacy")
	assertTopUpStatusWidened(t, db, "topup-schema-after-legacy")

	columnsBefore := sqliteColumnSignatures(t, db, TopUp{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, TopUp{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, TopUp{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, TopUp{}.TableName()))
	assertLegacyTopUpPreserved(t, db, legacy)
	assertLegacyTopUpNullsPreserved(t, db, "topup-schema-legacy-null")
}

func assertReferenceTopUpSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-topup.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&referenceTopUpSchema{}))

	referenceColumns := sqliteColumnSignatures(t, reference, TopUp{}.TableName())
	targetColumns := sqliteColumnSignatures(t, db, TopUp{}.TableName())
	filteredColumns := make([]sqliteColumnSignature, 0, len(referenceColumns))
	for _, column := range targetColumns {
		if _, targetOnly := topUpTargetOnlyColumns[column.Name]; targetOnly {
			continue
		}
		if column.Name == "trade_no" {
			assert.Equal(t, 1, column.NotNull, "target trade_no hardening")
			column.NotNull = 0
		}
		filteredColumns = append(filteredColumns, column)
	}
	assert.Equal(t, referenceColumns, filteredColumns,
		"TopUp common columns after target durability and trade-number hardening are normalized")

	referenceIndexes := sqliteIndexSignatures(t, reference, TopUp{}.TableName())
	targetIndexes := sqliteIndexSignatures(t, db, TopUp{}.TableName())
	filteredIndexes := make([]sqliteIndexSignature, 0, len(referenceIndexes))
	for _, index := range targetIndexes {
		if _, targetOnly := topUpTargetOnlyIndexes[index.Name]; targetOnly {
			continue
		}
		filteredIndexes = append(filteredIndexes, index)
	}
	sort.Slice(filteredIndexes, func(i, j int) bool { return filteredIndexes[i].Name < filteredIndexes[j].Name })
	assert.Equal(t, referenceIndexes, filteredIndexes,
		"TopUp reference indexes after target reconciliation indexes are removed")
}

// assertReferenceTopUpSchema is driver-neutral and shared with the opt-in
// MySQL/PostgreSQL migration gate.
func assertReferenceTopUpSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, index := range []struct {
		name   string
		unique bool
	}{
		{name: "idx_top_ups_user_id"},
		{name: "idx_top_ups_trade_no"},
		{name: "idx_topups_provider_session_id", unique: true},
		{name: "idx_top_ups_provider_expires_at"},
		{name: "idx_topup_reconcile"},
		{name: "idx_top_ups_reconciliation_lease_owner"},
		{name: "idx_top_ups_reconciliation_lease_expires_at"},
	} {
		assert.True(t, db.Migrator().HasIndex(&TopUp{}, index.name), "missing migrated index %s", index.name)
		assertIndexUnique(t, db, &TopUp{}, index.name, index.unique)
	}
	assert.False(t, db.Migrator().HasIndex(&TopUp{}, "idx_top_ups_payment_provider"),
		"the pinned reference does not index payment_provider")
	assertColumnUnique(t, db, TopUp{}.TableName(), "trade_no", true)
	assertColumnNullable(t, db, TopUp{}.TableName(), "trade_no", false)
	assertColumnNullable(t, db, TopUp{}.TableName(), "payment_provider", true)
	assertColumnDefault(t, db, TopUp{}.TableName(), "payment_provider", "")
	assertColumnNullable(t, db, TopUp{}.TableName(), "status", true)
	_, statusHasDefault := requireColumnType(t, db, TopUp{}.TableName(), "status").DefaultValue()
	assert.False(t, statusHasDefault, "TopUp.status must retain the reference's absent database default")

	statement := &gorm.Statement{DB: db}
	require.NoError(t, statement.Parse(&TopUp{}))
	status := statement.Schema.LookUpField("Status")
	require.NotNil(t, status)
	assert.Empty(t, status.TagSettings["TYPE"], "TopUp.Status must use the reference dialect-native string type")
	tradeNo := statement.Schema.LookUpField("TradeNo")
	require.NotNil(t, tradeNo)
	assert.True(t, tradeNo.NotNull, "TopUp.TradeNo must retain target security hardening")
	assert.True(t, tradeNo.Unique, "TopUp.TradeNo must use a unique column constraint")

	for column, expected := range map[string]string{
		"credit_quota": "0", "credit_quota_version": "0", "provider_amount_minor": "0",
		"provider_currency": "", "provider_binding_version": "0", "provider_order_type": "",
		"provider_mode": "", "reconciliation_state": "", "checkout_fingerprint": "",
		"provider_create_idempotency_key": "", "provider_expires_at": "0",
		"reconciliation_next_at": "0", "reconciliation_attempts": "0",
		"reconciliation_lease_owner": "", "reconciliation_lease_expires_at": "0",
	} {
		assertColumnDefault(t, db, TopUp{}.TableName(), column, expected)
		assertColumnNullable(t, db, TopUp{}.TableName(), column, false)
	}

	switch db.Dialector.Name() {
	case "sqlite":
		assertColumnDatabaseType(t, db, TopUp{}.TableName(), "status", "text")
	case "mysql":
		assertColumnLength(t, db, TopUp{}.TableName(), "trade_no", 255)
		assertColumnLength(t, db, TopUp{}.TableName(), "payment_provider", 50)
		assertColumnDatabaseType(t, db, TopUp{}.TableName(), "status", "longtext")
	case "postgres":
		assertColumnLength(t, db, TopUp{}.TableName(), "trade_no", 255)
		assertColumnLength(t, db, TopUp{}.TableName(), "payment_provider", 50)
		assertColumnDatabaseType(t, db, TopUp{}.TableName(), "status", "text")
	default:
		require.FailNow(t, "unsupported TopUp schema test dialect", db.Dialector.Name())
	}
}

func insertReferenceTopUpWithDatabaseDefaults(t *testing.T, db *gorm.DB, tradeNo string) int {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO top_ups (user_id, amount, money, trade_no, payment_method, create_time, complete_time)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, 71, int64(72), 73.5, tradeNo, "stripe", int64(74), int64(75)).Error)

	var id int
	var paymentProvider, status sql.NullString
	var creditQuota, providerAmount, providerExpires, reconciliationNext, reconciliationLeaseExpires sql.NullInt64
	var creditVersion, bindingVersion, reconciliationAttempts sql.NullInt64
	var providerCurrency, orderType, mode, reconciliationState sql.NullString
	var checkoutFingerprint, idempotencyKey, leaseOwner sql.NullString
	row := db.Table(TopUp{}.TableName()).Select(`id, payment_provider, status, credit_quota,
		credit_quota_version, provider_amount_minor, provider_currency, provider_binding_version,
		provider_order_type, provider_mode, reconciliation_state, checkout_fingerprint,
		provider_create_idempotency_key, provider_expires_at, reconciliation_next_at,
		reconciliation_attempts, reconciliation_lease_owner, reconciliation_lease_expires_at`).
		Where("trade_no = ?", tradeNo).Row()
	require.NoError(t, row.Scan(&id, &paymentProvider, &status, &creditQuota, &creditVersion,
		&providerAmount, &providerCurrency, &bindingVersion, &orderType, &mode, &reconciliationState,
		&checkoutFingerprint, &idempotencyKey, &providerExpires, &reconciliationNext,
		&reconciliationAttempts, &leaseOwner, &reconciliationLeaseExpires))
	assert.Positive(t, id)
	require.True(t, paymentProvider.Valid)
	assert.Empty(t, paymentProvider.String)
	assert.False(t, status.Valid, "TopUp.status must not gain a database default")
	for name, value := range map[string]sql.NullInt64{
		"credit_quota": creditQuota, "credit_quota_version": creditVersion,
		"provider_amount_minor": providerAmount, "provider_binding_version": bindingVersion,
		"provider_expires_at": providerExpires, "reconciliation_next_at": reconciliationNext,
		"reconciliation_attempts":         reconciliationAttempts,
		"reconciliation_lease_expires_at": reconciliationLeaseExpires,
	} {
		require.True(t, value.Valid, name+" default")
		assert.Zero(t, value.Int64, name+" default")
	}
	for name, value := range map[string]sql.NullString{
		"provider_currency": providerCurrency, "provider_order_type": orderType,
		"provider_mode": mode, "reconciliation_state": reconciliationState,
		"checkout_fingerprint":            checkoutFingerprint,
		"provider_create_idempotency_key": idempotencyKey,
		"reconciliation_lease_owner":      leaseOwner,
	} {
		require.True(t, value.Valid, name+" default")
		assert.Empty(t, value.String, name+" default")
	}
	return id
}

func makeLegacyTopUp(tradeNo string) legacyTopUpSchema {
	session := "cs_legacy_topup"
	return legacyTopUpSchema{
		UserId: 81, Amount: 82, CreditQuota: 83, CreditQuotaVersion: 2, Money: 84.5,
		TradeNo: tradeNo, PaymentMethod: "stripe", PaymentProvider: "stripe",
		ProviderAmountMinor: 8450, ProviderCurrency: "USD", ProviderBindingVersion: 3,
		ProviderSessionId: &session, ProviderOrderType: "payment", ProviderMode: "payment",
		ReconciliationState: "manual_review", ReconciliationDetail: `{"legacy":true}`,
		CheckoutRequest: `{"request":"legacy"}`, CheckoutFingerprint: strings.Repeat("f", 64),
		ProviderCreateIdempotencyKey: "wallet-checkout-" + tradeNo,
		ProviderExpiresAt:            85, ReconciliationNextAt: 86, ReconciliationAttempts: 4,
		ReconciliationLeaseOwner: "legacy-worker", ReconciliationLeaseExpiresAt: 87,
		CreateTime: 88, CompleteTime: 89, Status: strings.Repeat("s", 50),
	}
}

func assertLegacyTopUpPreserved(t *testing.T, db *gorm.DB, legacy legacyTopUpSchema) {
	t.Helper()
	var migrated TopUp
	require.NoError(t, db.First(&migrated, legacy.Id).Error)
	assert.Equal(t, legacy.UserId, migrated.UserId)
	assert.Equal(t, legacy.Amount, migrated.Amount)
	assert.Equal(t, legacy.CreditQuota, migrated.CreditQuota)
	assert.Equal(t, legacy.CreditQuotaVersion, migrated.CreditQuotaVersion)
	assert.Equal(t, legacy.Money, migrated.Money)
	assert.Equal(t, legacy.TradeNo, migrated.TradeNo)
	assert.Equal(t, legacy.PaymentMethod, migrated.PaymentMethod)
	assert.Equal(t, legacy.PaymentProvider, migrated.PaymentProvider,
		"adding the payment-provider default must not rewrite an explicit value")
	assert.Equal(t, legacy.ProviderAmountMinor, migrated.ProviderAmountMinor)
	assert.Equal(t, legacy.ProviderCurrency, migrated.ProviderCurrency)
	assert.Equal(t, legacy.ProviderBindingVersion, migrated.ProviderBindingVersion)
	assert.Equal(t, legacy.ProviderSessionId, migrated.ProviderSessionId)
	assert.Equal(t, legacy.ProviderOrderType, migrated.ProviderOrderType)
	assert.Equal(t, legacy.ProviderMode, migrated.ProviderMode)
	assert.Equal(t, legacy.ReconciliationState, migrated.ReconciliationState)
	assert.Equal(t, legacy.ReconciliationDetail, migrated.ReconciliationDetail)
	assert.Equal(t, legacy.CheckoutRequest, migrated.CheckoutRequest)
	assert.Equal(t, legacy.CheckoutFingerprint, migrated.CheckoutFingerprint)
	assert.Equal(t, legacy.ProviderCreateIdempotencyKey, migrated.ProviderCreateIdempotencyKey)
	assert.Equal(t, legacy.ProviderExpiresAt, migrated.ProviderExpiresAt)
	assert.Equal(t, legacy.ReconciliationNextAt, migrated.ReconciliationNextAt)
	assert.Equal(t, legacy.ReconciliationAttempts, migrated.ReconciliationAttempts)
	assert.Equal(t, legacy.ReconciliationLeaseOwner, migrated.ReconciliationLeaseOwner)
	assert.Equal(t, legacy.ReconciliationLeaseExpiresAt, migrated.ReconciliationLeaseExpiresAt)
	assert.Equal(t, legacy.CreateTime, migrated.CreateTime)
	assert.Equal(t, legacy.CompleteTime, migrated.CompleteTime)
	assert.Equal(t, legacy.Status, migrated.Status)
}

func assertLegacyTopUpNullsPreserved(t *testing.T, db *gorm.DB, tradeNo string) {
	t.Helper()
	var paymentProvider, status sql.NullString
	row := db.Table(TopUp{}.TableName()).Select("payment_provider, status").Where("trade_no = ?", tradeNo).Row()
	require.NoError(t, row.Scan(&paymentProvider, &status))
	assert.False(t, paymentProvider.Valid, "adding a default must not rewrite legacy NULL")
	assert.False(t, status.Valid, "widening status must not rewrite legacy NULL")
}

func assertTopUpStatusWidened(t *testing.T, db *gorm.DB, tradeNo string) {
	t.Helper()
	widened := strings.Repeat("w", 256)
	require.NoError(t, db.Table(TopUp{}.TableName()).Where("trade_no = ?", tradeNo).Update("status", widened).Error)
	var stored string
	require.NoError(t, db.Table(TopUp{}.TableName()).Select("status").Where("trade_no = ?", tradeNo).Scan(&stored).Error)
	assert.Equal(t, widened, stored)
}

func exerciseReferenceTopUpSchemaLegacyExternal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Contains(t, []string{"mysql", "postgres"}, db.Dialector.Name())
	require.NoError(t, db.Migrator().DropTable(&TopUp{}))
	require.NoError(t, db.AutoMigrate(&legacyTopUpSchema{}))
	legacy := makeLegacyTopUp("topup-schema-server-legacy")
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO top_ups (user_id, amount, money, trade_no, payment_method,
			payment_provider, create_time, complete_time, status)
		VALUES (?, ?, ?, ?, ?, NULL, ?, ?, NULL)`,
		92, int64(93), 94.5, "topup-schema-server-null", "stripe", int64(95), int64(96)).Error)

	require.NoError(t, migrateDB())
	assertReferenceTopUpSchema(t, db)
	assertLegacyTopUpPreserved(t, db, legacy)
	assertLegacyTopUpNullsPreserved(t, db, "topup-schema-server-null")
	insertReferenceTopUpWithDatabaseDefaults(t, db, "topup-schema-server-direct")
	assertTopUpStatusWidened(t, db, "topup-schema-server-direct")

	// A rolling restart must retain both the exact reference-compatible shape
	// and every target reconciliation value without repeating unsafe DDL.
	require.NoError(t, migrateDB())
	assertReferenceTopUpSchema(t, db)
	assertLegacyTopUpPreserved(t, db, legacy)
	assertLegacyTopUpNullsPreserved(t, db, "topup-schema-server-null")
}
