package store

import (
	"errors"
	"fmt"
	"gorm.io/gorm"
)

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
