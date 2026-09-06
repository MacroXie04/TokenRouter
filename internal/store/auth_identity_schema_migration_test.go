package store

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// pinnedReferenceTwoFABackupCodeSchema and
// pinnedReferenceExternalIdentityClaimSchema independently transcribe the
// complete declarations at the pinned reference commit. The external subject
// hash is the only additive persistent field in this pair.
type pinnedReferenceTwoFABackupCodeSchema struct {
	Id        int    `gorm:"primaryKey"`
	UserId    int    `gorm:"not null;index"`
	CodeHash  string `gorm:"type:varchar(255);not null"`
	IsUsed    bool
	UsedAt    *time.Time
	CreatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (pinnedReferenceTwoFABackupCodeSchema) TableName() string {
	return "two_fa_backup_codes"
}

// legacyUniqueTwoFABackupCodeSchema captures the prior target-only composite
// uniqueness so startup can prove it removes the schema residual without
// rewriting recovery-code history.
type legacyUniqueTwoFABackupCodeSchema struct {
	Id        int    `gorm:"primaryKey"`
	UserId    int    `gorm:"not null;index;uniqueIndex:ux_two_fa_backup_user_code,priority:1"`
	CodeHash  string `gorm:"type:varchar(255);not null;uniqueIndex:ux_two_fa_backup_user_code,priority:2"`
	IsUsed    bool
	UsedAt    *time.Time
	CreatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (legacyUniqueTwoFABackupCodeSchema) TableName() string {
	return "two_fa_backup_codes"
}

type pinnedReferenceExternalIdentityClaimSchema struct {
	Id        int64  `gorm:"primaryKey"`
	Provider  string `gorm:"type:varchar(32);not null;uniqueIndex:idx_external_identity_subject,priority:1;uniqueIndex:idx_external_identity_user,priority:1"`
	Subject   string `gorm:"type:varchar(128);not null;uniqueIndex:idx_external_identity_subject,priority:2"`
	UserId    int    `gorm:"not null;index;uniqueIndex:idx_external_identity_user,priority:2"`
	CreatedAt time.Time
}

func (pinnedReferenceExternalIdentityClaimSchema) TableName() string {
	return "external_identity_claims"
}

// legacyHashedExternalIdentityClaimSchema is the immediately preceding target
// layout: it has the additive hash key but is missing the reference's direct
// provider/subject index.
type legacyHashedExternalIdentityClaimSchema struct {
	Id          int64  `gorm:"primaryKey"`
	Provider    string `gorm:"type:varchar(32);not null;uniqueIndex:ux_external_identity_subject,priority:1;uniqueIndex:idx_external_identity_user,priority:1"`
	Subject     string `gorm:"type:varchar(128);not null"`
	SubjectHash string `gorm:"type:char(64);not null;uniqueIndex:ux_external_identity_subject,priority:2"`
	UserId      int    `gorm:"not null;index;uniqueIndex:idx_external_identity_user,priority:2"`
	CreatedAt   time.Time
}

func (legacyHashedExternalIdentityClaimSchema) TableName() string {
	return "external_identity_claims"
}

func TestReferenceTwoFABackupCodeSchemaPortableDialects(t *testing.T) {
	target := parseAuthIdentitySchema(t, &TwoFABackupCode{})
	reference := parseAuthIdentitySchema(t, &pinnedReferenceTwoFABackupCodeSchema{})
	assertAuthIdentityReferenceFields(t, reference, target)
	assertAuthIdentityReferenceDialectTypes(t, reference, target)
	assertAuthIdentityReferenceIndexes(t, reference, target)
}

func TestReferenceExternalIdentityClaimSchemaPortableDialects(t *testing.T) {
	target := parseAuthIdentitySchema(t, &ExternalIdentityClaim{})
	reference := parseAuthIdentitySchema(t, &pinnedReferenceExternalIdentityClaimSchema{})
	assertAuthIdentityReferenceFields(t, reference, target, "SubjectHash")
	assertAuthIdentityReferenceDialectTypes(t, reference, target)
	assertAuthIdentityReferenceIndexes(t, reference, target, "idx_external_identity_subject_hash")
	assertParsedAuthIdentityIndex(t, target, "idx_external_identity_subject", []string{"provider", "subject"}, true)
	assertParsedAuthIdentityIndex(t, target, "idx_external_identity_user", []string{"provider", "user_id"}, true)
	assertParsedAuthIdentityIndex(t, target, "idx_external_identity_subject_hash", []string{"provider", "subject_hash"}, false)

	subjectHash := target.LookUpField("SubjectHash")
	require.NotNil(t, subjectHash)
	assert.Equal(t, "subject_hash", subjectHash.DBName)
	assert.True(t, subjectHash.NotNull)
	assert.False(t, subjectHash.HasDefaultValue)
	assert.Equal(t, "char(64)", strings.ToLower(subjectHash.TagSettings["TYPE"]))
}

func TestReferenceTwoFABackupCodeSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceTwoFABackupCodeSchemaMatchesSQLite(t, db)
	assertReferenceAuthIdentitySchema(t, db)

	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, db.Exec(`INSERT INTO two_fa_backup_codes
		(user_id, code_hash, is_used, used_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		301, "direct-backup-hash", true, now, now).Error)
	var stored TwoFABackupCode
	require.NoError(t, db.Where("user_id = ?", 301).First(&stored).Error)
	assert.Equal(t, "direct-backup-hash", stored.CodeHash)
	assert.True(t, stored.IsUsed)
	require.NotNil(t, stored.UsedAt)

	require.NoError(t, db.Exec(`INSERT INTO two_fa_backup_codes (user_id, code_hash) VALUES (?, ?)`,
		302, "shared-across-users").Error)
	require.NoError(t, db.Exec(`INSERT INTO two_fa_backup_codes (user_id, code_hash) VALUES (?, ?)`,
		303, "shared-across-users").Error)
	require.NoError(t, db.Exec(`INSERT INTO two_fa_backup_codes (user_id, code_hash) VALUES (?, ?)`,
		302, "shared-across-users").Error, "duplicate rows are part of the reference-visible schema contract")
	assertRowCount(t, db, TwoFABackupCode{}.TableName(), "user_id = 302 AND code_hash = 'shared-across-users'", 2)

	columnsBefore := sqliteColumnSignatures(t, db, TwoFABackupCode{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, TwoFABackupCode{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, TwoFABackupCode{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, TwoFABackupCode{}.TableName()))
}

func TestReferenceExternalIdentityClaimSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceExternalIdentityClaimSchemaMatchesSQLite(t, db)
	assertReferenceAuthIdentitySchema(t, db)
	owners := []User{
		{Username: "schema-identity-1", Password: "password"},
		{Username: "schema-identity-2", Password: "password"},
		{Username: "schema-identity-3", Password: "password"},
		{Username: "schema-identity-4", Password: "password"},
		{Username: "schema-identity-5", Password: "password"},
	}
	require.NoError(t, db.Create(&owners).Error)

	subject := strings.Repeat("s", maxBuiltInExternalIdentitySubjectCharacters)
	require.NoError(t, ClaimExternalIdentityWithTx(db, ExternalIdentityProviderOIDC, subject, owners[0].Id))
	claim, err := FindExternalIdentityClaimWithTx(db, ExternalIdentityProviderOIDC, subject)
	require.NoError(t, err)
	assert.Equal(t, externalIdentitySubjectHash(subject), claim.SubjectHash)
	assert.ErrorIs(t,
		ClaimExternalIdentityWithTx(db, ExternalIdentityProviderDiscord, subject+"x", owners[1].Id),
		ErrInvalidPersistentIdentifier,
	)
	multibyteSubject := strings.Repeat("界", maxBuiltInExternalIdentitySubjectCharacters)
	require.NoError(t, ClaimExternalIdentityWithTx(db, ExternalIdentityProviderDiscord, multibyteSubject, owners[1].Id),
		"varchar(128) capacity is measured in characters, not UTF-8 bytes")

	// SQLite's reference text index is binary by default. Retain the target's
	// exact-subject behavior while proving the restored direct index is active.
	require.NoError(t, ClaimExternalIdentityWithTx(db, ExternalIdentityProviderGitHub, "Case-Sensitive", owners[2].Id))
	require.NoError(t, ClaimExternalIdentityWithTx(db, ExternalIdentityProviderGitHub, "case-sensitive", owners[3].Id))
	assert.ErrorIs(t,
		ClaimExternalIdentityWithTx(db, ExternalIdentityProviderGitHub, "Case-Sensitive", owners[4].Id),
		ErrExternalIdentityAlreadyClaimed,
	)

	columnsBefore := sqliteColumnSignatures(t, db, ExternalIdentityClaim{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, ExternalIdentityClaim{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, ExternalIdentityClaim{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, ExternalIdentityClaim{}.TableName()))
}

func TestReferenceExternalIdentityClaimHashLookupDoesNotConstrainCollisions(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	owners := []User{
		{Username: "hash-collision-owner-a", Password: "password"},
		{Username: "hash-collision-owner-b", Password: "password"},
	}
	require.NoError(t, db.Create(&owners).Error)

	// Inject one synthetic collision bucket directly. Production hashing is
	// unchanged; this seam proves the additive hash index and lookup remain
	// correct even in the theoretical collision case.
	forcedHash := strings.Repeat("0", 64)
	require.NoError(t, db.Exec(`INSERT INTO external_identity_claims
		(provider, subject, subject_hash, user_id) VALUES (?, ?, ?, ?), (?, ?, ?, ?)`,
		ExternalIdentityProviderOIDC, "collision-subject-a", forcedHash, owners[0].Id,
		ExternalIdentityProviderOIDC, "collision-subject-b", forcedHash, owners[1].Id,
	).Error)
	columns, unique := requirePortableIndex(t, db, &ExternalIdentityClaim{},
		ExternalIdentityClaim{}.TableName(), "idx_external_identity_subject_hash")
	assert.Equal(t, []string{"provider", "subject_hash"}, columns)
	assert.False(t, unique, "the additive hash lookup must not reject a reference-valid row")

	for i, subject := range []string{"collision-subject-a", "collision-subject-b"} {
		claim, err := findExternalIdentityClaimByHashWithTx(
			db, ExternalIdentityProviderOIDC, subject, forcedHash,
		)
		require.NoError(t, err)
		assert.Equal(t, owners[i].Id, claim.UserId)
	}

	// Startup deterministically repairs stale hashes without treating a hash
	// collision as duplicate identity ownership.
	require.NoError(t, migrateDB())
	for i, subject := range []string{"collision-subject-a", "collision-subject-b"} {
		claim, err := FindExternalIdentityClaimWithTx(db, ExternalIdentityProviderOIDC, subject)
		require.NoError(t, err)
		assert.Equal(t, owners[i].Id, claim.UserId)
		assert.Equal(t, externalIdentitySubjectHash(subject), claim.SubjectHash)
	}
}

func TestReferenceAuthIdentitySchemasSQLitePreserveLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&User{},
		&pinnedReferenceTwoFABackupCodeSchema{},
		&pinnedReferenceExternalIdentityClaimSchema{},
	))
	owners := []User{
		{Username: "legacy-auth-owner-a", Password: "password"},
		{Username: "legacy-auth-owner-b", Password: "password"},
	}
	require.NoError(t, db.Create(&owners).Error)

	usedAt := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	deletedAt := time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC)
	backup := pinnedReferenceTwoFABackupCodeSchema{
		UserId: owners[0].Id, CodeHash: "legacy-backup-hash", IsUsed: true,
		UsedAt: &usedAt, CreatedAt: usedAt.Add(-time.Hour),
		DeletedAt: gorm.DeletedAt{Time: deletedAt, Valid: true},
	}
	require.NoError(t, db.Create(&backup).Error)
	duplicateBackup := backup
	duplicateBackup.Id = 0
	require.NoError(t, db.Create(&duplicateBackup).Error,
		"a reference-valid duplicate must survive target migration")
	claim := pinnedReferenceExternalIdentityClaimSchema{
		Provider:  ExternalIdentityProviderOIDC,
		Subject:   strings.Repeat("i", maxBuiltInExternalIdentitySubjectCharacters),
		UserId:    owners[1].Id,
		CreatedAt: usedAt,
	}
	require.NoError(t, db.Create(&claim).Error)

	require.NoError(t, migrateDB())
	assertReferenceTwoFABackupCodeSchemaMatchesSQLite(t, db)
	assertReferenceExternalIdentityClaimSchemaMatchesSQLite(t, db)
	assertReferenceAuthIdentitySchema(t, db)

	var migratedBackup TwoFABackupCode
	require.NoError(t, db.Unscoped().First(&migratedBackup, backup.Id).Error)
	assert.Equal(t, backup.UserId, migratedBackup.UserId)
	assert.Equal(t, backup.CodeHash, migratedBackup.CodeHash)
	assert.Equal(t, backup.IsUsed, migratedBackup.IsUsed)
	require.NotNil(t, migratedBackup.UsedAt)
	assert.True(t, backup.UsedAt.Equal(*migratedBackup.UsedAt))
	assert.Equal(t, backup.DeletedAt.Valid, migratedBackup.DeletedAt.Valid)
	assert.True(t, backup.DeletedAt.Time.Equal(migratedBackup.DeletedAt.Time))
	var duplicateBackupCount int64
	require.NoError(t, db.Unscoped().Model(&TwoFABackupCode{}).
		Where("user_id = ? AND code_hash = ?", backup.UserId, backup.CodeHash).
		Count(&duplicateBackupCount).Error)
	assert.EqualValues(t, 2, duplicateBackupCount)

	var migratedClaim ExternalIdentityClaim
	require.NoError(t, db.First(&migratedClaim, claim.Id).Error)
	assert.Equal(t, claim.Provider, migratedClaim.Provider)
	assert.Equal(t, claim.Subject, migratedClaim.Subject)
	assert.Equal(t, claim.UserId, migratedClaim.UserId)
	assert.True(t, claim.CreatedAt.Equal(migratedClaim.CreatedAt))
	assert.Equal(t, externalIdentitySubjectHash(claim.Subject), migratedClaim.SubjectHash)

	backupColumns := sqliteColumnSignatures(t, db, TwoFABackupCode{}.TableName())
	backupIndexes := sqliteIndexSignatures(t, db, TwoFABackupCode{}.TableName())
	claimColumns := sqliteColumnSignatures(t, db, ExternalIdentityClaim{}.TableName())
	claimIndexes := sqliteIndexSignatures(t, db, ExternalIdentityClaim{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, backupColumns, sqliteColumnSignatures(t, db, TwoFABackupCode{}.TableName()))
	assert.Equal(t, backupIndexes, sqliteIndexSignatures(t, db, TwoFABackupCode{}.TableName()))
	assert.Equal(t, claimColumns, sqliteColumnSignatures(t, db, ExternalIdentityClaim{}.TableName()))
	assert.Equal(t, claimIndexes, sqliteIndexSignatures(t, db, ExternalIdentityClaim{}.TableName()))
}

func TestReferenceExternalIdentityClaimSchemaSQLiteRestoresDirectIndexOnHashedLegacyTable(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&User{}, &legacyHashedExternalIdentityClaimSchema{}))
	owner := User{Username: "legacy-hashed-identity-owner", Password: "password"}
	require.NoError(t, db.Create(&owner).Error)
	legacy := legacyHashedExternalIdentityClaimSchema{
		Provider:    ExternalIdentityProviderTelegram,
		Subject:     strings.Repeat("t", maxBuiltInExternalIdentitySubjectCharacters),
		SubjectHash: externalIdentitySubjectHash(strings.Repeat("t", maxBuiltInExternalIdentitySubjectCharacters)),
		UserId:      owner.Id,
		CreatedAt:   time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC),
	}
	require.NoError(t, db.Create(&legacy).Error)
	assert.False(t, db.Migrator().HasIndex(&ExternalIdentityClaim{}, "idx_external_identity_subject"))
	assert.True(t, db.Migrator().HasIndex(&ExternalIdentityClaim{}, "ux_external_identity_subject"))

	require.NoError(t, migrateDB())
	assertReferenceExternalIdentityClaimSchemaMatchesSQLite(t, db)
	assertReferenceAuthIdentitySchema(t, db)
	assert.False(t, db.Migrator().HasIndex(&ExternalIdentityClaim{}, "ux_external_identity_subject"))
	var migrated ExternalIdentityClaim
	require.NoError(t, db.First(&migrated, legacy.Id).Error)
	assert.Equal(t, legacy.Provider, migrated.Provider)
	assert.Equal(t, legacy.Subject, migrated.Subject)
	assert.Equal(t, legacy.SubjectHash, migrated.SubjectHash)
	assert.Equal(t, legacy.UserId, migrated.UserId)
	assert.True(t, legacy.CreatedAt.Equal(migrated.CreatedAt))

	columnsBefore := sqliteColumnSignatures(t, db, ExternalIdentityClaim{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, ExternalIdentityClaim{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, ExternalIdentityClaim{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, ExternalIdentityClaim{}.TableName()))
}

func TestReferenceTwoFABackupCodeSchemaSQLiteRemovesLegacyUniquenessWithoutRewritingRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacyUniqueTwoFABackupCodeSchema{}))
	createdAt := time.Date(2024, 4, 5, 6, 7, 8, 0, time.UTC)
	legacy := legacyUniqueTwoFABackupCodeSchema{
		UserId: 71, CodeHash: "formerly-unique", IsUsed: false, CreatedAt: createdAt,
	}
	require.NoError(t, db.Create(&legacy).Error)
	assert.True(t, db.Migrator().HasIndex(&TwoFABackupCode{}, "ux_two_fa_backup_user_code"))

	require.NoError(t, migrateDB())
	assertReferenceTwoFABackupCodeSchemaMatchesSQLite(t, db)
	assertReferenceAuthIdentitySchema(t, db)
	assert.False(t, db.Migrator().HasIndex(&TwoFABackupCode{}, "ux_two_fa_backup_user_code"))
	var migrated TwoFABackupCode
	require.NoError(t, db.First(&migrated, legacy.Id).Error)
	assert.Equal(t, legacy.UserId, migrated.UserId)
	assert.Equal(t, legacy.CodeHash, migrated.CodeHash)
	assert.Equal(t, legacy.IsUsed, migrated.IsUsed)
	assert.True(t, legacy.CreatedAt.Equal(migrated.CreatedAt))

	require.NoError(t, db.Exec(`INSERT INTO two_fa_backup_codes
		(user_id, code_hash, is_used, created_at) VALUES (?, ?, ?, ?)`,
		legacy.UserId, legacy.CodeHash, true, createdAt.Add(time.Minute)).Error,
		"the migrated schema must restore the reference's duplicate-row behavior")
	assertRowCount(t, db, TwoFABackupCode{}.TableName(),
		"user_id = 71 AND code_hash = 'formerly-unique'", 2)

	columnsBefore := sqliteColumnSignatures(t, db, TwoFABackupCode{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, TwoFABackupCode{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, TwoFABackupCode{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, TwoFABackupCode{}.TableName()))
}

func TestReferenceTwoFABackupCodeSchemaPreflightRejectsAmbiguousLegacyRows(t *testing.T) {
	tests := []struct {
		name      string
		insertSQL string
		rows      int64
		where     string
	}{
		{
			name:      "blank code hash",
			insertSQL: `INSERT INTO two_fa_backup_codes (id, user_id, code_hash) VALUES (1, 7, '')`,
			rows:      1,
			where:     "code_hash = ''",
		},
		{
			name:      "missing owner",
			insertSQL: `INSERT INTO two_fa_backup_codes (id, user_id, code_hash) VALUES (1, NULL, 'orphan')`,
			rows:      1,
			where:     "user_id IS NULL",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newReferenceSchemaTestDB(t)
			require.NoError(t, db.Exec(`CREATE TABLE two_fa_backup_codes (
				id INTEGER PRIMARY KEY, user_id INTEGER, code_hash varchar(255),
				is_used numeric, used_at datetime, created_at datetime, deleted_at datetime
			)`).Error)
			require.NoError(t, db.Exec(test.insertSQL).Error)

			err := migrateDB()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrAmbiguousPersistentIdentity)
			assertRowCount(t, db, TwoFABackupCode{}.TableName(), "", test.rows)
			if test.where != "" {
				assertRowCount(t, db, TwoFABackupCode{}.TableName(), test.where, test.rows)
			}
			assert.False(t, db.Migrator().HasIndex(&TwoFABackupCode{}, "ux_two_fa_backup_user_code"))
		})
	}
}

func TestReferenceExternalIdentityClaimSchemaPreflightRejectsUnrepresentableLegacyRows(t *testing.T) {
	tests := []struct {
		name      string
		insertSQL string
		rows      int64
		where     string
	}{
		{
			name: "duplicate subject ownership",
			insertSQL: `INSERT INTO external_identity_claims (id, provider, subject, user_id) VALUES
				(1, 'github', 'duplicate', 1), (2, 'github', 'duplicate', 2)`,
			rows: 2,
		},
		{
			name:      "subject exceeds reference width",
			insertSQL: `INSERT INTO external_identity_claims (id, provider, subject, user_id) VALUES (1, 'oidc', '` + strings.Repeat("x", maxBuiltInExternalIdentitySubjectCharacters+1) + `', 1)`,
			rows:      1,
			where:     "LENGTH(subject) = 129",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newReferenceSchemaTestDB(t)
			require.NoError(t, db.Exec(`CREATE TABLE external_identity_claims (
				id INTEGER PRIMARY KEY, provider varchar(32) NOT NULL,
				subject varchar(256) NOT NULL, user_id INTEGER NOT NULL, created_at datetime
			)`).Error)
			require.NoError(t, db.Exec(test.insertSQL).Error)

			err := migrateDB()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrAmbiguousPersistentIdentity)
			assertRowCount(t, db, ExternalIdentityClaim{}.TableName(), "", test.rows)
			if test.where != "" {
				assertRowCount(t, db, ExternalIdentityClaim{}.TableName(), test.where, test.rows)
			}
			assert.False(t, db.Migrator().HasIndex(&ExternalIdentityClaim{}, "idx_external_identity_subject"))
			var emptyHashes int64
			require.NoError(t, db.Table(ExternalIdentityClaim{}.TableName()).
				Where("subject_hash = ''").Count(&emptyHashes).Error)
			assert.Equal(t, test.rows, emptyHashes,
				"the rejected preflight must roll back every tentative hash backfill")
		})
	}
}

func TestReferenceExternalIdentityClaimSchemaPreflightHonorsDatabaseCollation(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&User{}))
	owners := []User{
		{Username: "collation-owner-a", Password: "password"},
		{Username: "collation-owner-b", Password: "password"},
	}
	require.NoError(t, db.Create(&owners).Error)
	require.NoError(t, db.Exec(`CREATE TABLE external_identity_claims (
		id INTEGER PRIMARY KEY, provider varchar(32) NOT NULL,
		subject varchar(128) COLLATE NOCASE NOT NULL,
		subject_hash char(64) NOT NULL, user_id INTEGER NOT NULL, created_at datetime
	)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO external_identity_claims
		(id, provider, subject, subject_hash, user_id) VALUES
		(1, ?, ?, ?, ?), (2, ?, ?, ?, ?)`,
		ExternalIdentityProviderOIDC, "Case-Sensitive", externalIdentitySubjectHash("Case-Sensitive"), owners[0].Id,
		ExternalIdentityProviderOIDC, "case-sensitive", externalIdentitySubjectHash("case-sensitive"), owners[1].Id,
	).Error)

	err := migrateDB()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAmbiguousPersistentIdentity)
	assertRowCount(t, db, ExternalIdentityClaim{}.TableName(), "", 2)
	assertRowCount(t, db, ExternalIdentityClaim{}.TableName(), "subject = 'Case-Sensitive'", 2)
	assert.False(t, db.Migrator().HasIndex(&ExternalIdentityClaim{}, "idx_external_identity_subject"))
	for _, owner := range owners {
		var persisted User
		require.NoError(t, db.First(&persisted, owner.Id).Error)
		assert.Empty(t, persisted.OidcId, "a rejected migration must roll back compatibility mirror writes")
	}
}

func parseAuthIdentitySchema(t *testing.T, entity any) *schema.Schema {
	t.Helper()
	parsed, err := schema.Parse(entity, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	return parsed
}

func assertAuthIdentityReferenceFields(t *testing.T, reference, target *schema.Schema, allowedExtra ...string) {
	t.Helper()
	extras := make(map[string]bool, len(allowedExtra))
	for _, name := range allowedExtra {
		extras[name] = true
	}
	referencePersistent := 0
	for _, field := range reference.Fields {
		if field.DBName != "" {
			referencePersistent++
		}
	}
	targetPersistent := 0
	for _, field := range target.Fields {
		if field.DBName != "" {
			targetPersistent++
		}
	}
	assert.Equal(t, referencePersistent+len(extras), targetPersistent,
		"the target must contain only reviewed additive fields")
	for _, referenceField := range reference.Fields {
		if referenceField.DBName == "" {
			continue
		}
		targetField := target.LookUpField(referenceField.Name)
		require.NotNil(t, targetField, "%s is missing", referenceField.Name)
		assert.Equal(t, referenceField.DBName, targetField.DBName, referenceField.Name+" column")
		assert.Equal(t, referenceField.PrimaryKey, targetField.PrimaryKey, referenceField.Name+" primary key")
		assert.Equal(t, referenceField.AutoIncrement, targetField.AutoIncrement, referenceField.Name+" auto increment")
		assert.Equal(t, referenceField.NotNull, targetField.NotNull, referenceField.Name+" nullability")
		assert.Equal(t, referenceField.HasDefaultValue, targetField.HasDefaultValue, referenceField.Name+" default presence")
		assert.Equal(t, referenceField.DefaultValue, targetField.DefaultValue, referenceField.Name+" default")
		assert.Equal(t, referenceField.Size, targetField.Size, referenceField.Name+" size")
	}
}

func assertAuthIdentityReferenceDialectTypes(t *testing.T, reference, target *schema.Schema) {
	t.Helper()
	mysqlDB, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user@tcp(127.0.0.1:1)/db?charset=utf8mb4&parseTime=true",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true, DryRun: true})
	require.NoError(t, err)
	postgresDB, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=gorm dbname=gorm sslmode=disable",
	}), &gorm.Config{DisableAutomaticPing: true, DryRun: true})
	require.NoError(t, err)
	for _, dialect := range []gorm.Dialector{mysqlDB.Dialector, postgresDB.Dialector} {
		t.Run(dialect.Name(), func(t *testing.T) {
			for _, referenceField := range reference.Fields {
				if referenceField.DBName == "" {
					continue
				}
				targetField := target.LookUpField(referenceField.Name)
				require.NotNil(t, targetField)
				assert.Equal(t, strings.ToLower(dialect.DataTypeOf(referenceField)), strings.ToLower(dialect.DataTypeOf(targetField)),
					referenceField.Name+" type")
			}
		})
	}
}

func assertAuthIdentityReferenceIndexes(t *testing.T, reference, target *schema.Schema, allowedExtra ...string) {
	t.Helper()
	assert.Len(t, target.ParseIndexes(), len(reference.ParseIndexes())+len(allowedExtra),
		"the target must contain only reviewed additive indexes")
	for _, referenceIndex := range reference.ParseIndexes() {
		targetIndex := target.LookIndex(referenceIndex.Name)
		require.NotNil(t, targetIndex, "missing reference index %s", referenceIndex.Name)
		assert.Equal(t, referenceIndex.Class, targetIndex.Class, referenceIndex.Name+" class")
		require.Len(t, targetIndex.Fields, len(referenceIndex.Fields), referenceIndex.Name+" fields")
		for i := range referenceIndex.Fields {
			assert.Equal(t, referenceIndex.Fields[i].DBName, targetIndex.Fields[i].DBName, referenceIndex.Name+" column")
			assert.Equal(t, referenceIndex.Fields[i].Priority, targetIndex.Fields[i].Priority, referenceIndex.Name+" priority")
		}
	}
}

func assertParsedAuthIdentityIndex(t *testing.T, parsed *schema.Schema, name string, columns []string, unique bool) {
	t.Helper()
	index := parsed.LookIndex(name)
	require.NotNil(t, index, "missing index %s", name)
	assert.Equal(t, unique, index.Class == "UNIQUE", name+" uniqueness")
	actualColumns := make([]string, 0, len(index.Fields))
	for _, field := range index.Fields {
		actualColumns = append(actualColumns, field.DBName)
	}
	assert.Equal(t, columns, actualColumns, name+" columns")
}

func assertReferenceTwoFABackupCodeSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(t.TempDir()+"/reference-backup-code.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&pinnedReferenceTwoFABackupCodeSchema{}))
	assert.Equal(t,
		sqliteColumnSignatures(t, reference, TwoFABackupCode{}.TableName()),
		sqliteColumnSignatures(t, db, TwoFABackupCode{}.TableName()),
	)
	assert.Equal(t,
		sqliteIndexSignatures(t, reference, TwoFABackupCode{}.TableName()),
		sqliteIndexSignatures(t, db, TwoFABackupCode{}.TableName()),
	)
}

func assertReferenceExternalIdentityClaimSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(t.TempDir()+"/reference-external-identity.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&pinnedReferenceExternalIdentityClaimSchema{}))
	assert.Equal(t,
		sqliteColumnSignatures(t, reference, ExternalIdentityClaim{}.TableName()),
		filterAuthIdentityColumns(sqliteColumnSignatures(t, db, ExternalIdentityClaim{}.TableName()), "subject_hash"),
	)
	assert.Equal(t,
		sqliteIndexSignatures(t, reference, ExternalIdentityClaim{}.TableName()),
		filterAuthIdentityIndexes(sqliteIndexSignatures(t, db, ExternalIdentityClaim{}.TableName()), "idx_external_identity_subject_hash"),
	)
}

// assertReferenceAuthIdentitySchema is driver-neutral so the credential-free
// checks, SQLite suite, and opt-in live server migration gate share one set of
// metadata assertions.
func assertReferenceAuthIdentitySchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, expected := range []struct {
		entity  any
		table   string
		name    string
		columns []string
		unique  bool
	}{
		{&TwoFABackupCode{}, "two_fa_backup_codes", "idx_two_fa_backup_codes_user_id", []string{"user_id"}, false},
		{&TwoFABackupCode{}, "two_fa_backup_codes", "idx_two_fa_backup_codes_deleted_at", []string{"deleted_at"}, false},
		{&ExternalIdentityClaim{}, "external_identity_claims", "idx_external_identity_subject", []string{"provider", "subject"}, true},
		{&ExternalIdentityClaim{}, "external_identity_claims", "idx_external_identity_user", []string{"provider", "user_id"}, true},
		{&ExternalIdentityClaim{}, "external_identity_claims", "idx_external_identity_claims_user_id", []string{"user_id"}, false},
		{&ExternalIdentityClaim{}, "external_identity_claims", "idx_external_identity_subject_hash", []string{"provider", "subject_hash"}, false},
	} {
		columns, unique := requirePortableIndex(t, db, expected.entity, expected.table, expected.name)
		assert.Equal(t, expected.columns, columns, expected.name+" columns")
		assert.Equal(t, expected.unique, unique, expected.name+" uniqueness")
	}

	for _, column := range []string{"user_id", "code_hash"} {
		assertColumnNullable(t, db, TwoFABackupCode{}.TableName(), column, false)
		assertAuthIdentityColumnHasNoDefault(t, db, TwoFABackupCode{}.TableName(), column)
	}
	for _, column := range []string{"is_used", "used_at", "created_at", "deleted_at"} {
		assertColumnNullable(t, db, TwoFABackupCode{}.TableName(), column, true)
		assertAuthIdentityColumnHasNoDefault(t, db, TwoFABackupCode{}.TableName(), column)
	}
	for _, column := range []string{"provider", "subject", "user_id", "subject_hash"} {
		assertColumnNullable(t, db, ExternalIdentityClaim{}.TableName(), column, false)
		assertAuthIdentityColumnHasNoDefault(t, db, ExternalIdentityClaim{}.TableName(), column)
	}
	assertColumnNullable(t, db, ExternalIdentityClaim{}.TableName(), "created_at", true)
	assertAuthIdentityColumnHasNoDefault(t, db, ExternalIdentityClaim{}.TableName(), "created_at")

	if db.Dialector.Name() != "sqlite" {
		assertColumnLength(t, db, TwoFABackupCode{}.TableName(), "code_hash", 255)
		assertColumnLength(t, db, ExternalIdentityClaim{}.TableName(), "provider", 32)
		assertColumnLength(t, db, ExternalIdentityClaim{}.TableName(), "subject", 128)
		assertColumnLength(t, db, ExternalIdentityClaim{}.TableName(), "subject_hash", 64)
	}
}

func assertAuthIdentityColumnHasNoDefault(t *testing.T, db *gorm.DB, table, column string) {
	t.Helper()
	_, hasDefault := requireColumnType(t, db, table, column).DefaultValue()
	assert.False(t, hasDefault, "%s.%s must not gain a database default", table, column)
}

func filterAuthIdentityColumns(columns []sqliteColumnSignature, excluded ...string) []sqliteColumnSignature {
	exclusions := make(map[string]bool, len(excluded))
	for _, name := range excluded {
		exclusions[name] = true
	}
	result := make([]sqliteColumnSignature, 0, len(columns))
	for _, column := range columns {
		if !exclusions[column.Name] {
			result = append(result, column)
		}
	}
	return result
}

func filterAuthIdentityIndexes(indexes []sqliteIndexSignature, excluded ...string) []sqliteIndexSignature {
	exclusions := make(map[string]bool, len(excluded))
	for _, name := range excluded {
		exclusions[name] = true
	}
	result := make([]sqliteIndexSignature, 0, len(indexes))
	for _, index := range indexes {
		if !exclusions[index.Name] {
			result = append(result, index)
		}
	}
	return result
}
