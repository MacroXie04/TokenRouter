package store

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

// ErrAmbiguousPersistentIdentity is returned when an automatic migration
// cannot safely choose an owner or a financial record for a duplicated key.
// Operators must reconcile those rows explicitly before startup continues.
var ErrAmbiguousPersistentIdentity = errors.New("ambiguous persistent identity or accounting key")

// ErrInvalidPersistentIdentifier rejects new rows whose security or
// idempotency identifier would be unusable even though SQL NOT NULL permits an
// empty Go string.
var ErrInvalidPersistentIdentifier = errors.New("invalid persistent identifier")

// prepareSecuritySchemaMigration repairs only legacy state for which there is
// a fail-closed, deterministic answer. It deliberately runs before
// AutoMigrate, because AutoMigrate creates the new unique indexes.
func prepareSecuritySchemaMigration() error {
	if DB == nil {
		return errors.New("database is nil")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := prepareBuiltInExternalIdentityOwnership(tx); err != nil {
			return err
		}
		if err := removeDeletedPasskeyCredentials(tx); err != nil {
			return err
		}
		if err := rejectAmbiguousDurableIdentities(tx); err != nil {
			return err
		}
		if err := normalizeLegacyPaymentIdentifiers(tx); err != nil {
			return err
		}
		if err := normalizeLegacyUserAuthVersions(tx); err != nil {
			return err
		}
		if err := removeInvalidOrAmbiguousSessions(tx); err != nil {
			return err
		}
		if err := removeInvalidOrAmbiguousAuthFlows(tx); err != nil {
			return err
		}
		return nil
	})
}

func rejectAmbiguousDurableIdentities(tx *gorm.DB) error {
	checks := []struct {
		table   string
		columns []string
		groupBy string
		where   string
		label   string
	}{
		{"user_oauth_bindings", []string{"user_id", "provider_id", "provider_user_id"}, "user_id, provider_id", "user_id > 0 AND provider_id > 0", "user/provider ownership"},
		{"user_oauth_bindings", []string{"provider_id", "provider_user_id"}, "provider_id, provider_user_id", "provider_id > 0 AND provider_user_id IS NOT NULL AND TRIM(provider_user_id) <> ''", "provider subject ownership"},
		{"two_fas", []string{"user_id"}, "user_id", "user_id > 0", "TOTP ownership"},
		{"passkey_credentials", []string{"user_id"}, "user_id", "user_id > 0", "passkey user ownership"},
		{"passkey_credentials", []string{"credential_id"}, "credential_id", "credential_id IS NOT NULL AND TRIM(credential_id) <> ''", "passkey ownership"},
		{"external_identity_claims", []string{"provider", "subject"}, "provider, subject", "provider IS NOT NULL AND TRIM(provider) <> '' AND subject IS NOT NULL AND TRIM(subject) <> ''", "reference external subject ownership"},
		{"external_identity_claims", []string{"provider", "user_id"}, "provider, user_id", "provider IS NOT NULL AND TRIM(provider) <> '' AND user_id > 0", "external provider ownership"},
		{"top_ups", []string{"trade_no"}, "trade_no", "trade_no IS NOT NULL AND TRIM(trade_no) <> ''", "top-up trade number"},
		{"subscription_orders", []string{"trade_no"}, "trade_no", "trade_no IS NOT NULL AND TRIM(trade_no) <> ''", "subscription trade number"},
		{"subscription_pre_consume_records", []string{"request_id"}, "request_id", "request_id IS NOT NULL AND TRIM(request_id) <> ''", "subscription request id"},
	}
	for _, check := range checks {
		if !hasTableColumns(tx, check.table, check.columns...) {
			continue
		}
		duplicate, err := hasDuplicateGroup(tx, check.table, check.groupBy, check.where)
		if err != nil {
			return err
		}
		if duplicate {
			return fmt.Errorf("%w: %s contains duplicate %s", ErrAmbiguousPersistentIdentity, check.table, check.label)
		}
	}

	invalidChecks := []struct {
		table     string
		columns   []string
		predicate string
		label     string
	}{
		{"user_oauth_bindings", []string{"user_id", "provider_id", "provider_user_id"}, "user_id IS NULL OR user_id <= 0 OR provider_id IS NULL OR provider_id <= 0 OR provider_user_id IS NULL OR TRIM(provider_user_id) = ''", "OAuth binding"},
		{"two_fas", []string{"user_id", "secret"}, "user_id IS NULL OR user_id <= 0 OR secret IS NULL OR TRIM(secret) = ''", "TOTP factor"},
		{"two_fa_backup_codes", []string{"user_id", "code_hash"}, "user_id IS NULL OR user_id <= 0 OR code_hash IS NULL OR TRIM(code_hash) = ''", "2FA backup code"},
		{"passkey_credentials", []string{"user_id", "credential_id", "public_key"}, "user_id IS NULL OR user_id <= 0 OR credential_id IS NULL OR TRIM(credential_id) = '' OR public_key IS NULL OR TRIM(public_key) = ''", "passkey credential"},
		{"external_identity_claims", []string{"provider", "subject", "subject_hash", "user_id"}, "provider IS NULL OR TRIM(provider) = '' OR subject IS NULL OR TRIM(subject) = '' OR subject_hash IS NULL OR TRIM(subject_hash) = '' OR user_id IS NULL OR user_id <= 0", "external identity claim"},
	}
	for _, check := range invalidChecks {
		if !hasTableColumns(tx, check.table, check.columns...) {
			continue
		}
		count, err := countRows(tx, check.table, check.predicate)
		if err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%w: %s contains %d invalid %s row(s)", ErrAmbiguousPersistentIdentity, check.table, count, check.label)
		}
	}
	return nil
}

func removeDeletedPasskeyCredentials(tx *gorm.DB) error {
	if !hasTableColumns(tx, "passkey_credentials", "deleted_at") {
		return nil
	}
	// Passkey tombstones contain authentication material and the reference's
	// unique user_id index necessarily excludes them before replacement. They
	// have no recovery value, so permanently deleting them is the safe,
	// deterministic migration repair.
	return tx.Exec("DELETE FROM passkey_credentials WHERE deleted_at IS NOT NULL").Error
}

func normalizeLegacyPaymentIdentifiers(tx *gorm.DB) error {
	for _, target := range []struct {
		table  string
		column string
		prefix string
	}{
		{"top_ups", "trade_no", "legacy-topup-"},
		{"subscription_orders", "trade_no", "legacy-subscription-"},
		{"subscription_pre_consume_records", "request_id", "legacy-preconsume-"},
	} {
		if !hasTableColumns(tx, target.table, "id", target.column) {
			continue
		}
		predicate := target.column + " IS NULL OR TRIM(" + target.column + ") = ''"
		var ids []int64
		if err := tx.Table(target.table).Where(predicate).Order("id").Pluck("id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			value := target.prefix + strconv.FormatInt(id, 10)
			var collisions int64
			if err := tx.Table(target.table).Where(target.column+" = ? AND id <> ?", value, id).Count(&collisions).Error; err != nil {
				return err
			}
			if collisions != 0 {
				return fmt.Errorf("%w: generated %s.%s value %q already exists", ErrAmbiguousPersistentIdentity, target.table, target.column, value)
			}
			if err := tx.Table(target.table).Where("id = ?", id).Update(target.column, value).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeLegacyUserAuthVersions(tx *gorm.DB) error {
	if !hasTableColumns(tx, "users", "auth_version") {
		return nil
	}
	return tx.Table("users").Where("auth_version IS NULL OR auth_version <= 0").Update("auth_version", 1).Error
}

func removeInvalidOrAmbiguousSessions(tx *gorm.DB) error {
	columns := []string{"sid", "user_id", "version", "user_auth_version", "status", "refresh_hash", "expires_at"}
	if !hasTableColumns(tx, "user_sessions", columns...) {
		return nil
	}
	invalid := "sid IS NULL OR TRIM(sid) = '' OR user_id IS NULL OR user_id <= 0 OR version IS NULL OR version <= 0 OR user_auth_version IS NULL OR user_auth_version <= 0 OR status IS NULL OR TRIM(status) = '' OR refresh_hash IS NULL OR TRIM(refresh_hash) = '' OR expires_at IS NULL OR expires_at <= 0"
	if err := tx.Exec("DELETE FROM user_sessions WHERE " + invalid).Error; err != nil {
		return err
	}
	var hashes []string
	if err := tx.Table("user_sessions").Select("refresh_hash").
		Group("refresh_hash").Having("COUNT(*) > 1").Pluck("refresh_hash", &hashes).Error; err != nil {
		return err
	}
	for _, hash := range hashes {
		// A shared refresh secret makes every owner ambiguous. Deleting every
		// matching session is the only fail-closed automatic repair.
		if err := tx.Exec("DELETE FROM user_sessions WHERE refresh_hash = ?", hash).Error; err != nil {
			return err
		}
	}
	return nil
}

func removeInvalidOrAmbiguousAuthFlows(tx *gorm.DB) error {
	if !hasTableColumns(tx, "auth_flows", "token_hash", "purpose", "expires_at") {
		return nil
	}
	if err := tx.Exec("DELETE FROM auth_flows WHERE token_hash IS NULL OR TRIM(token_hash) = '' OR purpose IS NULL OR TRIM(purpose) = '' OR expires_at IS NULL").Error; err != nil {
		return err
	}
	var hashes []string
	if err := tx.Table("auth_flows").Select("token_hash").
		Group("token_hash").Having("COUNT(*) > 1").Pluck("token_hash", &hashes).Error; err != nil {
		return err
	}
	for _, hash := range hashes {
		if err := tx.Exec("DELETE FROM auth_flows WHERE token_hash = ?", hash).Error; err != nil {
			return err
		}
	}
	return nil
}

func hasTableColumns(tx *gorm.DB, table string, columns ...string) bool {
	if !tx.Migrator().HasTable(table) {
		return false
	}
	for _, column := range columns {
		if !tx.Migrator().HasColumn(table, column) {
			return false
		}
	}
	return true
}

func hasDuplicateGroup(tx *gorm.DB, table, groupBy, where string) (bool, error) {
	var rows []struct {
		Count int64 `gorm:"column:duplicate_count"`
	}
	err := tx.Table(table).Select("COUNT(*) AS duplicate_count").Where(where).
		Group(groupBy).Having("COUNT(*) > 1").Limit(1).Scan(&rows).Error
	return len(rows) != 0, err
}

func countRows(tx *gorm.DB, table, predicate string) (int64, error) {
	var count int64
	err := tx.Table(table).Where(predicate).Count(&count).Error
	return count, err
}

func invalidIdentifier(entity, field string) error {
	return fmt.Errorf("%w: %s.%s", ErrInvalidPersistentIdentifier, entity, field)
}

func requiredText(value, entity, field string) error {
	if strings.TrimSpace(value) == "" {
		return invalidIdentifier(entity, field)
	}
	return nil
}

// BeforeCreate hooks complement portable NOT NULL constraints: SQL considers
// an empty string non-null, while these identifiers are useless when empty.
func (session *UserSession) BeforeCreate(_ *gorm.DB) error {
	if session == nil || session.UserID <= 0 || session.UserAuthVersion <= 0 {
		return invalidIdentifier("user_session", "owner_version")
	}
	if session.Version == 0 {
		session.Version = 1
	}
	if session.Version < 0 {
		return invalidIdentifier("user_session", "version")
	}
	for _, identifier := range []struct {
		field string
		value string
	}{
		{"sid", session.SID},
		{"status", session.Status},
		{"refresh_hash", session.RefreshHash},
	} {
		if err := requiredText(identifier.value, "user_session", identifier.field); err != nil {
			return err
		}
	}
	return nil
}

func (flow *AuthFlow) BeforeCreate(_ *gorm.DB) error {
	if flow == nil || flow.ExpiresAt.IsZero() {
		return invalidIdentifier("auth_flow", "expires_at")
	}
	if err := requiredText(flow.TokenHash, "auth_flow", "token_hash"); err != nil {
		return err
	}
	return requiredText(flow.Purpose, "auth_flow", "purpose")
}

func (binding *UserOAuthBinding) BeforeCreate(_ *gorm.DB) error {
	if binding == nil || binding.UserId <= 0 || binding.ProviderId <= 0 {
		return invalidIdentifier("user_oauth_binding", "owner")
	}
	return requiredText(binding.ProviderUserId, "user_oauth_binding", "provider_user_id")
}

func (factor *TwoFA) BeforeCreate(_ *gorm.DB) error {
	if factor == nil || factor.UserId <= 0 {
		return invalidIdentifier("two_fa", "user_id")
	}
	return requiredText(factor.Secret, "two_fa", "secret")
}

func (code *TwoFABackupCode) BeforeCreate(_ *gorm.DB) error {
	if code == nil || code.UserId <= 0 {
		return invalidIdentifier("two_fa_backup_code", "user_id")
	}
	return requiredText(code.CodeHash, "two_fa_backup_code", "code_hash")
}

func (credential *PasskeyCredential) BeforeCreate(_ *gorm.DB) error {
	if credential == nil || credential.UserID <= 0 {
		return invalidIdentifier("passkey_credential", "user_id")
	}
	if err := requiredText(credential.CredentialID, "passkey_credential", "credential_id"); err != nil {
		return err
	}
	return requiredText(credential.PublicKey, "passkey_credential", "public_key")
}

func (claim *ExternalIdentityClaim) BeforeCreate(_ *gorm.DB) error {
	if claim == nil || claim.UserId <= 0 {
		return invalidIdentifier("external_identity_claim", "user_id")
	}
	if err := requiredText(claim.Provider, "external_identity_claim", "provider"); err != nil {
		return err
	}
	if err := requiredText(claim.Subject, "external_identity_claim", "subject"); err != nil {
		return err
	}
	provider, err := validateBuiltInExternalIdentity(claim.Provider, claim.Subject)
	if err != nil {
		return err
	}
	claim.Provider = provider
	claim.SubjectHash = externalIdentitySubjectHash(claim.Subject)
	return nil
}

func (topUp *TopUp) BeforeCreate(_ *gorm.DB) error {
	if topUp == nil {
		return invalidIdentifier("top_up", "trade_no")
	}
	return requiredText(topUp.TradeNo, "top_up", "trade_no")
}

func (order *SubscriptionOrder) BeforeCreate(_ *gorm.DB) error {
	if order == nil {
		return invalidIdentifier("subscription_order", "trade_no")
	}
	return requiredText(order.TradeNo, "subscription_order", "trade_no")
}

func (record *SubscriptionPreConsumeRecord) BeforeCreate(_ *gorm.DB) error {
	if record == nil {
		return invalidIdentifier("subscription_pre_consume_record", "request_id")
	}
	return requiredText(record.RequestId, "subscription_pre_consume_record", "request_id")
}
