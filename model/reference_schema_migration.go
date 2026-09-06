package model

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
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

var referenceLogSchemaDefaults = []referenceSchemaDefault{
	{table: "logs", column: "username", wanted: "", literal: "''"},
	{table: "logs", column: "token_name", wanted: "", literal: "''"},
	{table: "logs", column: "model_name", wanted: "", literal: "''"},
	{table: "logs", column: "quota", wanted: "0", literal: "0"},
	{table: "logs", column: "prompt_tokens", wanted: "0", literal: "0"},
	{table: "logs", column: "completion_tokens", wanted: "0", literal: "0"},
	{table: "logs", column: "use_time", wanted: "0", literal: "0"},
	{table: "logs", column: "token_id", wanted: "0", literal: "0"},
	{table: "logs", column: "ip", wanted: "", literal: "''"},
	{table: "logs", column: "request_id", wanted: "", literal: "''"},
	{table: "logs", column: "upstream_request_id", wanted: "", literal: "''"},
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

// prepareReferenceSubscriptionPlanSchemaMigration proves every legacy value
// can be represented by the reference-compatible constraints before
// AutoMigrate changes the table. Missing required columns with no safe
// database default, surviving NULL configuration, and server-side INT
// overflow abort startup without guessing or rewriting plan economics.
func prepareReferenceSubscriptionPlanSchemaMigration() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if !DB.Migrator().HasTable(&SubscriptionPlan{}) {
		return nil
	}
	for _, field := range []struct {
		column                string
		missingHasSafeDefault bool
	}{
		{column: "title"},
		{column: "price_amount"},
		{column: "currency", missingHasSafeDefault: true},
		{column: "duration_unit", missingHasSafeDefault: true},
		{column: "duration_value", missingHasSafeDefault: true},
		{column: "custom_seconds", missingHasSafeDefault: true},
		{column: "total_amount", missingHasSafeDefault: true},
	} {
		if !DB.Migrator().HasColumn(&SubscriptionPlan{}, field.column) {
			if field.missingHasSafeDefault {
				continue
			}
			var rows int64
			if err := DB.Table(SubscriptionPlan{}.TableName()).Count(&rows).Error; err != nil {
				return fmt.Errorf("inspect legacy subscription_plans before adding %s: %w", field.column, err)
			}
			if rows != 0 {
				return fmt.Errorf("%w: subscription_plans.%s is missing for %d existing plan rows",
					ErrUnsafeReferenceSchemaMigration, field.column, rows)
			}
			continue
		}
		if err := rejectNullLegacySubscriptionPlanField(DB, field.column); err != nil {
			return err
		}
	}
	normalizeBlankPrices := false
	if DB.Migrator().HasColumn(&SubscriptionPlan{}, "price_amount") {
		var err error
		normalizeBlankPrices, err = validateLegacySubscriptionPlanPrices(DB)
		if err != nil {
			return err
		}
	}
	if DB.Dialector == nil || DB.Dialector.Name() != "sqlite" {
		for _, column := range []string{"duration_value", "sort_order", "max_purchase_per_user"} {
			if !DB.Migrator().HasColumn(&SubscriptionPlan{}, column) {
				continue
			}
			if err := rejectSubscriptionPlanIntOutsideReferenceRange(DB, column); err != nil {
				return err
			}
		}
	}
	if normalizeBlankPrices {
		expression := subscriptionPlanPriceTextExpression(DB)
		result := DB.Table(SubscriptionPlan{}.TableName()).
			Where("TRIM("+expression+") = ''").
			UpdateColumn("price_amount", "0")
		if result.Error != nil {
			return fmt.Errorf("normalize legacy blank subscription_plans.price_amount: %w", result.Error)
		}
	}
	return nil
}

// validateLegacySubscriptionPlanPrices proves a text/float legacy column can
// be changed to DECIMAL(10,6) without rounding, truncation, or overflow. An
// empty legacy string has always meant a free plan in TokenRouter, so callers
// may safely canonicalize it to numeric zero after every row has passed.
func validateLegacySubscriptionPlanPrices(db *gorm.DB) (bool, error) {
	if db == nil {
		return false, errors.New("database is nil")
	}
	expression := subscriptionPlanPriceTextExpression(db)
	oversized, err := db.Table(SubscriptionPlan{}.TableName()).Select("1").
		Where(fmt.Sprintf("LENGTH(%s) > ?", expression), referenceSubscriptionPriceLimit).
		Limit(1).Rows()
	if err != nil {
		return false, fmt.Errorf("inspect legacy subscription_plans.price_amount length: %w", err)
	}
	if oversized.Next() {
		_ = oversized.Close()
		return false, fmt.Errorf("%w: subscription_plans.price_amount exceeds %d bytes",
			ErrUnsafeReferenceSchemaMigration, referenceSubscriptionPriceLimit)
	}
	if err := oversized.Err(); err != nil {
		_ = oversized.Close()
		return false, fmt.Errorf("inspect legacy subscription_plans.price_amount length: %w", err)
	}
	if err := oversized.Close(); err != nil {
		return false, fmt.Errorf("close legacy subscription_plans.price_amount length scan: %w", err)
	}

	rows, err := db.Table(SubscriptionPlan{}.TableName()).
		Select("id, " + expression + " AS price_amount_text").Rows()
	if err != nil {
		return false, fmt.Errorf("inspect legacy subscription_plans.price_amount: %w", err)
	}
	defer rows.Close()
	normalizeBlank := false
	for rows.Next() {
		var (
			id  int
			raw sql.NullString
		)
		if err := rows.Scan(&id, &raw); err != nil {
			return false, fmt.Errorf("scan legacy subscription_plans.price_amount: %w", err)
		}
		if !raw.Valid {
			return false, fmt.Errorf("%w: subscription_plans.price_amount contains NULL required state",
				ErrUnsafeReferenceSchemaMigration)
		}
		blank, err := validateReferenceDecimalPrice(raw.String)
		if err != nil {
			return false, fmt.Errorf("%w: subscription_plans.price_amount for plan %d %v",
				ErrUnsafeReferenceSchemaMigration, id, err)
		}
		normalizeBlank = normalizeBlank || blank
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("inspect legacy subscription_plans.price_amount: %w", err)
	}
	return normalizeBlank, nil
}

func validateReferenceDecimalPrice(raw string) (bool, error) {
	if len(raw) > referenceSubscriptionPriceLimit {
		return false, fmt.Errorf("exceeds %d bytes", referenceSubscriptionPriceLimit)
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return true, nil
	}
	digits := 0
	dotSeen := false
	for i := 0; i < len(value); i++ {
		switch char := value[i]; {
		case char >= '0' && char <= '9':
			digits++
		case char == '.' && !dotSeen:
			dotSeen = true
		case (char == '+' || char == '-') && i == 0:
		default:
			return false, errors.New("is not a plain decimal")
		}
	}
	if digits == 0 {
		return false, errors.New("is not a plain decimal")
	}
	price, err := decimal.NewFromString(value)
	if err != nil {
		return false, errors.New("is not a valid decimal")
	}
	if !price.Equal(price.Truncate(6)) {
		return false, errors.New("has more than six fractional digits")
	}
	limit := decimal.New(9999999999, -6) // DECIMAL(10,6): 9999.999999
	if price.Abs().GreaterThan(limit) {
		return false, errors.New("exceeds DECIMAL(10,6)")
	}
	return false, nil
}

func subscriptionPlanPriceTextExpression(db *gorm.DB) string {
	column := quoteReferenceSchemaIdentifier(db, "price_amount")
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "mysql" {
		return "CAST(" + column + " AS CHAR)"
	}
	return "CAST(" + column + " AS TEXT)"
}

func rejectNullLegacySubscriptionPlanField(db *gorm.DB, column string) error {
	if db == nil {
		return errors.New("database is nil")
	}
	switch column {
	case "title", "price_amount", "currency", "duration_unit", "duration_value", "custom_seconds", "total_amount":
	default:
		return fmt.Errorf("%w: unsupported required subscription_plans column %q",
			ErrUnsafeReferenceSchemaMigration, column)
	}
	quotedColumn := quoteReferenceSchemaIdentifier(db, column)
	rows, err := db.Table(SubscriptionPlan{}.TableName()).Select("1").
		Where(quotedColumn + " IS NULL").Limit(1).Rows()
	if err != nil {
		return fmt.Errorf("inspect legacy subscription_plans.%s: %w", column, err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: subscription_plans.%s contains NULL required state",
			ErrUnsafeReferenceSchemaMigration, column)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect legacy subscription_plans.%s: %w", column, err)
	}
	return nil
}

// ensureReferenceSubscriptionPlanIntegerTypes performs the safe narrowing
// that GORM intentionally skips for existing BIGINT columns. The preflight
// already proved every value is representable as a signed 32-bit integer.
func ensureReferenceSubscriptionPlanIntegerTypes() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if DB.Dialector == nil || DB.Dialector.Name() == "sqlite" ||
		!DB.Migrator().HasTable(&SubscriptionPlan{}) {
		return nil
	}
	for _, field := range []struct {
		name       string
		column     string
		mysqlShape string
	}{
		{name: "DurationValue", column: "duration_value", mysqlShape: "INT NOT NULL DEFAULT 1"},
		{name: "SortOrder", column: "sort_order", mysqlShape: "INT DEFAULT 0"},
		{name: "MaxPurchasePerUser", column: "max_purchase_per_user", mysqlShape: "INT DEFAULT 0"},
	} {
		databaseType, err := referenceColumnDatabaseType(DB, SubscriptionPlan{}.TableName(), field.column)
		if err != nil {
			return err
		}
		if databaseType == "int" || databaseType == "integer" || databaseType == "int4" {
			continue
		}
		var query string
		switch DB.Dialector.Name() {
		case "postgres":
			table := quoteReferenceSchemaIdentifier(DB, SubscriptionPlan{}.TableName())
			column := quoteReferenceSchemaIdentifier(DB, field.column)
			query = fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE INTEGER USING %s::INTEGER",
				table, column, column)
		case "mysql":
			query = fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s",
				quoteReferenceSchemaIdentifier(DB, SubscriptionPlan{}.TableName()),
				quoteReferenceSchemaIdentifier(DB, field.column), field.mysqlShape)
		default:
			return fmt.Errorf("unsupported database %q for SubscriptionPlan integer alignment", DB.Dialector.Name())
		}
		if err := DB.Exec(query).Error; err != nil {
			return fmt.Errorf("narrow subscription_plans.%s to reference INT: %w", field.column, err)
		}
		databaseType, err = referenceColumnDatabaseType(DB, SubscriptionPlan{}.TableName(), field.column)
		if err != nil {
			return err
		}
		if databaseType != "int" && databaseType != "integer" && databaseType != "int4" {
			return fmt.Errorf("verify subscription_plans.%s reference INT type: got %s", field.column, databaseType)
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

// ensureReferenceTokenModelLimitsText explicitly widens the legacy
// varchar(1024) declaration before AutoMigrate. The conversion is lossless for
// character columns, idempotent across rolling starts, and refuses to cast an
// unexpected binary or numeric legacy type.
func ensureReferenceTokenModelLimitsText() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if !DB.Migrator().HasTable(&Token{}) || !DB.Migrator().HasColumn(&Token{}, "ModelLimits") {
		return nil
	}
	if DB.Dialector == nil {
		return errors.New("database dialect is nil")
	}
	dialect := DB.Dialector.Name()
	if dialect == "sqlite" {
		// SQLite character declarations share TEXT affinity; AutoMigrate handles
		// the declaration-level alignment while preserving the table contents.
		return nil
	}
	databaseType, err := referenceColumnDatabaseType(DB, Token{}.TableName(), "model_limits")
	if err != nil {
		return err
	}
	if strings.Contains(databaseType, "text") {
		return nil
	}
	switch databaseType {
	case "varchar", "character varying", "char", "character":
		// Safe widening candidates.
	default:
		return fmt.Errorf("%w: tokens.model_limits has unsupported type %q",
			ErrUnsafeReferenceSchemaMigration, databaseType)
	}

	table := quoteReferenceSchemaIdentifier(DB, Token{}.TableName())
	column := quoteReferenceSchemaIdentifier(DB, "model_limits")
	var query string
	switch dialect {
	case "postgres":
		query = fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE TEXT USING %s::TEXT", table, column, column)
	case "mysql":
		query = fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s TEXT NULL", table, column)
	default:
		return fmt.Errorf("unsupported database %q for tokens.model_limits widening", dialect)
	}
	if err := DB.Exec(query).Error; err != nil {
		return fmt.Errorf("widen tokens.model_limits to text: %w", err)
	}
	databaseType, err = referenceColumnDatabaseType(DB, Token{}.TableName(), "model_limits")
	if err != nil {
		return err
	}
	if !strings.Contains(databaseType, "text") {
		return fmt.Errorf("verify tokens.model_limits text type: got %s", databaseType)
	}
	return nil
}

// ensureReferenceSubscriptionPlanConstraints closes the same AutoMigrate
// portability gap as the user-session postcondition: SQLite can retain a
// legacy nullable declaration when the column already exists. The preflight
// has proved that no row contains ambiguous NULL economic state, so tightening
// only the declaration is lossless and safe on every supported dialect.
func ensureReferenceSubscriptionPlanConstraints() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if !DB.Migrator().HasTable(&SubscriptionPlan{}) {
		return nil
	}
	for _, field := range []struct {
		name   string
		column string
	}{
		{name: "Title", column: "title"},
		{name: "PriceAmount", column: "price_amount"},
		{name: "Currency", column: "currency"},
		{name: "DurationUnit", column: "duration_unit"},
		{name: "DurationValue", column: "duration_value"},
		{name: "CustomSeconds", column: "custom_seconds"},
		{name: "TotalAmount", column: "total_amount"},
	} {
		nullable, known, err := referenceColumnNullable(DB, SubscriptionPlan{}.TableName(), field.column)
		if err != nil {
			return err
		}
		if known && !nullable {
			continue
		}
		if err := DB.Migrator().AlterColumn(&SubscriptionPlan{}, field.name); err != nil {
			return fmt.Errorf("set reference NOT NULL constraint on subscription_plans.%s: %w", field.column, err)
		}
		nullable, known, err = referenceColumnNullable(DB, SubscriptionPlan{}.TableName(), field.column)
		if err != nil {
			return err
		}
		if !known || nullable {
			return fmt.Errorf("verify reference NOT NULL constraint on subscription_plans.%s", field.column)
		}
	}
	return nil
}

func rejectSubscriptionPlanIntOutsideReferenceRange(db *gorm.DB, column string) error {
	if db == nil {
		return errors.New("database is nil")
	}
	switch column {
	case "duration_value", "sort_order", "max_purchase_per_user":
	default:
		return fmt.Errorf("%w: unsupported subscription_plans INT column %q",
			ErrUnsafeReferenceSchemaMigration, column)
	}
	quotedColumn := quoteReferenceSchemaIdentifier(db, column)
	rows, err := db.Table(SubscriptionPlan{}.TableName()).Select("1").
		Where(fmt.Sprintf("%s < ? OR %s > ?", quotedColumn, quotedColumn), referenceIntMin, referenceIntMax).
		Limit(1).Rows()
	if err != nil {
		return fmt.Errorf("inspect legacy subscription_plans.%s range: %w", column, err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: subscription_plans.%s exceeds reference INT range",
			ErrUnsafeReferenceSchemaMigration, column)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect legacy subscription_plans.%s range: %w", column, err)
	}
	return nil
}

// prepareReferenceUserSessionSchemaMigration protects required legacy session
// identity and lifecycle fields before the reference NOT NULL constraints are
// installed. The security preflight has already removed sessions whose
// complete legacy core is invalid. If a required column is absent, or a
// surviving value is still NULL, startup fails instead of guessing state that
// could revive or misclassify a session. Only reference-declared defaults are
// safe when an entire column is absent.
func prepareReferenceUserSessionSchemaMigration() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if !DB.Migrator().HasTable(&UserSession{}) {
		return nil
	}
	for _, field := range []struct {
		column                string
		missingHasSafeDefault bool
	}{
		{column: "sid"},
		{column: "user_id"},
		{column: "version", missingHasSafeDefault: true},
		{column: "user_auth_version"},
		{column: "status"},
		{column: "refresh_hash"},
		{column: "previous_valid_until", missingHasSafeDefault: true},
		{column: "login_method"},
		{column: "last_active_at"},
		{column: "expires_at"},
		{column: "revoked_at", missingHasSafeDefault: true},
	} {
		if !DB.Migrator().HasColumn(&UserSession{}, field.column) {
			if field.missingHasSafeDefault {
				continue
			}
			var rows int64
			if err := DB.Table(UserSession{}.TableName()).Count(&rows).Error; err != nil {
				return fmt.Errorf("inspect legacy user_sessions before adding %s: %w", field.column, err)
			}
			if rows != 0 {
				return fmt.Errorf("%w: user_sessions.%s is missing for %d existing session rows",
					ErrUnsafeReferenceSchemaMigration, field.column, rows)
			}
			continue
		}
		if err := rejectNullLegacyUserSessionField(DB, field.column); err != nil {
			return err
		}
	}
	return nil
}

func rejectNullLegacyUserSessionField(db *gorm.DB, column string) error {
	if db == nil {
		return errors.New("database is nil")
	}
	switch column {
	case "sid", "user_id", "version", "user_auth_version", "status", "refresh_hash",
		"previous_valid_until", "login_method", "last_active_at", "expires_at", "revoked_at":
	default:
		return fmt.Errorf("%w: unsupported required user_sessions column %q",
			ErrUnsafeReferenceSchemaMigration, column)
	}
	quotedColumn := quoteReferenceSchemaIdentifier(db, column)
	rows, err := db.Table(UserSession{}.TableName()).Select("1").
		Where(quotedColumn + " IS NULL").Limit(1).Rows()
	if err != nil {
		return fmt.Errorf("inspect legacy user_sessions.%s: %w", column, err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: user_sessions.%s contains NULL required state",
			ErrUnsafeReferenceSchemaMigration, column)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect legacy user_sessions.%s: %w", column, err)
	}
	return nil
}

// ensureReferenceUserSessionConstraints closes an AutoMigrate portability
// gap: SQLite does not always tighten nullability when a legacy field keeps
// the same declared shape. The preflights above have already proved each
// surviving row is safe; this postcondition verifies every required field.
func ensureReferenceUserSessionConstraints() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if !DB.Migrator().HasTable(&UserSession{}) {
		return nil
	}
	altered := false
	for _, field := range []struct {
		name   string
		column string
	}{
		{name: "SID", column: "sid"},
		{name: "UserID", column: "user_id"},
		{name: "Version", column: "version"},
		{name: "UserAuthVersion", column: "user_auth_version"},
		{name: "Status", column: "status"},
		{name: "RefreshHash", column: "refresh_hash"},
		{name: "PreviousValidUntil", column: "previous_valid_until"},
		{name: "LoginMethod", column: "login_method"},
		{name: "LastActiveAt", column: "last_active_at"},
		{name: "ExpiresAt", column: "expires_at"},
		{name: "RevokedAt", column: "revoked_at"},
	} {
		nullable, known, err := referenceColumnNullable(DB, UserSession{}.TableName(), field.column)
		if err != nil {
			return err
		}
		if known && !nullable {
			continue
		}
		if err := DB.Migrator().AlterColumn(&UserSession{}, field.name); err != nil {
			return fmt.Errorf("set reference NOT NULL constraint on user_sessions.%s: %w", field.column, err)
		}
		altered = true
		nullable, known, err = referenceColumnNullable(DB, UserSession{}.TableName(), field.column)
		if err != nil {
			return err
		}
		if !known || nullable {
			return fmt.Errorf("verify reference NOT NULL constraint on user_sessions.%s", field.column)
		}
	}
	// SQLite rebuilds a table for AlterColumn and does not carry ordinary
	// indexes into the replacement. Re-run this one model's idempotent
	// migration so the reference lookup indexes and refresh-hash uniqueness are
	// restored before startup succeeds.
	if altered {
		if err := DB.AutoMigrate(&UserSession{}); err != nil {
			return fmt.Errorf("restore user_sessions indexes after constraint alignment: %w", err)
		}
	}
	return nil
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

// prepareReferenceUserSubscriptionSchemaMigration rejects legacy NULL quota
// amounts before AutoMigrate adds the reference NOT NULL constraints. A NULL
// financial value has no lossless representation in the reference schema, so
// startup fails closed instead of silently changing it to zero.
func prepareReferenceUserSubscriptionSchemaMigration() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if !DB.Migrator().HasTable(&UserSubscription{}) {
		return nil
	}
	for _, column := range []string{"amount_total", "amount_used"} {
		if !DB.Migrator().HasColumn(&UserSubscription{}, column) {
			continue
		}
		if err := rejectNullLegacyUserSubscriptionAmount(DB, column); err != nil {
			return err
		}
	}
	return nil
}

func rejectNullLegacyUserSubscriptionAmount(db *gorm.DB, column string) error {
	if db == nil {
		return errors.New("database is nil")
	}
	if column != "amount_total" && column != "amount_used" {
		return fmt.Errorf("%w: unsupported user_subscriptions financial column %q",
			ErrUnsafeReferenceSchemaMigration, column)
	}
	quotedColumn := quoteReferenceSchemaIdentifier(db, column)
	rows, err := db.Table(UserSubscription{}.TableName()).Select("1").
		Where(quotedColumn + " IS NULL").Limit(1).Rows()
	if err != nil {
		return fmt.Errorf("inspect legacy user_subscriptions.%s: %w", column, err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: user_subscriptions.%s contains NULL financial state",
			ErrUnsafeReferenceSchemaMigration, column)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect legacy user_subscriptions.%s: %w", column, err)
	}
	return nil
}

// prepareReferenceSubscriptionPreConsumeSchemaMigration rejects an ambiguous
// legacy NULL ledger amount before AutoMigrate adds the reference NOT NULL
// constraint. Zero is a valid accounting value, but it cannot be inferred
// losslessly from NULL, so operators must repair the row explicitly.
func prepareReferenceSubscriptionPreConsumeSchemaMigration() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if !DB.Migrator().HasTable(&SubscriptionPreConsumeRecord{}) ||
		!DB.Migrator().HasColumn(&SubscriptionPreConsumeRecord{}, "PreConsumed") {
		return nil
	}
	rows, err := DB.Table(SubscriptionPreConsumeRecord{}.TableName()).Select("1").
		Where("pre_consumed IS NULL").Limit(1).Rows()
	if err != nil {
		return fmt.Errorf("inspect legacy subscription_pre_consume_records.pre_consumed: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: subscription_pre_consume_records.pre_consumed contains NULL financial state",
			ErrUnsafeReferenceSchemaMigration)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect legacy subscription_pre_consume_records.pre_consumed: %w", err)
	}
	return nil
}

// prepareReferenceChannelSchemaMigration protects the one Channel widening
// set that becomes a narrowing on MySQL: an unbounded legacy TEXT base_url is
// represented as varchar(191) by GORM when the reference empty-string default
// is present. PostgreSQL and SQLite keep this field as unbounded text.
func prepareReferenceChannelSchemaMigration() error {
	if !UsingMySQL() || !DB.Migrator().HasTable(&Channel{}) || !DB.Migrator().HasColumn(&Channel{}, "BaseURL") {
		return nil
	}
	columns, err := DB.Migrator().ColumnTypes(Channel{}.TableName())
	if err != nil {
		return fmt.Errorf("inspect legacy channels schema before alignment: %w", err)
	}
	for _, column := range columns {
		if !strings.EqualFold(column.Name(), "base_url") {
			continue
		}
		if length, known := column.Length(); known && length <= referenceChannelMySQLBaseURLLimit {
			return nil
		}
		databaseType := strings.ToLower(strings.TrimSpace(column.DatabaseTypeName()))
		switch databaseType {
		case "varchar", "char", "tinytext", "text", "mediumtext", "longtext":
			return rejectOversizedLegacyChannelBaseURL(DB)
		default:
			return fmt.Errorf("%w: channels.base_url has unsupported legacy type %q",
				ErrUnsafeReferenceSchemaMigration, databaseType)
		}
	}
	return nil
}

func rejectOversizedLegacyChannelBaseURL(db *gorm.DB) error {
	lengthFunction := "LENGTH"
	if db.Dialector != nil && db.Dialector.Name() == "mysql" {
		lengthFunction = "CHAR_LENGTH"
	}
	rows, err := db.Table(Channel{}.TableName()).Select("1").
		Where(lengthFunction+"(base_url) > ?", referenceChannelMySQLBaseURLLimit).Limit(1).Rows()
	if err != nil {
		return fmt.Errorf("inspect legacy channels.base_url: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: channels.base_url contains a value longer than %d characters",
			ErrUnsafeReferenceSchemaMigration, referenceChannelMySQLBaseURLLimit)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect legacy channels.base_url: %w", err)
	}
	return nil
}

// prepareReferenceLogSchemaMigration protects the only Log alignment that
// can narrow an existing server column. GORM represents the reference's
// indexed, dialect-native string as varchar(191) on MySQL, while older
// TokenRouter releases declared model_name as varchar(255). PostgreSQL widens
// the same field to text and SQLite does not enforce VARCHAR lengths.
func prepareReferenceLogSchemaMigration(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is nil")
	}
	if db.Dialector == nil || db.Dialector.Name() != "mysql" ||
		!db.Migrator().HasTable(&Log{}) || !db.Migrator().HasColumn(&Log{}, "ModelName") {
		return nil
	}
	columns, err := db.Migrator().ColumnTypes(Log{}.TableName())
	if err != nil {
		return fmt.Errorf("inspect legacy logs schema before alignment: %w", err)
	}
	for _, column := range columns {
		if !strings.EqualFold(column.Name(), "model_name") {
			continue
		}
		if length, known := column.Length(); known && length <= referenceLogMySQLModelNameLimit {
			return nil
		}
		databaseType := strings.ToLower(strings.TrimSpace(column.DatabaseTypeName()))
		switch databaseType {
		case "varchar", "char", "tinytext", "text", "mediumtext", "longtext":
			return rejectOversizedLegacyLogModelName(db)
		default:
			return fmt.Errorf("%w: logs.model_name has unsupported legacy type %q",
				ErrUnsafeReferenceSchemaMigration, databaseType)
		}
	}
	return fmt.Errorf("inspect legacy logs schema before alignment: model_name column is missing")
}

func rejectOversizedLegacyLogModelName(db *gorm.DB) error {
	lengthFunction := "LENGTH"
	if db.Dialector != nil && db.Dialector.Name() == "mysql" {
		lengthFunction = "CHAR_LENGTH"
	}
	rows, err := db.Table(Log{}.TableName()).Select("1").
		Where(lengthFunction+"(model_name) > ?", referenceLogMySQLModelNameLimit).Limit(1).Rows()
	if err != nil {
		return fmt.Errorf("inspect legacy logs.model_name: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: logs.model_name contains a value longer than %d characters",
			ErrUnsafeReferenceSchemaMigration, referenceLogMySQLModelNameLimit)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect legacy logs.model_name: %w", err)
	}
	return nil
}

// prepareReferenceUserSchemaMigration checks every target column that the
// reference explicitly bounds before AutoMigrate can issue a narrowing ALTER
// on MySQL or PostgreSQL. SQLite does not enforce either VARCHAR length or the
// INT/BIGINT spelling, so its migration can preserve the same values safely.
func prepareReferenceUserSchemaMigration() error {
	if !DB.Migrator().HasTable(&User{}) || UsingSQLite() {
		return nil
	}
	columns, err := DB.Migrator().ColumnTypes(User{}.TableName())
	if err != nil {
		return fmt.Errorf("inspect legacy users schema before alignment: %w", err)
	}
	integerColumns := map[string]bool{
		"role": true, "status": true, "quota": true, "used_quota": true,
		"request_count": true, "aff_count": true, "aff_quota": true,
		"aff_history_quota": true, "inviter_id": true,
	}
	for _, column := range columns {
		name := strings.ToLower(column.Name())
		if name == "remark" {
			length, known := column.Length()
			if !known || length > referenceUserRemarkLimit {
				if err := rejectOversizedLegacyUserRemark(DB); err != nil {
					return err
				}
			}
			continue
		}
		if !integerColumns[name] {
			continue
		}
		databaseType := strings.ToLower(strings.TrimSpace(column.DatabaseTypeName()))
		if referenceUserIntegerTypeIsAtMost32Bits(databaseType) {
			continue
		}
		if databaseType != "bigint" && databaseType != "int8" {
			return fmt.Errorf("%w: users.%s has unsupported legacy type %q",
				ErrUnsafeReferenceSchemaMigration, name, databaseType)
		}
		if err := rejectOutOfRangeLegacyUserInteger(DB, name); err != nil {
			return err
		}
	}
	return nil
}

func referenceUserIntegerTypeIsAtMost32Bits(databaseType string) bool {
	switch strings.ToLower(strings.TrimSpace(databaseType)) {
	case "tinyint", "int1", "smallint", "int2", "mediumint", "int", "integer", "int4":
		return true
	default:
		return false
	}
}

func rejectOversizedLegacyUserRemark(db *gorm.DB) error {
	lengthFunction := "LENGTH"
	if db.Dialector != nil && db.Dialector.Name() == "mysql" {
		lengthFunction = "CHAR_LENGTH"
	}
	rows, err := db.Table(User{}.TableName()).Select("1").
		Where(lengthFunction+"(remark) > ?", referenceUserRemarkLimit).Limit(1).Rows()
	if err != nil {
		return fmt.Errorf("inspect legacy users.remark: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: users.remark contains a value longer than %d characters",
			ErrUnsafeReferenceSchemaMigration, referenceUserRemarkLimit)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect legacy users.remark: %w", err)
	}
	return nil
}

func rejectOutOfRangeLegacyUserInteger(db *gorm.DB, column string) error {
	predicate := fmt.Sprintf("%s < ? OR %s > ?", column, column)
	rows, err := db.Table(User{}.TableName()).Select("1").
		Where(predicate, referenceIntMin, referenceIntMax).Limit(1).Rows()
	if err != nil {
		return fmt.Errorf("inspect legacy users.%s: %w", column, err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: users.%s contains a value outside the signed INT range",
			ErrUnsafeReferenceSchemaMigration, column)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect legacy users.%s: %w", column, err)
	}
	return nil
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

type referenceLogIndex struct {
	name    string
	columns []string
}

var referenceLogIndexes = []referenceLogIndex{
	{name: "idx_logs_user_id", columns: []string{"user_id"}},
	{name: "idx_user_id_id", columns: []string{"user_id", "id"}},
	{name: "idx_created_at_id", columns: []string{"created_at", "id"}},
	{name: "idx_created_at_type", columns: []string{"created_at", "type"}},
	{name: "idx_logs_username", columns: []string{"username"}},
	{name: "index_username_model_name", columns: []string{"model_name", "username"}},
	{name: "idx_logs_token_name", columns: []string{"token_name"}},
	{name: "idx_logs_model_name", columns: []string{"model_name"}},
	{name: "idx_logs_channel_id", columns: []string{"channel_id"}},
	{name: "idx_logs_token_id", columns: []string{"token_id"}},
	{name: "idx_logs_group", columns: []string{"group"}},
	{name: "idx_logs_ip", columns: []string{"ip"}},
	{name: "idx_logs_request_id", columns: []string{"request_id"}},
	{name: "idx_logs_upstream_request_id", columns: []string{"upstream_request_id"}},
}

// ensureReferenceLogIndexes repairs legacy same-name indexes whose column
// list or order GORM's AutoMigrate does not update. A temporary equivalent
// index stays in place while each ordinary lookup index is replaced, avoiding
// an uncovered query path if creating the final index fails midway.
func ensureReferenceLogIndexes(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is nil")
	}
	if db.Dialector != nil && db.Dialector.Name() == "clickhouse" {
		return nil
	}
	if !db.Migrator().HasTable(&Log{}) {
		return nil
	}
	states, err := inspectReferenceLogIndexes(db)
	if err != nil {
		return err
	}
	for _, wanted := range referenceLogIndexes {
		state, exists := states[strings.ToLower(wanted.name)]
		_, temporaryExists := states[strings.ToLower("tmp_reference_"+wanted.name)]
		if exists && referenceLogIndexColumnsEqual(state.columns, wanted.columns) {
			if state.uniqueKnown && state.unique {
				return fmt.Errorf("%w: logs index %s is unexpectedly unique", ErrUnsafeReferenceSchemaMigration, wanted.name)
			}
			if temporaryExists {
				if err := cleanupReferenceLogTemporaryIndex(db, wanted); err != nil {
					return err
				}
			}
			continue
		}
		if exists && state.uniqueKnown && state.unique {
			return fmt.Errorf("%w: logs index %s is unexpectedly unique", ErrUnsafeReferenceSchemaMigration, wanted.name)
		}
		if !exists {
			if err := db.Migrator().CreateIndex(&Log{}, wanted.name); err != nil {
				return fmt.Errorf("create reference Log index %s: %w", wanted.name, err)
			}
			if err := verifyReferenceLogIndex(db, wanted); err != nil {
				return err
			}
			if temporaryExists {
				if err := cleanupReferenceLogTemporaryIndex(db, wanted); err != nil {
					return err
				}
			}
			continue
		}
		if err := replaceReferenceLogIndex(db, wanted); err != nil {
			return err
		}
	}
	return nil
}

func replaceReferenceLogIndex(db *gorm.DB, wanted referenceLogIndex) error {
	temporaryName := "tmp_reference_" + wanted.name
	temporaryColumns, temporaryExists, temporaryUnique, temporaryUniqueKnown, err := inspectReferenceLogIndex(db, temporaryName)
	if err != nil {
		return err
	}
	if temporaryExists && (!referenceLogIndexColumnsEqual(temporaryColumns, wanted.columns) || (temporaryUniqueKnown && temporaryUnique)) {
		return fmt.Errorf("%w: temporary logs index %s has an unexpected definition",
			ErrUnsafeReferenceSchemaMigration, temporaryName)
	}
	if !temporaryExists {
		quotedColumns := make([]string, 0, len(wanted.columns))
		for _, column := range wanted.columns {
			quotedColumns = append(quotedColumns, quoteReferenceSchemaIdentifier(db, column))
		}
		query := fmt.Sprintf("CREATE INDEX %s ON %s (%s)",
			quoteReferenceSchemaIdentifier(db, temporaryName),
			quoteReferenceSchemaIdentifier(db, Log{}.TableName()),
			strings.Join(quotedColumns, ", "))
		if err := db.Exec(query).Error; err != nil {
			return fmt.Errorf("create temporary Log index %s: %w", temporaryName, err)
		}
	}
	if err := dropIndexPortable(db, &Log{}, wanted.name); err != nil {
		return fmt.Errorf("drop legacy Log index %s: %w", wanted.name, err)
	}
	if err := db.Migrator().CreateIndex(&Log{}, wanted.name); err != nil {
		return fmt.Errorf("create reference Log index %s (temporary index retained): %w", wanted.name, err)
	}
	if err := verifyReferenceLogIndex(db, wanted); err != nil {
		return err
	}
	return cleanupReferenceLogTemporaryIndex(db, wanted)
}

func verifyReferenceLogIndex(db *gorm.DB, wanted referenceLogIndex) error {
	columns, exists, unique, uniqueKnown, err := inspectReferenceLogIndex(db, wanted.name)
	if err != nil {
		return err
	}
	if !exists || !referenceLogIndexColumnsEqual(columns, wanted.columns) || (uniqueKnown && unique) {
		return fmt.Errorf("create reference Log index %s: migrated definition did not match", wanted.name)
	}
	return nil
}

func cleanupReferenceLogTemporaryIndex(db *gorm.DB, wanted referenceLogIndex) error {
	temporaryName := "tmp_reference_" + wanted.name
	columns, exists, unique, uniqueKnown, err := inspectReferenceLogIndex(db, temporaryName)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if !referenceLogIndexColumnsEqual(columns, wanted.columns) || (uniqueKnown && unique) {
		return fmt.Errorf("%w: temporary logs index %s has an unexpected definition",
			ErrUnsafeReferenceSchemaMigration, temporaryName)
	}
	if err := dropIndexPortable(db, &Log{}, temporaryName); err != nil {
		return fmt.Errorf("drop temporary Log index %s: %w", temporaryName, err)
	}
	return nil
}

// dropIndexPortable avoids a PostgreSQL GORM migrator bug that renders a
// function call as an identifier (DROP INDEX "CURRENT_SCHEMA"."name"). An
// unqualified, quoted index name correctly resolves through the active search
// path and works for both the default and operator-selected schemas.
func dropIndexPortable(db *gorm.DB, value any, indexName string) error {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres" {
		return db.Exec(postgresDropIndexStatement(db, indexName)).Error
	}
	return db.Migrator().DropIndex(value, indexName)
}

func postgresDropIndexStatement(db *gorm.DB, indexName string) string {
	return "DROP INDEX IF EXISTS " + quoteReferenceSchemaIdentifier(db, indexName)
}

type referenceLogIndexState struct {
	columns     []string
	unique      bool
	uniqueKnown bool
}

func inspectReferenceLogIndexes(db *gorm.DB) (map[string]referenceLogIndexState, error) {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres" {
		return inspectPostgresReferenceIndexes(db, Log{}.TableName())
	}
	indexes, err := db.Migrator().GetIndexes(&Log{})
	if err != nil {
		return nil, fmt.Errorf("inspect Log indexes: %w", err)
	}
	states := make(map[string]referenceLogIndexState, len(indexes))
	for _, index := range indexes {
		unique, uniqueKnown := index.Unique()
		states[strings.ToLower(index.Name())] = referenceLogIndexState{
			columns: index.Columns(), unique: unique, uniqueKnown: uniqueKnown,
		}
	}
	return states, nil
}

// PostgreSQL's GORM GetIndexes query joins pg_attribute through ANY(indkey)
// without ordinality, so composite columns are returned in physical table
// order instead of index order. Query the catalog with ordinality because
// ordering is part of the reference index contract and otherwise causes an
// endless drop/recreate cycle on every startup.
func inspectPostgresReferenceIndexes(db *gorm.DB, table string) (map[string]referenceLogIndexState, error) {
	var rows []struct {
		Name    string `gorm:"column:index_name"`
		Unique  bool   `gorm:"column:is_unique"`
		Columns string `gorm:"column:columns"`
	}
	err := db.Raw(`
		SELECT ci.relname AS index_name,
		       i.indisunique AS is_unique,
		       string_agg(a.attname, ',' ORDER BY keys.ordinality) AS columns
		FROM pg_index AS i
		JOIN pg_class AS ct ON ct.oid = i.indrelid
		JOIN pg_namespace AS ns ON ns.oid = ct.relnamespace
		JOIN pg_class AS ci ON ci.oid = i.indexrelid
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS keys(attnum, ordinality) ON true
		JOIN pg_attribute AS a ON a.attrelid = ct.oid AND a.attnum = keys.attnum
		LEFT JOIN pg_constraint AS con ON con.conindid = i.indexrelid
		WHERE ns.nspname = current_schema()
		  AND ct.relkind = 'r'
		  AND ct.relname = ?
		  AND con.oid IS NULL
		GROUP BY ci.relname, i.indisunique`, table).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("inspect PostgreSQL %s indexes: %w", table, err)
	}
	states := make(map[string]referenceLogIndexState, len(rows))
	for _, row := range rows {
		columns := []string{}
		if row.Columns != "" {
			columns = strings.Split(row.Columns, ",")
		}
		states[strings.ToLower(row.Name)] = referenceLogIndexState{
			columns: columns, unique: row.Unique, uniqueKnown: true,
		}
	}
	return states, nil
}

func inspectReferenceLogIndex(db *gorm.DB, name string) (columns []string, exists, unique, uniqueKnown bool, err error) {
	indexes, err := inspectReferenceLogIndexes(db)
	if err != nil {
		return nil, false, false, false, err
	}
	index, exists := indexes[strings.ToLower(name)]
	if !exists {
		return nil, false, false, false, nil
	}
	return index.columns, true, index.unique, index.uniqueKnown, nil
}

func referenceLogIndexColumnsEqual(actual, wanted []string) bool {
	if len(actual) != len(wanted) {
		return false
	}
	for i := range actual {
		if !strings.EqualFold(actual[i], wanted[i]) {
			return false
		}
	}
	return true
}

// ensureReferenceUserIndexes preserves username uniqueness while replacing
// the former single unique index with the reference's unique column
// constraint plus a separate ordinary lookup index.
func ensureReferenceUserIndexes() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if !DB.Migrator().HasTable(&User{}) {
		return nil
	}
	const (
		constraintName = "uni_users_username"
		indexName      = "idx_users_username"
	)
	if !DB.Migrator().HasConstraint(&User{}, constraintName) {
		if err := DB.Migrator().CreateConstraint(&User{}, constraintName); err != nil {
			return fmt.Errorf("create reference User username constraint: %w", err)
		}
	}
	indexes, err := DB.Migrator().GetIndexes(&User{})
	if err != nil {
		return fmt.Errorf("inspect User indexes: %w", err)
	}
	for _, index := range indexes {
		if index.Name() != indexName {
			continue
		}
		unique, known := index.Unique()
		if !known || !unique {
			return nil
		}
		if err := dropIndexPortable(DB, &User{}, indexName); err != nil {
			return fmt.Errorf("replace legacy User username index: %w", err)
		}
		break
	}
	if !DB.Migrator().HasIndex(&User{}, indexName) {
		if err := DB.Migrator().CreateIndex(&User{}, indexName); err != nil {
			return fmt.Errorf("create reference User username lookup index: %w", err)
		}
	}
	return nil
}

// ensureReferenceTwoFAIndexes upgrades the former single unique index into
// the reference shape: a unique column constraint plus a separate ordinary
// lookup index. The unique constraint is installed before a legacy unique
// index is replaced, so user ownership is never left unenforced.
func ensureReferenceTwoFAIndexes() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if !DB.Migrator().HasTable(&TwoFA{}) {
		return nil
	}
	const (
		constraintName = "uni_two_fas_user_id"
		indexName      = "idx_two_fas_user_id"
	)
	if !DB.Migrator().HasConstraint(&TwoFA{}, constraintName) {
		if err := DB.Migrator().CreateConstraint(&TwoFA{}, constraintName); err != nil {
			return fmt.Errorf("create reference TwoFA user constraint: %w", err)
		}
	}
	indexes, err := DB.Migrator().GetIndexes(&TwoFA{})
	if err != nil {
		return fmt.Errorf("inspect TwoFA indexes: %w", err)
	}
	for _, index := range indexes {
		if index.Name() != indexName {
			continue
		}
		unique, known := index.Unique()
		if !known {
			return fmt.Errorf("inspect TwoFA index %s uniqueness", indexName)
		}
		if !unique {
			break
		}
		if err := dropIndexPortable(DB, &TwoFA{}, indexName); err != nil {
			return fmt.Errorf("replace legacy TwoFA unique index: %w", err)
		}
		break
	}
	if !DB.Migrator().HasIndex(&TwoFA{}, indexName) {
		if err := DB.Migrator().CreateIndex(&TwoFA{}, indexName); err != nil {
			return fmt.Errorf("create reference TwoFA lookup index: %w", err)
		}
	}
	// Earlier target builds added a composite uniqueness key that is absent
	// from the reference and rejects otherwise valid duplicate recovery-code
	// rows. Dropping only that index is data-preserving; the two reference
	// lookup indexes remain managed by AutoMigrate.
	const legacyBackupCodeIndex = "ux_two_fa_backup_user_code"
	if DB.Migrator().HasTable(&TwoFABackupCode{}) &&
		DB.Migrator().HasIndex(&TwoFABackupCode{}, legacyBackupCodeIndex) {
		if err := dropIndexPortable(DB, &TwoFABackupCode{}, legacyBackupCodeIndex); err != nil {
			return fmt.Errorf("remove target-only TwoFA backup-code uniqueness: %w", err)
		}
	}
	return nil
}

// ensureReferenceTopUpIndexes preserves payment idempotency while upgrading
// the former single named unique index to the reference shape: a unique
// column constraint plus a separate ordinary lookup index. The constraint is
// installed first, so no supported migration path observes an unprotected
// trade number while the legacy index is replaced.
func ensureReferenceTopUpIndexes() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if !DB.Migrator().HasTable(&TopUp{}) {
		return nil
	}
	const (
		constraintName = "uni_top_ups_trade_no"
		indexName      = "idx_top_ups_trade_no"
	)
	if !DB.Migrator().HasConstraint(&TopUp{}, constraintName) {
		if err := DB.Migrator().CreateConstraint(&TopUp{}, constraintName); err != nil {
			return fmt.Errorf("create reference TopUp trade-number constraint: %w", err)
		}
	}
	indexes, err := DB.Migrator().GetIndexes(&TopUp{})
	if err != nil {
		return fmt.Errorf("inspect TopUp indexes: %w", err)
	}
	for _, index := range indexes {
		if index.Name() != indexName {
			continue
		}
		unique, known := index.Unique()
		if !known {
			return fmt.Errorf("inspect TopUp index %s uniqueness", indexName)
		}
		if !unique {
			return nil
		}
		if err := dropIndexPortable(DB, &TopUp{}, indexName); err != nil {
			return fmt.Errorf("replace legacy TopUp trade-number index: %w", err)
		}
		break
	}
	if !DB.Migrator().HasIndex(&TopUp{}, indexName) {
		if err := DB.Migrator().CreateIndex(&TopUp{}, indexName); err != nil {
			return fmt.Errorf("create reference TopUp trade-number lookup index: %w", err)
		}
	}
	return nil
}

// ensureReferenceSubscriptionOrderIndexes preserves fulfillment idempotency
// while upgrading the former single named unique index to the reference
// shape: a unique column constraint plus a separate ordinary lookup index.
// The constraint is installed first, so no supported migration path observes
// an unprotected trade number while the legacy index is replaced.
func ensureReferenceSubscriptionOrderIndexes() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if !DB.Migrator().HasTable(&SubscriptionOrder{}) {
		return nil
	}
	const (
		constraintName = "uni_subscription_orders_trade_no"
		indexName      = "idx_subscription_orders_trade_no"
	)
	if !DB.Migrator().HasConstraint(&SubscriptionOrder{}, constraintName) {
		if err := DB.Migrator().CreateConstraint(&SubscriptionOrder{}, constraintName); err != nil {
			return fmt.Errorf("create reference SubscriptionOrder trade-number constraint: %w", err)
		}
	}
	indexes, err := DB.Migrator().GetIndexes(&SubscriptionOrder{})
	if err != nil {
		return fmt.Errorf("inspect SubscriptionOrder indexes: %w", err)
	}
	for _, index := range indexes {
		if index.Name() != indexName {
			continue
		}
		unique, known := index.Unique()
		if !known {
			return fmt.Errorf("inspect SubscriptionOrder index %s uniqueness", indexName)
		}
		if !unique {
			return nil
		}
		if err := dropIndexPortable(DB, &SubscriptionOrder{}, indexName); err != nil {
			return fmt.Errorf("replace legacy SubscriptionOrder trade-number index: %w", err)
		}
		break
	}
	if !DB.Migrator().HasIndex(&SubscriptionOrder{}, indexName) {
		if err := DB.Migrator().CreateIndex(&SubscriptionOrder{}, indexName); err != nil {
			return fmt.Errorf("create reference SubscriptionOrder trade-number lookup index: %w", err)
		}
	}
	return nil
}

// referenceSubscriptionPlanSQLiteDefaults mirrors the three defaults that
// the pinned reference installs only through its hand-built SQLite DDL. They
// intentionally remain absent from SubscriptionPlan's GORM tags: on server
// databases the two pointer fields have no default, while an Enabled tag
// would turn an explicitly created false value into true.
type referenceSubscriptionPlanSQLiteDefaults struct {
	Enabled             bool  `gorm:"default:true"`
	AllowBalancePay     *bool `gorm:"default:true"`
	AllowWalletOverflow *bool `gorm:"default:true"`
}

func (referenceSubscriptionPlanSQLiteDefaults) TableName() string { return "subscription_plans" }

func ensureReferenceSubscriptionPlanSQLiteDefaults() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	if DB.Dialector == nil || DB.Dialector.Name() != "sqlite" ||
		!DB.Migrator().HasTable(&SubscriptionPlan{}) {
		return nil
	}
	for _, field := range []struct {
		name   string
		column string
	}{
		{name: "Enabled", column: "enabled"},
		{name: "AllowBalancePay", column: "allow_balance_pay"},
		{name: "AllowWalletOverflow", column: "allow_wallet_overflow"},
	} {
		matches, err := existingColumnDefaultMatches(DB, referenceSchemaDefault{
			table: SubscriptionPlan{}.TableName(), column: field.column, wanted: "1",
		})
		if err != nil {
			return err
		}
		if matches {
			continue
		}
		if err := DB.Migrator().AlterColumn(&referenceSubscriptionPlanSQLiteDefaults{}, field.name); err != nil {
			return fmt.Errorf("set reference SQLite default for subscription_plans.%s: %w", field.column, err)
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

func quotaDimensionMayNeedNarrowing(db *gorm.DB, column string, limit int) (bool, error) {
	// SQLite does not enforce VARCHAR lengths, so AutoMigrate cannot truncate
	// this value even when the declared size changes.
	if db.Dialector != nil && db.Dialector.Name() == "sqlite" {
		return false, nil
	}
	columns, err := db.Migrator().ColumnTypes(QuotaData{}.TableName())
	if err != nil {
		return false, fmt.Errorf("inspect quota_data schema before narrowing %s: %w", column, err)
	}
	for _, columnType := range columns {
		if columnType.Name() != column {
			continue
		}
		if length, known := columnType.Length(); known {
			return length > int64(limit), nil
		}
		// Unknown-length server columns (for example TEXT) still require the
		// data preflight before conversion.
		return true, nil
	}
	return false, fmt.Errorf("inspect quota_data schema before narrowing: column %s is missing", column)
}

func rejectOversizedLegacyQuotaDimension(db *gorm.DB, column string, limit int) error {
	lengthFunction := "LENGTH"
	if db.Dialector != nil && db.Dialector.Name() == "mysql" {
		// LENGTH counts bytes in MySQL; VARCHAR limits characters. CHAR_LENGTH
		// matches PostgreSQL and SQLite LENGTH semantics for this migration.
		lengthFunction = "CHAR_LENGTH"
	}
	predicate := fmt.Sprintf("%s(%s) > ?", lengthFunction, column)
	rows, err := db.Table(QuotaData{}.TableName()).Select("1").Where(predicate, limit).Limit(1).Rows()
	if err != nil {
		return fmt.Errorf("inspect legacy quota dimension %s: %w", column, err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: quota_data.%s contains a value longer than %d characters", ErrUnsafeReferenceSchemaMigration, column, limit)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect legacy quota dimension %s: %w", column, err)
	}
	return nil
}
