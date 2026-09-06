package model

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newExternalIdentityTestDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "external-identity.db")), &gorm.Config{})
	require.NoError(t, err)
	previousDB, previousLogDB := DB, LOG_DB
	DB, LOG_DB = db, db
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
	})
	if len(models) != 0 {
		require.NoError(t, db.AutoMigrate(models...))
	}
	return db
}

func setExternalIdentityField(user *User, provider, subject string) {
	switch provider {
	case ExternalIdentityProviderGitHub:
		user.GitHubId = subject
	case ExternalIdentityProviderDiscord:
		user.DiscordId = subject
	case ExternalIdentityProviderOIDC:
		user.OidcId = subject
	case ExternalIdentityProviderWeChat:
		user.WeChatId = subject
	case ExternalIdentityProviderTelegram:
		user.TelegramId = subject
	case ExternalIdentityProviderLinuxDO:
		user.LinuxDOId = subject
	}
}

func TestExternalIdentityClaimsEnforceExactSingleOwnershipForEveryBuiltIn(t *testing.T) {
	db := newExternalIdentityTestDB(t, &User{}, &ExternalIdentityClaim{})

	for _, provider := range builtInExternalIdentityProviders {
		t.Run(provider, func(t *testing.T) {
			upperOwner := User{Username: provider + "-upper", Password: "password"}
			lowerOwner := User{Username: provider + "-lower", Password: "password"}
			competitor := User{Username: provider + "-competitor", Password: "password"}
			require.NoError(t, db.Create(&upperOwner).Error)
			require.NoError(t, db.Create(&lowerOwner).Error)
			require.NoError(t, db.Create(&competitor).Error)

			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return ClaimExternalIdentityWithTx(tx, provider, "Case-Sensitive", upperOwner.Id)
			}))
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return ClaimExternalIdentityWithTx(tx, provider, "case-sensitive", lowerOwner.Id)
			}))
			err := db.Transaction(func(tx *gorm.DB) error {
				return ClaimExternalIdentityWithTx(tx, provider, "Case-Sensitive", competitor.Id)
			})
			assert.ErrorIs(t, err, ErrExternalIdentityAlreadyClaimed)

			claim, err := FindExternalIdentityClaimWithTx(db, provider, "Case-Sensitive")
			require.NoError(t, err)
			assert.Equal(t, upperOwner.Id, claim.UserId)
			claim, err = FindExternalIdentityClaimWithTx(db, provider, "case-sensitive")
			require.NoError(t, err)
			assert.Equal(t, lowerOwner.Id, claim.UserId)
		})
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		return ClaimExternalIdentityWithTx(tx, ExternalIdentityProviderGitHub, " ", 1)
	})
	assert.ErrorIs(t, err, ErrInvalidPersistentIdentifier)
}

func TestBuiltInExternalIdentityMigrationBackfillsAllProvidersAndIgnoresEmptyValues(t *testing.T) {
	db := newExternalIdentityTestDB(t, &User{})
	owner := User{Username: "legacy-owner", Password: "password"}
	for _, provider := range builtInExternalIdentityProviders {
		setExternalIdentityField(&owner, provider, provider+"-legacy-subject")
	}
	emptyOne := User{Username: "empty-one", Password: "password"}
	emptyTwo := User{Username: "empty-two", Password: "password", GitHubId: "   "}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&emptyOne).Error)
	require.NoError(t, db.Create(&emptyTwo).Error)
	require.NoError(t, db.Delete(&owner).Error)

	require.NoError(t, ensureExternalIdentityClaimStorage())
	require.NoError(t, prepareSecuritySchemaMigration())
	require.NoError(t, installExternalIdentityClaimSubjectIndex())
	require.NoError(t, db.AutoMigrate(&ExternalIdentityClaim{}))
	require.NoError(t, finalizeExternalIdentityClaimIndexes())
	require.NoError(t, prepareSecuritySchemaMigration(), "backfill must be idempotent")

	var claims []ExternalIdentityClaim
	require.NoError(t, db.Order("provider").Find(&claims).Error)
	require.Len(t, claims, len(builtInExternalIdentityProviders))
	for _, claim := range claims {
		assert.Equal(t, owner.Id, claim.UserId)
		assert.Equal(t, externalIdentitySubjectHash(claim.Subject), claim.SubjectHash)
	}
	var normalized User
	require.NoError(t, db.First(&normalized, emptyTwo.Id).Error)
	assert.Empty(t, normalized.GitHubId)
	_, err := FindUserByExternalIdentity(ExternalIdentityProviderGitHub, owner.GitHubId)
	assert.ErrorIs(t, err, ErrExternalIdentityOwnerInvalid, "soft-deleted owners retain the claim")
	assert.False(t, db.Migrator().HasIndex(&ExternalIdentityClaim{}, "ux_external_identity_subject"))
	assert.True(t, db.Migrator().HasIndex(&ExternalIdentityClaim{}, "idx_external_identity_subject_hash"))
	assert.True(t, db.Migrator().HasIndex(&ExternalIdentityClaim{}, "idx_external_identity_subject"))
}

func TestBuiltInExternalIdentityMigrationRejectsAmbiguousLegacyOwners(t *testing.T) {
	for _, provider := range builtInExternalIdentityProviders {
		t.Run(provider, func(t *testing.T) {
			db := newExternalIdentityTestDB(t, &User{})
			first := User{Username: provider + "-first", Password: "password"}
			second := User{Username: provider + "-second", Password: "password"}
			setExternalIdentityField(&first, provider, "duplicate-subject")
			setExternalIdentityField(&second, provider, "duplicate-subject")
			require.NoError(t, db.Create(&first).Error)
			require.NoError(t, db.Create(&second).Error)
			require.NoError(t, ensureExternalIdentityClaimStorage())

			err := prepareSecuritySchemaMigration()
			assert.ErrorIs(t, err, ErrAmbiguousPersistentIdentity)
			var count int64
			require.NoError(t, db.Model(&ExternalIdentityClaim{}).Count(&count).Error)
			assert.Zero(t, count, "a failed preflight must not choose an owner")
		})
	}
}

func TestBuiltInExternalIdentityMigrationRejectsClaimMirrorConflict(t *testing.T) {
	db := newExternalIdentityTestDB(t, &User{}, &ExternalIdentityClaim{})
	user := User{Username: "conflicting-owner", Password: "password", GitHubId: "mirror-subject"}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&ExternalIdentityClaim{
		Provider: ExternalIdentityProviderGitHub, Subject: "claim-subject", UserId: user.Id,
	}).Error)

	err := prepareSecuritySchemaMigration()
	assert.True(t, errors.Is(err, ErrAmbiguousPersistentIdentity), "error: %v", err)
}

func TestLegacyClaimHashMigrationRepairsDeterministicState(t *testing.T) {
	db := newExternalIdentityTestDB(t, &User{})
	user := User{Username: "old-claim-owner", Password: "password"}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Exec(`CREATE TABLE external_identity_claims (
		id INTEGER PRIMARY KEY, provider varchar(32) NOT NULL, subject varchar(128) NOT NULL,
		user_id INTEGER NOT NULL, created_at DATETIME
	)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_external_identity_subject
		ON external_identity_claims (provider, subject)`).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO external_identity_claims (id, provider, subject, user_id) VALUES (1, ?, ?, ?)",
		ExternalIdentityProviderGitHub, "Legacy-Case", user.Id,
	).Error)

	require.NoError(t, ensureExternalIdentityClaimStorage())
	require.NoError(t, prepareSecuritySchemaMigration())
	require.NoError(t, installExternalIdentityClaimSubjectIndex())
	require.NoError(t, db.AutoMigrate(&ExternalIdentityClaim{}))
	require.NoError(t, finalizeExternalIdentityClaimIndexes())

	var migrated User
	require.NoError(t, db.First(&migrated, user.Id).Error)
	assert.Equal(t, "Legacy-Case", migrated.GitHubId)
	claim, err := FindExternalIdentityClaimWithTx(db, ExternalIdentityProviderGitHub, "Legacy-Case")
	require.NoError(t, err)
	assert.Equal(t, externalIdentitySubjectHash("Legacy-Case"), claim.SubjectHash)

	second := User{Username: "case-distinct-owner", Password: "password"}
	require.NoError(t, db.Create(&second).Error)
	require.NoError(t, ClaimExternalIdentityWithTx(db, ExternalIdentityProviderGitHub, "legacy-case", second.Id),
		fmt.Sprintf("case-distinct subjects must remain distinct on %s", db.Dialector.Name()))
}
