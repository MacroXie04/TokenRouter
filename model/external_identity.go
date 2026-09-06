package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ExternalIdentityProviderGitHub   = "github"
	ExternalIdentityProviderDiscord  = "discord"
	ExternalIdentityProviderOIDC     = "oidc"
	ExternalIdentityProviderWeChat   = "wechat"
	ExternalIdentityProviderTelegram = "telegram"
	ExternalIdentityProviderLinuxDO  = "linuxdo"

	maxBuiltInExternalIdentitySubjectCharacters = 128
)

var (
	ErrExternalIdentityAlreadyClaimed = errors.New("external identity is already claimed")
	ErrExternalIdentityOwnerInvalid   = errors.New("external identity owner is unavailable")
)

var builtInExternalIdentityColumns = map[string]string{
	ExternalIdentityProviderGitHub:   "github_id",
	ExternalIdentityProviderDiscord:  "discord_id",
	ExternalIdentityProviderOIDC:     "oidc_id",
	ExternalIdentityProviderWeChat:   "wechat_id",
	ExternalIdentityProviderTelegram: "telegram_id",
	ExternalIdentityProviderLinuxDO:  "linuxdo_id",
}

var builtInExternalIdentityProviders = []string{
	ExternalIdentityProviderGitHub,
	ExternalIdentityProviderDiscord,
	ExternalIdentityProviderOIDC,
	ExternalIdentityProviderWeChat,
	ExternalIdentityProviderTelegram,
	ExternalIdentityProviderLinuxDO,
}

// BuiltInExternalIdentityColumn returns the compatibility column for a
// supported built-in provider. Ownership itself lives in
// external_identity_claims; callers must never use the compatibility column
// as an ownership check.
func BuiltInExternalIdentityColumn(provider string) (string, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	column, ok := builtInExternalIdentityColumns[provider]
	return column, ok
}

func validateBuiltInExternalIdentity(provider, subject string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if _, ok := builtInExternalIdentityColumns[provider]; !ok {
		return "", fmt.Errorf("%w: unsupported external identity provider", ErrInvalidPersistentIdentifier)
	}
	if subject == "" || subject != strings.TrimSpace(subject) ||
		utf8.RuneCountInString(subject) > maxBuiltInExternalIdentitySubjectCharacters ||
		validateBoundedCustomOAuthText(subject, maxBuiltInExternalIdentitySubjectCharacters*utf8.UTFMax) != nil {
		return "", invalidIdentifier("external_identity_claim", "subject")
	}
	return provider, nil
}

func externalIdentitySubjectHash(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:])
}

func externalIdentitySubjectKey(provider, subject string) string {
	return provider + "\x00" + subject
}

func externalIdentityUserKey(provider string, userID int) string {
	return provider + "\x00" + fmt.Sprintf("%d", userID)
}

// FindExternalIdentityClaimWithTx resolves an exact, case-sensitive provider
// subject through its collation-independent hash key. Every row in the hash
// bucket is compared in Go, so a theoretical collision cannot hide either
// subject or make the additive lookup index constrain reference-valid rows.
func FindExternalIdentityClaimWithTx(tx *gorm.DB, provider, subject string) (*ExternalIdentityClaim, error) {
	if tx == nil {
		return nil, errors.New("database is nil")
	}
	provider, err := validateBuiltInExternalIdentity(provider, subject)
	if err != nil {
		return nil, err
	}
	return findExternalIdentityClaimByHashWithTx(tx, provider, subject, externalIdentitySubjectHash(subject))
}

func findExternalIdentityClaimByHashWithTx(tx *gorm.DB, provider, subject, subjectHash string) (*ExternalIdentityClaim, error) {
	var claims []ExternalIdentityClaim
	if err := tx.Where("provider = ? AND subject_hash = ?", provider, subjectHash).Order("id").Find(&claims).Error; err != nil {
		return nil, err
	}
	for i := range claims {
		if claims[i].Subject != subject {
			continue
		}
		if claims[i].UserId <= 0 {
			return nil, ErrExternalIdentityOwnerInvalid
		}
		return &claims[i], nil
	}
	return nil, gorm.ErrRecordNotFound
}

// FindUserByExternalIdentity returns the active user owning a built-in
// identity. A claim owned by a soft-deleted or missing user is not treated as
// unclaimed, which prevents identity reassignment after account deletion.
func FindUserByExternalIdentity(provider, subject string) (*User, error) {
	claim, err := FindExternalIdentityClaimWithTx(DB, provider, subject)
	if err != nil {
		return nil, err
	}
	var user User
	if err := DB.First(&user, claim.UserId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: user %d", ErrExternalIdentityOwnerInvalid, claim.UserId)
		}
		return nil, err
	}
	return &user, nil
}

// ClaimExternalIdentityWithTx atomically claims one provider subject for one
// user. Repeating the exact mapping is idempotent. Both a competing owner and
// a second subject for the same provider slot are rejected by portable unique
// indexes, with read-back used instead of dialect-specific duplicate errors.
func ClaimExternalIdentityWithTx(tx *gorm.DB, provider, subject string, userID int) error {
	if tx == nil || userID <= 0 {
		return invalidIdentifier("external_identity_claim", "owner")
	}
	provider, err := validateBuiltInExternalIdentity(provider, subject)
	if err != nil {
		return err
	}

	if existing, findErr := FindExternalIdentityClaimWithTx(tx, provider, subject); findErr == nil {
		if existing.UserId == userID {
			return nil
		}
		return ErrExternalIdentityAlreadyClaimed
	} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
		return findErr
	}
	var userClaim ExternalIdentityClaim
	userErr := tx.Where("provider = ? AND user_id = ?", provider, userID).First(&userClaim).Error
	if userErr == nil {
		if userClaim.Subject == subject && userClaim.SubjectHash == externalIdentitySubjectHash(subject) {
			return nil
		}
		return ErrExternalIdentityAlreadyClaimed
	}
	if !errors.Is(userErr, gorm.ErrRecordNotFound) {
		return userErr
	}

	claim := ExternalIdentityClaim{
		Provider: provider, Subject: subject, SubjectHash: externalIdentitySubjectHash(subject), UserId: userID,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&claim).Error; err != nil {
		return err
	}

	subjectOwner, subjectErr := FindExternalIdentityClaimWithTx(tx, provider, subject)
	if subjectErr != nil {
		if errors.Is(subjectErr, gorm.ErrRecordNotFound) {
			return ErrExternalIdentityAlreadyClaimed
		}
		return subjectErr
	}
	if subjectOwner.UserId != userID {
		return ErrExternalIdentityAlreadyClaimed
	}
	if err := tx.Where("provider = ? AND user_id = ?", provider, userID).First(&userClaim).Error; err != nil {
		return err
	}
	if userClaim.Subject != subject || userClaim.SubjectHash != externalIdentitySubjectHash(subject) {
		return ErrExternalIdentityAlreadyClaimed
	}
	return nil
}

// ReleaseExternalIdentityWithTx frees one user's provider slot. Soft-deleting
// a user must not call this; ownership survives soft deletion by design.
func ReleaseExternalIdentityWithTx(tx *gorm.DB, provider string, userID int) error {
	if tx == nil || userID <= 0 {
		return invalidIdentifier("external_identity_claim", "owner")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if _, ok := builtInExternalIdentityColumns[provider]; !ok {
		return invalidIdentifier("external_identity_claim", "provider")
	}
	return tx.Where("provider = ? AND user_id = ?", provider, userID).Delete(&ExternalIdentityClaim{}).Error
}

// ClaimUserExternalIdentitiesWithTx registers every non-empty built-in
// identity present on a newly-created/imported user row in the same
// transaction as that row.
func ClaimUserExternalIdentitiesWithTx(tx *gorm.DB, user *User) error {
	if tx == nil || user == nil || user.Id <= 0 {
		return invalidIdentifier("external_identity_claim", "owner")
	}
	for _, provider := range builtInExternalIdentityProviders {
		subject := builtInExternalIdentityField(user, provider)
		if subject == "" {
			continue
		}
		if err := ClaimExternalIdentityWithTx(tx, provider, subject, user.Id); err != nil {
			return fmt.Errorf("claim %s identity for user %d: %w", provider, user.Id, err)
		}
	}
	return nil
}

func builtInExternalIdentityField(user *User, provider string) string {
	switch provider {
	case ExternalIdentityProviderGitHub:
		return user.GitHubId
	case ExternalIdentityProviderDiscord:
		return user.DiscordId
	case ExternalIdentityProviderOIDC:
		return user.OidcId
	case ExternalIdentityProviderWeChat:
		return user.WeChatId
	case ExternalIdentityProviderTelegram:
		return user.TelegramId
	case ExternalIdentityProviderLinuxDO:
		return user.LinuxDOId
	default:
		return ""
	}
}

// externalIdentityClaimHashBootstrap uses a temporary empty default so every
// supported database can add a NOT NULL column to a populated legacy table.
// Preflight replaces every temporary value before the lookup index is added;
// normal writes are also guarded by the model hook.
type externalIdentityClaimHashBootstrap struct {
	SubjectHash string `gorm:"column:subject_hash;type:char(64);not null;default:''"`
}

func (externalIdentityClaimHashBootstrap) TableName() string { return "external_identity_claims" }

func ensureExternalIdentityClaimStorage() error {
	if !DB.Migrator().HasTable(&ExternalIdentityClaim{}) {
		return DB.Migrator().CreateTable(&ExternalIdentityClaim{})
	}
	coreColumns := []string{"id", "provider", "subject", "user_id"}
	for _, column := range coreColumns {
		if DB.Migrator().HasColumn(&ExternalIdentityClaim{}, column) {
			continue
		}
		var count int64
		if err := DB.Table("external_identity_claims").Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%w: external_identity_claims is missing %s", ErrAmbiguousPersistentIdentity, column)
		}
		return DB.AutoMigrate(&ExternalIdentityClaim{})
	}
	if !DB.Migrator().HasColumn(&ExternalIdentityClaim{}, "subject_hash") {
		return DB.Migrator().AddColumn(&externalIdentityClaimHashBootstrap{}, "SubjectHash")
	}
	return nil
}

func prepareBuiltInExternalIdentityOwnership(tx *gorm.DB) error {
	if !hasTableColumns(tx, "external_identity_claims", "id", "provider", "subject", "subject_hash", "user_id") {
		return nil
	}

	var claims []ExternalIdentityClaim
	if err := tx.Order("id").Find(&claims).Error; err != nil {
		return err
	}
	claimBySubject := make(map[string]ExternalIdentityClaim, len(claims))
	claimByUser := make(map[string]ExternalIdentityClaim, len(claims))
	for _, claim := range claims {
		provider, err := validateBuiltInExternalIdentity(claim.Provider, claim.Subject)
		if err != nil || provider != claim.Provider {
			return fmt.Errorf("%w: external identity claim %d has an invalid provider or subject", ErrAmbiguousPersistentIdentity, claim.Id)
		}
		subjectKey := externalIdentitySubjectKey(provider, claim.Subject)
		userKey := externalIdentityUserKey(provider, claim.UserId)
		if _, exists := claimBySubject[subjectKey]; exists {
			return fmt.Errorf("%w: duplicate %s subject ownership", ErrAmbiguousPersistentIdentity, provider)
		}
		if _, exists := claimByUser[userKey]; exists {
			return fmt.Errorf("%w: duplicate %s user ownership", ErrAmbiguousPersistentIdentity, provider)
		}
		claimBySubject[subjectKey] = claim
		claimByUser[userKey] = claim
		expectedHash := externalIdentitySubjectHash(claim.Subject)
		if claim.SubjectHash != expectedHash {
			if err := tx.Model(&ExternalIdentityClaim{}).Where("id = ?", claim.Id).
				Update("subject_hash", expectedHash).Error; err != nil {
				return err
			}
			claim.SubjectHash = expectedHash
			claimBySubject[subjectKey] = claim
			claimByUser[userKey] = claim
		}
	}

	if !tx.Migrator().HasTable("users") || !tx.Migrator().HasColumn("users", "id") {
		if len(claims) != 0 {
			return fmt.Errorf("%w: external identity claims exist without users", ErrAmbiguousPersistentIdentity)
		}
		return nil
	}

	type legacyIdentity struct {
		Provider string `gorm:"-"`
		UserID   int    `gorm:"column:user_id"`
		Subject  string `gorm:"column:subject"`
	}
	var legacyIdentities []legacyIdentity
	legacyBySubject := make(map[string]legacyIdentity)
	legacyByUser := make(map[string]legacyIdentity)
	for _, provider := range builtInExternalIdentityProviders {
		column := builtInExternalIdentityColumns[provider]
		if !tx.Migrator().HasColumn("users", column) {
			continue
		}
		if err := tx.Table("users").Where(column+" IS NULL OR TRIM("+column+") = ''").Update(column, "").Error; err != nil {
			return err
		}
		var identities []legacyIdentity
		if err := tx.Table("users").Select("id AS user_id, " + column + " AS subject").
			Where(column + " <> ''").Order("id").Scan(&identities).Error; err != nil {
			return err
		}
		for _, identity := range identities {
			identity.Provider = provider
			if _, err := validateBuiltInExternalIdentity(provider, identity.Subject); err != nil {
				return fmt.Errorf("%w: users.%s contains an invalid identity for user %d", ErrAmbiguousPersistentIdentity, column, identity.UserID)
			}
			subjectKey := externalIdentitySubjectKey(provider, identity.Subject)
			if previous, exists := legacyBySubject[subjectKey]; exists && previous.UserID != identity.UserID {
				return fmt.Errorf("%w: users.%s subject is owned by users %d and %d", ErrAmbiguousPersistentIdentity, column, previous.UserID, identity.UserID)
			}
			legacyBySubject[subjectKey] = identity
			legacyByUser[externalIdentityUserKey(provider, identity.UserID)] = identity
			legacyIdentities = append(legacyIdentities, identity)
		}
	}

	// Existing claims are authoritative only when they agree with the legacy
	// mirror. A blank mirror can be repaired deterministically; two non-empty
	// values for one provider/user cannot be reconciled safely.
	for _, claim := range claims {
		column := builtInExternalIdentityColumns[claim.Provider]
		var ownerCount int64
		if err := tx.Table("users").Where("id = ?", claim.UserId).Count(&ownerCount).Error; err != nil {
			return err
		}
		if ownerCount != 1 {
			return fmt.Errorf("%w: %s subject references missing user %d", ErrAmbiguousPersistentIdentity, claim.Provider, claim.UserId)
		}
		userKey := externalIdentityUserKey(claim.Provider, claim.UserId)
		legacy, hasMirror := legacyByUser[userKey]
		if hasMirror && legacy.Subject != claim.Subject {
			return fmt.Errorf("%w: user %d has conflicting %s identities", ErrAmbiguousPersistentIdentity, claim.UserId, claim.Provider)
		}
		if existing, exists := legacyBySubject[externalIdentitySubjectKey(claim.Provider, claim.Subject)]; exists && existing.UserID != claim.UserId {
			return fmt.Errorf("%w: %s subject has conflicting owners", ErrAmbiguousPersistentIdentity, claim.Provider)
		}
		if !hasMirror {
			if err := tx.Table("users").Where("id = ?", claim.UserId).Update(column, claim.Subject).Error; err != nil {
				return err
			}
			identity := legacyIdentity{UserID: claim.UserId, Subject: claim.Subject}
			legacyByUser[userKey] = identity
			legacyBySubject[externalIdentitySubjectKey(claim.Provider, claim.Subject)] = identity
		}
	}

	for _, identity := range legacyIdentities {
		provider := identity.Provider
		subjectKey := externalIdentitySubjectKey(provider, identity.Subject)
		if claim, exists := claimBySubject[subjectKey]; exists {
			if claim.UserId != identity.UserID {
				return fmt.Errorf("%w: %s subject has conflicting owners", ErrAmbiguousPersistentIdentity, provider)
			}
			continue
		}
		userKey := externalIdentityUserKey(provider, identity.UserID)
		if claim, exists := claimByUser[userKey]; exists && claim.Subject != identity.Subject {
			return fmt.Errorf("%w: user %d has conflicting %s identities", ErrAmbiguousPersistentIdentity, identity.UserID, provider)
		}
		if err := ClaimExternalIdentityWithTx(tx, provider, identity.Subject, identity.UserID); err != nil {
			return fmt.Errorf("backfill %s identity for user %d: %w", provider, identity.UserID, err)
		}
	}
	return nil
}

func installExternalIdentityClaimSubjectIndex() error {
	const directSubjectIndex = "idx_external_identity_subject"
	if !DB.Migrator().HasIndex(&ExternalIdentityClaim{}, directSubjectIndex) {
		if err := DB.Migrator().CreateIndex(&ExternalIdentityClaim{}, directSubjectIndex); err != nil {
			return fmt.Errorf("create reference external identity subject index: %w", err)
		}
	}
	exists, unique, err := externalIdentityClaimIndexState(directSubjectIndex)
	if err != nil {
		return err
	}
	if !exists || !unique {
		return fmt.Errorf("reference external identity subject uniqueness index is invalid")
	}

	// Earlier target builds enforced ownership through a hash UNIQUE index.
	// The exact reference provider/subject constraint is now active, so the
	// legacy index can be replaced without a uniqueness gap or any row rewrite.
	const legacyHashIndex = "ux_external_identity_subject"
	if DB.Migrator().HasIndex(&ExternalIdentityClaim{}, legacyHashIndex) {
		if err := dropIndexPortable(DB, &ExternalIdentityClaim{}, legacyHashIndex); err != nil {
			return fmt.Errorf("remove legacy external identity hash uniqueness: %w", err)
		}
	}

	const hashLookupIndex = "idx_external_identity_subject_hash"
	if DB.Migrator().HasIndex(&ExternalIdentityClaim{}, hashLookupIndex) {
		_, hashUnique, err := externalIdentityClaimIndexState(hashLookupIndex)
		if err != nil {
			return err
		}
		if hashUnique {
			if err := dropIndexPortable(DB, &ExternalIdentityClaim{}, hashLookupIndex); err != nil {
				return fmt.Errorf("replace unique external identity hash index: %w", err)
			}
		}
	}
	if !DB.Migrator().HasIndex(&ExternalIdentityClaim{}, hashLookupIndex) {
		if err := DB.Migrator().CreateIndex(&ExternalIdentityClaim{}, hashLookupIndex); err != nil {
			return fmt.Errorf("create external identity hash lookup index: %w", err)
		}
	}
	return nil
}

func finalizeExternalIdentityClaimIndexes() error {
	for _, expected := range []struct {
		name   string
		unique bool
	}{
		{"idx_external_identity_subject", true},
		{"idx_external_identity_subject_hash", false},
	} {
		exists, unique, err := externalIdentityClaimIndexState(expected.name)
		if err != nil {
			return err
		}
		if !exists || unique != expected.unique {
			return fmt.Errorf("external identity index %s has an invalid definition", expected.name)
		}
	}
	return nil
}

func externalIdentityClaimIndexState(name string) (exists, unique bool, err error) {
	indexes, err := DB.Migrator().GetIndexes(&ExternalIdentityClaim{})
	if err != nil {
		return false, false, fmt.Errorf("inspect external identity indexes: %w", err)
	}
	for _, index := range indexes {
		if index.Name() != name {
			continue
		}
		unique, known := index.Unique()
		if !known {
			return true, false, fmt.Errorf("inspect external identity index %s uniqueness", name)
		}
		return true, unique, nil
	}
	return false, false, nil
}
