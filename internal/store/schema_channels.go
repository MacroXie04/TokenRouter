package store

import (
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
)

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
