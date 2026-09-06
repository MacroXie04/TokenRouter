package store

import (
	"database/sql"
	"errors"
	"fmt"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"strings"
)

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
