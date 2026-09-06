package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
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

// referenceSubscriptionOrderSchema is an independently transcribed mirror of
// the pinned reference entity. TokenRouter's immutable purchase and provider
// recovery state is intentionally absent from this comparison type.
type referenceSubscriptionOrderSchema struct {
	Id              int
	UserId          int `gorm:"index"`
	PlanId          int `gorm:"index"`
	Money           float64
	TradeNo         string `gorm:"unique;type:varchar(255);index"`
	PaymentMethod   string `gorm:"type:varchar(50)"`
	PaymentProvider string `gorm:"type:varchar(50);default:''"`
	Status          string
	CreateTime      int64
	CompleteTime    int64
	ProviderPayload string `json:"provider_payload" gorm:"type:text"`
}

func (referenceSubscriptionOrderSchema) TableName() string { return "subscription_orders" }

// legacySubscriptionOrderSchema captures the target immediately before this
// alignment. Every target-only field is present so the migration test guards
// immutable entitlement data, provider bindings, and reconciliation leases.
type legacySubscriptionOrderSchema struct {
	Id                           int `gorm:"primaryKey"`
	UserId                       int `gorm:"index"`
	PlanId                       int `gorm:"index"`
	Money                        float64
	TradeNo                      string  `gorm:"type:varchar(255);not null;uniqueIndex"`
	PaymentMethod                string  `gorm:"type:varchar(50)"`
	PaymentProvider              string  `gorm:"type:varchar(50)"`
	ProviderAmountMinor          int64   `gorm:"type:bigint;not null;default:0"`
	ProviderCurrency             string  `gorm:"type:varchar(8);not null;default:''"`
	ProviderBindingVersion       int     `gorm:"not null;default:0"`
	ProviderSessionId            *string `gorm:"type:varchar(255);uniqueIndex:idx_subscription_orders_provider_session_id"`
	ProviderOrderType            string  `gorm:"type:varchar(32);not null;default:''"`
	ProviderMode                 string  `gorm:"type:varchar(32);not null;default:''"`
	ProviderPriceId              string  `gorm:"type:varchar(255);not null;default:''"`
	EntitlementSnapshot          string  `gorm:"type:text"`
	CapacityReserved             bool    `gorm:"not null;default:false;index"`
	ReconciliationState          string  `gorm:"type:varchar(64);not null;default:''"`
	ReconciliationDetail         string  `gorm:"type:text"`
	CheckoutRequest              string  `gorm:"type:text"`
	CheckoutFingerprint          string  `gorm:"type:varchar(64);not null;default:''"`
	ProviderCreateIdempotencyKey string  `gorm:"type:varchar(255);not null;default:''"`
	ProviderExpiresAt            int64   `gorm:"type:bigint;not null;default:0;index"`
	ReconciliationNextAt         int64   `gorm:"type:bigint;not null;default:0;index:idx_subscription_order_reconcile,priority:2"`
	ReconciliationAttempts       int     `gorm:"not null;default:0"`
	ReconciliationLeaseOwner     string  `gorm:"type:varchar(128);not null;default:'';index"`
	ReconciliationLeaseExpiresAt int64   `gorm:"type:bigint;not null;default:0;index"`
	Status                       string  `gorm:"type:varchar(50)"`
	CreateTime                   int64
	CompleteTime                 int64
	ProviderPayload              string `gorm:"type:text"`
}

func (legacySubscriptionOrderSchema) TableName() string { return "subscription_orders" }

var subscriptionOrderTargetOnlyFields = []string{
	"ProviderAmountMinor", "ProviderCurrency", "ProviderBindingVersion", "ProviderSessionId",
	"ProviderOrderType", "ProviderMode", "ProviderPriceId", "EntitlementSnapshot",
	"CapacityReserved", "ReconciliationState", "ReconciliationDetail", "CheckoutRequest",
	"CheckoutFingerprint", "ProviderCreateIdempotencyKey", "ProviderExpiresAt",
	"ReconciliationNextAt", "ReconciliationAttempts", "ReconciliationLeaseOwner",
	"ReconciliationLeaseExpiresAt",
}

var subscriptionOrderTargetOnlyColumns = map[string]struct{}{
	"provider_amount_minor": {}, "provider_currency": {}, "provider_binding_version": {},
	"provider_session_id": {}, "provider_order_type": {}, "provider_mode": {},
	"provider_price_id": {}, "entitlement_snapshot": {}, "capacity_reserved": {},
	"reconciliation_state": {}, "reconciliation_detail": {}, "checkout_request": {},
	"checkout_fingerprint": {}, "provider_create_idempotency_key": {},
	"provider_expires_at": {}, "reconciliation_next_at": {},
	"reconciliation_attempts": {}, "reconciliation_lease_owner": {},
	"reconciliation_lease_expires_at": {},
}

var subscriptionOrderTargetOnlyIndexes = map[string]struct{}{
	"idx_subscription_orders_provider_session_id":             {},
	"idx_subscription_orders_capacity_reserved":               {},
	"idx_subscription_orders_provider_expires_at":             {},
	"idx_subscription_order_reconcile":                        {},
	"idx_subscription_orders_reconciliation_lease_owner":      {},
	"idx_subscription_orders_reconciliation_lease_expires_at": {},
}

func TestReferenceSubscriptionOrderSchemaPortableDialects(t *testing.T) {
	targetSchema, err := schema.Parse(&SubscriptionOrder{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceSchema, err := schema.Parse(&referenceSubscriptionOrderSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	for _, referenceField := range referenceSchema.Fields {
		targetField := targetSchema.LookUpField(referenceField.Name)
		require.NotNil(t, targetField, "SubscriptionOrder.%s is missing", referenceField.Name)
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
	for _, fieldName := range subscriptionOrderTargetOnlyFields {
		assert.Nil(t, referenceSchema.LookUpField(fieldName), fieldName+" must remain a target extension")
		require.NotNil(t, targetSchema.LookUpField(fieldName), fieldName+" target durability field")
	}
	assert.Equal(t, len(referenceSchema.Fields)+len(subscriptionOrderTargetOnlyFields), len(targetSchema.Fields))

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
					"SubscriptionOrder.%s %s type", referenceField.Name, dialect.Name())
			}
		})
	}

	for _, referenceIndex := range referenceSchema.ParseIndexes() {
		targetIndex := targetSchema.LookIndex(referenceIndex.Name)
		require.NotNil(t, targetIndex, "missing SubscriptionOrder index %s", referenceIndex.Name)
		assert.Equal(t, referenceIndex.Class, targetIndex.Class, referenceIndex.Name+" class")
		require.Len(t, targetIndex.Fields, len(referenceIndex.Fields), referenceIndex.Name+" fields")
		for i := range referenceIndex.Fields {
			assert.Equal(t, referenceIndex.Fields[i].DBName, targetIndex.Fields[i].DBName,
				referenceIndex.Name+" column")
			assert.Equal(t, referenceIndex.Fields[i].Priority, targetIndex.Fields[i].Priority,
				referenceIndex.Name+" priority")
		}
	}
	targetIndexes := targetSchema.ParseIndexes()
	for _, index := range targetIndexes {
		if referenceSchema.LookIndex(index.Name) != nil {
			continue
		}
		_, ok := subscriptionOrderTargetOnlyIndexes[index.Name]
		assert.True(t, ok, "unexpected target-only SubscriptionOrder index %s", index.Name)
	}
	assert.Len(t, targetIndexes, len(referenceSchema.ParseIndexes())+len(subscriptionOrderTargetOnlyIndexes))

	referenceConstraints := referenceSchema.ParseUniqueConstraints()
	targetConstraints := targetSchema.ParseUniqueConstraints()
	require.Len(t, referenceConstraints, 1)
	require.Len(t, targetConstraints, 1)
	require.Contains(t, referenceConstraints, "uni_subscription_orders_trade_no")
	require.Contains(t, targetConstraints, "uni_subscription_orders_trade_no")

	referencePayload, ok := reflect.TypeOf(referenceSubscriptionOrderSchema{}).FieldByName("ProviderPayload")
	require.True(t, ok)
	targetPayload, ok := reflect.TypeOf(SubscriptionOrder{}).FieldByName("ProviderPayload")
	require.True(t, ok)
	assert.Equal(t, "provider_payload", referencePayload.Tag.Get("json"))
	assert.Equal(t, "-", targetPayload.Tag.Get("json"),
		"raw provider callback evidence must remain outside serialized API models")
}

func TestReferenceSubscriptionOrderSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceSubscriptionOrderSchemaMatchesSQLite(t, db)
	assertReferenceSubscriptionOrderSchema(t, db)

	insertReferenceSubscriptionOrderWithDatabaseDefaults(t, db, "sub-schema-direct", "subscription-provider-secret-direct")
	assertSubscriptionOrderProviderPayloadRedacted(t, db, "sub-schema-direct", "subscription-provider-secret-direct")
	assert.Error(t, db.Exec(`INSERT INTO subscription_orders (trade_no) VALUES (NULL)`).Error,
		"the target must not relax the trade-number security constraint")
	assert.Error(t, db.Create(&SubscriptionOrder{}).Error, "the ORM hook must reject an empty trade number")
	assert.Error(t, db.Exec(`INSERT INTO subscription_orders (trade_no) VALUES (?)`, "sub-schema-direct").Error,
		"trade-number uniqueness must survive the index-shape migration")
	assertSubscriptionOrderStatusWidened(t, db, "sub-schema-direct")

	columnsBefore := sqliteColumnSignatures(t, db, SubscriptionOrder{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, SubscriptionOrder{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, SubscriptionOrder{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, SubscriptionOrder{}.TableName()))
	assertReferenceSubscriptionOrderSchemaMatchesSQLite(t, db)
}

func TestReferenceSubscriptionOrderSchemaSQLitePreservesLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacySubscriptionOrderSchema{}))
	legacy := makeLegacySubscriptionOrder("sub-schema-legacy")
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO subscription_orders (user_id, plan_id, money, trade_no, payment_method,
			payment_provider, status, create_time, complete_time, provider_payload)
		VALUES (?, ?, ?, ?, ?, NULL, NULL, ?, ?, NULL)`,
		82, 83, 84.5, "sub-schema-legacy-null", "stripe", int64(85), int64(86)).Error)

	require.NoError(t, migrateDB())
	assertReferenceSubscriptionOrderSchemaMatchesSQLite(t, db)
	assertReferenceSubscriptionOrderSchema(t, db)
	assertLegacySubscriptionOrderPreserved(t, db, legacy)
	assertLegacySubscriptionOrderNullsPreserved(t, db, "sub-schema-legacy-null")
	assertSubscriptionOrderProviderPayloadRedacted(t, db, legacy.TradeNo, "subscription-provider-secret-legacy")
	insertReferenceSubscriptionOrderWithDatabaseDefaults(t, db, "sub-schema-after-legacy", "subscription-provider-secret-after")
	assertSubscriptionOrderStatusWidened(t, db, "sub-schema-after-legacy")

	columnsBefore := sqliteColumnSignatures(t, db, SubscriptionOrder{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, SubscriptionOrder{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, SubscriptionOrder{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, SubscriptionOrder{}.TableName()))
	assertLegacySubscriptionOrderPreserved(t, db, legacy)
	assertLegacySubscriptionOrderNullsPreserved(t, db, "sub-schema-legacy-null")
}

func assertReferenceSubscriptionOrderSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-subscription-order.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&referenceSubscriptionOrderSchema{}))

	referenceColumns := sqliteColumnSignatures(t, reference, SubscriptionOrder{}.TableName())
	targetColumns := sqliteColumnSignatures(t, db, SubscriptionOrder{}.TableName())
	filteredColumns := make([]sqliteColumnSignature, 0, len(referenceColumns))
	for _, column := range targetColumns {
		if _, targetOnly := subscriptionOrderTargetOnlyColumns[column.Name]; targetOnly {
			continue
		}
		if column.Name == "trade_no" {
			assert.Equal(t, 1, column.NotNull, "target trade_no hardening")
			column.NotNull = 0
		}
		filteredColumns = append(filteredColumns, column)
	}
	assert.Equal(t, referenceColumns, filteredColumns,
		"SubscriptionOrder common columns after target durability and trade-number hardening are normalized")

	referenceIndexes := sqliteIndexSignatures(t, reference, SubscriptionOrder{}.TableName())
	targetIndexes := sqliteIndexSignatures(t, db, SubscriptionOrder{}.TableName())
	filteredIndexes := make([]sqliteIndexSignature, 0, len(referenceIndexes))
	for _, index := range targetIndexes {
		if _, targetOnly := subscriptionOrderTargetOnlyIndexes[index.Name]; targetOnly {
			continue
		}
		filteredIndexes = append(filteredIndexes, index)
	}
	sort.Slice(filteredIndexes, func(i, j int) bool { return filteredIndexes[i].Name < filteredIndexes[j].Name })
	assert.Equal(t, referenceIndexes, filteredIndexes,
		"SubscriptionOrder reference indexes after target recovery indexes are removed")
}

// assertReferenceSubscriptionOrderSchema is shared with the opt-in live
// MySQL/PostgreSQL migration gate.
func assertReferenceSubscriptionOrderSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, index := range []struct {
		name   string
		unique bool
	}{
		{name: "idx_subscription_orders_user_id"},
		{name: "idx_subscription_orders_plan_id"},
		{name: "idx_subscription_orders_trade_no"},
		{name: "idx_subscription_orders_provider_session_id", unique: true},
		{name: "idx_subscription_orders_capacity_reserved"},
		{name: "idx_subscription_orders_provider_expires_at"},
		{name: "idx_subscription_order_reconcile"},
		{name: "idx_subscription_orders_reconciliation_lease_owner"},
		{name: "idx_subscription_orders_reconciliation_lease_expires_at"},
	} {
		assert.True(t, db.Migrator().HasIndex(&SubscriptionOrder{}, index.name), "missing migrated index %s", index.name)
		assertIndexUnique(t, db, &SubscriptionOrder{}, index.name, index.unique)
	}
	assertColumnUnique(t, db, SubscriptionOrder{}.TableName(), "trade_no", true)
	assertColumnNullable(t, db, SubscriptionOrder{}.TableName(), "trade_no", false)
	assertColumnNullable(t, db, SubscriptionOrder{}.TableName(), "payment_provider", true)
	assertColumnDefault(t, db, SubscriptionOrder{}.TableName(), "payment_provider", "")
	assertColumnNullable(t, db, SubscriptionOrder{}.TableName(), "status", true)
	_, statusHasDefault := requireColumnType(t, db, SubscriptionOrder{}.TableName(), "status").DefaultValue()
	assert.False(t, statusHasDefault, "SubscriptionOrder.status must retain the reference's absent database default")
	assertColumnNullable(t, db, SubscriptionOrder{}.TableName(), "provider_payload", true)
	_, payloadHasDefault := requireColumnType(t, db, SubscriptionOrder{}.TableName(), "provider_payload").DefaultValue()
	assert.False(t, payloadHasDefault, "SubscriptionOrder.provider_payload must not gain a database default")

	statement := &gorm.Statement{DB: db}
	require.NoError(t, statement.Parse(&SubscriptionOrder{}))
	status := statement.Schema.LookUpField("Status")
	require.NotNil(t, status)
	assert.Empty(t, status.TagSettings["TYPE"], "SubscriptionOrder.Status must use the reference dialect-native string type")
	tradeNo := statement.Schema.LookUpField("TradeNo")
	require.NotNil(t, tradeNo)
	assert.True(t, tradeNo.NotNull, "SubscriptionOrder.TradeNo must retain target security hardening")
	assert.True(t, tradeNo.Unique, "SubscriptionOrder.TradeNo must use a unique column constraint")

	for column, expected := range map[string]string{
		"provider_amount_minor": "0", "provider_currency": "", "provider_binding_version": "0",
		"provider_order_type": "", "provider_mode": "", "provider_price_id": "",
		"capacity_reserved": "0", "reconciliation_state": "", "checkout_fingerprint": "",
		"provider_create_idempotency_key": "", "provider_expires_at": "0",
		"reconciliation_next_at": "0", "reconciliation_attempts": "0",
		"reconciliation_lease_owner": "", "reconciliation_lease_expires_at": "0",
	} {
		assertColumnDefault(t, db, SubscriptionOrder{}.TableName(), column, expected)
		assertColumnNullable(t, db, SubscriptionOrder{}.TableName(), column, false)
	}
	for _, column := range []string{"provider_session_id", "entitlement_snapshot", "reconciliation_detail", "checkout_request"} {
		assertColumnNullable(t, db, SubscriptionOrder{}.TableName(), column, true)
		_, hasDefault := requireColumnType(t, db, SubscriptionOrder{}.TableName(), column).DefaultValue()
		assert.False(t, hasDefault, "SubscriptionOrder.%s must not gain a database default", column)
	}

	switch db.Dialector.Name() {
	case "sqlite":
		assertColumnDatabaseType(t, db, SubscriptionOrder{}.TableName(), "status", "text")
		assertColumnDatabaseType(t, db, SubscriptionOrder{}.TableName(), "provider_payload", "text")
	case "mysql":
		assertColumnLength(t, db, SubscriptionOrder{}.TableName(), "trade_no", 255)
		assertColumnLength(t, db, SubscriptionOrder{}.TableName(), "payment_provider", 50)
		assertColumnDatabaseType(t, db, SubscriptionOrder{}.TableName(), "status", "longtext")
		assertColumnDatabaseType(t, db, SubscriptionOrder{}.TableName(), "provider_payload", "text")
	case "postgres":
		assertColumnLength(t, db, SubscriptionOrder{}.TableName(), "trade_no", 255)
		assertColumnLength(t, db, SubscriptionOrder{}.TableName(), "payment_provider", 50)
		assertColumnDatabaseType(t, db, SubscriptionOrder{}.TableName(), "status", "text")
		assertColumnDatabaseType(t, db, SubscriptionOrder{}.TableName(), "provider_payload", "text")
	default:
		require.FailNow(t, "unsupported SubscriptionOrder schema test dialect", db.Dialector.Name())
	}
}

func insertReferenceSubscriptionOrderWithDatabaseDefaults(t *testing.T, db *gorm.DB, tradeNo, payload string) int {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO subscription_orders (user_id, plan_id, money, trade_no, payment_method,
			create_time, complete_time, provider_payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		71, 72, 73.5, tradeNo, "stripe", int64(74), int64(75), payload).Error)

	var id int
	var paymentProvider, status, storedPayload sql.NullString
	var providerAmount, providerExpires, reconciliationNext, reconciliationLeaseExpires sql.NullInt64
	var bindingVersion, reconciliationAttempts sql.NullInt64
	var capacityReserved sql.NullBool
	var providerCurrency, orderType, mode, priceID, reconciliationState sql.NullString
	var checkoutFingerprint, idempotencyKey, leaseOwner sql.NullString
	row := db.Table(SubscriptionOrder{}.TableName()).Select(`id, payment_provider, status, provider_payload,
		provider_amount_minor, provider_currency, provider_binding_version, provider_order_type,
		provider_mode, provider_price_id, capacity_reserved, reconciliation_state,
		checkout_fingerprint, provider_create_idempotency_key, provider_expires_at,
		reconciliation_next_at, reconciliation_attempts, reconciliation_lease_owner,
		reconciliation_lease_expires_at`).Where("trade_no = ?", tradeNo).Row()
	require.NoError(t, row.Scan(&id, &paymentProvider, &status, &storedPayload,
		&providerAmount, &providerCurrency, &bindingVersion, &orderType, &mode, &priceID,
		&capacityReserved, &reconciliationState, &checkoutFingerprint, &idempotencyKey,
		&providerExpires, &reconciliationNext, &reconciliationAttempts, &leaseOwner,
		&reconciliationLeaseExpires))
	assert.Positive(t, id)
	require.True(t, paymentProvider.Valid)
	assert.Empty(t, paymentProvider.String)
	assert.False(t, status.Valid, "SubscriptionOrder.status must not gain a database default")
	require.True(t, storedPayload.Valid)
	assert.Equal(t, payload, storedPayload.String, "provider callback evidence must remain durable")
	for name, value := range map[string]sql.NullInt64{
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
		"provider_mode": mode, "provider_price_id": priceID, "reconciliation_state": reconciliationState,
		"checkout_fingerprint": checkoutFingerprint, "provider_create_idempotency_key": idempotencyKey,
		"reconciliation_lease_owner": leaseOwner,
	} {
		require.True(t, value.Valid, name+" default")
		assert.Empty(t, value.String, name+" default")
	}
	require.True(t, capacityReserved.Valid)
	assert.False(t, capacityReserved.Bool)
	return id
}

func makeLegacySubscriptionOrder(tradeNo string) legacySubscriptionOrderSchema {
	session := "cs_subscription_legacy"
	return legacySubscriptionOrderSchema{
		UserId: 81, PlanId: 82, Money: 83.5, TradeNo: tradeNo,
		PaymentMethod: "stripe", PaymentProvider: "stripe", ProviderAmountMinor: 8350,
		ProviderCurrency: "USD", ProviderBindingVersion: 3, ProviderSessionId: &session,
		ProviderOrderType: "subscription", ProviderMode: "payment", ProviderPriceId: "price_legacy",
		EntitlementSnapshot: `{"version":1,"amount_total":5000000}`, CapacityReserved: true,
		ReconciliationState: "manual_review", ReconciliationDetail: `{"legacy":true}`,
		CheckoutRequest: `{"request":"legacy"}`, CheckoutFingerprint: strings.Repeat("f", 64),
		ProviderCreateIdempotencyKey: "subscription-checkout-" + tradeNo,
		ProviderExpiresAt:            84, ReconciliationNextAt: 85, ReconciliationAttempts: 4,
		ReconciliationLeaseOwner: "legacy-worker", ReconciliationLeaseExpiresAt: 86,
		Status: strings.Repeat("s", 50), CreateTime: 87, CompleteTime: 88,
		ProviderPayload: `{"marker":"subscription-provider-secret-legacy"}`,
	}
}

func assertLegacySubscriptionOrderPreserved(t *testing.T, db *gorm.DB, legacy legacySubscriptionOrderSchema) {
	t.Helper()
	var migrated legacySubscriptionOrderSchema
	require.NoError(t, db.First(&migrated, legacy.Id).Error)
	assert.Equal(t, legacy, migrated,
		"schema alignment must not rewrite immutable purchase, provider, or reconciliation state")
}

func assertLegacySubscriptionOrderNullsPreserved(t *testing.T, db *gorm.DB, tradeNo string) {
	t.Helper()
	var paymentProvider, status, providerPayload sql.NullString
	row := db.Table(SubscriptionOrder{}.TableName()).Select("payment_provider, status, provider_payload").
		Where("trade_no = ?", tradeNo).Row()
	require.NoError(t, row.Scan(&paymentProvider, &status, &providerPayload))
	assert.False(t, paymentProvider.Valid, "adding a default must not rewrite legacy NULL")
	assert.False(t, status.Valid, "widening status must not rewrite legacy NULL")
	assert.False(t, providerPayload.Valid, "provider payload alignment must not invent callback evidence")
}

func assertSubscriptionOrderProviderPayloadRedacted(t *testing.T, db *gorm.DB, tradeNo, marker string) {
	t.Helper()
	var order SubscriptionOrder
	require.NoError(t, db.Where("trade_no = ?", tradeNo).First(&order).Error)
	assert.Contains(t, order.ProviderPayload, marker, "provider callback evidence must remain stored")
	encoded, err := json.Marshal(order)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "provider_payload")
	assert.NotContains(t, string(encoded), marker, "provider callback evidence must not cross the JSON boundary")
}

func assertSubscriptionOrderStatusWidened(t *testing.T, db *gorm.DB, tradeNo string) {
	t.Helper()
	widened := strings.Repeat("w", 256)
	require.NoError(t, db.Table(SubscriptionOrder{}.TableName()).Where("trade_no = ?", tradeNo).
		Update("status", widened).Error)
	var stored string
	require.NoError(t, db.Table(SubscriptionOrder{}.TableName()).Select("status").
		Where("trade_no = ?", tradeNo).Scan(&stored).Error)
	assert.Equal(t, widened, stored)
}

func exerciseReferenceSubscriptionOrderSchemaLegacyExternal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Contains(t, []string{"mysql", "postgres"}, db.Dialector.Name())
	require.NoError(t, db.Migrator().DropTable(&SubscriptionOrder{}))
	require.NoError(t, db.AutoMigrate(&legacySubscriptionOrderSchema{}))
	legacy := makeLegacySubscriptionOrder("sub-schema-server-legacy")
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO subscription_orders (user_id, plan_id, money, trade_no, payment_method,
			payment_provider, status, create_time, complete_time, provider_payload)
		VALUES (?, ?, ?, ?, ?, NULL, NULL, ?, ?, NULL)`,
		92, 93, 94.5, "sub-schema-server-null", "stripe", int64(95), int64(96)).Error)

	require.NoError(t, migrateDB())
	assertReferenceSubscriptionOrderSchema(t, db)
	assertLegacySubscriptionOrderPreserved(t, db, legacy)
	assertLegacySubscriptionOrderNullsPreserved(t, db, "sub-schema-server-null")
	assertSubscriptionOrderProviderPayloadRedacted(t, db, legacy.TradeNo, "subscription-provider-secret-legacy")
	insertReferenceSubscriptionOrderWithDatabaseDefaults(t, db, "sub-schema-server-direct", "subscription-provider-secret-server")
	assertSubscriptionOrderStatusWidened(t, db, "sub-schema-server-direct")

	require.NoError(t, migrateDB())
	assertReferenceSubscriptionOrderSchema(t, db)
	assertLegacySubscriptionOrderPreserved(t, db, legacy)
	assertLegacySubscriptionOrderNullsPreserved(t, db, "sub-schema-server-null")
}
