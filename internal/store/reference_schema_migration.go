package store

import (
	"errors"
	"fmt"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"strings"
)

const referenceQuotaDimensionLimit = 64

const (
	referenceUserRemarkLimit          = 255
	referenceChannelMySQLBaseURLLimit = 191
	referenceLogMySQLModelNameLimit   = 191
	referenceSubscriptionPriceLimit   = 64
	referenceIntMin                   = int64(-1 << 31)
	referenceIntMax                   = int64(1<<31 - 1)
)

// ErrUnsafeReferenceSchemaMigration reports legacy data that cannot be
// represented by the reference schema without truncation. Startup fails
// closed instead of letting a permissive database shorten persisted values
// while AutoMigrate narrows their columns.
var ErrUnsafeReferenceSchemaMigration = errors.New("unsafe reference schema migration")

type referenceSchemaDefault struct {
	table   string
	column  string
	wanted  string
	literal string
}

var referenceSchemaDefaults = append([]referenceSchemaDefault{
	{table: "users", column: "role", wanted: "1", literal: "1"},
	{table: "users", column: "status", wanted: "1", literal: "1"},
	{table: "users", column: "quota", wanted: "0", literal: "0"},
	{table: "users", column: "used_quota", wanted: "0", literal: "0"},
	{table: "users", column: "request_count", wanted: "0", literal: "0"},
	{table: "users", column: "group", wanted: "default", literal: "'default'"},
	{table: "users", column: "aff_count", wanted: "0", literal: "0"},
	{table: "users", column: "aff_quota", wanted: "0", literal: "0"},
	{table: "users", column: "aff_history_quota", wanted: "0", literal: "0"},
	{table: "users", column: "last_login_at", wanted: "0", literal: "0"},
	{table: "users", column: "auth_version", wanted: "1", literal: "1"},
	{table: "users", column: "quota_reminder_at", wanted: "0", literal: "0"},
	{table: "tokens", column: "status", wanted: "1", literal: "1"},
	{table: "tokens", column: "expired_time", wanted: "-1", literal: "-1"},
	{table: "tokens", column: "remain_quota", wanted: "0", literal: "0"},
	{table: "tokens", column: "allow_ips", wanted: "", literal: "''"},
	{table: "tokens", column: "used_quota", wanted: "0", literal: "0"},
	{table: "tokens", column: "group", wanted: "", literal: "''"},
	{table: "channels", column: "type", wanted: "0", literal: "0"},
	{table: "channels", column: "status", wanted: "1", literal: "1"},
	{table: "channels", column: "weight", wanted: "0", literal: "0"},
	{table: "channels", column: "base_url", wanted: "", literal: "''"},
	{table: "channels", column: "group", wanted: "default", literal: "'default'"},
	{table: "channels", column: "used_quota", wanted: "0", literal: "0"},
	{table: "channels", column: "status_code_mapping", wanted: "", literal: "''"},
	{table: "channels", column: "auto_ban", wanted: "1", literal: "1"},
	{table: "subscription_plans", column: "subtitle", wanted: "", literal: "''"},
	{table: "subscription_plans", column: "price_amount", wanted: "0", literal: "0"},
	{table: "subscription_plans", column: "currency", wanted: "USD", literal: "'USD'"},
	{table: "subscription_plans", column: "duration_unit", wanted: "month", literal: "'month'"},
	{table: "subscription_plans", column: "duration_value", wanted: "1", literal: "1"},
	{table: "subscription_plans", column: "custom_seconds", wanted: "0", literal: "0"},
	{table: "subscription_plans", column: "enabled", wanted: "1", literal: "true"},
	{table: "subscription_plans", column: "sort_order", wanted: "0", literal: "0"},
	{table: "subscription_plans", column: "stripe_price_id", wanted: "", literal: "''"},
	{table: "subscription_plans", column: "creem_product_id", wanted: "", literal: "''"},
	{table: "subscription_plans", column: "waffo_pancake_product_id", wanted: "", literal: "''"},
	{table: "subscription_plans", column: "max_purchase_per_user", wanted: "0", literal: "0"},
	{table: "subscription_plans", column: "upgrade_group", wanted: "", literal: "''"},
	{table: "subscription_plans", column: "downgrade_group", wanted: "", literal: "''"},
	{table: "subscription_plans", column: "total_amount", wanted: "0", literal: "0"},
	{table: "subscription_plans", column: "quota_reset_period", wanted: "never", literal: "'never'"},
	{table: "subscription_plans", column: "quota_reset_custom_seconds", wanted: "0", literal: "0"},
	{table: "top_ups", column: "payment_provider", wanted: "", literal: "''"},
	{table: "subscription_orders", column: "payment_provider", wanted: "", literal: "''"},
	{table: "user_subscriptions", column: "amount_total", wanted: "0", literal: "0"},
	{table: "user_subscriptions", column: "amount_used", wanted: "0", literal: "0"},
	{table: "user_subscriptions", column: "source", wanted: "order", literal: "'order'"},
	{table: "user_subscriptions", column: "last_reset_time", wanted: "0", literal: "0"},
	{table: "user_subscriptions", column: "next_reset_time", wanted: "0", literal: "0"},
	{table: "user_subscriptions", column: "upgrade_group", wanted: "", literal: "''"},
	{table: "user_subscriptions", column: "prev_user_group", wanted: "", literal: "''"},
	{table: "user_subscriptions", column: "downgrade_group", wanted: "", literal: "''"},
	{table: "subscription_pre_consume_records", column: "pre_consumed", wanted: "0", literal: "0"},
	{table: "abilities", column: "priority", wanted: "0", literal: "0"},
	{table: "abilities", column: "weight", wanted: "0", literal: "0"},
	{table: "redemptions", column: "status", wanted: "1", literal: "1"},
	{table: "redemptions", column: "quota", wanted: "100", literal: "100"},
	{table: "quota_data", column: "username", wanted: "", literal: "''"},
	{table: "quota_data", column: "model_name", wanted: "", literal: "''"},
	{table: "quota_data", column: "use_group", wanted: "", literal: "''"},
	{table: "quota_data", column: "token_id", wanted: "0", literal: "0"},
	{table: "quota_data", column: "channel_id", wanted: "0", literal: "0"},
	{table: "quota_data", column: "node_name", wanted: "", literal: "''"},
	{table: "quota_data", column: "token_used", wanted: "0", literal: "0"},
	{table: "quota_data", column: "count", wanted: "0", literal: "0"},
	{table: "quota_data", column: "quota", wanted: "0", literal: "0"},
	{table: "models", column: "status", wanted: "1", literal: "1"},
	{table: "models", column: "sync_official", wanted: "1", literal: "1"},
	{table: "models", column: "name_rule", wanted: "0", literal: "0"},
	{table: "vendors", column: "status", wanted: "1", literal: "1"},
	{table: "two_fas", column: "failed_attempts", wanted: "0", literal: "0"},
	{table: "custom_oauth_providers", column: "icon", wanted: "", literal: "''"},
	{table: "custom_oauth_providers", column: "enabled", wanted: "0", literal: "false"},
	{table: "custom_oauth_providers", column: "scopes", wanted: "openid profile email", literal: "'openid profile email'"},
	{table: "custom_oauth_providers", column: "user_id_field", wanted: "sub", literal: "'sub'"},
	{table: "custom_oauth_providers", column: "username_field", wanted: "preferred_username", literal: "'preferred_username'"},
	{table: "custom_oauth_providers", column: "display_name_field", wanted: "name", literal: "'name'"},
	{table: "custom_oauth_providers", column: "email_field", wanted: "email", literal: "'email'"},
	{table: "custom_oauth_providers", column: "auth_style", wanted: "0", literal: "0"},
	{table: "user_sessions", column: "version", wanted: "1", literal: "1"},
	{table: "user_sessions", column: "previous_valid_until", wanted: "0", literal: "0"},
	{table: "user_sessions", column: "revoked_at", wanted: "0", literal: "0"},
}, referenceLogSchemaDefaults...)

func prepareReferenceSchemaMigration() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if err := prepareReferenceChannelSchemaMigration(); err != nil {
		return err
	}
	if err := prepareReferenceUserSchemaMigration(); err != nil {
		return err
	}
	if err := prepareReferenceSubscriptionPlanSchemaMigration(); err != nil {
		return err
	}
	// UserSession is checked separately after the security preflight removes
	// invalid or duplicate session secrets. Surviving ambiguous state still
	// aborts before AutoMigrate changes any schema.
	if err := prepareReferenceUserSubscriptionSchemaMigration(); err != nil {
		return err
	}
	if err := prepareReferenceSubscriptionPreConsumeSchemaMigration(); err != nil {
		return err
	}
	if err := prepareReferenceLogSchemaMigration(DB); err != nil {
		return err
	}
	if !DB.Migrator().HasTable(&QuotaData{}) {
		return nil
	}
	for _, field := range []struct {
		name   string
		column string
	}{
		{name: "ModelName", column: "model_name"},
		{name: "NodeName", column: "node_name"},
	} {
		if !DB.Migrator().HasColumn(&QuotaData{}, field.name) {
			continue
		}
		needsCheck, err := quotaDimensionMayNeedNarrowing(DB, field.column, referenceQuotaDimensionLimit)
		if err != nil {
			return err
		}
		if !needsCheck {
			continue
		}
		if err := rejectOversizedLegacyQuotaDimension(DB, field.column, referenceQuotaDimensionLimit); err != nil {
			return err
		}
	}
	return nil
}

func referenceColumnDatabaseType(db *gorm.DB, table, column string) (string, error) {
	columns, err := db.Migrator().ColumnTypes(table)
	if err != nil {
		return "", fmt.Errorf("inspect %s.%s database type: %w", table, column, err)
	}
	for _, candidate := range columns {
		if strings.EqualFold(candidate.Name(), column) {
			return strings.ToLower(strings.TrimSpace(candidate.DatabaseTypeName())), nil
		}
	}
	return "", fmt.Errorf("inspect %s.%s database type: column is missing", table, column)
}

func referenceColumnNullable(db *gorm.DB, table, column string) (bool, bool, error) {
	columns, err := db.Migrator().ColumnTypes(table)
	if err != nil {
		return false, false, fmt.Errorf("inspect %s.%s nullability: %w", table, column, err)
	}
	for _, candidate := range columns {
		if strings.EqualFold(candidate.Name(), column) {
			nullable, known := candidate.Nullable()
			return nullable, known, nil
		}
	}
	return false, false, fmt.Errorf("inspect %s.%s nullability: column is missing", table, column)
}

// ensureReferenceSchemaDefaults closes a GORM AutoMigrate portability gap:
// PostgreSQL and MySQL do not consistently add a newly declared default to an
// existing column. The fixed literals below are reference constants, not user
// input. Existing values are untouched.
func ensureReferenceSchemaDefaults() error {
	return ensureReferenceSchemaDefaultsOn(DB, referenceSchemaDefaults)
}

func ensureReferenceLogSchemaDefaults(db *gorm.DB) error {
	return ensureReferenceSchemaDefaultsOn(db, referenceLogSchemaDefaults)
}

func ensureReferenceSchemaDefaultsOn(db *gorm.DB, defaults []referenceSchemaDefault) error {
	if db == nil {
		return errors.New("database is nil")
	}
	if db.Dialector != nil && db.Dialector.Name() == "sqlite" {
		// SQLite's GORM migrator rebuilds legacy tables and carries the model
		// defaults into the replacement DDL.
		return nil
	}
	for _, spec := range defaults {
		matches, err := existingColumnDefaultMatches(db, spec)
		if err != nil {
			return err
		}
		if matches {
			continue
		}
		query := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s",
			quoteReferenceSchemaIdentifier(db, spec.table),
			quoteReferenceSchemaIdentifier(db, spec.column), spec.literal)
		if err := db.Exec(query).Error; err != nil {
			return fmt.Errorf("set reference default for %s.%s: %w", spec.table, spec.column, err)
		}
	}
	return nil
}

func existingColumnDefaultMatches(db *gorm.DB, spec referenceSchemaDefault) (bool, error) {
	columns, err := db.Migrator().ColumnTypes(spec.table)
	if err != nil {
		return false, fmt.Errorf("inspect default for %s.%s: %w", spec.table, spec.column, err)
	}
	for _, column := range columns {
		if !strings.EqualFold(column.Name(), spec.column) {
			continue
		}
		value, ok := column.DefaultValue()
		if !ok {
			return false, nil
		}
		normalized := normalizeReferenceSchemaDefault(value)
		if spec.table == (SubscriptionPlan{}).TableName() && spec.column == "price_amount" {
			actual, actualErr := decimal.NewFromString(normalized)
			wanted, wantedErr := decimal.NewFromString(spec.wanted)
			return actualErr == nil && wantedErr == nil && actual.Equal(wanted), nil
		}
		return normalized == spec.wanted, nil
	}
	return false, fmt.Errorf("inspect default for %s.%s: column is missing", spec.table, spec.column)
}

func quoteReferenceSchemaIdentifier(db *gorm.DB, identifier string) string {
	if db.Dialector != nil && db.Dialector.Name() == "mysql" {
		return "`" + identifier + "`"
	}
	return `"` + identifier + `"`
}

func normalizeReferenceSchemaDefault(value string) string {
	value = strings.TrimSpace(value)
	if cast := strings.Index(value, "::"); cast >= 0 {
		value = value[:cast]
	}
	for len(value) >= 2 && strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")") {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	value = strings.Trim(value, "'\"")
	switch strings.ToLower(value) {
	case "false":
		return "0"
	case "true":
		return "1"
	default:
		return value
	}
}
