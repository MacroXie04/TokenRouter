package model

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestDatabaseMatrixSQLiteMigration is the executable manifest behind the
// database parity matrix. It guards the complete primary-database migration
// surface (the reference entities plus TokenRouter's durable relay quota
// reservation ledger, audit-log outbox, and append-only operator-review audit) and proves
// that the startup migration is idempotent.
// Field-level differences from the reference remain documented as gaps in
// docs/parity/DATABASE_MATRIX.md; table presence alone is not parity.
func TestDatabaseMatrixSQLiteMigration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "database-matrix.db")), &gorm.Config{})
	require.NoError(t, err)

	previousDB, previousLogDB := DB, LOG_DB
	DB, LOG_DB = db, db
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
	})

	expectedTables := []string{
		"users",
		"tokens",
		"channels",
		"abilities",
		"options",
		"redemptions",
		"logs",
		"audit_log_outboxes",
		"midjourneys",
		"top_ups",
		"quota_data",
		"tasks",
		"jimeng_task_operations",
		"task_operations",
		"models",
		"vendors",
		"prefill_groups",
		"setups",
		"two_fas",
		"two_fa_backup_codes",
		"checkins",
		"subscription_plans",
		"subscription_orders",
		"user_subscriptions",
		"subscription_pre_consume_records",
		"relay_quota_reservations",
		"relay_quota_reservation_review_events",
		"custom_oauth_providers",
		"user_oauth_bindings",
		"perf_metrics",
		"system_instances",
		"system_tasks",
		"system_task_locks",
		"casbin_rule",
		"authz_roles",
		"user_sessions",
		"auth_flows",
		"external_identity_claims",
		"passkey_credentials",
	}
	require.Len(t, AllModels, len(expectedTables), "update the migration manifest test when AllModels changes")

	require.NoError(t, migrateDB())
	for _, table := range expectedTables {
		assert.True(t, db.Migrator().HasTable(table), "missing migrated table %s", table)
	}
	statement := &gorm.Statement{DB: db}
	require.NoError(t, statement.Parse(&AuthFlow{}))
	intentField := statement.Schema.LookUpField("Intent")
	require.NotNil(t, intentField)
	assert.Equal(t, "varchar(128)", intentField.TagSettings["TYPE"],
		"auth-flow intents must fit every supported security-proof scope on strict SQL backends")

	// Named uniqueness and composite-index invariants that are important to
	// authentication, accounting, routing, and idempotency must survive the
	// aggregate migration, not just isolated AutoMigrate calls.
	for _, index := range []struct {
		model any
		name  string
	}{
		{&User{}, "ux_users_verified_email_key"},
		{&Log{}, "ux_logs_audit_event_id"},
		{&AuditLogOutbox{}, "ux_audit_log_outbox_event"},
		{&AuditLogOutbox{}, "idx_audit_log_outbox_due"},
		{&AuditLogOutbox{}, "idx_audit_log_outbox_scrub"},
		{&Ability{}, "idx_abilities_channel_id"},
		{&Model{}, "uk_model_name_delete_at"},
		{&Model{}, "uk_model_active_name"},
		{&Vendor{}, "uk_vendor_name_delete_at"},
		{&Vendor{}, "uk_vendor_active_name"},
		{&PrefillGroup{}, "uk_prefill_name"},
		{&Midjourney{}, "idx_midjourneys_mj_id"},
		{&TaskOperation{}, "idx_task_operations_task_id"},
		{&TaskOperation{}, "idx_task_operations_reservation_id"},
		{&TaskOperation{}, "idx_task_operation_recovery"},
		{&TwoFA{}, "idx_two_fas_user_id"},
		{&Checkin{}, "idx_user_checkin_date"},
		{&TopUp{}, "idx_top_ups_trade_no"},
		{&TopUp{}, "idx_topups_provider_session_id"},
		{&SubscriptionOrder{}, "idx_subscription_orders_trade_no"},
		{&SubscriptionOrder{}, "idx_subscription_orders_provider_session_id"},
		{&UserSubscription{}, "idx_user_sub_active"},
		{&UserOAuthBinding{}, "ux_user_provider"},
		{&UserOAuthBinding{}, "ux_provider_userid"},
		{&CustomOAuthProvider{}, "idx_custom_oauth_providers_slug"},
		{&PerfMetric{}, "idx_perf_model_group_bucket"},
		{&SystemTask{}, "idx_system_tasks_schedule"},
		{&CasbinRule{}, "idx_casbin_rule_unique"},
		{&UserSession{}, "ux_user_sessions_refresh_hash"},
		{&ExternalIdentityClaim{}, "idx_external_identity_subject"},
		{&ExternalIdentityClaim{}, "idx_external_identity_subject_hash"},
		{&ExternalIdentityClaim{}, "idx_external_identity_user"},
		{&PasskeyCredential{}, "ux_passkey_user"},
		{&RelayQuotaReservationReviewEvent{}, "ux_relay_quota_review_revision"},
	} {
		assert.True(t, db.Migrator().HasIndex(index.model, index.name), "missing migrated index %s", index.name)
	}
	assertReferenceSubscriptionPlanSchema(t, db)
	assertReferenceTopUpSchema(t, db)
	assertReferenceSubscriptionOrderSchema(t, db)
	assertReferenceAuthIdentitySchema(t, db)

	// A second startup over the same populated schema must remain safe.
	require.NoError(t, migrateDB())
}
