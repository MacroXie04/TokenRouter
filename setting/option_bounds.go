package setting

import (
	"database/sql"
	"errors"
	"fmt"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

// These limits are the authoritative storage and publication boundary for
// database-backed settings. HTTP handlers may apply stricter, field-specific
// limits, but every writer and hot-reload path passes through this layer.
const (
	MaxOptionKeyBytes       = 128
	MaxOptionValueBytes     = 1 << 20
	MaxOptionRows           = 4096
	MaxOptionAggregateBytes = 8 << 20
)

var ErrOptionSnapshotTooLarge = errors.New("system option snapshot exceeds the safety limit")

type optionStorageBounds struct {
	RowCount      int64 `gorm:"column:row_count"`
	MaxKeyBytes   int64 `gorm:"column:max_key_bytes"`
	MaxValueBytes int64 `gorm:"column:max_value_bytes"`
	TotalBytes    int64 `gorm:"column:total_bytes"`
}

// ValidateOptionEntry enforces the shared key/value envelope without
// interpreting a setting's contents. Any valid UTF-8 remains representable so
// field-specific parsers can retain their established validation, fallback,
// and sanitization behavior for JSON, HTML, and multiline text settings.
func ValidateOptionEntry(key, value string) error {
	if key == "" || len(key) > MaxOptionKeyBytes || !utf8.ValidString(key) {
		return fmt.Errorf("invalid system option key")
	}
	if len(value) > MaxOptionValueBytes || !utf8.ValidString(value) {
		return fmt.Errorf("invalid value for system option %q", key)
	}
	return nil
}

func validateOptionSnapshot(options map[string]string) error {
	if len(options) > MaxOptionRows {
		return fmt.Errorf("%w: more than %d rows", ErrOptionSnapshotTooLarge, MaxOptionRows)
	}
	total := int64(0)
	for key, value := range options {
		if err := ValidateOptionEntry(key, value); err != nil {
			return err
		}
		entryBytes := int64(len(key)) + int64(len(value))
		if entryBytes > MaxOptionAggregateBytes || total > int64(MaxOptionAggregateBytes)-entryBytes {
			return fmt.Errorf("%w: more than %d aggregate bytes", ErrOptionSnapshotTooLarge, MaxOptionAggregateBytes)
		}
		total += entryBytes
	}
	return nil
}

// loadBoundedOptionSnapshot performs a metadata-only preflight before asking
// the driver to materialize TEXT values. This prevents one oversized row, an
// excessive row count, or an excessive aggregate from being loaded into the
// live process merely because the database was modified out of band.
func loadBoundedOptionSnapshot(db *gorm.DB) (map[string]string, error) {
	if db == nil || db.Dialector == nil {
		return nil, errors.New("system option database is unavailable")
	}
	var loaded map[string]string
	err := db.Transaction(func(tx *gorm.DB) error {
		var loadErr error
		loaded, loadErr = loadBoundedOptionSnapshotTransaction(tx)
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	return loaded, nil
}

func loadBoundedOptionSnapshotTransaction(db *gorm.DB) (map[string]string, error) {
	keyLength, valueLength, err := optionByteLengthExpressions(db.Dialector.Name())
	if err != nil {
		return nil, err
	}
	keyBytes := "COALESCE(" + keyLength + ", 0)"
	valueBytes := "COALESCE(" + valueLength + ", 0)"
	selection := fmt.Sprintf(
		"COUNT(*) AS row_count, COALESCE(MAX(%s), 0) AS max_key_bytes, "+
			"COALESCE(MAX(%s), 0) AS max_value_bytes, COALESCE(SUM(%s + %s), 0) AS total_bytes",
		keyBytes, valueBytes, keyBytes, valueBytes,
	)
	var bounds optionStorageBounds
	if err := db.Model(&model.Option{}).Select(selection).Scan(&bounds).Error; err != nil {
		return nil, fmt.Errorf("preflight system options: %w", err)
	}
	if bounds.RowCount < 0 || bounds.RowCount > MaxOptionRows ||
		bounds.MaxKeyBytes < 0 || bounds.MaxKeyBytes > MaxOptionKeyBytes ||
		bounds.MaxValueBytes < 0 || bounds.MaxValueBytes > MaxOptionValueBytes ||
		bounds.TotalBytes < 0 || bounds.TotalBytes > MaxOptionAggregateBytes {
		return nil, fmt.Errorf("%w: rows=%d key_bytes=%d value_bytes=%d total_bytes=%d",
			ErrOptionSnapshotTooLarge, bounds.RowCount, bounds.MaxKeyBytes, bounds.MaxValueBytes, bounds.TotalBytes)
	}

	var stored []model.Option
	if err := db.Order("key asc").Limit(MaxOptionRows + 1).Find(&stored).Error; err != nil {
		return nil, fmt.Errorf("load system options: %w", err)
	}
	loaded := make(map[string]string, len(stored))
	for _, option := range stored {
		if _, duplicate := loaded[option.Key]; duplicate {
			return nil, fmt.Errorf("duplicate system option key %q", option.Key)
		}
		loaded[option.Key] = option.Value
	}
	if err := validateOptionSnapshot(loaded); err != nil {
		return nil, err
	}
	return loaded, nil
}

func optionByteLengthExpressions(dialect string) (key, value string, err error) {
	switch dialect {
	case "sqlite":
		return `length(CAST("key" AS BLOB))`, `length(CAST("value" AS BLOB))`, nil
	case "mysql":
		return "octet_length(`key`)", "octet_length(`value`)", nil
	case "postgres":
		return `octet_length("key")`, `octet_length("value")`, nil
	default:
		return "", "", fmt.Errorf("unsupported system option database dialect %q", dialect)
	}
}
