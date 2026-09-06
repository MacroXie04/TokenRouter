package model

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
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

const referenceChannelIndexedStringLimit = int64(referenceChannelMySQLBaseURLLimit)

// referenceChannelInfo is the database-facing shape used by the pinned
// reference. Only its Scanner/Valuer behavior and declared JSON type affect
// the channels schema exercised here.
type referenceChannelInfo struct {
	IsMultiKey             bool           `json:"is_multi_key"`
	MultiKeySize           int            `json:"multi_key_size"`
	MultiKeyStatusList     map[int]int    `json:"multi_key_status_list"`
	MultiKeyDisabledReason map[int]string `json:"multi_key_disabled_reason,omitempty"`
	MultiKeyDisabledTime   map[int]int64  `json:"multi_key_disabled_time,omitempty"`
	MultiKeyPollingIndex   int            `json:"multi_key_polling_index"`
	MultiKeyMode           int            `json:"multi_key_mode"`
}

func (c referenceChannelInfo) Value() (driver.Value, error) {
	return json.Marshal(&c)
}

func (c *referenceChannelInfo) Scan(value any) error {
	if value == nil {
		*c = referenceChannelInfo{}
		return nil
	}
	var data []byte
	switch typed := value.(type) {
	case []byte:
		data = typed
	case string:
		data = []byte(typed)
	default:
		return fmt.Errorf("unsupported reference ChannelInfo value %T", value)
	}
	return json.Unmarshal(data, c)
}

// referenceChannelSchema independently transcribes the pinned reference
// Channel model. It deliberately does not reuse Channel's tags.
type referenceChannelSchema struct {
	Id                 int
	Type               int    `gorm:"default:0"`
	Key                string `gorm:"not null"`
	OpenAIOrganization *string
	TestModel          *string
	Status             int    `gorm:"default:1"`
	Name               string `gorm:"index"`
	Weight             *uint  `gorm:"default:0"`
	CreatedTime        int64  `gorm:"bigint"`
	TestTime           int64  `gorm:"bigint"`
	ResponseTime       int
	BaseURL            *string `gorm:"column:base_url;default:''"`
	Other              string
	Balance            float64
	BalanceUpdatedTime int64 `gorm:"bigint"`
	Models             string
	Group              string  `gorm:"type:varchar(64);default:'default'"`
	UsedQuota          int64   `gorm:"bigint;default:0"`
	ModelMapping       *string `gorm:"type:text"`
	StatusCodeMapping  *string `gorm:"type:varchar(1024);default:''"`
	Priority           *int64  `gorm:"bigint;default:0"`
	AutoBan            *int    `gorm:"default:1"`
	OtherInfo          string
	Tag                *string              `gorm:"index"`
	Setting            *string              `gorm:"type:text"`
	ParamOverride      *string              `gorm:"type:text"`
	HeaderOverride     *string              `gorm:"type:text"`
	Remark             *string              `gorm:"type:varchar(255)"`
	ChannelInfo        referenceChannelInfo `gorm:"type:json"`
	OtherSettings      string               `gorm:"column:settings"`
}

func (referenceChannelSchema) TableName() string { return "channels" }

// legacyChannelSchema is the exact pre-alignment TokenRouter declaration.
// Its value-string representation and text channel_info column are retained
// so migration tests cover real existing installations.
type legacyChannelSchema struct {
	Id                 int `gorm:"primaryKey"`
	Type               int
	Key                string `gorm:"not null"`
	OpenAIOrganization string `gorm:"type:varchar(128)"`
	TestModel          string `gorm:"type:varchar(128)"`
	Status             int
	Name               string `gorm:"index;type:varchar(64)"`
	Weight             *uint
	CreatedTime        int64
	TestTime           int64
	ResponseTime       int
	BaseURL            string `gorm:"type:text"`
	Other              string `gorm:"type:text"`
	Balance            float64
	BalanceUpdatedTime int64
	Models             string `gorm:"type:text"`
	Group              string `gorm:"type:varchar(64)"`
	UsedQuota          int64
	ModelMapping       string `gorm:"type:text"`
	StatusCodeMapping  string `gorm:"type:varchar(1024)"`
	Priority           *int64
	AutoBan            *int
	OtherInfo          string `gorm:"type:text"`
	Tag                string `gorm:"index;type:varchar(64)"`
	Setting            string `gorm:"type:text"`
	ParamOverride      string `gorm:"type:text"`
	HeaderOverride     string `gorm:"type:text"`
	Remark             string `gorm:"type:varchar(255)"`
	ChannelInfo        string `gorm:"type:text"`
	OtherSettings      string `gorm:"column:settings;type:text"`
}

func (legacyChannelSchema) TableName() string { return "channels" }

var referenceChannelPointerRepresentationResiduals = map[string]bool{
	"OpenAIOrganization": true,
	"TestModel":          true,
	"BaseURL":            true,
	"ModelMapping":       true,
	"StatusCodeMapping":  true,
	"Tag":                true,
	"Setting":            true,
	"ParamOverride":      true,
	"HeaderOverride":     true,
	"Remark":             true,
}

var referenceChannelNoDefaultColumns = []string{
	"open_ai_organization", "test_model", "name", "created_time", "test_time",
	"response_time", "other", "balance", "balance_updated_time", "models",
	"model_mapping", "priority", "other_info", "tag", "setting", "param_override",
	"header_override", "remark", "channel_info", "settings",
}

func TestReferenceChannelSchemaPortableDialects(t *testing.T) {
	targetSchema, err := schema.Parse(&Channel{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceSchema, err := schema.Parse(&referenceChannelSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	for _, referenceField := range referenceSchema.Fields {
		if referenceField.DBName == "" {
			continue
		}
		targetField := targetSchema.LookUpField(referenceField.Name)
		require.NotNil(t, targetField, "Channel.%s is missing", referenceField.Name)
		assert.Equal(t, referenceField.DBName, targetField.DBName, referenceField.Name+" column")
		assert.Equal(t, referenceField.PrimaryKey, targetField.PrimaryKey, referenceField.Name+" primary key")
		assert.Equal(t, referenceField.AutoIncrement, targetField.AutoIncrement, referenceField.Name+" auto increment")
		assert.Equal(t, referenceField.NotNull, targetField.NotNull, referenceField.Name+" nullability")
		assert.Equal(t, referenceField.Unique, targetField.Unique, referenceField.Name+" uniqueness")
		if referenceField.Name == "Priority" {
			assert.True(t, referenceField.HasDefaultValue)
			assert.Equal(t, "0", referenceField.DefaultValue)
			assert.False(t, targetField.HasDefaultValue, "nil Channel priority must remain the ability-priority inheritance marker")
		} else {
			assert.Equal(t, referenceField.HasDefaultValue, targetField.HasDefaultValue, referenceField.Name+" default presence")
			assert.Equal(t, referenceField.DefaultValue, targetField.DefaultValue, referenceField.Name+" default")
		}
		assert.Equal(t, referenceField.Size, targetField.Size, referenceField.Name+" size")
		if referenceField.Name == "ChannelInfo" {
			assert.Equal(t, "json", strings.ToLower(referenceField.TagSettings["TYPE"]))
			assert.Equal(t, "text", strings.ToLower(targetField.TagSettings["TYPE"]))
		}
		if referenceChannelPointerRepresentationResiduals[referenceField.Name] {
			assert.Equal(t, reflect.Ptr, referenceField.FieldType.Kind(), referenceField.Name+" reference representation")
			assert.NotEqual(t, reflect.Ptr, targetField.FieldType.Kind(), referenceField.Name+" retained target representation")
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
				if referenceField.Name == "ChannelInfo" {
					assert.Equal(t, "json", strings.ToLower(dialect.DataTypeOf(referenceField)))
					assert.Equal(t, "text", strings.ToLower(dialect.DataTypeOf(targetField)))
					continue
				}
				assert.Equal(t, dialect.DataTypeOf(referenceField), dialect.DataTypeOf(targetField),
					"Channel.%s %s type", referenceField.Name, dialect.Name())
				if referenceField.Name == "BaseURL" {
					if dialect.Name() == "mysql" {
						assert.Equal(t, "varchar(191)", strings.ToLower(dialect.DataTypeOf(referenceField)))
					} else {
						assert.Equal(t, "text", strings.ToLower(dialect.DataTypeOf(referenceField)))
					}
				}
			}
		})
	}

	referenceIndexes := referenceSchema.ParseIndexes()
	targetIndexes := targetSchema.ParseIndexes()
	require.Len(t, targetIndexes, len(referenceIndexes))
	for _, referenceIndex := range referenceIndexes {
		targetIndex := targetSchema.LookIndex(referenceIndex.Name)
		require.NotNil(t, targetIndex, "missing Channel index %s", referenceIndex.Name)
		assert.Equal(t, referenceIndex.Class, targetIndex.Class, referenceIndex.Name+" class")
		require.Len(t, targetIndex.Fields, len(referenceIndex.Fields), referenceIndex.Name+" fields")
		for i := range referenceIndex.Fields {
			assert.Equal(t, referenceIndex.Fields[i].DBName, targetIndex.Fields[i].DBName, referenceIndex.Name+" column")
			assert.Equal(t, referenceIndex.Fields[i].Priority, targetIndex.Fields[i].Priority, referenceIndex.Name+" priority")
		}
	}
	assert.Empty(t, referenceSchema.ParseUniqueConstraints())
	assert.Empty(t, targetSchema.ParseUniqueConstraints())
}

func TestReferenceChannelSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceChannelSchemaMatchesSQLite(t, db)
	assertReferenceChannelSchema(t, db)

	directID := insertReferenceChannelWithDatabaseDefaults(t, db, "schema-direct-key")
	assertChannelColumnsNull(t, db, directID, referenceChannelNoDefaultColumns)

	created := Channel{Key: "schema-gorm-key"}
	require.NoError(t, db.Create(&created).Error)
	var loaded Channel
	require.NoError(t, db.First(&loaded, created.Id).Error)
	assertReferenceChannelDefaultsLoaded(t, loaded)

	columnsBefore := sqliteColumnSignatures(t, db, Channel{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, Channel{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, Channel{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, Channel{}.TableName()))
	assertReferenceChannelSchemaMatchesSQLite(t, db)
}

func TestReferenceChannelSchemaSQLitePreservesLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacyChannelSchema{}))

	legacy := makeLegacyReferenceChannel("legacy-channel-key")
	require.NoError(t, db.Create(&legacy).Error)
	nullID := insertChannelKeyOnly(t, db, "legacy-null-key")

	require.NoError(t, migrateDB())
	assertReferenceChannelSchemaMatchesSQLite(t, db)
	assertReferenceChannelSchema(t, db)
	assertLegacyReferenceChannelPreserved(t, db, legacy)
	assertChannelColumnsNull(t, db, nullID, append(referenceChannelDefaultColumnNames(), referenceChannelNoDefaultColumns...))

	columnsBefore := sqliteColumnSignatures(t, db, Channel{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, Channel{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, Channel{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, Channel{}.TableName()))
	assertLegacyReferenceChannelPreserved(t, db, legacy)
	assertChannelColumnsNull(t, db, nullID, append(referenceChannelDefaultColumnNames(), referenceChannelNoDefaultColumns...))
}

func TestReferenceChannelSchemaPreflightRejectsUnsafeLegacyBaseURL(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacyChannelSchema{}))
	legacy := makeLegacyReferenceChannel("unsafe-channel-key")
	legacy.BaseURL = strings.Repeat("b", referenceChannelMySQLBaseURLLimit+1)
	require.NoError(t, db.Create(&legacy).Error)

	err := rejectOversizedLegacyChannelBaseURL(db)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
	assert.Contains(t, err.Error(), "channels.base_url")
	var stored legacyChannelSchema
	require.NoError(t, db.First(&stored, legacy.Id).Error)
	assert.Equal(t, legacy.BaseURL, stored.BaseURL)

	// SQLite retains its unbounded TEXT representation, so complete startup is
	// safe there even though MySQL must refuse the varchar(191) narrowing.
	require.NoError(t, migrateDB())
	var migrated Channel
	require.NoError(t, db.First(&migrated, legacy.Id).Error)
	assert.Equal(t, legacy.BaseURL, migrated.BaseURL)
}

func assertReferenceChannelSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-channel.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&referenceChannelSchema{}))

	referenceColumns := sqliteColumnSignatures(t, reference, Channel{}.TableName())
	targetColumns := sqliteColumnSignatures(t, db, Channel{}.TableName())
	require.Len(t, targetColumns, len(referenceColumns))
	targetByName := make(map[string]sqliteColumnSignature, len(targetColumns))
	for _, column := range targetColumns {
		targetByName[column.Name] = column
	}
	for _, referenceColumn := range referenceColumns {
		targetColumn, ok := targetByName[referenceColumn.Name]
		require.True(t, ok, "missing Channel column %s", referenceColumn.Name)
		delete(targetByName, referenceColumn.Name)
		if referenceColumn.Name == "channel_info" {
			assert.Equal(t, "json", strings.ToLower(referenceColumn.Type))
			assert.Equal(t, "text", strings.ToLower(targetColumn.Type))
			targetColumn.Type = referenceColumn.Type
		}
		if referenceColumn.Name == "priority" {
			assert.True(t, referenceColumn.HasDefault)
			assert.Equal(t, "0", referenceColumn.Default)
			assert.False(t, targetColumn.HasDefault, "nil Channel priority must remain the ability-priority inheritance marker")
			targetColumn.HasDefault = referenceColumn.HasDefault
			targetColumn.Default = referenceColumn.Default
		}
		assert.Equal(t, referenceColumn, targetColumn, "Channel column %s", referenceColumn.Name)
	}
	assert.Empty(t, targetByName)
	assert.Equal(t,
		sqliteIndexSignatures(t, reference, Channel{}.TableName()),
		sqliteIndexSignatures(t, db, Channel{}.TableName()),
		"Channel indexes",
	)
}

// assertReferenceChannelSchema is driver-neutral and is also invoked by the
// opt-in MySQL/PostgreSQL migration gate.
func assertReferenceChannelSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, index := range []string{"idx_channels_name", "idx_channels_tag"} {
		assert.True(t, db.Migrator().HasIndex(&Channel{}, index), "missing migrated index %s", index)
		assertIndexUnique(t, db, &Channel{}, index, false)
	}
	assertColumnNullable(t, db, Channel{}.TableName(), "key", false)
	for _, column := range append(referenceChannelDefaultColumnNames(), referenceChannelNoDefaultColumns...) {
		assertColumnNullable(t, db, Channel{}.TableName(), column, true)
	}
	for column, expected := range map[string]string{
		"type": "0", "status": "1", "weight": "0", "base_url": "",
		"group": "default", "used_quota": "0", "status_code_mapping": "", "auto_ban": "1",
	} {
		assertColumnDefault(t, db, Channel{}.TableName(), column, expected)
	}
	priorityColumn := requireColumnType(t, db, Channel{}.TableName(), "priority")
	_, priorityHasDefault := priorityColumn.DefaultValue()
	assert.False(t, priorityHasDefault, "Channel priority must preserve nil-as-inherit semantics")

	statement := &gorm.Statement{DB: db}
	require.NoError(t, statement.Parse(&Channel{}))
	for _, fieldName := range []string{
		"OpenAIOrganization", "TestModel", "Name", "BaseURL", "Other", "Models",
		"OtherInfo", "Tag", "OtherSettings",
	} {
		field := statement.Schema.LookUpField(fieldName)
		require.NotNil(t, field)
		assert.Empty(t, field.TagSettings["TYPE"], "Channel.%s must use the reference dialect-native string type", fieldName)
	}
	for _, fieldName := range []string{"CreatedTime", "TestTime", "BalanceUpdatedTime", "UsedQuota", "Priority"} {
		field := statement.Schema.LookUpField(fieldName)
		require.NotNil(t, field)
		_, declared := field.TagSettings["BIGINT"]
		assert.True(t, declared, "Channel.%s must retain the reference bigint setting", fieldName)
	}
	channelInfo := statement.Schema.LookUpField("ChannelInfo")
	require.NotNil(t, channelInfo)
	assert.Equal(t, "text", strings.ToLower(channelInfo.TagSettings["TYPE"]))

	switch db.Dialector.Name() {
	case "sqlite":
		assertColumnDatabaseType(t, db, Channel{}.TableName(), "channel_info", "text")
	case "mysql":
		for _, column := range []string{"name", "tag", "base_url"} {
			assertColumnLength(t, db, Channel{}.TableName(), column, referenceChannelIndexedStringLimit)
		}
		assertColumnLength(t, db, Channel{}.TableName(), "group", 64)
		assertColumnLength(t, db, Channel{}.TableName(), "status_code_mapping", 1024)
		assertColumnLength(t, db, Channel{}.TableName(), "remark", 255)
		for _, column := range []string{
			"open_ai_organization", "test_model", "other", "models", "other_info", "settings",
		} {
			assertColumnDatabaseType(t, db, Channel{}.TableName(), column, "longtext")
		}
		for _, column := range []string{"model_mapping", "setting", "param_override", "header_override", "channel_info"} {
			assertColumnDatabaseType(t, db, Channel{}.TableName(), column, "text")
		}
	case "postgres":
		for _, column := range []string{
			"open_ai_organization", "test_model", "name", "base_url", "other", "models", "model_mapping",
			"other_info", "tag", "setting", "param_override", "header_override", "channel_info", "settings",
		} {
			assertColumnDatabaseType(t, db, Channel{}.TableName(), column, "text")
		}
		assertColumnLength(t, db, Channel{}.TableName(), "group", 64)
		assertColumnLength(t, db, Channel{}.TableName(), "status_code_mapping", 1024)
		assertColumnLength(t, db, Channel{}.TableName(), "remark", 255)
	default:
		require.FailNow(t, "unsupported Channel schema test dialect", db.Dialector.Name())
	}
}

type referenceChannelDefaultState struct {
	Type              sql.NullInt64
	Status            sql.NullInt64
	Weight            sql.NullInt64
	BaseURL           sql.NullString
	Group             sql.NullString
	UsedQuota         sql.NullInt64
	StatusCodeMapping sql.NullString
	AutoBan           sql.NullInt64
}

func insertReferenceChannelWithDatabaseDefaults(t *testing.T, db *gorm.DB, key string) int {
	t.Helper()
	id := insertChannelKeyOnly(t, db, key)
	state := readReferenceChannelDefaultState(t, db, id)
	assert.Equal(t, sql.NullInt64{Int64: 0, Valid: true}, state.Type)
	assert.Equal(t, sql.NullInt64{Int64: 1, Valid: true}, state.Status)
	assert.Equal(t, sql.NullInt64{Int64: 0, Valid: true}, state.Weight)
	assert.Equal(t, sql.NullString{String: "", Valid: true}, state.BaseURL)
	assert.Equal(t, sql.NullString{String: "default", Valid: true}, state.Group)
	assert.Equal(t, sql.NullInt64{Int64: 0, Valid: true}, state.UsedQuota)
	assert.Equal(t, sql.NullString{String: "", Valid: true}, state.StatusCodeMapping)
	assert.Equal(t, sql.NullInt64{Int64: 1, Valid: true}, state.AutoBan)
	return id
}

func insertChannelKeyOnly(t *testing.T, db *gorm.DB, key string) int {
	t.Helper()
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (?)",
		quoteReferenceSchemaIdentifier(db, Channel{}.TableName()),
		quoteReferenceSchemaIdentifier(db, "key"))
	require.NoError(t, db.Exec(query, key).Error)
	var id int
	keyPredicate := quoteReferenceSchemaIdentifier(db, "key") + " = ?"
	require.NoError(t, db.Table(Channel{}.TableName()).Select("id").Where(keyPredicate, key).Scan(&id).Error)
	require.NotZero(t, id)
	return id
}

func readReferenceChannelDefaultState(t *testing.T, db *gorm.DB, id int) referenceChannelDefaultState {
	t.Helper()
	columns := referenceChannelDefaultColumnNames()
	quoted := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted = append(quoted, quoteReferenceSchemaIdentifier(db, column))
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s = ?",
		strings.Join(quoted, ", "),
		quoteReferenceSchemaIdentifier(db, Channel{}.TableName()),
		quoteReferenceSchemaIdentifier(db, "id"))
	var state referenceChannelDefaultState
	row := db.Raw(query, id).Row()
	require.NoError(t, row.Scan(
		&state.Type, &state.Status, &state.Weight, &state.BaseURL, &state.Group,
		&state.UsedQuota, &state.StatusCodeMapping, &state.AutoBan,
	))
	return state
}

func referenceChannelDefaultColumnNames() []string {
	return []string{
		"type", "status", "weight", "base_url", "group", "used_quota",
		"status_code_mapping", "auto_ban",
	}
}

func assertReferenceChannelDefaultsLoaded(t *testing.T, channel Channel) {
	t.Helper()
	assert.Zero(t, channel.Type)
	assert.Equal(t, 1, channel.Status)
	if assert.NotNil(t, channel.Weight) {
		assert.Zero(t, *channel.Weight)
	}
	assert.Empty(t, channel.BaseURL)
	assert.Equal(t, "default", channel.Group)
	assert.Zero(t, channel.UsedQuota)
	assert.Empty(t, channel.StatusCodeMapping)
	assert.Nil(t, channel.Priority, "nil Channel priority must continue to inherit the ability priority")
	if assert.NotNil(t, channel.AutoBan) {
		assert.Equal(t, 1, *channel.AutoBan)
	}
}

func assertChannelColumnsNull(t *testing.T, db *gorm.DB, id int, columns []string) {
	t.Helper()
	columns = append([]string(nil), columns...)
	sort.Strings(columns)
	predicates := make([]string, 0, len(columns))
	for _, column := range columns {
		predicates = append(predicates, quoteReferenceSchemaIdentifier(db, column)+" IS NULL")
	}
	var count int64
	require.NoError(t, db.Table(Channel{}.TableName()).
		Where("id = ? AND "+strings.Join(predicates, " AND "), id).Count(&count).Error)
	assert.EqualValues(t, 1, count, "Channel %d must retain NULL values for %s", id, strings.Join(columns, ", "))
}

func makeLegacyReferenceChannel(key string) legacyChannelSchema {
	weight := uint(0)
	priority := int64(0)
	autoBan := 0
	return legacyChannelSchema{
		Type: 0, Key: key,
		OpenAIOrganization: strings.Repeat("o", 128), TestModel: strings.Repeat("t", 128),
		Status: 0, Name: strings.Repeat("n", 64), Weight: &weight,
		CreatedTime: 11, TestTime: 12, ResponseTime: 13,
		BaseURL: strings.Repeat("b", referenceChannelMySQLBaseURLLimit),
		Other:   `{"legacy":true}`, Balance: 14.5, BalanceUpdatedTime: 15,
		Models: "model-a,model-b", Group: "", UsedQuota: 16,
		ModelMapping: `{"model-a":"model-b"}`, StatusCodeMapping: "",
		Priority: &priority, AutoBan: &autoBan, OtherInfo: "legacy-other-info",
		Tag: strings.Repeat("g", 64), Setting: `{"legacy":true}`,
		ParamOverride: `{"temperature":0}`, HeaderOverride: `{"x-test":"legacy"}`,
		Remark: strings.Repeat("r", 255), ChannelInfo: "legacy-non-json-channel-info",
		OtherSettings: `{"legacy_setting":true}`,
	}
}

func assertLegacyReferenceChannelPreserved(t *testing.T, db *gorm.DB, legacy legacyChannelSchema) {
	t.Helper()
	var migrated Channel
	require.NoError(t, db.First(&migrated, legacy.Id).Error)
	assert.Equal(t, legacy.Type, migrated.Type, "adding a default must preserve the unknown legacy type marker")
	assert.Equal(t, legacy.Key, migrated.Key)
	assert.Equal(t, legacy.OpenAIOrganization, migrated.OpenAIOrganization)
	assert.Equal(t, legacy.TestModel, migrated.TestModel)
	assert.Equal(t, legacy.Status, migrated.Status, "adding a default must not enable a legacy disabled channel")
	assert.Equal(t, legacy.Name, migrated.Name)
	if assert.NotNil(t, migrated.Weight) && assert.NotNil(t, legacy.Weight) {
		assert.Equal(t, *legacy.Weight, *migrated.Weight)
	}
	assert.Equal(t, legacy.CreatedTime, migrated.CreatedTime)
	assert.Equal(t, legacy.TestTime, migrated.TestTime)
	assert.Equal(t, legacy.ResponseTime, migrated.ResponseTime)
	assert.Equal(t, legacy.BaseURL, migrated.BaseURL)
	assert.Equal(t, legacy.Other, migrated.Other)
	assert.Equal(t, legacy.Balance, migrated.Balance)
	assert.Equal(t, legacy.BalanceUpdatedTime, migrated.BalanceUpdatedTime)
	assert.Equal(t, legacy.Models, migrated.Models)
	assert.Equal(t, legacy.Group, migrated.Group, "adding a default must preserve the empty legacy group marker")
	assert.Equal(t, legacy.UsedQuota, migrated.UsedQuota)
	assert.Equal(t, legacy.ModelMapping, migrated.ModelMapping)
	assert.Equal(t, legacy.StatusCodeMapping, migrated.StatusCodeMapping)
	if assert.NotNil(t, migrated.Priority) && assert.NotNil(t, legacy.Priority) {
		assert.Equal(t, *legacy.Priority, *migrated.Priority)
	}
	if assert.NotNil(t, migrated.AutoBan) && assert.NotNil(t, legacy.AutoBan) {
		assert.Equal(t, *legacy.AutoBan, *migrated.AutoBan)
	}
	assert.Equal(t, legacy.OtherInfo, migrated.OtherInfo)
	assert.Equal(t, legacy.Tag, migrated.Tag)
	assert.Equal(t, legacy.Setting, migrated.Setting)
	assert.Equal(t, legacy.ParamOverride, migrated.ParamOverride)
	assert.Equal(t, legacy.HeaderOverride, migrated.HeaderOverride)
	assert.Equal(t, legacy.Remark, migrated.Remark)
	assert.Equal(t, legacy.ChannelInfo, migrated.ChannelInfo, "the retained text representation must not rewrite arbitrary legacy data")
	assert.Equal(t, legacy.OtherSettings, migrated.OtherSettings)
}

func exerciseReferenceChannelSchemaLegacyExternal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Contains(t, []string{"mysql", "postgres"}, db.Dialector.Name())
	require.NoError(t, db.Migrator().DropTable(&Channel{}))
	require.NoError(t, db.AutoMigrate(&legacyChannelSchema{}))

	legacy := makeLegacyReferenceChannel("server-legacy-channel-key")
	require.NoError(t, db.Create(&legacy).Error)
	nullID := insertChannelKeyOnly(t, db, "server-legacy-null-key")

	require.NoError(t, migrateDB())
	assertReferenceChannelSchema(t, db)
	assertLegacyReferenceChannelPreserved(t, db, legacy)
	assertChannelColumnsNull(t, db, nullID, append(referenceChannelDefaultColumnNames(), referenceChannelNoDefaultColumns...))
	insertReferenceChannelWithDatabaseDefaults(t, db, "server-direct-channel-key")

	require.NoError(t, migrateDB())
	assertReferenceChannelSchema(t, db)
	assertLegacyReferenceChannelPreserved(t, db, legacy)
	assertChannelColumnsNull(t, db, nullID, append(referenceChannelDefaultColumnNames(), referenceChannelNoDefaultColumns...))

	if db.Dialector.Name() == "mysql" {
		t.Run("external unsafe Channel base URL width", func(t *testing.T) {
			require.NoError(t, db.Migrator().DropTable(&Channel{}))
			require.NoError(t, db.AutoMigrate(&legacyChannelSchema{}))
			unsafe := makeLegacyReferenceChannel("server-unsafe-channel-key")
			unsafe.BaseURL = strings.Repeat("b", referenceChannelMySQLBaseURLLimit+1)
			require.NoError(t, db.Create(&unsafe).Error)

			err := migrateDB()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnsafeReferenceSchemaMigration)
			assert.Contains(t, err.Error(), "channels.base_url")
			var stored legacyChannelSchema
			require.NoError(t, db.First(&stored, unsafe.Id).Error)
			assert.Equal(t, unsafe.BaseURL, stored.BaseURL, "the rejected migration must not truncate the legacy URL")

			require.NoError(t, db.Delete(&stored).Error)
			require.NoError(t, migrateDB())
			assertReferenceChannelSchema(t, db)
		})
	}
}
