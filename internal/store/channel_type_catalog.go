package store

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	channelTypeCatalogOption      = "_tokenrouter_channel_type_catalog"
	channelTypeCatalogEnvironment = "TOKENROUTER_CHANNEL_TYPE_CATALOG"
	channelTypeCatalogCompactV0   = "compact-v0"
	channelTypeCatalogMigratingV0 = "migrating-compact-v0"
	channelTypeCatalogReferenceV1 = "reference-v1"
	firstCompactChannelType       = 28
	lastCompactChannelType        = 57
)

// ensureChannelTypeCatalog makes persisted channel identifiers compatible
// with the reference catalog. Early TokenRouter builds compacted reserved IDs
// 28-30 and 32; silently interpreting one catalog as the other can route a
// secret to the wrong provider. A durable marker makes the conversion exactly
// once. Every nonempty pre-marker database that uses the shifted portion of
// the catalog fails closed until the operator declares its origin with
// TOKENROUTER_CHANNEL_TYPE_CATALOG. Requiring that acknowledgement prevents a
// new node from renumbering shared rows while an old binary is still serving.
func ensureChannelTypeCatalog() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	override := strings.TrimSpace(os.Getenv(channelTypeCatalogEnvironment))
	if override != "" && override != channelTypeCatalogCompactV0 && override != channelTypeCatalogReferenceV1 {
		return fmt.Errorf("%s must be %q or %q", channelTypeCatalogEnvironment, channelTypeCatalogCompactV0, channelTypeCatalogReferenceV1)
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		var marker Option
		lookup := tx.Where(&Option{Key: channelTypeCatalogOption}).Limit(1).Find(&marker)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected == 1 {
			return reconcileMarkedChannelTypeCatalog(tx, marker.Value)
		}

		catalog, err := detectUnmarkedChannelTypeCatalog(tx, override)
		if err != nil {
			return err
		}
		claim := Option{Key: channelTypeCatalogOption, Value: catalog}
		created := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoNothing: true,
		}).Create(&claim)
		if created.Error != nil {
			return created.Error
		}
		// Do not infer ownership from RowsAffected. MySQL can report a
		// duplicate no-op as affected when clientFoundRows is enabled. Reading
		// the durable marker makes both the winner and a concurrent loser take
		// the same idempotent reconciliation path.
		if err := tx.Where(&Option{Key: channelTypeCatalogOption}).First(&marker).Error; err != nil {
			return err
		}
		return reconcileMarkedChannelTypeCatalog(tx, marker.Value)
	})
}

func detectUnmarkedChannelTypeCatalog(tx *gorm.DB, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	var postGapCount int64
	if err := tx.Model(&Channel{}).Where("type >= ?", firstCompactChannelType).Count(&postGapCount).Error; err != nil {
		return "", err
	}
	var legacyJimengTaskCount int64
	if err := tx.Model(&Task{}).Where("platform = ?", "47").Count(&legacyJimengTaskCount).Error; err != nil {
		return "", err
	}
	if postGapCount == 0 && legacyJimengTaskCount == 0 {
		return channelTypeCatalogReferenceV1, nil
	}
	return "", fmt.Errorf(
		"unmarked database uses shifted channel type IDs; stop every old TokenRouter node, then set %s=%s for an early TokenRouter database or %s=%s for reference-compatible data before startup",
		channelTypeCatalogEnvironment, channelTypeCatalogCompactV0,
		channelTypeCatalogEnvironment, channelTypeCatalogReferenceV1,
	)
}

func reconcileMarkedChannelTypeCatalog(tx *gorm.DB, catalog string) error {
	switch catalog {
	case channelTypeCatalogReferenceV1:
		return nil
	case channelTypeCatalogCompactV0:
		claimed := tx.Model(&Option{}).
			Where(&Option{Key: channelTypeCatalogOption, Value: channelTypeCatalogCompactV0}).
			Update("value", channelTypeCatalogMigratingV0)
		if claimed.Error != nil {
			return claimed.Error
		}
		if claimed.RowsAffected == 1 {
			return migrateClaimedCompactChannelTypeCatalog(tx)
		}
		var current Option
		if err := tx.Where(&Option{Key: channelTypeCatalogOption}).First(&current).Error; err != nil {
			return err
		}
		if current.Value == channelTypeCatalogReferenceV1 {
			return nil
		}
		return fmt.Errorf("channel type catalog migration is not available in state %q", current.Value)
	case channelTypeCatalogMigratingV0:
		return errors.New("channel type catalog migration is already in progress")
	default:
		return fmt.Errorf("unsupported channel type catalog marker %q", catalog)
	}
}

func migrateClaimedCompactChannelTypeCatalog(tx *gorm.DB) error {
	// Compact ID 28 maps to reference ID 31. Compact IDs 29-57 map four
	// positions higher because reference IDs 28-30 and 32 are reserved.
	// Early Jimeng Task rows also serialized the provider type as a decimal
	// platform string. ChannelId is a row foreign key and must not be changed.
	if err := tx.Model(&Task{}).Where("platform = ?", "47").Update("platform", "51").Error; err != nil {
		return fmt.Errorf("migrate compact Jimeng task platform: %w", err)
	}
	if err := tx.Model(&Channel{}).
		Where("type BETWEEN ? AND ?", firstCompactChannelType, lastCompactChannelType).
		Update("type", gorm.Expr("CASE WHEN type = ? THEN ? ELSE type + ? END", 28, 31, 4)).Error; err != nil {
		return fmt.Errorf("migrate compact channel type catalog: %w", err)
	}
	updated := tx.Model(&Option{}).
		Where(&Option{Key: channelTypeCatalogOption}).
		Where("value IN ?", []string{channelTypeCatalogCompactV0, channelTypeCatalogMigratingV0}).
		Update("value", channelTypeCatalogReferenceV1)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return errors.New("channel type catalog marker changed during migration")
	}
	return nil
}
