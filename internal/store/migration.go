package store

import (
	"errors"
	"gorm.io/gorm"
	"strings"
	"time"
)

// migrateDB runs AutoMigrate for every entity on the primary DB.
func migrateDB() error {
	if err := prepareReferenceSchemaMigration(); err != nil {
		return err
	}
	if err := ensureReferenceTokenModelLimitsText(); err != nil {
		return err
	}
	if err := ensureExternalIdentityClaimStorage(); err != nil {
		return err
	}
	if err := prepareSecuritySchemaMigration(); err != nil {
		return err
	}
	// First discard sessions whose existing identity/lifecycle core is already
	// unusable. Any surviving NULL or missing reference lifecycle state remains
	// ambiguous and must fail closed without being guessed or rewritten.
	if err := prepareReferenceUserSessionSchemaMigration(); err != nil {
		return err
	}
	if err := installExternalIdentityClaimSubjectIndex(); err != nil {
		return err
	}
	// Install the named username constraint before AutoMigrate can replace the
	// legacy unique index, so no supported dialect observes a uniqueness gap.
	if err := ensureReferenceUserIndexes(); err != nil {
		return err
	}
	// Preserve top-up trade-number uniqueness before AutoMigrate replaces the
	// legacy named unique index with the reference's ordinary lookup index.
	if err := ensureReferenceTopUpIndexes(); err != nil {
		return err
	}
	// Preserve subscription-order fulfillment uniqueness before AutoMigrate
	// replaces the legacy named unique index with the reference lookup index.
	if err := ensureReferenceSubscriptionOrderIndexes(); err != nil {
		return err
	}
	if err := DB.AutoMigrate(AllModels...); err != nil {
		return err
	}
	if err := ensureReferenceSubscriptionPlanIntegerTypes(); err != nil {
		return err
	}
	if err := ensureReferenceUserSessionConstraints(); err != nil {
		return err
	}
	if err := ensureReferenceSubscriptionPlanConstraints(); err != nil {
		return err
	}
	if err := ensureReferenceLogIndexes(DB); err != nil {
		return err
	}
	if err := ensureReferenceUserIndexes(); err != nil {
		return err
	}
	if err := ensureReferenceTwoFAIndexes(); err != nil {
		return err
	}
	if err := ensureReferenceTopUpIndexes(); err != nil {
		return err
	}
	if err := ensureReferenceSubscriptionOrderIndexes(); err != nil {
		return err
	}
	if err := ensureReferenceSubscriptionPlanSQLiteDefaults(); err != nil {
		return err
	}
	if err := ensureReferenceSchemaDefaults(); err != nil {
		return err
	}
	if err := ensureChannelTypeCatalog(); err != nil {
		return err
	}
	if err := migrateLogDB(); err != nil {
		return err
	}
	if err := initializeVerifiedEmailKeys(); err != nil {
		return err
	}
	if err := ensureRegistryActiveNames(); err != nil {
		return err
	}
	if err := finalizeExternalIdentityClaimIndexes(); err != nil {
		return err
	}
	return ensurePrefillGroupPartialIndex()
}

// migrateLogDB evolves a separately configured relational log sink. The
// primary database is already migrated through AllModels, while ClickHouse is
// evolved by openClickHouseLog's explicit DDL path.
func migrateLogDB() error {
	if LOG_DB == nil {
		return errors.New("log database is nil")
	}
	if LOG_DB == DB || UsingClickHouseLog() {
		return nil
	}
	if err := prepareReferenceLogSchemaMigration(LOG_DB); err != nil {
		return err
	}
	if err := LOG_DB.AutoMigrate(&Log{}); err != nil {
		return err
	}
	if err := ensureReferenceLogIndexes(LOG_DB); err != nil {
		return err
	}
	return ensureReferenceLogSchemaDefaults(LOG_DB)
}

func ensureRegistryActiveNames() error {
	if err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Model(&Model{}).
			Where("deleted_at IS NOT NULL AND active_name IS NOT NULL").
			Update("active_name", nil).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Model(&Vendor{}).
			Where("deleted_at IS NOT NULL AND active_name IS NOT NULL").
			Update("active_name", nil).Error; err != nil {
			return err
		}
		if err := reconcileDuplicateModelNames(tx); err != nil {
			return err
		}
		if err := reconcileDuplicateVendorNames(tx); err != nil {
			return err
		}
		if err := tx.Model(&Model{}).
			Where("active_name IS NULL OR active_name = ''").
			Update("active_name", gorm.Expr("model_name")).Error; err != nil {
			return err
		}
		return tx.Model(&Vendor{}).
			Where("active_name IS NULL OR active_name = ''").
			Update("active_name", gorm.Expr("name")).Error
	}); err != nil {
		return err
	}
	if !DB.Migrator().HasIndex(&Model{}, "uk_model_active_name") {
		if err := DB.Exec("CREATE UNIQUE INDEX uk_model_active_name ON models (active_name)").Error; err != nil && !DB.Migrator().HasIndex(&Model{}, "uk_model_active_name") {
			return err
		}
	}
	if !DB.Migrator().HasIndex(&Vendor{}, "uk_vendor_active_name") {
		if err := DB.Exec("CREATE UNIQUE INDEX uk_vendor_active_name ON vendors (active_name)").Error; err != nil && !DB.Migrator().HasIndex(&Vendor{}, "uk_vendor_active_name") {
			return err
		}
	}
	return nil
}

func reconcileDuplicateModelNames(tx *gorm.DB) error {
	var names []string
	if err := tx.Unscoped().Model(&Model{}).
		Where("deleted_at IS NULL").
		Group("model_name").
		Having("COUNT(*) > 1").
		Pluck("model_name", &names).Error; err != nil {
		return err
	}
	for _, name := range names {
		var rows []Model
		if err := tx.Where("model_name = ?", name).
			Order("updated_time DESC").Order("id DESC").Find(&rows).Error; err != nil {
			return err
		}
		for index := 1; index < len(rows); index++ {
			deletedAt := time.Now().UTC().Add(-time.Duration(index) * time.Second)
			if err := tx.Unscoped().Model(&Model{}).Where("id = ?", rows[index].Id).
				Updates(map[string]any{"active_name": nil, "deleted_at": deletedAt}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func reconcileDuplicateVendorNames(tx *gorm.DB) error {
	var names []string
	if err := tx.Unscoped().Model(&Vendor{}).
		Where("deleted_at IS NULL").
		Group("name").
		Having("COUNT(*) > 1").
		Pluck("name", &names).Error; err != nil {
		return err
	}
	for _, name := range names {
		var rows []Vendor
		if err := tx.Where("name = ?", name).
			Order("updated_time DESC").Order("id DESC").Find(&rows).Error; err != nil {
			return err
		}
		canonicalID := rows[0].Id
		for index := 1; index < len(rows); index++ {
			if err := tx.Unscoped().Model(&Model{}).Where("vendor_id = ?", rows[index].Id).
				Update("vendor_id", canonicalID).Error; err != nil {
				return err
			}
			deletedAt := time.Now().UTC().Add(-time.Duration(index) * time.Second)
			if err := tx.Unscoped().Model(&Vendor{}).Where("id = ?", rows[index].Id).
				Updates(map[string]any{"active_name": nil, "deleted_at": deletedAt}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func ensurePrefillGroupPartialIndex() error {
	dialect := DB.Dialector.Name()
	if dialect == "mysql" {
		return nil
	}
	var definition string
	switch dialect {
	case "sqlite":
		if err := DB.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?", "uk_prefill_name").Scan(&definition).Error; err != nil {
			return err
		}
	case "postgres":
		if err := DB.Raw("SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?", "uk_prefill_name").Scan(&definition).Error; err != nil {
			return err
		}
	default:
		return nil
	}
	if definition == "" || strings.Contains(strings.ToUpper(definition), "WHERE") {
		return nil
	}
	if err := dropIndexPortable(DB, &PrefillGroup{}, "uk_prefill_name"); err != nil {
		return err
	}
	return DB.Migrator().CreateIndex(&PrefillGroup{}, "uk_prefill_name")
}
