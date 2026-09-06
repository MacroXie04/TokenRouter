package store

import (
	"database/sql"
	"path/filepath"
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

// pinnedReferenceUserSessionSchema independently transcribes the complete
// UserSession declaration at the pinned reference commit.
type pinnedReferenceUserSessionSchema struct {
	SID                 string `gorm:"column:sid;type:varchar(64);primaryKey"`
	UserID              int    `gorm:"column:user_id;not null;index:idx_user_sessions_user_status_expiry,priority:1;index:idx_user_sessions_user_created,priority:1"`
	Version             int64  `gorm:"type:bigint;not null;default:1"`
	UserAuthVersion     int64  `gorm:"type:bigint;not null"`
	Status              string `gorm:"type:varchar(16);not null;index:idx_user_sessions_user_status_expiry,priority:2;index:idx_user_sessions_status_revoked,priority:1"`
	RefreshHash         string `gorm:"type:char(64);not null"`
	PreviousRefreshHash string `gorm:"type:varchar(64)"`
	PreviousValidUntil  int64  `gorm:"type:bigint;not null;default:0"`
	LoginMethod         string `gorm:"type:varchar(32);not null"`
	IP                  string `gorm:"type:varchar(64)"`
	UserAgent           string `gorm:"type:text"`
	CreatedAt           int64  `gorm:"autoCreateTime;column:created_at;index:idx_user_sessions_user_created,priority:2"`
	LastActiveAt        int64  `gorm:"type:bigint;not null;column:last_active_at"`
	ExpiresAt           int64  `gorm:"type:bigint;not null;column:expires_at;index:idx_user_sessions_user_status_expiry,priority:3;index:idx_user_sessions_expires_at"`
	RevokedAt           int64  `gorm:"type:bigint;not null;default:0;column:revoked_at;index:idx_user_sessions_status_revoked,priority:2"`
	RevokedReason       string `gorm:"type:varchar(64);column:revoked_reason"`
}

func (pinnedReferenceUserSessionSchema) TableName() string { return "user_sessions" }

// legacyUserSessionSchema captures TokenRouter immediately before this
// alignment. The datetime CreatedAt and unique refresh hash are intentional
// target security/API behavior and remain present throughout the upgrade.
type legacyUserSessionSchema struct {
	SID                 string `gorm:"column:sid;primaryKey;type:varchar(64);not null"`
	UserID              int    `gorm:"not null;index:idx_user_sessions_user_status_expiry,priority:1;index:idx_user_sessions_user_created,priority:1"`
	Version             int64
	UserAuthVersion     int64
	Status              string `gorm:"type:varchar(16);not null;index:idx_user_sessions_user_status_expiry,priority:2;index:idx_user_sessions_status_revoked,priority:1"`
	RefreshHash         string `gorm:"type:char(64);not null;uniqueIndex:ux_user_sessions_refresh_hash"`
	PreviousRefreshHash string `gorm:"type:varchar(64)"`
	PreviousValidUntil  int64
	LoginMethod         string    `gorm:"type:varchar(32)"`
	IP                  string    `gorm:"type:varchar(64)"`
	UserAgent           string    `gorm:"type:text"`
	CreatedAt           time.Time `gorm:"index:idx_user_sessions_user_created,priority:2"`
	LastActiveAt        int64
	ExpiresAt           int64  `gorm:"index:idx_user_sessions_user_status_expiry,priority:3;index:idx_user_sessions_expires_at"`
	RevokedAt           int64  `gorm:"index:idx_user_sessions_status_revoked,priority:2"`
	RevokedReason       string `gorm:"type:varchar(64)"`
}

func (legacyUserSessionSchema) TableName() string { return "user_sessions" }

type expectedUserSessionIndex struct {
	name    string
	columns []string
	unique  bool
}

var expectedReferenceUserSessionIndexes = []expectedUserSessionIndex{
	{name: "idx_user_sessions_user_status_expiry", columns: []string{"user_id", "status", "expires_at"}},
	{name: "idx_user_sessions_user_created", columns: []string{"user_id", "created_at"}},
	{name: "idx_user_sessions_status_revoked", columns: []string{"status", "revoked_at"}},
	{name: "idx_user_sessions_expires_at", columns: []string{"expires_at"}},
}

const targetUserSessionRefreshHashIndex = "ux_user_sessions_refresh_hash"

func TestReferenceUserSessionSchemaPortableDialects(t *testing.T) {
	targetSchema, err := schema.Parse(&UserSession{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceSchema, err := schema.Parse(&pinnedReferenceUserSessionSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	assert.Equal(t, len(referenceSchema.Fields), len(targetSchema.Fields),
		"UserSession must not have unreviewed persistent fields")

	for _, referenceField := range referenceSchema.Fields {
		targetField := targetSchema.LookUpField(referenceField.Name)
		require.NotNil(t, targetField, "UserSession.%s is missing", referenceField.Name)
		assert.Equal(t, referenceField.DBName, targetField.DBName, referenceField.Name+" column")
		assert.Equal(t, referenceField.PrimaryKey, targetField.PrimaryKey, referenceField.Name+" primary key")
		assert.Equal(t, referenceField.AutoIncrement, targetField.AutoIncrement, referenceField.Name+" auto increment")
		if referenceField.Name == "SID" {
			assert.False(t, referenceField.NotNull, "the reference relies on primary-key null rejection")
			assert.True(t, targetField.NotNull, "the target must retain explicit SID null rejection")
		} else {
			assert.Equal(t, referenceField.NotNull, targetField.NotNull, referenceField.Name+" nullability")
		}
		assert.Equal(t, referenceField.HasDefaultValue, targetField.HasDefaultValue, referenceField.Name+" default presence")
		assert.Equal(t, referenceField.DefaultValue, targetField.DefaultValue, referenceField.Name+" default")
		if referenceField.Name == "CreatedAt" {
			assert.NotEqual(t, referenceField.Size, targetField.Size,
				"the target datetime CreatedAt size residual must stay explicit")
		} else {
			assert.Equal(t, referenceField.Size, targetField.Size, referenceField.Name+" size")
		}
	}

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
			for _, referenceField := range referenceSchema.Fields {
				targetField := targetSchema.LookUpField(referenceField.Name)
				require.NotNil(t, targetField)
				if referenceField.Name == "CreatedAt" {
					assert.NotEqual(t, dialect.DataTypeOf(referenceField), dialect.DataTypeOf(targetField),
						"the target datetime CreatedAt residual must stay explicit for %s", dialect.Name())
					continue
				}
				assert.Equal(t, dialect.DataTypeOf(referenceField), dialect.DataTypeOf(targetField),
					"UserSession.%s %s type", referenceField.Name, dialect.Name())
			}
		})
	}

	referenceIndexes := referenceSchema.ParseIndexes()
	targetIndexes := targetSchema.ParseIndexes()
	assert.Len(t, referenceIndexes, len(expectedReferenceUserSessionIndexes))
	assert.Len(t, targetIndexes, len(referenceIndexes)+1,
		"unique refresh hashes must remain the only target-only index")
	for _, referenceIndex := range referenceIndexes {
		targetIndex := targetSchema.LookIndex(referenceIndex.Name)
		require.NotNil(t, targetIndex, "missing UserSession index %s", referenceIndex.Name)
		assert.Equal(t, referenceIndex.Class, targetIndex.Class, referenceIndex.Name+" class")
		require.Len(t, targetIndex.Fields, len(referenceIndex.Fields), referenceIndex.Name+" fields")
		for i := range referenceIndex.Fields {
			assert.Equal(t, referenceIndex.Fields[i].DBName, targetIndex.Fields[i].DBName,
				referenceIndex.Name+" column")
			assert.Equal(t, referenceIndex.Fields[i].Priority, targetIndex.Fields[i].Priority,
				referenceIndex.Name+" priority")
		}
	}
	refreshIndex := targetSchema.LookIndex(targetUserSessionRefreshHashIndex)
	require.NotNil(t, refreshIndex)
	assert.Equal(t, "UNIQUE", refreshIndex.Class)
}

func TestReferenceUserSessionSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceUserSessionSchemaMatchesSQLite(t, db)
	assertReferenceUserSessionSchema(t, db)
	sid, refreshHash := insertReferenceUserSessionWithDatabaseDefaults(t, db, "fresh")
	assertReferenceUserSessionIdentityConstraints(t, db, sid, refreshHash)

	created := UserSession{
		SID: "orm-session", UserID: 71, UserAuthVersion: 1, Status: "active",
		RefreshHash: strings.Repeat("b", 64), LoginMethod: "password",
		LastActiveAt: 100, ExpiresAt: 200,
	}
	require.NoError(t, db.Create(&created).Error)
	assert.Equal(t, int64(1), created.Version)
	assert.Zero(t, created.PreviousValidUntil)
	assert.Zero(t, created.RevokedAt)
	assert.False(t, created.CreatedAt.IsZero())

	legacyDigest := strings.Repeat("c", 64)
	require.NoError(t, db.Model(&created).UpdateColumn("previous_refresh_hash", legacyDigest+"   ").Error)
	var normalized UserSession
	require.NoError(t, db.Where("sid = ?", created.SID).First(&normalized).Error)
	assert.Equal(t, legacyDigest, normalized.PreviousRefreshHash,
		"legacy CHAR padding must not change refresh-token family matching")

	columnsBefore := sqliteColumnSignatures(t, db, UserSession{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, UserSession{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, UserSession{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, UserSession{}.TableName()))
	assertReferenceUserSessionSchemaMatchesSQLite(t, db)
}

func TestReferenceUserSessionSchemaSQLitePreservesLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacyUserSessionSchema{}))
	legacy := makeLegacyReferenceUserSession(81)
	require.NoError(t, db.Create(&legacy).Error)
	zero := makeZeroLegacyReferenceUserSession(82)
	require.NoError(t, db.Create(&zero).Error)

	require.NoError(t, migrateDB())
	assertReferenceUserSessionSchemaMatchesSQLite(t, db)
	assertReferenceUserSessionSchema(t, db)
	assertLegacyReferenceUserSessionPreserved(t, db, legacy)
	assertLegacyReferenceUserSessionPreserved(t, db, zero)
	insertReferenceUserSessionWithDatabaseDefaults(t, db, "legacy")

	columnsBefore := sqliteColumnSignatures(t, db, UserSession{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, UserSession{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, UserSession{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, UserSession{}.TableName()))
	assertLegacyReferenceUserSessionPreserved(t, db, legacy)
	assertLegacyReferenceUserSessionPreserved(t, db, zero)
}

func TestReferenceUserSessionSchemaPreflightRejectsNullLifecycleState(t *testing.T) {
	for _, test := range []struct {
		column string
		repair any
	}{
		{column: "previous_valid_until", repair: int64(0)},
		{column: "login_method", repair: "legacy"},
		{column: "last_active_at", repair: int64(123)},
		{column: "revoked_at", repair: int64(0)},
	} {
		t.Run(test.column, func(t *testing.T) {
			db := newReferenceSchemaTestDB(t)
			require.NoError(t, db.AutoMigrate(&legacyUserSessionSchema{}))
			legacy := makeLegacyReferenceUserSession(91)
			require.NoError(t, db.Create(&legacy).Error)
			quotedColumn := quoteReferenceSchemaIdentifier(db, test.column)
			require.NoError(t, db.Exec("UPDATE user_sessions SET "+quotedColumn+" = NULL WHERE sid = ?", legacy.SID).Error)

			err := migrateDB()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
			assert.Contains(t, err.Error(), "user_sessions."+test.column)
			var count int64
			require.NoError(t, db.Table(UserSession{}.TableName()).
				Where("sid = ? AND "+quotedColumn+" IS NULL", legacy.SID).Count(&count).Error)
			assert.EqualValues(t, 1, count, "a rejected migration must not rewrite ambiguous session state")
			assert.False(t, db.Migrator().HasTable(&User{}), "the preflight must abort before aggregate schema mutation")

			require.NoError(t, db.Exec("UPDATE user_sessions SET "+quotedColumn+" = ? WHERE sid = ?", test.repair, legacy.SID).Error)
			require.NoError(t, migrateDB())
			assertReferenceUserSessionSchema(t, db)
		})
	}
}

func TestReferenceUserSessionSchemaPreflightRejectsMissingRequiredState(t *testing.T) {
	for _, column := range []string{"user_auth_version", "login_method", "last_active_at", "expires_at"} {
		t.Run(column, func(t *testing.T) {
			db := newReferenceSchemaTestDB(t)
			require.NoError(t, db.AutoMigrate(&legacyUserSessionSchema{}))
			legacy := makeLegacyReferenceUserSession(95)
			require.NoError(t, db.Create(&legacy).Error)
			require.NoError(t, db.Migrator().DropColumn(&legacyUserSessionSchema{}, column))

			err := migrateDB()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
			assert.Contains(t, err.Error(), "user_sessions."+column+" is missing")
			var count int64
			require.NoError(t, db.Table(UserSession{}.TableName()).Where("sid = ?", legacy.SID).Count(&count).Error)
			assert.EqualValues(t, 1, count, "a rejected migration must retain the session row")
			assert.False(t, db.Migrator().HasTable(&User{}), "the preflight must abort before aggregate schema mutation")
		})
	}
}

func assertReferenceUserSessionSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-user-session.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&pinnedReferenceUserSessionSchema{}))
	referenceColumns := sqliteColumnSignatures(t, reference, UserSession{}.TableName())
	assert.Equal(t, referenceColumns,
		normalizeUserSessionTargetColumns(sqliteColumnSignatures(t, db, UserSession{}.TableName()), referenceColumns),
		"UserSession columns after explicit target residuals are normalized",
	)
	assert.Equal(t,
		sqliteIndexSignatures(t, reference, UserSession{}.TableName()),
		filterUserSessionIndexSignatures(sqliteIndexSignatures(t, db, UserSession{}.TableName()),
			targetUserSessionRefreshHashIndex),
		"UserSession reference indexes after unique refresh-hash hardening is removed",
	)
}

// assertReferenceUserSessionSchema is shared by SQLite and the opt-in
// MySQL/PostgreSQL migration gate.
func assertReferenceUserSessionSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, index := range expectedReferenceUserSessionIndexes {
		assertUserSessionIndex(t, db, index)
	}
	assertUserSessionIndex(t, db, expectedUserSessionIndex{
		name: targetUserSessionRefreshHashIndex, columns: []string{"refresh_hash"}, unique: true,
	})
	for column, expected := range map[string]string{
		"version": "1", "previous_valid_until": "0", "revoked_at": "0",
	} {
		assertColumnDefault(t, db, UserSession{}.TableName(), column, expected)
	}
	for _, column := range []string{
		"sid", "user_id", "version", "user_auth_version", "status", "refresh_hash",
		"previous_valid_until", "login_method", "last_active_at", "expires_at", "revoked_at",
	} {
		assertColumnNullable(t, db, UserSession{}.TableName(), column, false)
	}
	for _, column := range []string{
		"previous_refresh_hash", "ip", "user_agent", "created_at", "revoked_reason",
	} {
		assertColumnNullable(t, db, UserSession{}.TableName(), column, true)
	}

	switch db.Dialector.Name() {
	case "sqlite":
		for _, column := range []string{
			"version", "user_auth_version", "previous_valid_until", "last_active_at", "expires_at", "revoked_at",
		} {
			assertColumnDatabaseType(t, db, UserSession{}.TableName(), column, "bigint")
		}
		assertColumnDatabaseType(t, db, UserSession{}.TableName(), "created_at", "datetime")
	case "mysql":
		for _, column := range []string{
			"version", "user_auth_version", "previous_valid_until", "last_active_at", "expires_at", "revoked_at",
		} {
			assertColumnDatabaseType(t, db, UserSession{}.TableName(), column, "bigint")
		}
		assertColumnDatabaseType(t, db, UserSession{}.TableName(), "created_at", "datetime")
		assertReferenceUserSessionLengths(t, db)
	case "postgres":
		for _, column := range []string{
			"version", "user_auth_version", "previous_valid_until", "last_active_at", "expires_at", "revoked_at",
		} {
			assertColumnDatabaseType(t, db, UserSession{}.TableName(), column, "int8")
		}
		assertColumnDatabaseType(t, db, UserSession{}.TableName(), "created_at", "timestamptz")
		assertReferenceUserSessionLengths(t, db)
	default:
		require.FailNow(t, "unsupported UserSession schema test dialect", db.Dialector.Name())
	}
}

func assertReferenceUserSessionLengths(t *testing.T, db *gorm.DB) {
	t.Helper()
	for column, length := range map[string]int64{
		"sid": 64, "status": 16, "refresh_hash": 64, "previous_refresh_hash": 64,
		"login_method": 32, "ip": 64, "revoked_reason": 64,
	} {
		assertColumnLength(t, db, UserSession{}.TableName(), column, length)
	}
	assertColumnDatabaseType(t, db, UserSession{}.TableName(), "user_agent", "text")
}

func assertUserSessionIndex(t *testing.T, db *gorm.DB, expected expectedUserSessionIndex) {
	t.Helper()
	columns, unique := requirePortableIndex(t, db, &UserSession{}, UserSession{}.TableName(), expected.name)
	assert.Equal(t, expected.columns, columns, expected.name+" columns")
	assert.Equal(t, expected.unique, unique, expected.name+" uniqueness")
}

func insertReferenceUserSessionWithDatabaseDefaults(t *testing.T, db *gorm.DB, marker string) (string, string) {
	t.Helper()
	sid := "session-default-" + marker
	refreshHash := strings.Repeat("a", 63) + marker[:1]
	require.NoError(t, db.Exec(`INSERT INTO user_sessions
		(sid, user_id, user_auth_version, status, refresh_hash, login_method, last_active_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, sid, 200+len(marker), 1, "active", refreshHash,
		"password", int64(100), int64(200)).Error)

	var version, previousValidUntil, revokedAt sql.NullInt64
	row := db.Table(UserSession{}.TableName()).
		Select("version, previous_valid_until, revoked_at").Where("sid = ?", sid).Row()
	require.NoError(t, row.Scan(&version, &previousValidUntil, &revokedAt))
	require.True(t, version.Valid)
	assert.Equal(t, int64(1), version.Int64)
	require.True(t, previousValidUntil.Valid)
	assert.Zero(t, previousValidUntil.Int64)
	require.True(t, revokedAt.Valid)
	assert.Zero(t, revokedAt.Int64)
	for _, column := range []string{"previous_refresh_hash", "ip", "user_agent", "created_at", "revoked_reason"} {
		var count int64
		quotedColumn := quoteReferenceSchemaIdentifier(db, column)
		require.NoError(t, db.Table(UserSession{}.TableName()).
			Where("sid = ? AND "+quotedColumn+" IS NULL", sid).Count(&count).Error)
		assert.EqualValues(t, 1, count, "UserSession.%s must retain the reference's absent database default", column)
	}
	return sid, refreshHash
}

func assertReferenceUserSessionIdentityConstraints(t *testing.T, db *gorm.DB, sid, refreshHash string) {
	t.Helper()
	assert.Error(t, db.Exec(`INSERT INTO user_sessions
		(sid, user_id, user_auth_version, status, refresh_hash, login_method, last_active_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, sid+"-duplicate-hash", 999, 1, "active", refreshHash,
		"password", int64(100), int64(200)).Error, "refresh hashes must remain globally unique")
	assert.ErrorIs(t, db.Create(&UserSession{}).Error, ErrInvalidPersistentIdentifier,
		"ORM writes must reject empty session identities")
}

func makeLegacyReferenceUserSession(seed int) legacyUserSessionSchema {
	createdAt := time.Date(2025, time.February, 3, 4, 5, seed%60, 0, time.UTC)
	return legacyUserSessionSchema{
		SID: "legacy-session-" + string(rune('a'+seed%26)), UserID: seed, Version: int64(seed + 1),
		UserAuthVersion: int64(seed + 2), Status: "active", RefreshHash: strings.Repeat(string(rune('a'+seed%20)), 64),
		PreviousRefreshHash: strings.Repeat(string(rune('f'+seed%15)), 64), PreviousValidUntil: int64(seed + 3),
		LoginMethod: "password", IP: "127.0.0.1", UserAgent: "legacy-agent", CreatedAt: createdAt,
		LastActiveAt: int64(seed + 4), ExpiresAt: int64(seed + 1000), RevokedAt: int64(seed + 5),
		RevokedReason: "legacy-reason",
	}
}

func makeZeroLegacyReferenceUserSession(seed int) legacyUserSessionSchema {
	session := makeLegacyReferenceUserSession(seed)
	session.PreviousRefreshHash = ""
	session.PreviousValidUntil = 0
	session.LoginMethod = ""
	session.LastActiveAt = 0
	session.RevokedAt = 0
	session.RevokedReason = ""
	return session
}

func assertLegacyReferenceUserSessionPreserved(t *testing.T, db *gorm.DB, legacy legacyUserSessionSchema) {
	t.Helper()
	var migrated UserSession
	require.NoError(t, db.Where("sid = ?", legacy.SID).First(&migrated).Error)
	assert.Equal(t, legacy.UserID, migrated.UserID)
	assert.Equal(t, legacy.Version, migrated.Version)
	assert.Equal(t, legacy.UserAuthVersion, migrated.UserAuthVersion)
	assert.Equal(t, legacy.Status, migrated.Status)
	assert.Equal(t, legacy.RefreshHash, migrated.RefreshHash)
	assert.Equal(t, legacy.PreviousRefreshHash, migrated.PreviousRefreshHash)
	assert.Equal(t, legacy.PreviousValidUntil, migrated.PreviousValidUntil)
	assert.Equal(t, legacy.LoginMethod, migrated.LoginMethod)
	assert.Equal(t, legacy.IP, migrated.IP)
	assert.Equal(t, legacy.UserAgent, migrated.UserAgent)
	assert.True(t, legacy.CreatedAt.Equal(migrated.CreatedAt), "CreatedAt must survive migration")
	assert.Equal(t, legacy.LastActiveAt, migrated.LastActiveAt)
	assert.Equal(t, legacy.ExpiresAt, migrated.ExpiresAt)
	assert.Equal(t, legacy.RevokedAt, migrated.RevokedAt)
	assert.Equal(t, legacy.RevokedReason, migrated.RevokedReason)
}

func normalizeUserSessionTargetColumns(
	columns []sqliteColumnSignature,
	reference []sqliteColumnSignature,
) []sqliteColumnSignature {
	referenceByName := make(map[string]sqliteColumnSignature, len(reference))
	for _, column := range reference {
		referenceByName[column.Name] = column
	}
	result := append([]sqliteColumnSignature(nil), columns...)
	for i := range result {
		switch result[i].Name {
		case "sid":
			result[i].NotNull = referenceByName[result[i].Name].NotNull
		case "created_at":
			result[i].Type = referenceByName[result[i].Name].Type
		}
	}
	return result
}

func filterUserSessionIndexSignatures(indexes []sqliteIndexSignature, excluded ...string) []sqliteIndexSignature {
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

func exerciseReferenceUserSessionSchemaLegacyExternal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Contains(t, []string{"mysql", "postgres"}, db.Dialector.Name())
	require.NoError(t, db.Migrator().DropTable(&UserSession{}))
	require.NoError(t, db.AutoMigrate(&legacyUserSessionSchema{}))
	legacy := makeLegacyReferenceUserSession(101)
	require.NoError(t, db.Create(&legacy).Error)
	zero := makeZeroLegacyReferenceUserSession(102)
	require.NoError(t, db.Create(&zero).Error)

	require.NoError(t, migrateDB())
	assertReferenceUserSessionSchema(t, db)
	assertLegacyReferenceUserSessionPreserved(t, db, legacy)
	assertLegacyReferenceUserSessionPreserved(t, db, zero)
	sid, refreshHash := insertReferenceUserSessionWithDatabaseDefaults(t, db, "server")
	assertReferenceUserSessionIdentityConstraints(t, db, sid, refreshHash)
	require.NoError(t, migrateDB())
	assertReferenceUserSessionSchema(t, db)
	assertLegacyReferenceUserSessionPreserved(t, db, legacy)
	assertLegacyReferenceUserSessionPreserved(t, db, zero)

	require.NoError(t, db.Migrator().DropTable(&UserSession{}))
	require.NoError(t, db.AutoMigrate(&legacyUserSessionSchema{}))
	unsafe := makeLegacyReferenceUserSession(103)
	require.NoError(t, db.Create(&unsafe).Error)
	require.NoError(t, db.Exec(`UPDATE user_sessions SET revoked_at = NULL WHERE sid = ?`, unsafe.SID).Error)
	err := migrateDB()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
	assert.Contains(t, err.Error(), "user_sessions.revoked_at")
	var count int64
	require.NoError(t, db.Table(UserSession{}.TableName()).
		Where("sid = ? AND revoked_at IS NULL", unsafe.SID).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	require.NoError(t, db.Exec(`UPDATE user_sessions SET revoked_at = 0 WHERE sid = ?`, unsafe.SID).Error)
	require.NoError(t, migrateDB())
	assertReferenceUserSessionSchema(t, db)
}
