package model

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These mirror structs intentionally reproduce the audited reference tags.
// Migrating them into a second SQLite database lets the test compare actual
// DDL metadata instead of treating table presence as schema parity.
type referenceMidjourneySchema struct {
	Id          int `gorm:"primaryKey"`
	Code        int
	UserId      int    `gorm:"index"`
	Action      string `gorm:"type:varchar(40);index"`
	MjId        string `gorm:"index"`
	Prompt      string
	PromptEn    string
	Description string
	State       string
	SubmitTime  int64 `gorm:"index"`
	StartTime   int64 `gorm:"index"`
	FinishTime  int64 `gorm:"index"`
	ImageUrl    string
	VideoUrl    string
	VideoUrls   string
	Status      string `gorm:"type:varchar(20);index"`
	Progress    string `gorm:"type:varchar(30);index"`
	FailReason  string
	ChannelId   int
	Quota       int
	Buttons     string
	Properties  string
}

func (referenceMidjourneySchema) TableName() string { return "midjourneys" }

type referenceTwoFASchema struct {
	Id             int    `gorm:"primaryKey"`
	UserId         int    `gorm:"unique;not null;index"`
	Secret         string `gorm:"type:varchar(255);not null"`
	IsEnabled      bool
	FailedAttempts int `gorm:"default:0"`
	LockedUntil    *time.Time
	LastUsedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`
}

func (referenceTwoFASchema) TableName() string { return "two_fas" }

type referenceCustomOAuthProviderSchema struct {
	Id                    int    `gorm:"primaryKey"`
	Name                  string `gorm:"type:varchar(64);not null"`
	Slug                  string `gorm:"type:varchar(64);uniqueIndex;not null"`
	Icon                  string `gorm:"type:varchar(128);default:''"`
	Enabled               bool   `gorm:"default:false"`
	ClientId              string `gorm:"type:varchar(256)"`
	ClientSecret          string `gorm:"type:varchar(512)"`
	AuthorizationEndpoint string `gorm:"type:varchar(512)"`
	TokenEndpoint         string `gorm:"type:varchar(512)"`
	UserInfoEndpoint      string `gorm:"type:varchar(512)"`
	Scopes                string `gorm:"type:varchar(256);default:'openid profile email'"`
	UserIdField           string `gorm:"type:varchar(128);default:'sub'"`
	UsernameField         string `gorm:"type:varchar(128);default:'preferred_username'"`
	DisplayNameField      string `gorm:"type:varchar(128);default:'name'"`
	EmailField            string `gorm:"type:varchar(128);default:'email'"`
	WellKnown             string `gorm:"type:varchar(512)"`
	AuthStyle             int    `gorm:"default:0"`
	AccessPolicy          string `gorm:"type:text"`
	AccessDeniedMessage   string `gorm:"type:varchar(512)"`
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (referenceCustomOAuthProviderSchema) TableName() string { return "custom_oauth_providers" }

type legacyMidjourneyMetadataSchema struct {
	Id          int `gorm:"primaryKey"`
	Code        int
	UserId      int    `gorm:"index"`
	Action      string `gorm:"type:varchar(40);index"`
	MjId        string `gorm:"index;type:varchar(64)"`
	Prompt      string `gorm:"type:text"`
	PromptEn    string `gorm:"type:text"`
	Description string `gorm:"type:text"`
	State       string `gorm:"type:varchar(40)"`
	SubmitTime  int64  `gorm:"index"`
	StartTime   int64  `gorm:"index"`
	FinishTime  int64  `gorm:"index"`
	ImageUrl    string `gorm:"type:text"`
	VideoUrl    string `gorm:"type:text"`
	VideoUrls   string `gorm:"type:text"`
	Status      string `gorm:"type:varchar(20);index"`
	Progress    string `gorm:"type:varchar(30);index"`
	FailReason  string `gorm:"type:text"`
	ChannelId   int
	Quota       int
	Buttons     string `gorm:"type:text"`
	Properties  string `gorm:"type:text"`
}

func (legacyMidjourneyMetadataSchema) TableName() string { return "midjourneys" }

type legacyTwoFAMetadataSchema struct {
	Id             int    `gorm:"primaryKey"`
	UserId         int    `gorm:"uniqueIndex;not null"`
	Secret         string `gorm:"type:varchar(255);not null"`
	IsEnabled      bool
	FailedAttempts int
	LockedUntil    *time.Time
	LastUsedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`
}

func (legacyTwoFAMetadataSchema) TableName() string { return "two_fas" }

type legacyCustomOAuthProviderMetadataSchema struct {
	Id                    int    `gorm:"primaryKey"`
	Name                  string `gorm:"type:varchar(64);not null"`
	Slug                  string `gorm:"type:varchar(64);not null;uniqueIndex"`
	Icon                  string `gorm:"type:varchar(128)"`
	Enabled               bool
	ClientId              string `gorm:"type:varchar(256)"`
	ClientSecret          string `gorm:"type:varchar(512)"`
	AuthorizationEndpoint string `gorm:"type:varchar(512)"`
	TokenEndpoint         string `gorm:"type:varchar(512)"`
	UserInfoEndpoint      string `gorm:"type:varchar(512)"`
	Scopes                string `gorm:"type:varchar(256)"`
	UserIdField           string `gorm:"type:varchar(128)"`
	UsernameField         string `gorm:"type:varchar(128)"`
	DisplayNameField      string `gorm:"type:varchar(128)"`
	EmailField            string `gorm:"type:varchar(128)"`
	WellKnown             string `gorm:"type:varchar(512)"`
	AuthStyle             int
	AccessPolicy          string `gorm:"type:text"`
	AccessDeniedMessage   string `gorm:"type:varchar(512)"`
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (legacyCustomOAuthProviderMetadataSchema) TableName() string {
	return "custom_oauth_providers"
}

func TestReferenceMetadataSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceMetadataSchemaMatchesSQLite(t, db)
	assertReferenceMetadataSchema(t, db)
	exerciseReferenceMetadataDirectInsertAndSoftDelete(t, db)
}

func TestReferenceMetadataSchemaPreservesLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&legacyMidjourneyMetadataSchema{},
		&legacyTwoFAMetadataSchema{},
		&legacyCustomOAuthProviderMetadataSchema{},
	))

	midjourney := legacyMidjourneyMetadataSchema{
		Code: 1, UserId: 7, Action: "IMAGINE", MjId: strings.Repeat("m", 64),
		Prompt: strings.Repeat("p", 256), State: strings.Repeat("s", 40), Status: "SUCCESS",
	}
	require.NoError(t, db.Create(&midjourney).Error)
	twoFA := legacyTwoFAMetadataSchema{
		UserId: 8, Secret: "legacy-secret", IsEnabled: true, FailedAttempts: 7,
	}
	require.NoError(t, db.Create(&twoFA).Error)
	provider := legacyCustomOAuthProviderMetadataSchema{
		Name: "Legacy", Slug: "legacy", Icon: "legacy-icon", Enabled: true,
		Scopes: "legacy scope", UserIdField: "legacy-id", UsernameField: "legacy-user",
		DisplayNameField: "legacy-name", EmailField: "legacy-email", AuthStyle: 2,
	}
	require.NoError(t, db.Create(&provider).Error)

	require.NoError(t, migrateDB())
	assertReferenceMetadataSchemaMatchesSQLite(t, db)
	assertReferenceMetadataSchema(t, db)

	var migratedMidjourney Midjourney
	require.NoError(t, db.First(&migratedMidjourney, midjourney.Id).Error)
	assert.Equal(t, midjourney.MjId, migratedMidjourney.MjId)
	assert.Equal(t, midjourney.Prompt, migratedMidjourney.Prompt)
	assert.Equal(t, midjourney.State, migratedMidjourney.State)
	var migratedTwoFA TwoFA
	require.NoError(t, db.First(&migratedTwoFA, twoFA.Id).Error)
	assert.Equal(t, twoFA.Secret, migratedTwoFA.Secret)
	assert.Equal(t, twoFA.FailedAttempts, migratedTwoFA.FailedAttempts)
	var migratedProvider CustomOAuthProvider
	require.NoError(t, db.First(&migratedProvider, provider.Id).Error)
	assert.Equal(t, provider.Icon, migratedProvider.Icon)
	assert.Equal(t, provider.Scopes, migratedProvider.Scopes)
	assert.Equal(t, provider.UserIdField, migratedProvider.UserIdField)
	assert.Equal(t, provider.AuthStyle, migratedProvider.AuthStyle)

	// Rolling restarts must not rewrite explicit legacy values or drift DDL.
	require.NoError(t, migrateDB())
	assertReferenceMetadataSchemaMatchesSQLite(t, db)
}

func exerciseReferenceMetadataDirectInsertAndSoftDelete(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO two_fas (user_id, secret) VALUES (?, ?)`, 101, "direct-secret").Error)
	var directTwoFA TwoFA
	require.NoError(t, db.Where("user_id = ?", 101).First(&directTwoFA).Error)
	assert.Zero(t, directTwoFA.FailedAttempts)

	require.NoError(t, db.Exec(`INSERT INTO custom_oauth_providers (name, slug) VALUES (?, ?)`, "Direct", "direct").Error)
	var directProvider CustomOAuthProvider
	require.NoError(t, db.Where("slug = ?", "direct").First(&directProvider).Error)
	assert.Empty(t, directProvider.Icon)
	assert.False(t, directProvider.Enabled)
	assert.Equal(t, "openid profile email", directProvider.Scopes)
	assert.Equal(t, "sub", directProvider.UserIdField)
	assert.Equal(t, "preferred_username", directProvider.UsernameField)
	assert.Equal(t, "name", directProvider.DisplayNameField)
	assert.Equal(t, "email", directProvider.EmailField)
	assert.Zero(t, directProvider.AuthStyle)

	factor := TwoFA{UserId: 102, Secret: "soft-delete-secret", IsEnabled: true}
	require.NoError(t, db.Create(&factor).Error)
	require.NoError(t, db.Delete(&factor).Error)
	var visible, unscoped int64
	require.NoError(t, db.Model(&TwoFA{}).Where("id = ?", factor.Id).Count(&visible).Error)
	require.NoError(t, db.Unscoped().Model(&TwoFA{}).Where("id = ?", factor.Id).Count(&unscoped).Error)
	assert.Zero(t, visible)
	assert.EqualValues(t, 1, unscoped)
	assert.True(t, db.Migrator().HasColumn(&TwoFA{}, "DeletedAt"))
	assert.True(t, db.Migrator().HasIndex(&TwoFA{}, "idx_two_fas_deleted_at"))
	assert.False(t, db.Migrator().HasColumn(&Midjourney{}, "DeletedAt"))
	assert.False(t, db.Migrator().HasColumn(&CustomOAuthProvider{}, "DeletedAt"))
}

func assertReferenceMetadataSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, index := range []struct {
		model any
		name  string
	}{
		{&Midjourney{}, "idx_midjourneys_user_id"},
		{&Midjourney{}, "idx_midjourneys_action"},
		{&Midjourney{}, "idx_midjourneys_mj_id"},
		{&Midjourney{}, "idx_midjourneys_submit_time"},
		{&Midjourney{}, "idx_midjourneys_start_time"},
		{&Midjourney{}, "idx_midjourneys_finish_time"},
		{&Midjourney{}, "idx_midjourneys_status"},
		{&Midjourney{}, "idx_midjourneys_progress"},
		{&TwoFA{}, "idx_two_fas_user_id"},
		{&TwoFA{}, "idx_two_fas_deleted_at"},
		{&CustomOAuthProvider{}, "idx_custom_oauth_providers_slug"},
	} {
		assert.True(t, db.Migrator().HasIndex(index.model, index.name), "missing migrated index %s", index.name)
	}
	assertIndexUnique(t, db, &TwoFA{}, "idx_two_fas_user_id", false)
	assertIndexUnique(t, db, &CustomOAuthProvider{}, "idx_custom_oauth_providers_slug", true)
	assertColumnUnique(t, db, "two_fas", "user_id", true)
	assertColumnNullable(t, db, "two_fas", "user_id", false)
	assertColumnNullable(t, db, "two_fas", "failed_attempts", true)
	assertColumnDefault(t, db, "two_fas", "failed_attempts", "0")
	for column, expected := range map[string]string{
		"icon":               "",
		"enabled":            "0",
		"scopes":             "openid profile email",
		"user_id_field":      "sub",
		"username_field":     "preferred_username",
		"display_name_field": "name",
		"email_field":        "email",
		"auth_style":         "0",
	} {
		assertColumnDefault(t, db, "custom_oauth_providers", column, expected)
	}

	statement := &gorm.Statement{DB: db}
	require.NoError(t, statement.Parse(&Midjourney{}))
	for fieldName, expectedType := range map[string]string{
		"Action": "varchar(40)", "MjId": "", "Prompt": "", "State": "",
		"Status": "varchar(20)", "Progress": "varchar(30)", "Properties": "",
	} {
		field := statement.Schema.LookUpField(fieldName)
		require.NotNil(t, field, "Midjourney.%s is missing", fieldName)
		assert.Equal(t, expectedType, strings.ToLower(field.TagSettings["TYPE"]), "Midjourney.%s declared type", fieldName)
	}

	if db.Dialector.Name() == "mysql" {
		assertColumnLength(t, db, "midjourneys", "action", 40)
		assertColumnLength(t, db, "midjourneys", "mj_id", 191)
		assertColumnLength(t, db, "midjourneys", "status", 20)
		assertColumnLength(t, db, "midjourneys", "progress", 30)
		for _, column := range []string{"prompt", "prompt_en", "description", "state", "image_url", "video_url", "video_urls", "fail_reason", "buttons", "properties"} {
			assertColumnDatabaseType(t, db, "midjourneys", column, "longtext")
		}
	}
	if db.Dialector.Name() == "postgres" {
		assertColumnLength(t, db, "midjourneys", "action", 40)
		assertColumnLength(t, db, "midjourneys", "status", 20)
		assertColumnLength(t, db, "midjourneys", "progress", 30)
		for _, column := range []string{"mj_id", "prompt", "prompt_en", "description", "state", "image_url", "video_url", "video_urls", "fail_reason", "buttons", "properties"} {
			assertColumnDatabaseType(t, db, "midjourneys", column, "text")
		}
	}
}

func assertReferenceMetadataSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-metadata.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(
		&referenceMidjourneySchema{}, &referenceTwoFASchema{}, &referenceCustomOAuthProviderSchema{},
	))
	for _, table := range []string{"midjourneys", "two_fas", "custom_oauth_providers"} {
		assert.Equal(t, sqliteColumnSignatures(t, reference, table), sqliteColumnSignatures(t, db, table), table+" columns")
		assert.Equal(t, sqliteIndexSignatures(t, reference, table), sqliteIndexSignatures(t, db, table), table+" indexes")
	}
}

func assertIndexUnique(t *testing.T, db *gorm.DB, entity any, name string, expected bool) {
	t.Helper()
	indexes, err := db.Migrator().GetIndexes(entity)
	require.NoError(t, err)
	for _, index := range indexes {
		if index.Name() != name {
			continue
		}
		unique, known := index.Unique()
		require.True(t, known, "index %s uniqueness is unknown", name)
		assert.Equal(t, expected, unique, "index %s uniqueness", name)
		return
	}
	require.FailNow(t, "missing index metadata", name)
}

func assertColumnUnique(t *testing.T, db *gorm.DB, table, name string, expected bool) {
	t.Helper()
	column := requireColumnType(t, db, table, name)
	unique, known := column.Unique()
	require.True(t, known, "%s.%s uniqueness is unknown", table, name)
	assert.Equal(t, expected, unique, "%s.%s uniqueness", table, name)
}

func assertColumnDatabaseType(t *testing.T, db *gorm.DB, table, name, expected string) {
	t.Helper()
	column := requireColumnType(t, db, table, name)
	assert.Equal(t, strings.ToLower(expected), strings.ToLower(column.DatabaseTypeName()), "%s.%s database type", table, name)
}

type sqliteColumnSignature struct {
	Name       string
	Type       string
	NotNull    int
	Default    string
	HasDefault bool
	PrimaryKey int
}

func sqliteColumnSignatures(t *testing.T, db *gorm.DB, table string) []sqliteColumnSignature {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	rows, err := sqlDB.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	require.NoError(t, err)
	defer rows.Close()
	var result []sqliteColumnSignature
	for rows.Next() {
		var cid int
		var signature sqliteColumnSignature
		var defaultValue sql.NullString
		require.NoError(t, rows.Scan(&cid, &signature.Name, &signature.Type, &signature.NotNull, &defaultValue, &signature.PrimaryKey))
		if defaultValue.Valid {
			signature.HasDefault = true
			signature.Default = normalizeDatabaseDefault(defaultValue.String)
		}
		result = append(result, signature)
	}
	require.NoError(t, rows.Err())
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

type sqliteIndexSignature struct {
	Name    string
	Unique  int
	Origin  string
	Partial int
	Columns string
}

func sqliteIndexSignatures(t *testing.T, db *gorm.DB, table string) []sqliteIndexSignature {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	rows, err := sqlDB.Query(fmt.Sprintf(`PRAGMA index_list(%q)`, table))
	require.NoError(t, err)
	defer rows.Close()
	var result []sqliteIndexSignature
	for rows.Next() {
		var seq int
		var signature sqliteIndexSignature
		require.NoError(t, rows.Scan(&seq, &signature.Name, &signature.Unique, &signature.Origin, &signature.Partial))
		columnRows, err := sqlDB.Query(fmt.Sprintf(`PRAGMA index_info(%q)`, signature.Name))
		require.NoError(t, err)
		var columns []string
		for columnRows.Next() {
			var seqNo, cid int
			var name string
			require.NoError(t, columnRows.Scan(&seqNo, &cid, &name))
			columns = append(columns, name)
		}
		require.NoError(t, columnRows.Err())
		require.NoError(t, columnRows.Close())
		signature.Columns = strings.Join(columns, ",")
		result = append(result, signature)
	}
	require.NoError(t, rows.Err())
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
