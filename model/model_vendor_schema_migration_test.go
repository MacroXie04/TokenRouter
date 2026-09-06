package model

import (
	"path/filepath"
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

// pinnedReferenceModelSchema and pinnedReferenceVendorSchema independently
// describe the persistent registry contracts observed in the pinned
// reference. ActiveName is intentionally absent: it remains a target-only
// cross-node uniqueness key.
type pinnedReferenceModelSchema struct {
	Id           int
	ModelName    string         `gorm:"size:128;not null;uniqueIndex:uk_model_name_delete_at,priority:1"`
	Description  string         `gorm:"type:text"`
	Icon         string         `gorm:"type:varchar(128)"`
	Tags         string         `gorm:"type:varchar(255)"`
	VendorID     int            `gorm:"index"`
	Endpoints    string         `gorm:"type:text"`
	Status       int            `gorm:"default:1"`
	SyncOfficial int            `gorm:"default:1"`
	CreatedTime  int64          `gorm:"bigint"`
	UpdatedTime  int64          `gorm:"bigint"`
	DeletedAt    gorm.DeletedAt `gorm:"index;uniqueIndex:uk_model_name_delete_at,priority:2"`
	NameRule     int            `gorm:"default:0"`
}

func (pinnedReferenceModelSchema) TableName() string { return "models" }

type pinnedReferenceVendorSchema struct {
	Id          int
	Name        string         `gorm:"size:128;not null;uniqueIndex:uk_vendor_name_delete_at,priority:1"`
	Description string         `gorm:"type:text"`
	Icon        string         `gorm:"type:varchar(128)"`
	Status      int            `gorm:"default:1"`
	CreatedTime int64          `gorm:"bigint"`
	UpdatedTime int64          `gorm:"bigint"`
	DeletedAt   gorm.DeletedAt `gorm:"index;uniqueIndex:uk_vendor_name_delete_at,priority:2"`
}

func (pinnedReferenceVendorSchema) TableName() string { return "vendors" }

type legacyRegistryModelSchema struct {
	Id           int     `gorm:"primaryKey"`
	ModelName    string  `gorm:"type:varchar(128);not null"`
	ActiveName   *string `gorm:"type:varchar(128)"`
	Description  string  `gorm:"type:text"`
	Icon         string  `gorm:"type:varchar(128)"`
	Tags         string  `gorm:"type:varchar(255)"`
	VendorID     int     `gorm:"index"`
	Endpoints    string  `gorm:"type:text"`
	Status       int
	SyncOfficial int
	CreatedTime  int64
	UpdatedTime  int64
	NameRule     int
	DeletedAt    gorm.DeletedAt `gorm:"index"`
}

func (legacyRegistryModelSchema) TableName() string { return "models" }

type legacyRegistryVendorSchema struct {
	Id          int     `gorm:"primaryKey"`
	Name        string  `gorm:"type:varchar(128);not null"`
	ActiveName  *string `gorm:"type:varchar(128)"`
	Description string  `gorm:"type:text"`
	Icon        string  `gorm:"type:varchar(128)"`
	Status      int
	CreatedTime int64
	UpdatedTime int64
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}

func (legacyRegistryVendorSchema) TableName() string { return "vendors" }

func TestReferenceRegistrySchemaPortableDialects(t *testing.T) {
	targetModel, err := schema.Parse(&Model{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceModel, err := schema.Parse(&pinnedReferenceModelSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	targetVendor, err := schema.Parse(&Vendor{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceVendor, err := schema.Parse(&pinnedReferenceVendorSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	assertRegistryReferenceFields(t, referenceModel, targetModel, "ActiveName")
	assertRegistryReferenceFields(t, referenceVendor, targetVendor, "ActiveName")

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
			assertRegistryReferenceDialectTypes(t, dialect, referenceModel, targetModel)
			assertRegistryReferenceDialectTypes(t, dialect, referenceVendor, targetVendor)
		})
	}

	assertRegistryIndexDefinition(t, targetModel, "uk_model_name_delete_at", []string{"model_name", "deleted_at"}, true)
	assertRegistryIndexDefinition(t, targetVendor, "uk_vendor_name_delete_at", []string{"name", "deleted_at"}, true)
	assertRegistryIndexDefinition(t, targetModel, "idx_models_vendor_id", []string{"vendor_id"}, false)
	assertRegistryIndexDefinition(t, targetModel, "idx_models_deleted_at", []string{"deleted_at"}, false)
	assertRegistryIndexDefinition(t, targetVendor, "idx_vendors_deleted_at", []string{"deleted_at"}, false)
}

func TestReferenceRegistrySchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceRegistrySchemaMatchesSQLite(t, db)
	assertReferenceRegistrySchema(t, db)
	exerciseReferenceRegistryDefaults(t, db)

	zero := Model{ModelName: "explicit-zero", Status: 0, SyncOfficial: 0, NameRule: 0}
	require.NoError(t, CreateModelMetadata(&zero))
	var persisted Model
	require.NoError(t, db.First(&persisted, zero.Id).Error)
	assert.Zero(t, persisted.Status)
	assert.Zero(t, persisted.SyncOfficial)
	zeroVendor := Vendor{Name: "explicit-zero-vendor", Status: 0}
	require.NoError(t, CreateVendorMetadata(&zeroVendor))
	var persistedVendor Vendor
	require.NoError(t, db.First(&persistedVendor, zeroVendor.Id).Error)
	assert.Zero(t, persistedVendor.Status)

	columnsBefore := sqliteColumnSignatures(t, db, Model{}.TableName())
	modelIndexesBefore := sqliteIndexSignatures(t, db, Model{}.TableName())
	vendorIndexesBefore := sqliteIndexSignatures(t, db, Vendor{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, Model{}.TableName()))
	assert.Equal(t, modelIndexesBefore, sqliteIndexSignatures(t, db, Model{}.TableName()))
	assert.Equal(t, vendorIndexesBefore, sqliteIndexSignatures(t, db, Vendor{}.TableName()))
}

func TestReferenceRegistrySchemaPreservesLegacyRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "registry-schema.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&legacyRegistryModelSchema{}, &legacyRegistryVendorSchema{}))
	modelActive := "legacy-model"
	vendorActive := "legacy-vendor"
	legacyModel := legacyRegistryModelSchema{
		ModelName: modelActive, ActiveName: &modelActive, Description: "description", Icon: "icon",
		Tags: "tag", VendorID: 7, Endpoints: "endpoint", Status: 0, SyncOfficial: 0,
		CreatedTime: 11, UpdatedTime: 12, NameRule: 3,
	}
	legacyVendor := legacyRegistryVendorSchema{
		Name: vendorActive, ActiveName: &vendorActive, Description: "description", Icon: "icon",
		Status: 0, CreatedTime: 21, UpdatedTime: 22,
	}
	require.NoError(t, db.Create(&legacyModel).Error)
	require.NoError(t, db.Create(&legacyVendor).Error)
	setRegistryTestDB(t, db)
	require.NoError(t, migrateDB())

	var migratedModel Model
	require.NoError(t, db.First(&migratedModel, legacyModel.Id).Error)
	assert.Equal(t, legacyModel.ModelName, migratedModel.ModelName)
	assert.Equal(t, legacyModel.Description, migratedModel.Description)
	assert.Equal(t, legacyModel.Status, migratedModel.Status)
	assert.Equal(t, legacyModel.SyncOfficial, migratedModel.SyncOfficial)
	assert.Equal(t, legacyModel.CreatedTime, migratedModel.CreatedTime)
	assert.Equal(t, legacyModel.UpdatedTime, migratedModel.UpdatedTime)
	assert.Equal(t, legacyModel.NameRule, migratedModel.NameRule)
	var migratedVendor Vendor
	require.NoError(t, db.First(&migratedVendor, legacyVendor.Id).Error)
	assert.Equal(t, legacyVendor.Name, migratedVendor.Name)
	assert.Equal(t, legacyVendor.Status, migratedVendor.Status)
	assert.Equal(t, legacyVendor.CreatedTime, migratedVendor.CreatedTime)
	assert.Equal(t, legacyVendor.UpdatedTime, migratedVendor.UpdatedTime)
	assertReferenceRegistrySchemaMatchesSQLite(t, db)
	assertReferenceRegistrySchema(t, db)
}

func assertRegistryReferenceFields(t *testing.T, reference, target *schema.Schema, allowedExtra ...string) {
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
	assert.Equal(t, referencePersistent+len(extras), targetPersistent)
	for _, field := range reference.Fields {
		actual := target.LookUpField(field.Name)
		require.NotNil(t, actual, "%s is missing", field.Name)
		assert.Equal(t, field.DBName, actual.DBName, field.Name+" column")
		assert.Equal(t, field.NotNull, actual.NotNull, field.Name+" nullability")
		assert.Equal(t, field.HasDefaultValue, actual.HasDefaultValue, field.Name+" default presence")
		assert.Equal(t, field.DefaultValue, actual.DefaultValue, field.Name+" default")
		assert.Equal(t, field.Size, actual.Size, field.Name+" size")
	}
}

func assertRegistryReferenceDialectTypes(t *testing.T, dialect gorm.Dialector, reference, target *schema.Schema) {
	t.Helper()
	for _, field := range reference.Fields {
		actual := target.LookUpField(field.Name)
		require.NotNil(t, actual)
		assert.Equal(t, strings.ToLower(dialect.DataTypeOf(field)), strings.ToLower(dialect.DataTypeOf(actual)), field.Name)
	}
}

func assertRegistryIndexDefinition(t *testing.T, parsed *schema.Schema, name string, columns []string, unique bool) {
	t.Helper()
	index := parsed.LookIndex(name)
	require.NotNil(t, index, "missing index %s", name)
	assert.Equal(t, unique, index.Class == "UNIQUE", name+" uniqueness")
	actual := make([]string, 0, len(index.Fields))
	for _, field := range index.Fields {
		actual = append(actual, field.DBName)
	}
	assert.Equal(t, columns, actual, name+" columns")
}

func assertReferenceRegistrySchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-registry.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&pinnedReferenceModelSchema{}, &pinnedReferenceVendorSchema{}))

	modelColumns := filterRegistryColumnSignatures(sqliteColumnSignatures(t, db, Model{}.TableName()), "active_name")
	modelIndexes := filterRegistryIndexSignatures(sqliteIndexSignatures(t, db, Model{}.TableName()), "uk_model_active_name")
	assert.Equal(t, sqliteColumnSignatures(t, reference, Model{}.TableName()), modelColumns)
	assert.Equal(t, sqliteIndexSignatures(t, reference, Model{}.TableName()), modelIndexes)
	vendorColumns := filterRegistryColumnSignatures(sqliteColumnSignatures(t, db, Vendor{}.TableName()), "active_name")
	vendorIndexes := filterRegistryIndexSignatures(sqliteIndexSignatures(t, db, Vendor{}.TableName()), "uk_vendor_active_name")
	assert.Equal(t, sqliteColumnSignatures(t, reference, Vendor{}.TableName()), vendorColumns)
	assert.Equal(t, sqliteIndexSignatures(t, reference, Vendor{}.TableName()), vendorIndexes)
}

func assertReferenceRegistrySchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, expected := range []struct {
		entity  any
		table   string
		name    string
		columns []string
		unique  bool
	}{
		{&Model{}, "models", "uk_model_name_delete_at", []string{"model_name", "deleted_at"}, true},
		{&Model{}, "models", "uk_model_active_name", []string{"active_name"}, true},
		{&Vendor{}, "vendors", "uk_vendor_name_delete_at", []string{"name", "deleted_at"}, true},
		{&Vendor{}, "vendors", "uk_vendor_active_name", []string{"active_name"}, true},
	} {
		columns, unique := requirePortableIndex(t, db, expected.entity, expected.table, expected.name)
		assert.Equal(t, expected.columns, columns, expected.name+" columns")
		assert.Equal(t, expected.unique, unique, expected.name+" uniqueness")
	}
	for column, expected := range map[string]string{"status": "1", "sync_official": "1", "name_rule": "0"} {
		assertColumnDefault(t, db, Model{}.TableName(), column, expected)
	}
	assertColumnDefault(t, db, Vendor{}.TableName(), "status", "1")
	timestampType := ""
	switch db.Dialector.Name() {
	case "sqlite":
		timestampType = "integer"
	case "mysql":
		timestampType = "bigint"
	case "postgres":
		timestampType = "int8"
	default:
		require.FailNow(t, "unsupported registry schema test dialect", db.Dialector.Name())
	}
	for _, table := range []string{Model{}.TableName(), Vendor{}.TableName()} {
		assertColumnDatabaseType(t, db, table, "created_time", timestampType)
		assertColumnDatabaseType(t, db, table, "updated_time", timestampType)
	}
}

func exerciseReferenceRegistryDefaults(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO models (model_name) VALUES (?)`, "raw-default-model").Error)
	var metadata Model
	require.NoError(t, db.Where("model_name = ?", "raw-default-model").First(&metadata).Error)
	assert.Equal(t, 1, metadata.Status)
	assert.Equal(t, 1, metadata.SyncOfficial)
	assert.Zero(t, metadata.NameRule)
	require.NoError(t, db.Exec(`INSERT INTO vendors (name) VALUES (?)`, "raw-default-vendor").Error)
	var vendor Vendor
	require.NoError(t, db.Where("name = ?", "raw-default-vendor").First(&vendor).Error)
	assert.Equal(t, 1, vendor.Status)
}

func filterRegistryColumnSignatures(columns []sqliteColumnSignature, excluded ...string) []sqliteColumnSignature {
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

func filterRegistryIndexSignatures(indexes []sqliteIndexSignature, excluded ...string) []sqliteIndexSignature {
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
