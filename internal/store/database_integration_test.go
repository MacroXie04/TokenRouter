package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDatabaseDialectDetectionUsesConnectedDriver(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "dialect.db")), &gorm.Config{})
	require.NoError(t, err)
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	// An inherited or Unix-socket DSN must not override the actual connected
	// driver. This also prevents server-only FOR UPDATE syntax in SQLite tests.
	t.Setenv("SQL_DSN", "root@unix(/tmp/mysql.sock)/tokenrouter")
	assert.True(t, UsingSQLite())
	assert.False(t, UsingMySQL())
	assert.False(t, UsingPostgreSQL())
}

// TestDatabaseMatrixExternalMigration exercises the real driver selected by
// TOKENROUTER_TEST_SQL_DSN. It is skipped during ordinary unit-test runs so CI
// and local verification can opt into MySQL/PostgreSQL without silently
// substituting SQLite.
func TestDatabaseMatrixExternalMigration(t *testing.T) {
	dsn := os.Getenv("TOKENROUTER_TEST_SQL_DSN")
	if dsn == "" {
		t.Skip("set TOKENROUTER_TEST_SQL_DSN to an isolated MySQL or PostgreSQL database")
	}
	if os.Getenv("TOKENROUTER_TEST_ALLOW_SCHEMA_RESET") != "1" {
		t.Fatal("set TOKENROUTER_TEST_ALLOW_SCHEMA_RESET=1 only for an isolated disposable database; this test recreates parity tables")
	}

	previousDB, previousLogDB := DB, LOG_DB
	t.Setenv("SQL_DSN", dsn)
	t.Setenv("SQLITE_PATH", "")
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, InitDB())
	externalDB := DB
	t.Cleanup(func() {
		if sqlDB, err := externalDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		DB, LOG_DB = previousDB, previousLogDB
	})

	assert.Contains(t, []string{"mysql", "postgres"}, DB.Dialector.Name(),
		"the external migration gate must use a real supported server driver")
	for _, entity := range AllModels {
		assert.True(t, DB.Migrator().HasTable(entity), "missing migrated table for %T", entity)
	}

	for _, index := range []struct {
		model any
		name  string
	}{
		{&User{}, "ux_users_verified_email_key"},
		{&Model{}, "uk_model_active_name"},
		{&Model{}, "uk_model_name_delete_at"},
		{&Vendor{}, "uk_vendor_active_name"},
		{&Vendor{}, "uk_vendor_name_delete_at"},
		{&Checkin{}, "idx_user_checkin_date"},
		{&TopUp{}, "idx_top_ups_trade_no"},
		{&TopUp{}, "idx_topups_provider_session_id"},
		{&SubscriptionOrder{}, "idx_subscription_orders_trade_no"},
		{&SubscriptionOrder{}, "idx_subscription_orders_provider_session_id"},
		{&TaskOperation{}, "idx_task_operations_task_id"},
		{&TaskOperation{}, "idx_task_operations_reservation_id"},
		{&TaskOperation{}, "idx_task_operation_recovery"},
		{&UserOAuthBinding{}, "ux_user_provider"},
		{&ExternalIdentityClaim{}, "idx_external_identity_subject"},
		{&ExternalIdentityClaim{}, "idx_external_identity_subject_hash"},
		{&PasskeyCredential{}, "ux_passkey_user"},
		{&RelayQuotaReservationReviewEvent{}, "ux_relay_quota_review_revision"},
	} {
		assert.True(t, DB.Migrator().HasIndex(index.model, index.name), "missing migrated index %s", index.name)
	}
	assertReferenceUserSchema(t, DB)
	exerciseReferenceUserSchemaLegacyExternal(t, DB)
	assertReferenceChannelSchema(t, DB)
	exerciseReferenceChannelSchemaLegacyExternal(t, DB)
	assertReferenceLogSchema(t, DB)
	exerciseReferenceLogSchemaLegacyExternal(t, DB)
	assertReferenceTaskSchema(t, DB)
	exerciseReferenceTaskSchemaLegacyExternal(t, DB)
	assertReferenceSubscriptionPlanSchema(t, DB)
	exerciseReferenceSubscriptionPlanSchemaLegacyExternal(t, DB)
	assertReferenceUserSubscriptionSchema(t, DB)
	exerciseReferenceUserSubscriptionSchemaLegacyExternal(t, DB)
	assertReferenceSubscriptionPreConsumeSchema(t, DB)
	exerciseReferenceSubscriptionPreConsumeSchemaLegacyExternal(t, DB)
	assertReferenceCustomOAuthProviderSchema(t, DB)
	exerciseReferenceCustomOAuthProviderSchemaLegacyExternal(t, DB)
	assertReferenceUserSessionSchema(t, DB)
	exerciseReferenceUserSessionSchemaLegacyExternal(t, DB)
	assertReferenceAlignedCoreSchema(t, DB)
	assertReferenceTokenSchema(t, DB)
	exerciseReferenceTokenSchemaLegacyExternal(t, DB)
	assertReferenceTopUpSchema(t, DB)
	exerciseReferenceTopUpSchemaLegacyExternal(t, DB)
	assertReferenceSubscriptionOrderSchema(t, DB)
	exerciseReferenceSubscriptionOrderSchemaLegacyExternal(t, DB)
	assertReferenceAuthIdentitySchema(t, DB)
	exerciseReferenceSchemaLegacyNarrowingExternal(t, DB)

	// Re-running the complete startup migration against an already-populated
	// schema must be safe for rolling restarts and multi-instance deployments.
	require.NoError(t, migrateDB())
	assert.NoError(t, DB.Session(&gorm.Session{}).Exec("SELECT 1").Error)
}

// TestRelationalLogRetentionExternalDatabaseLifecycle verifies that retention
// uses bounded physical deletion on the real MySQL/PostgreSQL dialects,
// including rows soft-deleted by older builds.
func TestRelationalLogRetentionExternalDatabaseLifecycle(t *testing.T) {
	dsn := os.Getenv("TOKENROUTER_TEST_SQL_DSN")
	if dsn == "" {
		t.Skip("set TOKENROUTER_TEST_SQL_DSN to an isolated MySQL or PostgreSQL database")
	}
	if os.Getenv("TOKENROUTER_TEST_ALLOW_SCHEMA_RESET") != "1" {
		t.Fatal("set TOKENROUTER_TEST_ALLOW_SCHEMA_RESET=1 only for an isolated disposable database")
	}

	previousDB, previousLogDB := DB, LOG_DB
	t.Setenv("SQL_DSN", dsn)
	t.Setenv("SQLITE_PATH", "")
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, InitDB())
	externalDB := DB
	t.Cleanup(func() {
		if sqlDB, err := externalDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		DB, LOG_DB = previousDB, previousLogDB
	})
	require.Contains(t, []string{"mysql", "postgres"}, DB.Dialector.Name())

	// The migration gate intentionally shares one disposable schema across its
	// exact selectors. Reset only the log table so global retention counts remain
	// deterministic regardless of selector registration order.
	require.NoError(t, LOG_DB.Exec("DELETE FROM logs").Error)
	oldLiveOne := Log{CreatedAt: 10, Content: "external-old-live-one"}
	oldTombstone := Log{CreatedAt: 20, Content: "external-old-tombstone"}
	oldLiveTwo := Log{CreatedAt: 30, Content: "external-old-live-two"}
	fresh := Log{CreatedAt: 200, Content: "external-fresh"}
	require.NoError(t, LOG_DB.Create(&[]*Log{&oldLiveOne, &oldTombstone, &oldLiveTwo, &fresh}).Error)
	require.NoError(t, LOG_DB.Delete(&oldTombstone).Error)

	total, err := CountOldLog(context.Background(), 100)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	deleted, err := DeleteOldLogBatch(context.Background(), 100, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted, "the relational cleanup must honor its batch limit")
	total, err = CountOldLog(context.Background(), 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	deleted, err = DeleteOldLogBatch(context.Background(), 100, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	var remainingOld int64
	require.NoError(t, LOG_DB.Unscoped().Model(&Log{}).
		Where("created_at < ?", 100).Count(&remainingOld).Error)
	assert.Zero(t, remainingOld, "retention must physically remove legacy tombstones")
	var remaining int64
	require.NoError(t, LOG_DB.Unscoped().Model(&Log{}).Count(&remaining).Error)
	assert.Equal(t, int64(1), remaining, "the fresh row must remain")
}
