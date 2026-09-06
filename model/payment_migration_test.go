package model

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyTopUpForPaymentMigration struct {
	Id              int `gorm:"primaryKey"`
	UserId          int
	Amount          int64
	Money           float64
	TradeNo         string `gorm:"type:varchar(255);not null;uniqueIndex"`
	PaymentMethod   string
	PaymentProvider string
	Status          string
}

func (legacyTopUpForPaymentMigration) TableName() string { return "top_ups" }

type legacySubscriptionOrderForPaymentMigration struct {
	Id              int `gorm:"primaryKey"`
	UserId          int
	PlanId          int
	Money           float64
	TradeNo         string `gorm:"type:varchar(255);not null;uniqueIndex"`
	PaymentMethod   string
	PaymentProvider string
	Status          string
}

type legacyUserSubscriptionForPaymentMigration struct {
	Id        int `gorm:"primaryKey"`
	UserId    int
	PlanId    int
	Status    string
	StartTime int64
	EndTime   int64
}

func (legacyUserSubscriptionForPaymentMigration) TableName() string { return "user_subscriptions" }

func (legacySubscriptionOrderForPaymentMigration) TableName() string { return "subscription_orders" }

func TestPaymentBindingMigrationPreservesLegacyRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "payment-migration.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&legacyTopUpForPaymentMigration{}, &legacySubscriptionOrderForPaymentMigration{},
		&legacyUserSubscriptionForPaymentMigration{}))
	require.NoError(t, db.Create(&legacyTopUpForPaymentMigration{
		UserId: 1, Amount: 10, Money: 1, TradeNo: "ref_legacy_migration", PaymentMethod: "stripe",
		PaymentProvider: "stripe", Status: "success",
	}).Error)
	require.NoError(t, db.Create(&legacySubscriptionOrderForPaymentMigration{
		UserId: 1, PlanId: 1, Money: 1, TradeNo: "sub_ref_legacy_migration", PaymentMethod: "stripe",
		PaymentProvider: "stripe", Status: "pending",
	}).Error)
	require.NoError(t, db.Create(&legacyUserSubscriptionForPaymentMigration{
		UserId: 1, PlanId: 1, Status: "active", StartTime: 1, EndTime: 2,
	}).Error)

	require.NoError(t, db.AutoMigrate(&TopUp{}, &SubscriptionOrder{}, &UserSubscription{}))
	var topup TopUp
	require.NoError(t, db.Where("trade_no = ?", "ref_legacy_migration").First(&topup).Error)
	assert.Zero(t, topup.ProviderBindingVersion)
	assert.Zero(t, topup.CreditQuotaVersion)
	assert.Zero(t, topup.CreditQuota)
	assert.Nil(t, topup.ProviderSessionId)
	assert.Empty(t, topup.CheckoutFingerprint)
	assert.Zero(t, topup.ReconciliationAttempts)
	assert.Equal(t, "success", topup.Status)
	var subscription SubscriptionOrder
	require.NoError(t, db.Where("trade_no = ?", "sub_ref_legacy_migration").First(&subscription).Error)
	assert.Zero(t, subscription.ProviderBindingVersion)
	assert.Nil(t, subscription.ProviderSessionId)
	assert.False(t, subscription.CapacityReserved)
	assert.Empty(t, subscription.CheckoutFingerprint)
	assert.Zero(t, subscription.ReconciliationAttempts)
	assert.Equal(t, "pending", subscription.Status)
	var userSubscription UserSubscription
	require.NoError(t, db.First(&userSubscription).Error)
	assert.Zero(t, userSubscription.EntitlementVersion)
	assert.Zero(t, userSubscription.UsageEpoch)
	assert.Equal(t, "pending", userSubscription.EntitlementMigrationState)
	assert.Empty(t, userSubscription.QuotaResetPeriodSnapshot)
	assert.True(t, db.Migrator().HasIndex(&TopUp{}, "idx_topups_provider_session_id"))
	assert.True(t, db.Migrator().HasIndex(&SubscriptionOrder{}, "idx_subscription_orders_provider_session_id"))
}
