package model

import (
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

const referenceUserIndexedStringLimit = int64(191)

// referenceUserSchema independently transcribes the audited reference User
// tags. The two ignored request-only fields are intentionally omitted because
// they do not participate in database schema generation.
type referenceUserSchema struct {
	Id              int
	Username        string         `gorm:"unique;index"`
	Password        string         `gorm:"not null;"`
	DisplayName     string         `gorm:"index"`
	Role            int            `gorm:"type:int;default:1"`
	Status          int            `gorm:"type:int;default:1"`
	Email           string         `gorm:"index"`
	GitHubId        string         `gorm:"column:github_id;index"`
	DiscordId       string         `gorm:"column:discord_id;index"`
	OidcId          string         `gorm:"column:oidc_id;index"`
	WeChatId        string         `gorm:"column:wechat_id;index"`
	TelegramId      string         `gorm:"column:telegram_id;index"`
	AccessToken     *string        `gorm:"type:char(32);column:access_token;uniqueIndex"`
	Quota           int            `gorm:"type:int;default:0"`
	UsedQuota       int            `gorm:"type:int;default:0;column:used_quota"`
	RequestCount    int            `gorm:"type:int;default:0;"`
	Group           string         `gorm:"type:varchar(64);default:'default'"`
	AffCode         string         `gorm:"type:varchar(32);column:aff_code;uniqueIndex"`
	AffCount        int            `gorm:"type:int;default:0;column:aff_count"`
	AffQuota        int            `gorm:"type:int;default:0;column:aff_quota"`
	AffHistoryQuota int            `gorm:"type:int;default:0;column:aff_history"`
	InviterId       int            `gorm:"type:int;column:inviter_id;index"`
	DeletedAt       gorm.DeletedAt `gorm:"index"`
	LinuxDOId       string         `gorm:"column:linux_do_id;index"`
	Setting         string         `gorm:"type:text;column:setting"`
	Remark          string         `gorm:"type:varchar(255)"`
	StripeCustomer  string         `gorm:"type:varchar(64);column:stripe_customer;index"`
	CreatedAt       int64          `gorm:"autoCreateTime;column:created_at"`
	LastLoginAt     int64          `gorm:"default:0;column:last_login_at"`
	AuthVersion     int64          `gorm:"type:bigint;not null;default:1;column:auth_version"`
}

func (referenceUserSchema) TableName() string { return "users" }

// legacyUserSchema captures TokenRouter's User table immediately before this
// alignment. It retains the target security columns so the upgrade test also
// proves they and their data survive unchanged.
type legacyUserSchema struct {
	Id               int    `gorm:"primaryKey"`
	Username         string `gorm:"uniqueIndex;type:varchar(64)"`
	Password         string `gorm:"not null"`
	DisplayName      string `gorm:"index;type:varchar(64)"`
	Role             int
	Status           int
	Email            string `gorm:"index;type:varchar(128)"`
	EmailVerified    bool
	VerifiedEmailKey *string `gorm:"column:verified_email_key;type:char(64);uniqueIndex:ux_users_verified_email_key"`
	QuotaReminderAt  int64   `gorm:"column:quota_reminder_at;default:0"`
	GitHubId         string  `gorm:"column:github_id;index;type:varchar(64)"`
	DiscordId        string  `gorm:"column:discord_id;index;type:varchar(64)"`
	OidcId           string  `gorm:"column:oidc_id;index;type:varchar(64)"`
	WeChatId         string  `gorm:"column:wechat_id;index;type:varchar(64)"`
	TelegramId       string  `gorm:"column:telegram_id;index;type:varchar(64)"`
	AccessToken      *string `gorm:"type:char(32);uniqueIndex"`
	Quota            int
	UsedQuota        int
	RequestCount     int
	Group            string `gorm:"type:varchar(64)"`
	AffCode          string `gorm:"type:varchar(32);uniqueIndex"`
	AffCount         int
	AffQuota         int
	AffHistoryQuota  int
	InviterId        int    `gorm:"index"`
	LinuxDOId        string `gorm:"column:linuxdo_id;index;type:varchar(64)"`
	Setting          string `gorm:"type:text"`
	Remark           string
	StripeCustomer   string `gorm:"index;type:varchar(128)"`
	CreatedAt        int64
	LastLoginAt      int64
	AuthVersion      int64          `gorm:"not null;default:1"`
	DeletedAt        gorm.DeletedAt `gorm:"index"`
}

func (legacyUserSchema) TableName() string { return "users" }

func TestReferenceUserSchemaPortableDialects(t *testing.T) {
	targetSchema, err := schema.Parse(&User{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceSchema, err := schema.Parse(&referenceUserSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	for _, referenceField := range referenceSchema.Fields {
		if referenceField.DBName == "" {
			continue
		}
		targetField := targetSchema.LookUpField(referenceField.Name)
		require.NotNil(t, targetField, "User.%s is missing", referenceField.Name)
		expectedTargetColumn := referenceUserTargetColumn(referenceField.DBName)
		assert.Equal(t, expectedTargetColumn, targetField.DBName, referenceField.Name+" target column")
		assert.Equal(t, referenceField.PrimaryKey, targetField.PrimaryKey, referenceField.Name+" primary key")
		assert.Equal(t, referenceField.AutoIncrement, targetField.AutoIncrement, referenceField.Name+" auto increment")
		assert.Equal(t, referenceField.NotNull, targetField.NotNull, referenceField.Name+" nullability")
		assert.Equal(t, referenceField.Unique, targetField.Unique, referenceField.Name+" column uniqueness")
		assert.Equal(t, referenceField.HasDefaultValue, targetField.HasDefaultValue, referenceField.Name+" default presence")
		assert.Equal(t, referenceField.DefaultValue, targetField.DefaultValue, referenceField.Name+" default")
		assert.Equal(t, referenceField.AutoCreateTime, targetField.AutoCreateTime, referenceField.Name+" automatic creation time")
		if referenceField.Name == "StripeCustomer" {
			assert.Equal(t, "varchar(64)", strings.ToLower(referenceField.TagSettings["TYPE"]))
			assert.Equal(t, "varchar(128)", strings.ToLower(targetField.TagSettings["TYPE"]))
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
				if referenceField.DBName == "" {
					continue
				}
				targetField := targetSchema.LookUpField(referenceField.Name)
				require.NotNil(t, targetField)
				if referenceField.Name == "StripeCustomer" {
					assert.Equal(t, "varchar(64)", strings.ToLower(dialect.DataTypeOf(referenceField)))
					assert.Equal(t, "varchar(128)", strings.ToLower(dialect.DataTypeOf(targetField)))
					continue
				}
				assert.Equal(t, dialect.DataTypeOf(referenceField), dialect.DataTypeOf(targetField),
					"User.%s %s type", referenceField.Name, dialect.Name())
			}
		})
	}

	referenceIndexes := referenceSchema.ParseIndexes()
	targetIndexes := targetSchema.ParseIndexes()
	assert.Len(t, targetIndexes, len(referenceIndexes)+1, "the verified-email ownership index is the only target-only User index")
	for _, referenceIndex := range referenceIndexes {
		targetName := referenceUserTargetIndex(referenceIndex.Name)
		targetIndex := targetSchema.LookIndex(targetName)
		require.NotNil(t, targetIndex, "missing User index %s", targetName)
		assert.Equal(t, referenceIndex.Class, targetIndex.Class, referenceIndex.Name+" class")
		require.Len(t, targetIndex.Fields, len(referenceIndex.Fields), referenceIndex.Name+" fields")
		for i := range referenceIndex.Fields {
			assert.Equal(t, referenceUserTargetColumn(referenceIndex.Fields[i].DBName), targetIndex.Fields[i].DBName,
				referenceIndex.Name+" column")
			assert.Equal(t, referenceIndex.Fields[i].Priority, targetIndex.Fields[i].Priority,
				referenceIndex.Name+" priority")
		}
	}
	require.NotNil(t, targetSchema.LookIndex("ux_users_verified_email_key"))
	referenceConstraints := referenceSchema.ParseUniqueConstraints()
	targetConstraints := targetSchema.ParseUniqueConstraints()
	require.Len(t, referenceConstraints, 1)
	require.Len(t, targetConstraints, 1)
	require.Contains(t, referenceConstraints, "uni_users_username")
	require.Contains(t, targetConstraints, "uni_users_username")
}

func TestReferenceUserSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceUserSchemaMatchesSQLite(t, db)
	assertReferenceUserSchema(t, db)

	insertReferenceUserWithDatabaseDefaults(t, db, "schema-direct-user", "schema-direct-aff")

	duplicateErr := db.Exec(`INSERT INTO users (username, password, aff_code) VALUES (?, ?, ?)`,
		"schema-direct-user", "password", "schema-direct-aff-2").Error
	assert.Error(t, duplicateErr, "username uniqueness must survive the index-shape migration")

	created := User{Username: "schema-auto-created", Password: "password", AffCode: "schema-auto-aff"}
	require.NoError(t, db.Create(&created).Error)
	assert.NotZero(t, created.CreatedAt, "the reference autoCreateTime tag must populate GORM-created rows")
	assert.Equal(t, "default", created.Group)
	assert.Equal(t, 1, created.Role)
	assert.Equal(t, 1, created.Status)

	columnsBefore := sqliteColumnSignatures(t, db, User{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, User{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, User{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, User{}.TableName()))
	assertReferenceUserSchemaMatchesSQLite(t, db)
}

func TestReferenceUserSchemaSQLitePreservesLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacyUserSchema{}))

	legacy := makeLegacyReferenceUser("legacy-user", 7)
	require.NoError(t, db.Create(&legacy).Error)
	normalizedEmail, verifiedKey, err := NormalizeVerifiedEmail("verified@example.com")
	require.NoError(t, err)
	verified := legacyUserSchema{
		Username: "verified-legacy", Password: "password", Email: normalizedEmail,
		EmailVerified: true, VerifiedEmailKey: &verifiedKey, AffCode: "verified-aff",
		Role: 1, Status: 1, Group: "default", AuthVersion: 3, QuotaReminderAt: 17,
	}
	require.NoError(t, db.Create(&verified).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO users (username, password, aff_code, auth_version, role, status, quota, "group")
		VALUES (?, ?, ?, ?, NULL, NULL, NULL, NULL)`,
		"nullable-legacy", "password", "nullable-aff", 2).Error)
	var nullableID int
	require.NoError(t, db.Table(User{}.TableName()).Select("id").Where("username = ?", "nullable-legacy").Scan(&nullableID).Error)

	require.NoError(t, migrateDB())
	assertReferenceUserSchemaMatchesSQLite(t, db)
	assertReferenceUserSchema(t, db)
	assertLegacyReferenceUserPreserved(t, db, legacy)
	assertLegacyVerifiedUserPreserved(t, db, verified)
	assertLegacyUserNullDefaultsPreserved(t, db, nullableID)

	columnsBefore := sqliteColumnSignatures(t, db, User{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, User{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, User{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, User{}.TableName()))
	assertLegacyReferenceUserPreserved(t, db, legacy)
	assertLegacyVerifiedUserPreserved(t, db, verified)
	assertLegacyUserNullDefaultsPreserved(t, db, nullableID)
}

func TestReferenceUserSchemaPreflightRejectsUnsafeLegacyValues(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacyUserSchema{}))
	legacy := makeLegacyReferenceUser("unsafe-user", 5)
	legacy.Remark = strings.Repeat("r", referenceUserRemarkLimit+1)
	require.NoError(t, db.Create(&legacy).Error)

	err := rejectOversizedLegacyUserRemark(db)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
	assert.Contains(t, err.Error(), "users.remark")
	var stored legacyUserSchema
	require.NoError(t, db.First(&stored, legacy.Id).Error)
	assert.Equal(t, legacy.Remark, stored.Remark)

	require.NoError(t, db.Model(&legacyUserSchema{}).Where("id = ?", legacy.Id).
		Update("quota", referenceIntMax+1).Error)
	err = rejectOutOfRangeLegacyUserInteger(db, "quota")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
	assert.Contains(t, err.Error(), "users.quota")
	require.NoError(t, db.First(&stored, legacy.Id).Error)
	assert.EqualValues(t, referenceIntMax+1, stored.Quota)
}

func assertReferenceUserSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-user.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&referenceUserSchema{}))

	referenceColumns := sqliteColumnSignatures(t, reference, User{}.TableName())
	targetColumns := sqliteColumnSignatures(t, db, User{}.TableName())
	targetByName := make(map[string]sqliteColumnSignature, len(targetColumns))
	for _, column := range targetColumns {
		targetByName[column.Name] = column
	}
	for _, referenceColumn := range referenceColumns {
		targetName := referenceUserTargetColumn(referenceColumn.Name)
		targetColumn, ok := targetByName[targetName]
		require.True(t, ok, "missing mapped User column %s", targetName)
		delete(targetByName, targetName)
		targetColumn.Name = referenceColumn.Name
		if referenceColumn.Name == "stripe_customer" {
			assert.Equal(t, "varchar(64)", strings.ToLower(referenceColumn.Type))
			assert.Equal(t, "varchar(128)", strings.ToLower(targetColumn.Type))
			targetColumn.Type = referenceColumn.Type
		}
		assert.Equal(t, referenceColumn, targetColumn, "User column %s", referenceColumn.Name)
	}
	assert.Equal(t, []string{"email_verified", "quota_reminder_at", "verified_email_key"}, sortedUserColumnNames(targetByName),
		"verified-email/reminder columns must be the only target-only User columns")

	referenceIndexes := sqliteIndexSignatures(t, reference, User{}.TableName())
	targetIndexes := sqliteIndexSignatures(t, db, User{}.TableName())
	normalizedTargetIndexes := make([]sqliteIndexSignature, 0, len(targetIndexes)-1)
	for _, index := range targetIndexes {
		if index.Name == "ux_users_verified_email_key" {
			continue
		}
		if index.Columns == "linuxdo_id" {
			index.Columns = "linux_do_id"
		}
		normalizedTargetIndexes = append(normalizedTargetIndexes, index)
	}
	sort.Slice(normalizedTargetIndexes, func(i, j int) bool { return normalizedTargetIndexes[i].Name < normalizedTargetIndexes[j].Name })
	assert.Equal(t, referenceIndexes, normalizedTargetIndexes, "User indexes after explicit target-only normalization")
	assert.False(t, db.Migrator().HasColumn(&User{}, "aff_history"), "the live target column must not be renamed destructively")
	assert.False(t, db.Migrator().HasColumn(&User{}, "linux_do_id"), "the live target column must not be renamed destructively")
}

// assertReferenceUserSchema is driver-neutral and is also invoked by the
// opt-in MySQL/PostgreSQL migration gate.
func assertReferenceUserSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, index := range []struct {
		name   string
		unique bool
	}{
		{name: "idx_users_username"},
		{name: "idx_users_display_name"},
		{name: "idx_users_email"},
		{name: "idx_users_git_hub_id"},
		{name: "idx_users_discord_id"},
		{name: "idx_users_oidc_id"},
		{name: "idx_users_we_chat_id"},
		{name: "idx_users_telegram_id"},
		{name: "idx_users_access_token", unique: true},
		{name: "idx_users_aff_code", unique: true},
		{name: "idx_users_inviter_id"},
		{name: "idx_users_linux_do_id"},
		{name: "idx_users_stripe_customer"},
		{name: "idx_users_deleted_at"},
		{name: "ux_users_verified_email_key", unique: true},
	} {
		assert.True(t, db.Migrator().HasIndex(&User{}, index.name), "missing migrated index %s", index.name)
		assertIndexUnique(t, db, &User{}, index.name, index.unique)
	}
	assertColumnUnique(t, db, User{}.TableName(), "username", true)
	assertColumnNullable(t, db, User{}.TableName(), "password", false)
	assertColumnNullable(t, db, User{}.TableName(), "auth_version", false)
	for column, expected := range map[string]string{
		"role": "1", "status": "1", "quota": "0", "used_quota": "0",
		"request_count": "0", "group": "default", "aff_count": "0",
		"aff_quota": "0", "aff_history_quota": "0", "last_login_at": "0",
		"auth_version": "1", "quota_reminder_at": "0",
	} {
		assertColumnDefault(t, db, User{}.TableName(), column, expected)
	}

	statement := &gorm.Statement{DB: db}
	require.NoError(t, statement.Parse(&User{}))
	for _, fieldName := range []string{"Username", "DisplayName", "Email", "GitHubId", "DiscordId", "OidcId", "WeChatId", "TelegramId", "LinuxDOId"} {
		field := statement.Schema.LookUpField(fieldName)
		require.NotNil(t, field)
		assert.Empty(t, field.TagSettings["TYPE"], "User.%s must use the reference dialect-native string type", fieldName)
	}
	for _, fieldName := range []string{"Role", "Status", "Quota", "UsedQuota", "RequestCount", "AffCount", "AffQuota", "AffHistoryQuota", "InviterId"} {
		field := statement.Schema.LookUpField(fieldName)
		require.NotNil(t, field)
		assert.Equal(t, "int", strings.ToLower(field.TagSettings["TYPE"]), "User.%s declared type", fieldName)
	}
	authVersion := statement.Schema.LookUpField("AuthVersion")
	require.NotNil(t, authVersion)
	assert.Equal(t, "bigint", strings.ToLower(authVersion.TagSettings["TYPE"]))
	createdAt := statement.Schema.LookUpField("CreatedAt")
	require.NotNil(t, createdAt)
	assert.NotZero(t, createdAt.AutoCreateTime)

	switch db.Dialector.Name() {
	case "sqlite":
		for _, column := range []string{"username", "display_name", "email", "github_id", "discord_id", "oidc_id", "wechat_id", "telegram_id", "linuxdo_id"} {
			assertColumnDatabaseType(t, db, User{}.TableName(), column, "text")
		}
	case "mysql":
		for _, column := range []string{"username", "display_name", "email", "github_id", "discord_id", "oidc_id", "wechat_id", "telegram_id", "linuxdo_id"} {
			assertColumnLength(t, db, User{}.TableName(), column, referenceUserIndexedStringLimit)
		}
		assertColumnLength(t, db, User{}.TableName(), "remark", referenceUserRemarkLimit)
		assertColumnLength(t, db, User{}.TableName(), "stripe_customer", 128)
	case "postgres":
		for _, column := range []string{"username", "display_name", "email", "github_id", "discord_id", "oidc_id", "wechat_id", "telegram_id", "linuxdo_id"} {
			assertColumnDatabaseType(t, db, User{}.TableName(), column, "text")
		}
		assertColumnLength(t, db, User{}.TableName(), "remark", referenceUserRemarkLimit)
		assertColumnLength(t, db, User{}.TableName(), "stripe_customer", 128)
	default:
		require.FailNow(t, "unsupported User schema test dialect", db.Dialector.Name())
	}
}

func makeLegacyReferenceUser(username string, authVersion int64) legacyUserSchema {
	accessToken := strings.Repeat("a", 32)
	return legacyUserSchema{
		Username: username + strings.Repeat("u", 64-len(username)), Password: "legacy-password",
		DisplayName: strings.Repeat("d", 64), Role: 0, Status: 0,
		Email: strings.Repeat("e", 128), GitHubId: strings.Repeat("g", 64),
		DiscordId: strings.Repeat("d", 64), OidcId: strings.Repeat("o", 64),
		WeChatId: strings.Repeat("w", 64), TelegramId: strings.Repeat("t", 64),
		AccessToken: &accessToken, Quota: 101, UsedQuota: 102, RequestCount: 103,
		Group: "", AffCode: username + "-aff", AffCount: 104,
		AffQuota: 105, AffHistoryQuota: 106, InviterId: 107,
		LinuxDOId: strings.Repeat("l", 64), Setting: `{"legacy":true}`,
		Remark: strings.Repeat("r", referenceUserRemarkLimit), StripeCustomer: strings.Repeat("s", 128),
		CreatedAt: 108, LastLoginAt: 109, AuthVersion: authVersion, QuotaReminderAt: 110,
	}
}

func assertLegacyReferenceUserPreserved(t *testing.T, db *gorm.DB, legacy legacyUserSchema) {
	t.Helper()
	var migrated User
	require.NoError(t, db.Unscoped().First(&migrated, legacy.Id).Error)
	assert.Equal(t, legacy.Username, migrated.Username)
	assert.Equal(t, legacy.Password, migrated.Password)
	assert.Equal(t, legacy.DisplayName, migrated.DisplayName)
	assert.Equal(t, legacy.Role, migrated.Role, "adding a default must not rewrite an explicit legacy role")
	assert.Equal(t, legacy.Status, migrated.Status, "adding a default must not enable a disabled legacy user")
	assert.Equal(t, legacy.Email, migrated.Email)
	assert.Equal(t, legacy.EmailVerified, migrated.EmailVerified)
	assert.Nil(t, migrated.VerifiedEmailKey)
	assert.Equal(t, legacy.QuotaReminderAt, migrated.QuotaReminderAt)
	assert.Equal(t, legacy.GitHubId, migrated.GitHubId)
	assert.Equal(t, legacy.DiscordId, migrated.DiscordId)
	assert.Equal(t, legacy.OidcId, migrated.OidcId)
	assert.Equal(t, legacy.WeChatId, migrated.WeChatId)
	assert.Equal(t, legacy.TelegramId, migrated.TelegramId)
	assert.Equal(t, legacy.AccessToken, migrated.AccessToken)
	assert.Equal(t, legacy.Quota, migrated.Quota)
	assert.Equal(t, legacy.UsedQuota, migrated.UsedQuota)
	assert.Equal(t, legacy.RequestCount, migrated.RequestCount)
	assert.Equal(t, legacy.Group, migrated.Group, "adding a default must preserve the legacy empty group marker")
	assert.Equal(t, legacy.AffCode, migrated.AffCode)
	assert.Equal(t, legacy.AffCount, migrated.AffCount)
	assert.Equal(t, legacy.AffQuota, migrated.AffQuota)
	assert.Equal(t, legacy.AffHistoryQuota, migrated.AffHistoryQuota)
	assert.Equal(t, legacy.InviterId, migrated.InviterId)
	assert.Equal(t, legacy.LinuxDOId, migrated.LinuxDOId)
	assert.Equal(t, legacy.Setting, migrated.Setting)
	assert.Equal(t, legacy.Remark, migrated.Remark)
	assert.Equal(t, legacy.StripeCustomer, migrated.StripeCustomer, "the wider target Stripe identifier must remain intact")
	assert.Equal(t, legacy.CreatedAt, migrated.CreatedAt)
	assert.Equal(t, legacy.LastLoginAt, migrated.LastLoginAt)
	assert.Equal(t, legacy.AuthVersion, migrated.AuthVersion)
}

func assertLegacyVerifiedUserPreserved(t *testing.T, db *gorm.DB, legacy legacyUserSchema) {
	t.Helper()
	var migrated User
	require.NoError(t, db.Unscoped().First(&migrated, legacy.Id).Error)
	assert.True(t, migrated.EmailVerified)
	assert.Equal(t, legacy.Email, migrated.Email)
	if assert.NotNil(t, migrated.VerifiedEmailKey) && assert.NotNil(t, legacy.VerifiedEmailKey) {
		assert.Equal(t, *legacy.VerifiedEmailKey, *migrated.VerifiedEmailKey)
	}
	assert.Equal(t, legacy.QuotaReminderAt, migrated.QuotaReminderAt)
	assert.Equal(t, legacy.AuthVersion, migrated.AuthVersion)
}

func insertReferenceUserWithDatabaseDefaults(t *testing.T, db *gorm.DB, username, affCode string) User {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO users (username, password, aff_code) VALUES (?, ?, ?)`,
		username, "password", affCode).Error)
	var direct User
	require.NoError(t, db.Where("username = ?", username).First(&direct).Error)
	assert.Equal(t, 1, direct.Role)
	assert.Equal(t, 1, direct.Status)
	assert.Zero(t, direct.Quota)
	assert.Zero(t, direct.UsedQuota)
	assert.Zero(t, direct.RequestCount)
	assert.Equal(t, "default", direct.Group)
	assert.Zero(t, direct.AffCount)
	assert.Zero(t, direct.AffQuota)
	assert.Zero(t, direct.AffHistoryQuota)
	assert.Zero(t, direct.LastLoginAt)
	assert.EqualValues(t, 1, direct.AuthVersion)
	assert.Zero(t, direct.QuotaReminderAt)
	return direct
}

func assertLegacyUserNullDefaultsPreserved(t *testing.T, db *gorm.DB, id int) {
	t.Helper()
	var role, status, quota sql.NullInt64
	var group sql.NullString
	row := db.Table(User{}.TableName()).Select("role, status, quota, `group`").Where("id = ?", id).Row()
	require.NoError(t, row.Scan(&role, &status, &quota, &group))
	assert.False(t, role.Valid, "adding the role default must not rewrite legacy NULL")
	assert.False(t, status.Valid, "adding the status default must not rewrite legacy NULL")
	assert.False(t, quota.Valid, "adding the quota default must not rewrite legacy NULL")
	assert.False(t, group.Valid, "adding the group default must not rewrite legacy NULL")
}

func exerciseReferenceUserSchemaLegacyExternal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Contains(t, []string{"mysql", "postgres"}, db.Dialector.Name())
	require.NoError(t, db.Exec("DELETE FROM external_identity_claims").Error)
	require.NoError(t, db.Migrator().DropTable(&User{}))
	require.NoError(t, db.AutoMigrate(&legacyUserSchema{}))
	legacy := makeLegacyReferenceUser("server-user", 9)
	require.NoError(t, db.Create(&legacy).Error)

	require.NoError(t, migrateDB())
	assertReferenceUserSchema(t, db)
	assertLegacyReferenceUserPreserved(t, db, legacy)
	insertReferenceUserWithDatabaseDefaults(t, db, "server-direct-user", "server-direct-aff")
	require.NoError(t, migrateDB())
	assertLegacyReferenceUserPreserved(t, db, legacy)

	for _, unsafe := range []struct {
		name   string
		column string
		value  any
	}{
		{name: "remark width", column: "remark", value: strings.Repeat("r", referenceUserRemarkLimit+1)},
		{name: "integer range", column: "quota", value: referenceIntMax + 1},
	} {
		t.Run("external unsafe User "+unsafe.name, func(t *testing.T) {
			require.NoError(t, db.Exec("DELETE FROM external_identity_claims").Error)
			require.NoError(t, db.Migrator().DropTable(&User{}))
			require.NoError(t, db.AutoMigrate(&legacyUserSchema{}))
			row := makeLegacyReferenceUser("unsafe-server", 4)
			require.NoError(t, db.Create(&row).Error)
			require.NoError(t, db.Table(User{}.TableName()).Where("id = ?", row.Id).Update(unsafe.column, unsafe.value).Error)

			err := migrateDB()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
			assert.Contains(t, err.Error(), "users."+unsafe.column)
			if unsafe.column == "remark" {
				var stored string
				require.NoError(t, db.Table(User{}.TableName()).Select(unsafe.column).Where("id = ?", row.Id).Scan(&stored).Error)
				assert.Equal(t, unsafe.value, stored, "the rejected migration must not truncate the legacy remark")
			} else {
				var stored int64
				require.NoError(t, db.Table(User{}.TableName()).Select(unsafe.column).Where("id = ?", row.Id).Scan(&stored).Error)
				assert.EqualValues(t, unsafe.value, stored, "the rejected migration must not rewrite the legacy integer")
			}

			require.NoError(t, db.Unscoped().Delete(&legacyUserSchema{}, row.Id).Error)
			require.NoError(t, migrateDB())
			assertReferenceUserSchema(t, db)
		})
	}
}

func referenceUserTargetColumn(reference string) string {
	switch reference {
	case "aff_history":
		return "aff_history_quota"
	case "linux_do_id":
		return "linuxdo_id"
	default:
		return reference
	}
}

func referenceUserTargetIndex(reference string) string {
	return reference
}

func sortedUserColumnNames(columns map[string]sqliteColumnSignature) []string {
	result := make([]string, 0, len(columns))
	for name := range columns {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
