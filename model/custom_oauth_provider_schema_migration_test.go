package model

import (
	"database/sql"
	"path/filepath"
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

// pinnedReferenceCustomOAuthProviderSchema independently transcribes the
// complete provider declaration at the pinned reference commit.
type pinnedReferenceCustomOAuthProviderSchema struct {
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

func (pinnedReferenceCustomOAuthProviderSchema) TableName() string {
	return "custom_oauth_providers"
}

// legacyCustomOAuthProviderSchema captures the target before reference
// defaults were reconciled. Widths, required identity, and slug uniqueness
// were already exact and are deliberately retained during the upgrade.
type legacyCustomOAuthProviderSchema struct {
	Id                    int    `gorm:"primaryKey"`
	Name                  string `gorm:"type:varchar(64);not null"`
	Slug                  string `gorm:"type:varchar(64);uniqueIndex;not null"`
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

func (legacyCustomOAuthProviderSchema) TableName() string {
	return "custom_oauth_providers"
}

func TestReferenceCustomOAuthProviderSchemaPortableDialects(t *testing.T) {
	targetSchema, err := schema.Parse(&CustomOAuthProvider{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceSchema, err := schema.Parse(&pinnedReferenceCustomOAuthProviderSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	assert.Equal(t, len(referenceSchema.Fields), len(targetSchema.Fields),
		"CustomOAuthProvider must not have unreviewed persistent extensions")
	for _, referenceField := range referenceSchema.Fields {
		targetField := targetSchema.LookUpField(referenceField.Name)
		require.NotNil(t, targetField, "CustomOAuthProvider.%s is missing", referenceField.Name)
		assert.Equal(t, referenceField.DBName, targetField.DBName, referenceField.Name+" column")
		assert.Equal(t, referenceField.PrimaryKey, targetField.PrimaryKey, referenceField.Name+" primary key")
		assert.Equal(t, referenceField.AutoIncrement, targetField.AutoIncrement, referenceField.Name+" auto increment")
		assert.Equal(t, referenceField.NotNull, targetField.NotNull, referenceField.Name+" nullability")
		assert.Equal(t, referenceField.HasDefaultValue, targetField.HasDefaultValue, referenceField.Name+" default presence")
		assert.Equal(t, referenceField.DefaultValue, targetField.DefaultValue, referenceField.Name+" default")
		assert.Equal(t, referenceField.Size, targetField.Size, referenceField.Name+" size")
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
				assert.Equal(t, dialect.DataTypeOf(referenceField), dialect.DataTypeOf(targetField),
					"CustomOAuthProvider.%s %s type", referenceField.Name, dialect.Name())
			}
		})
	}

	referenceIndexes := referenceSchema.ParseIndexes()
	targetIndexes := targetSchema.ParseIndexes()
	require.Len(t, referenceIndexes, 1)
	require.Len(t, targetIndexes, len(referenceIndexes))
	for _, referenceIndex := range referenceIndexes {
		targetIndex := targetSchema.LookIndex(referenceIndex.Name)
		require.NotNil(t, targetIndex, "missing CustomOAuthProvider index %s", referenceIndex.Name)
		assert.Equal(t, referenceIndex.Class, targetIndex.Class, referenceIndex.Name+" class")
		require.Len(t, targetIndex.Fields, len(referenceIndex.Fields), referenceIndex.Name+" fields")
		for i := range referenceIndex.Fields {
			assert.Equal(t, referenceIndex.Fields[i].DBName, targetIndex.Fields[i].DBName,
				referenceIndex.Name+" column")
			assert.Equal(t, referenceIndex.Fields[i].Priority, targetIndex.Fields[i].Priority,
				referenceIndex.Name+" priority")
		}
	}
}

func TestReferenceCustomOAuthProviderSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceCustomOAuthProviderSchemaMatchesSQLite(t, db)
	assertReferenceCustomOAuthProviderSchema(t, db)
	slug := insertReferenceCustomOAuthProviderWithDatabaseDefaults(t, db, "fresh")
	assertReferenceCustomOAuthProviderIdentityConstraints(t, db, slug)

	created := CustomOAuthProvider{Name: "ORM provider", Slug: "orm-provider"}
	require.NoError(t, db.Create(&created).Error)
	assert.Empty(t, created.Icon)
	assert.False(t, created.Enabled)
	assert.Equal(t, "openid profile email", created.Scopes)
	assert.Equal(t, "sub", created.UserIdField)
	assert.Equal(t, "preferred_username", created.UsernameField)
	assert.Equal(t, "name", created.DisplayNameField)
	assert.Equal(t, "email", created.EmailField)
	assert.Zero(t, created.AuthStyle)
	assert.False(t, created.CreatedAt.IsZero())
	assert.False(t, created.UpdatedAt.IsZero())

	columnsBefore := sqliteColumnSignatures(t, db, CustomOAuthProvider{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, CustomOAuthProvider{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, CustomOAuthProvider{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, CustomOAuthProvider{}.TableName()))
	assertReferenceCustomOAuthProviderSchemaMatchesSQLite(t, db)
}

func TestReferenceCustomOAuthProviderSchemaSQLitePreservesLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacyCustomOAuthProviderSchema{}))
	legacy := makeLegacyReferenceCustomOAuthProvider(81)
	require.NoError(t, db.Create(&legacy).Error)
	nullable := makeLegacyReferenceCustomOAuthProvider(82)
	require.NoError(t, db.Create(&nullable).Error)
	require.NoError(t, setLegacyCustomOAuthProviderDefaultColumnsNull(db, nullable.Id))
	zero := makeZeroLegacyReferenceCustomOAuthProvider(83)
	require.NoError(t, db.Create(&zero).Error)

	require.NoError(t, migrateDB())
	assertReferenceCustomOAuthProviderSchemaMatchesSQLite(t, db)
	assertReferenceCustomOAuthProviderSchema(t, db)
	assertLegacyReferenceCustomOAuthProviderPreserved(t, db, legacy)
	assertLegacyCustomOAuthProviderNullDefaultsPreserved(t, db, nullable.Id)
	assertLegacyReferenceCustomOAuthProviderPreserved(t, db, zero)
	insertReferenceCustomOAuthProviderWithDatabaseDefaults(t, db, "legacy")

	columnsBefore := sqliteColumnSignatures(t, db, CustomOAuthProvider{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, CustomOAuthProvider{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, CustomOAuthProvider{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, CustomOAuthProvider{}.TableName()))
	assertLegacyReferenceCustomOAuthProviderPreserved(t, db, legacy)
	assertLegacyCustomOAuthProviderNullDefaultsPreserved(t, db, nullable.Id)
	assertLegacyReferenceCustomOAuthProviderPreserved(t, db, zero)
}

func assertReferenceCustomOAuthProviderSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-custom-oauth.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&pinnedReferenceCustomOAuthProviderSchema{}))
	assert.Equal(t,
		sqliteColumnSignatures(t, reference, CustomOAuthProvider{}.TableName()),
		sqliteColumnSignatures(t, db, CustomOAuthProvider{}.TableName()),
		"CustomOAuthProvider columns",
	)
	assert.Equal(t,
		sqliteIndexSignatures(t, reference, CustomOAuthProvider{}.TableName()),
		sqliteIndexSignatures(t, db, CustomOAuthProvider{}.TableName()),
		"CustomOAuthProvider indexes",
	)
}

// assertReferenceCustomOAuthProviderSchema is shared by SQLite and the opt-in
// MySQL/PostgreSQL migration gate.
func assertReferenceCustomOAuthProviderSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	const table = "custom_oauth_providers"
	assert.True(t, db.Migrator().HasIndex(&CustomOAuthProvider{}, "idx_custom_oauth_providers_slug"))
	assertIndexUnique(t, db, &CustomOAuthProvider{}, "idx_custom_oauth_providers_slug", true)
	assertColumnNullable(t, db, table, "name", false)
	assertColumnNullable(t, db, table, "slug", false)
	for _, column := range []string{
		"icon", "enabled", "client_id", "client_secret", "authorization_endpoint", "token_endpoint",
		"user_info_endpoint", "scopes", "user_id_field", "username_field", "display_name_field",
		"email_field", "well_known", "auth_style", "access_policy", "access_denied_message",
		"created_at", "updated_at",
	} {
		assertColumnNullable(t, db, table, column, true)
	}
	for column, expected := range map[string]string{
		"icon": "", "enabled": "0", "scopes": "openid profile email", "user_id_field": "sub",
		"username_field": "preferred_username", "display_name_field": "name",
		"email_field": "email", "auth_style": "0",
	} {
		assertColumnDefault(t, db, table, column, expected)
	}
	assert.False(t, db.Migrator().HasColumn(&CustomOAuthProvider{}, "DeletedAt"))

	if db.Dialector.Name() == "mysql" || db.Dialector.Name() == "postgres" {
		for column, length := range map[string]int64{
			"name": 64, "slug": 64, "icon": 128, "client_id": 256, "client_secret": 512,
			"authorization_endpoint": 512, "token_endpoint": 512, "user_info_endpoint": 512,
			"scopes": 256, "user_id_field": 128, "username_field": 128,
			"display_name_field": 128, "email_field": 128, "well_known": 512,
			"access_denied_message": 512,
		} {
			assertColumnLength(t, db, table, column, length)
		}
		assertColumnDatabaseType(t, db, table, "access_policy", "text")
	}
}

func insertReferenceCustomOAuthProviderWithDatabaseDefaults(t *testing.T, db *gorm.DB, marker string) string {
	t.Helper()
	name := "Direct " + marker
	slug := "direct-" + marker
	require.NoError(t, db.Exec(`INSERT INTO custom_oauth_providers (name, slug) VALUES (?, ?)`, name, slug).Error)

	var id int
	var icon, scopes, userIDField, usernameField, displayNameField, emailField sql.NullString
	var enabled sql.NullBool
	var authStyle sql.NullInt64
	row := db.Table(CustomOAuthProvider{}.TableName()).
		Select("id, icon, enabled, scopes, user_id_field, username_field, display_name_field, email_field, auth_style").
		Where("slug = ?", slug).Row()
	require.NoError(t, row.Scan(&id, &icon, &enabled, &scopes, &userIDField, &usernameField,
		&displayNameField, &emailField, &authStyle))
	assert.Positive(t, id)
	require.True(t, icon.Valid)
	assert.Empty(t, icon.String)
	require.True(t, enabled.Valid)
	assert.False(t, enabled.Bool)
	require.True(t, scopes.Valid)
	assert.Equal(t, "openid profile email", scopes.String)
	require.True(t, userIDField.Valid)
	assert.Equal(t, "sub", userIDField.String)
	require.True(t, usernameField.Valid)
	assert.Equal(t, "preferred_username", usernameField.String)
	require.True(t, displayNameField.Valid)
	assert.Equal(t, "name", displayNameField.String)
	require.True(t, emailField.Valid)
	assert.Equal(t, "email", emailField.String)
	require.True(t, authStyle.Valid)
	assert.Zero(t, authStyle.Int64)

	for _, column := range []string{
		"client_id", "client_secret", "authorization_endpoint", "token_endpoint", "user_info_endpoint",
		"well_known", "access_policy", "access_denied_message", "created_at", "updated_at",
	} {
		var count int64
		quotedColumn := quoteReferenceSchemaIdentifier(db, column)
		require.NoError(t, db.Table(CustomOAuthProvider{}.TableName()).
			Where("id = ? AND "+quotedColumn+" IS NULL", id).Count(&count).Error)
		assert.EqualValues(t, 1, count, "CustomOAuthProvider.%s must retain the reference's absent database default", column)
	}
	return slug
}

func assertReferenceCustomOAuthProviderIdentityConstraints(t *testing.T, db *gorm.DB, slug string) {
	t.Helper()
	assert.Error(t, db.Exec(`INSERT INTO custom_oauth_providers (name, slug) VALUES (?, ?)`, "Duplicate", slug).Error,
		"provider slugs must remain unique")
	assert.Error(t, db.Exec(`INSERT INTO custom_oauth_providers (slug) VALUES (?)`, "missing-name").Error,
		"provider names must remain non-null")
	assert.Error(t, db.Exec(`INSERT INTO custom_oauth_providers (name) VALUES (?)`, "Missing slug").Error,
		"provider slugs must remain non-null")
}

func makeLegacyReferenceCustomOAuthProvider(seed int) legacyCustomOAuthProviderSchema {
	createdAt := time.Date(2025, time.January, 2, 3, 4, seed%60, 0, time.UTC)
	return legacyCustomOAuthProviderSchema{
		Name: "Legacy provider", Slug: "legacy-provider-" + string(rune('a'+seed%26)),
		Icon: "legacy-icon", Enabled: true, ClientId: "legacy-client", ClientSecret: "legacy-secret",
		AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token",
		UserInfoEndpoint: "https://issuer.example/userinfo", Scopes: "legacy scope", UserIdField: "legacy.id",
		UsernameField: "legacy.username", DisplayNameField: "legacy.name", EmailField: "legacy.email",
		WellKnown: "https://issuer.example/.well-known/openid-configuration", AuthStyle: 2,
		AccessPolicy:        `{"logic":"and","conditions":[{"field":"team","op":"eq","value":"legacy"}]}`,
		AccessDeniedMessage: "legacy denied", CreatedAt: createdAt, UpdatedAt: createdAt.Add(time.Minute),
	}
}

func makeZeroLegacyReferenceCustomOAuthProvider(seed int) legacyCustomOAuthProviderSchema {
	provider := makeLegacyReferenceCustomOAuthProvider(seed)
	provider.Icon = ""
	provider.Enabled = false
	provider.Scopes = ""
	provider.UserIdField = ""
	provider.UsernameField = ""
	provider.DisplayNameField = ""
	provider.EmailField = ""
	provider.AuthStyle = 0
	return provider
}

func assertLegacyReferenceCustomOAuthProviderPreserved(t *testing.T, db *gorm.DB, legacy legacyCustomOAuthProviderSchema) {
	t.Helper()
	var migrated CustomOAuthProvider
	require.NoError(t, db.First(&migrated, legacy.Id).Error)
	assert.Equal(t, legacy.Name, migrated.Name)
	assert.Equal(t, legacy.Slug, migrated.Slug)
	assert.Equal(t, legacy.Icon, migrated.Icon)
	assert.Equal(t, legacy.Enabled, migrated.Enabled)
	assert.Equal(t, legacy.ClientId, migrated.ClientId)
	assert.Equal(t, legacy.ClientSecret, migrated.ClientSecret)
	assert.Equal(t, legacy.AuthorizationEndpoint, migrated.AuthorizationEndpoint)
	assert.Equal(t, legacy.TokenEndpoint, migrated.TokenEndpoint)
	assert.Equal(t, legacy.UserInfoEndpoint, migrated.UserInfoEndpoint)
	assert.Equal(t, legacy.Scopes, migrated.Scopes)
	assert.Equal(t, legacy.UserIdField, migrated.UserIdField)
	assert.Equal(t, legacy.UsernameField, migrated.UsernameField)
	assert.Equal(t, legacy.DisplayNameField, migrated.DisplayNameField)
	assert.Equal(t, legacy.EmailField, migrated.EmailField)
	assert.Equal(t, legacy.WellKnown, migrated.WellKnown)
	assert.Equal(t, legacy.AuthStyle, migrated.AuthStyle)
	assert.Equal(t, legacy.AccessPolicy, migrated.AccessPolicy)
	assert.Equal(t, legacy.AccessDeniedMessage, migrated.AccessDeniedMessage)
	assert.True(t, legacy.CreatedAt.Equal(migrated.CreatedAt), "CreatedAt must survive migration")
	assert.True(t, legacy.UpdatedAt.Equal(migrated.UpdatedAt), "UpdatedAt must survive migration")
}

func setLegacyCustomOAuthProviderDefaultColumnsNull(db *gorm.DB, id int) error {
	return db.Exec(`UPDATE custom_oauth_providers SET icon = NULL, enabled = NULL, scopes = NULL,
		user_id_field = NULL, username_field = NULL, display_name_field = NULL,
		email_field = NULL, auth_style = NULL WHERE id = ?`, id).Error
}

func assertLegacyCustomOAuthProviderNullDefaultsPreserved(t *testing.T, db *gorm.DB, id int) {
	t.Helper()
	for _, column := range []string{
		"icon", "enabled", "scopes", "user_id_field", "username_field",
		"display_name_field", "email_field", "auth_style",
	} {
		var count int64
		quotedColumn := quoteReferenceSchemaIdentifier(db, column)
		require.NoError(t, db.Table(CustomOAuthProvider{}.TableName()).
			Where("id = ? AND "+quotedColumn+" IS NULL", id).Count(&count).Error)
		assert.EqualValues(t, 1, count, "adding the %s default must not rewrite legacy NULL", column)
	}
}

func exerciseReferenceCustomOAuthProviderSchemaLegacyExternal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Contains(t, []string{"mysql", "postgres"}, db.Dialector.Name())
	require.NoError(t, db.Migrator().DropTable(&CustomOAuthProvider{}))
	require.NoError(t, db.AutoMigrate(&legacyCustomOAuthProviderSchema{}))
	legacy := makeLegacyReferenceCustomOAuthProvider(101)
	require.NoError(t, db.Create(&legacy).Error)
	nullable := makeLegacyReferenceCustomOAuthProvider(102)
	require.NoError(t, db.Create(&nullable).Error)
	require.NoError(t, setLegacyCustomOAuthProviderDefaultColumnsNull(db, nullable.Id))
	zero := makeZeroLegacyReferenceCustomOAuthProvider(103)
	require.NoError(t, db.Create(&zero).Error)

	require.NoError(t, migrateDB())
	assertReferenceCustomOAuthProviderSchema(t, db)
	assertLegacyReferenceCustomOAuthProviderPreserved(t, db, legacy)
	assertLegacyCustomOAuthProviderNullDefaultsPreserved(t, db, nullable.Id)
	assertLegacyReferenceCustomOAuthProviderPreserved(t, db, zero)
	slug := insertReferenceCustomOAuthProviderWithDatabaseDefaults(t, db, "server")
	assertReferenceCustomOAuthProviderIdentityConstraints(t, db, slug)
	require.NoError(t, migrateDB())
	assertReferenceCustomOAuthProviderSchema(t, db)
	assertLegacyReferenceCustomOAuthProviderPreserved(t, db, legacy)
	assertLegacyCustomOAuthProviderNullDefaultsPreserved(t, db, nullable.Id)
	assertLegacyReferenceCustomOAuthProviderPreserved(t, db, zero)
}
