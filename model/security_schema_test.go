package model

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNormalizeVerifiedEmailBounds(t *testing.T) {
	accepted := strings.Repeat("a", 38) + "@example.com"
	assert.Len(t, accepted, 50)
	normalized, key, err := NormalizeVerifiedEmail(accepted)
	require.NoError(t, err)
	assert.Equal(t, accepted, normalized)
	assert.Len(t, key, 64)

	rejected := strings.Repeat("a", 39) + "@example.com"
	assert.Len(t, rejected, 51)
	_, _, err = NormalizeVerifiedEmail(rejected)
	assert.Error(t, err)
}

func newSecuritySchemaTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "security-schema.db")), &gorm.Config{})
	require.NoError(t, err)
	previousDB, previousLogDB := DB, LOG_DB
	DB, LOG_DB = db, db
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
	})
	return db
}

// securityLegacyUserSessionSchema keeps the pre-alignment nullable lifecycle
// fields while omitting refresh-hash uniqueness, so the security preparation
// test can exercise deterministic duplicate-session removal without relying
// on hand-written SQLite DDL parsing.
type securityLegacyUserSessionSchema struct {
	SID                string `gorm:"column:sid;primaryKey;type:varchar(64);not null"`
	UserID             int    `gorm:"not null"`
	Version            int64  `gorm:"type:bigint;not null;default:1"`
	UserAuthVersion    int64  `gorm:"type:bigint;not null"`
	Status             string `gorm:"type:varchar(16);not null"`
	RefreshHash        string `gorm:"type:char(64);not null"`
	PreviousValidUntil int64
	LoginMethod        string `gorm:"type:varchar(32)"`
	LastActiveAt       int64
	ExpiresAt          int64 `gorm:"type:bigint;not null"`
	RevokedAt          int64
}

func (securityLegacyUserSessionSchema) TableName() string { return "user_sessions" }

func TestSecuritySchemaConstraints(t *testing.T) {
	db := newSecuritySchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&User{}, &UserSession{}, &AuthFlow{}, &UserOAuthBinding{}, &TwoFA{},
		&TwoFABackupCode{}, &PasskeyCredential{}, &ExternalIdentityClaim{},
		&TopUp{}, &SubscriptionOrder{}, &SubscriptionPreConsumeRecord{},
	))

	for table, columns := range map[string][]string{
		"users":                            {"auth_version"},
		"user_sessions":                    {"sid", "user_id", "version", "user_auth_version", "status", "refresh_hash", "previous_valid_until", "login_method", "last_active_at", "expires_at", "revoked_at"},
		"user_oauth_bindings":              {"user_id", "provider_id", "provider_user_id"},
		"external_identity_claims":         {"provider", "subject", "subject_hash", "user_id"},
		"top_ups":                          {"trade_no"},
		"subscription_orders":              {"trade_no"},
		"subscription_pre_consume_records": {"request_id"},
	} {
		assertColumnsNotNull(t, db, table, columns...)
	}
	assert.True(t, db.Migrator().HasIndex(&User{}, "ux_users_verified_email_key"))
	assert.True(t, db.Migrator().HasIndex(&UserSession{}, "ux_user_sessions_refresh_hash"))
	assert.False(t, db.Migrator().HasIndex(&TwoFABackupCode{}, "ux_two_fa_backup_user_code"))
	assert.True(t, db.Migrator().HasIndex(&UserOAuthBinding{}, "ux_user_provider"))
	assert.True(t, db.Migrator().HasIndex(&UserOAuthBinding{}, "ux_provider_userid"))
	assert.False(t, db.Migrator().HasIndex(&ExternalIdentityClaim{}, "ux_external_identity_subject"))
	assert.True(t, db.Migrator().HasIndex(&ExternalIdentityClaim{}, "idx_external_identity_subject_hash"))
	assert.True(t, db.Migrator().HasIndex(&ExternalIdentityClaim{}, "idx_external_identity_subject"))
	assert.True(t, db.Migrator().HasIndex(&ExternalIdentityClaim{}, "idx_external_identity_user"))
	assert.True(t, db.Migrator().HasIndex(&PasskeyCredential{}, "ux_passkey_user"))

	now := time.Now()
	session := UserSession{
		SID: "sid-one", UserID: 1, UserAuthVersion: 1, Status: "active",
		RefreshHash: "refresh-one", ExpiresAt: now.Add(time.Hour).Unix(),
	}
	require.NoError(t, db.Create(&session).Error)
	assert.EqualValues(t, 1, session.Version)
	duplicateRefresh := session
	duplicateRefresh.SID = "sid-two"
	assert.Error(t, db.Create(&duplicateRefresh).Error)

	firstCode := TwoFABackupCode{UserId: 1, CodeHash: "code-hash", CreatedAt: now}
	require.NoError(t, db.Create(&firstCode).Error)
	require.NoError(t, db.Create(&TwoFABackupCode{UserId: 1, CodeHash: "code-hash", CreatedAt: now}).Error,
		"the reference schema permits duplicate recovery-code rows")

	firstBinding := UserOAuthBinding{UserId: 1, ProviderId: 7, ProviderUserId: "subject-one"}
	require.NoError(t, db.Create(&firstBinding).Error)
	assert.Error(t, db.Create(&UserOAuthBinding{UserId: 1, ProviderId: 7, ProviderUserId: "subject-two"}).Error)
	assert.Error(t, db.Create(&UserOAuthBinding{UserId: 2, ProviderId: 7, ProviderUserId: "subject-one"}).Error)

	assert.Error(t, db.Create(&TopUp{TradeNo: " "}).Error)
	assert.Error(t, db.Create(&SubscriptionOrder{}).Error)
	assert.Error(t, db.Create(&SubscriptionPreConsumeRecord{}).Error)
	assert.Error(t, db.Create(&AuthFlow{TokenHash: "", Purpose: "oauth", ExpiresAt: now.Add(time.Minute)}).Error)
	assert.Error(t, db.Create(&PasskeyCredential{UserID: 1, CredentialID: "credential", PublicKey: " "}).Error)
	require.NoError(t, db.Create(&PasskeyCredential{UserID: 1, CredentialID: "credential-one", PublicKey: "key-one"}).Error)
	assert.Error(t, db.Create(&PasskeyCredential{UserID: 1, CredentialID: "credential-two", PublicKey: "key-two"}).Error)
	assert.Error(t, db.Create(&ExternalIdentityClaim{Provider: "github", Subject: "", UserId: 1}).Error)
}

func TestVerifiedEmailOwnershipAndMigrationFailClosed(t *testing.T) {
	db := newSecuritySchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&User{}))

	first := User{Username: "email-owner", Password: "password", Email: " Owner@Example.com ", EmailVerified: true}
	require.NoError(t, db.Create(&first).Error)
	require.NotNil(t, first.VerifiedEmailKey)
	assert.Equal(t, "owner@example.com", first.Email)

	duplicate := User{Username: "email-duplicate", Password: "password", Email: "OWNER@example.com", EmailVerified: true}
	assert.Error(t, db.Create(&duplicate).Error, "verified ownership must be case-insensitively unique")

	unverifiedA := User{Username: "unverified-a", Password: "password", Email: "same@example.com"}
	unverifiedB := User{Username: "unverified-b", Password: "password", Email: "same@example.com"}
	require.NoError(t, db.Create(&unverifiedA).Error)
	require.NoError(t, db.Create(&unverifiedB).Error)
	assert.Nil(t, unverifiedA.VerifiedEmailKey)
	assert.Nil(t, unverifiedB.VerifiedEmailKey)

	require.NoError(t, db.Unscoped().Model(&first).Update("verified_email_key", nil).Error)
	require.NoError(t, initializeVerifiedEmailKeys())
	var repaired User
	require.NoError(t, db.First(&repaired, first.Id).Error)
	require.NotNil(t, repaired.VerifiedEmailKey)
	_, expectedKey, err := NormalizeVerifiedEmail("owner@example.com")
	require.NoError(t, err)
	assert.Equal(t, expectedKey, *repaired.VerifiedEmailKey)
}

func TestVerifiedEmailMigrationRejectsAmbiguousLegacyOwners(t *testing.T) {
	db := newSecuritySchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&User{}))
	// Bypass the model hook to emulate pre-key legacy rows.
	require.NoError(t, db.Exec(`INSERT INTO users (id, username, password, email, email_verified, auth_version)
		VALUES (101, 'legacy-email-a', 'password', 'Case@Example.com', 1, 1),
		       (102, 'legacy-email-b', 'password', 'case@example.com', 1, 1)`).Error)
	err := initializeVerifiedEmailKeys()
	assert.ErrorIs(t, err, ErrAmbiguousPersistentIdentity)
}

func TestPrepareSecuritySchemaMigrationPurgesDeletedPasskeyTombstones(t *testing.T) {
	db := newSecuritySchemaTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE passkey_credentials (
		id INTEGER PRIMARY KEY, user_id INTEGER, credential_id TEXT,
		public_key TEXT, deleted_at DATETIME
	)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO passkey_credentials
		(id, user_id, credential_id, public_key, deleted_at) VALUES
		(1, 7, 'retired', 'retired-key', CURRENT_TIMESTAMP),
		(2, 7, 'active', 'active-key', NULL)`).Error)

	require.NoError(t, prepareSecuritySchemaMigration())
	var rows int64
	require.NoError(t, db.Table("passkey_credentials").Count(&rows).Error)
	assert.Equal(t, int64(1), rows)
	require.NoError(t, db.AutoMigrate(&PasskeyCredential{}))
	assert.True(t, db.Migrator().HasIndex(&PasskeyCredential{}, "ux_passkey_user"))
}

func TestPrepareSecuritySchemaMigrationRepairsOnlyFailClosedLegacyRows(t *testing.T) {
	db := newSecuritySchemaTestDB(t)
	legacyDDL := []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, auth_version INTEGER)`,
		`CREATE TABLE auth_flows (id INTEGER PRIMARY KEY, token_hash TEXT, purpose TEXT, expires_at DATETIME)`,
		`CREATE TABLE two_fa_backup_codes (id INTEGER PRIMARY KEY, user_id INTEGER, code_hash TEXT, deleted_at DATETIME)`,
		`CREATE TABLE top_ups (id INTEGER PRIMARY KEY, trade_no TEXT)`,
		`CREATE TABLE subscription_orders (id INTEGER PRIMARY KEY, trade_no TEXT)`,
		`CREATE TABLE subscription_pre_consume_records (id INTEGER PRIMARY KEY, request_id TEXT)`,
	}
	for _, statement := range legacyDDL {
		require.NoError(t, db.Exec(statement).Error)
	}
	require.NoError(t, db.AutoMigrate(&securityLegacyUserSessionSchema{}))

	require.NoError(t, db.Exec(`INSERT INTO users (id, auth_version) VALUES (1, 0), (2, NULL)`).Error)
	future := time.Now().Add(time.Hour)
	require.NoError(t, db.Exec(`INSERT INTO user_sessions
		(sid, user_id, version, user_auth_version, status, refresh_hash,
		 previous_valid_until, login_method, last_active_at, expires_at, revoked_at) VALUES
		('valid', 1, 1, 1, 'active', 'unique-refresh', 0, 'password', 1, ?, 0),
		('duplicate-a', 1, 1, 1, 'active', 'shared-refresh', 0, 'password', 1, ?, 0),
		('duplicate-b', 2, 1, 1, 'active', 'shared-refresh', 0, 'password', 1, ?, 0),
		('invalid-version', 1, 0, 1, 'active', 'invalid-refresh', 0, 'password', 1, ?, 0)`,
		future.Unix(), future.Unix(), future.Unix(), future.Unix()).Error)
	require.NoError(t, db.Exec(`INSERT INTO auth_flows (id, token_hash, purpose, expires_at) VALUES
		(1, 'valid-flow', 'oauth', ?),
		(2, 'shared-flow', 'oauth', ?),
		(3, 'shared-flow', 'oauth', ?),
		(4, '', 'oauth', ?)`, future, future, future, future).Error)
	require.NoError(t, db.Exec(`INSERT INTO two_fa_backup_codes (id, user_id, code_hash) VALUES
		(1, 1, 'valid-code')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO top_ups (id, trade_no) VALUES (10, NULL), (11, ' ')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO subscription_orders (id, trade_no) VALUES (20, NULL)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO subscription_pre_consume_records (id, request_id) VALUES (30, '')`).Error)

	require.NoError(t, prepareSecuritySchemaMigration())

	var authVersions []int64
	require.NoError(t, db.Table("users").Order("id").Pluck("auth_version", &authVersions).Error)
	assert.Equal(t, []int64{1, 1}, authVersions)
	assertRowCount(t, db, "user_sessions", "", 1)
	assertRowCount(t, db, "user_sessions", "sid = 'valid'", 1)
	assertRowCount(t, db, "auth_flows", "", 1)
	assertRowCount(t, db, "auth_flows", "token_hash = 'valid-flow'", 1)
	assertRowCount(t, db, "two_fa_backup_codes", "", 1)
	assertRowCount(t, db, "two_fa_backup_codes", "code_hash = 'valid-code'", 1)

	for _, target := range []struct {
		table    string
		id       int
		column   string
		expected string
	}{
		{"top_ups", 10, "trade_no", "legacy-topup-10"},
		{"top_ups", 11, "trade_no", "legacy-topup-11"},
		{"subscription_orders", 20, "trade_no", "legacy-subscription-20"},
		{"subscription_pre_consume_records", 30, "request_id", "legacy-preconsume-30"},
	} {
		var value string
		require.NoError(t, db.Table(target.table).Select(target.column).Where("id = ?", target.id).Scan(&value).Error)
		assert.Equal(t, target.expected, value)
	}

	// The repaired legacy rows can accept the new portable NOT NULL and unique
	// constraints; this is the same ordering used by migrateDB.
	require.NoError(t, prepareReferenceUserSessionSchemaMigration())
	require.NoError(t, db.AutoMigrate(
		&UserSession{}, &AuthFlow{}, &TwoFABackupCode{},
		&TopUp{}, &SubscriptionOrder{}, &SubscriptionPreConsumeRecord{},
	))
	require.NoError(t, ensureReferenceUserSessionConstraints())
	assert.True(t, db.Migrator().HasIndex(&UserSession{}, "ux_user_sessions_refresh_hash"))
	assert.False(t, db.Migrator().HasIndex(&TwoFABackupCode{}, "ux_two_fa_backup_user_code"))
}

func TestMigrateDBRunsSecurityPreparationBeforeIndexes(t *testing.T) {
	db := newSecuritySchemaTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE user_sessions (
		sid TEXT, user_id INTEGER, version INTEGER, user_auth_version INTEGER,
		status TEXT, refresh_hash TEXT, expires_at INTEGER
	)`).Error)
	expiresAt := time.Now().Add(time.Hour).Unix()
	require.NoError(t, db.Exec(`INSERT INTO user_sessions VALUES
		('first', 1, 1, 1, 'active', 'shared-refresh', ?),
		('second', 2, 1, 1, 'active', 'shared-refresh', ?)`, expiresAt, expiresAt).Error)

	require.NoError(t, migrateDB())
	assertRowCount(t, db, "user_sessions", "", 0)
	assert.True(t, db.Migrator().HasIndex(&UserSession{}, "ux_user_sessions_refresh_hash"))
}

func TestPrepareSecuritySchemaMigrationRefusesAmbiguousDurableRows(t *testing.T) {
	tests := []struct {
		name   string
		ddl    string
		insert string
	}{
		{
			name:   "OAuth subject has multiple owners",
			ddl:    `CREATE TABLE user_oauth_bindings (id INTEGER PRIMARY KEY, user_id INTEGER, provider_id INTEGER, provider_user_id TEXT)`,
			insert: `INSERT INTO user_oauth_bindings VALUES (1, 1, 9, 'subject'), (2, 2, 9, 'subject')`,
		},
		{
			name:   "OAuth user has multiple provider bindings",
			ddl:    `CREATE TABLE user_oauth_bindings (id INTEGER PRIMARY KEY, user_id INTEGER, provider_id INTEGER, provider_user_id TEXT)`,
			insert: `INSERT INTO user_oauth_bindings VALUES (1, 1, 9, 'subject-a'), (2, 1, 9, 'subject-b')`,
		},
		{
			name:   "top-up trade number is duplicated",
			ddl:    `CREATE TABLE top_ups (id INTEGER PRIMARY KEY, trade_no TEXT)`,
			insert: `INSERT INTO top_ups VALUES (1, 'trade'), (2, 'trade')`,
		},
		{
			name:   "subscription trade number is duplicated",
			ddl:    `CREATE TABLE subscription_orders (id INTEGER PRIMARY KEY, trade_no TEXT)`,
			insert: `INSERT INTO subscription_orders VALUES (1, 'trade'), (2, 'trade')`,
		},
		{
			name:   "subscription request id is duplicated",
			ddl:    `CREATE TABLE subscription_pre_consume_records (id INTEGER PRIMARY KEY, request_id TEXT)`,
			insert: `INSERT INTO subscription_pre_consume_records VALUES (1, 'request'), (2, 'request')`,
		},
		{
			name:   "passkey has multiple owners",
			ddl:    `CREATE TABLE passkey_credentials (id INTEGER PRIMARY KEY, user_id INTEGER, credential_id TEXT, public_key TEXT)`,
			insert: `INSERT INTO passkey_credentials VALUES (1, 1, 'credential', 'key-a'), (2, 2, 'credential', 'key-b')`,
		},
		{
			name:   "user has multiple active passkeys",
			ddl:    `CREATE TABLE passkey_credentials (id INTEGER PRIMARY KEY, user_id INTEGER, credential_id TEXT, public_key TEXT)`,
			insert: `INSERT INTO passkey_credentials VALUES (1, 1, 'credential-a', 'key-a'), (2, 1, 'credential-b', 'key-b')`,
		},
		{
			name:   "backup code identity is invalid",
			ddl:    `CREATE TABLE two_fa_backup_codes (id INTEGER PRIMARY KEY, user_id INTEGER, code_hash TEXT)`,
			insert: `INSERT INTO two_fa_backup_codes VALUES (1, 1, '')`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newSecuritySchemaTestDB(t)
			require.NoError(t, db.Exec(test.ddl).Error)
			require.NoError(t, db.Exec(test.insert).Error)
			err := prepareSecuritySchemaMigration()
			assert.True(t, errors.Is(err, ErrAmbiguousPersistentIdentity), "error: %v", err)
		})
	}
}

func assertColumnsNotNull(t *testing.T, db *gorm.DB, table string, names ...string) {
	t.Helper()
	columns, err := db.Migrator().ColumnTypes(table)
	require.NoError(t, err)
	byName := make(map[string]gorm.ColumnType, len(columns))
	for _, column := range columns {
		byName[column.Name()] = column
	}
	for _, name := range names {
		column, ok := byName[name]
		require.True(t, ok, "%s.%s is missing", table, name)
		nullable, known := column.Nullable()
		require.True(t, known, "%s.%s nullability is unknown", table, name)
		assert.False(t, nullable, "%s.%s must be NOT NULL", table, name)
	}
}

func assertRowCount(t *testing.T, db *gorm.DB, table, where string, expected int64) {
	t.Helper()
	query := db.Table(table)
	if where != "" {
		query = query.Where(where)
	}
	var count int64
	require.NoError(t, query.Count(&count).Error)
	assert.Equal(t, expected, count)
}
