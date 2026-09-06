package store

import (
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
)

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
