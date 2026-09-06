package model

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyModelMetadata struct {
	Id        int            `gorm:"primaryKey"`
	ModelName string         `gorm:"type:varchar(128);not null"`
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (legacyModelMetadata) TableName() string { return "models" }

type legacyVendorMetadata struct {
	Id        int            `gorm:"primaryKey"`
	Name      string         `gorm:"type:varchar(128);not null"`
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (legacyVendorMetadata) TableName() string { return "vendors" }

func setRegistryTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previousDB, previousLogDB := DB, LOG_DB
	DB, LOG_DB = db, db
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
	})
}

func TestEnsureRegistryActiveNamesMigratesLegacyRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "registry.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&legacyModelMetadata{}, &legacyVendorMetadata{}))
	require.NoError(t, db.Create(&legacyModelMetadata{ModelName: "active-model"}).Error)
	deletedModel := legacyModelMetadata{ModelName: "reusable-model"}
	require.NoError(t, db.Create(&deletedModel).Error)
	require.NoError(t, db.Delete(&deletedModel).Error)
	require.NoError(t, db.Create(&legacyVendorMetadata{Name: "active-vendor"}).Error)

	setRegistryTestDB(t, db)
	require.NoError(t, DB.AutoMigrate(&Model{}, &Vendor{}))
	require.NoError(t, ensureRegistryActiveNames())

	var active Model
	require.NoError(t, DB.Where("model_name = ?", "active-model").First(&active).Error)
	require.NotNil(t, active.ActiveName)
	assert.Equal(t, "active-model", *active.ActiveName)

	var deleted Model
	require.NoError(t, DB.Unscoped().Where("model_name = ?", "reusable-model").First(&deleted).Error)
	assert.Nil(t, deleted.ActiveName)

	duplicate := Model{ModelName: "active-model"}
	assert.Error(t, DB.Create(&duplicate).Error)

	reused := Model{ModelName: "reusable-model"}
	require.NoError(t, DB.Create(&reused).Error)
}

func TestEnsureRegistryActiveNamesRepairsLegacyDuplicates(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "duplicates.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&legacyModelMetadata{}, &legacyVendorMetadata{}))
	firstModel := legacyModelMetadata{ModelName: "duplicate"}
	secondModel := legacyModelMetadata{ModelName: "duplicate"}
	vendorUser := legacyModelMetadata{ModelName: "vendor-user"}
	firstVendor := legacyVendorMetadata{Name: "duplicate-vendor"}
	secondVendor := legacyVendorMetadata{Name: "duplicate-vendor"}
	for _, value := range []any{&firstModel, &secondModel, &vendorUser, &firstVendor, &secondVendor} {
		require.NoError(t, db.Create(value).Error)
	}
	setRegistryTestDB(t, db)
	require.NoError(t, DB.AutoMigrate(&Model{}, &Vendor{}))
	require.NoError(t, DB.Model(&Model{}).Where("id = ?", vendorUser.Id).Update("vendor_id", firstVendor.Id).Error)

	require.NoError(t, ensureRegistryActiveNames())
	assert.True(t, DB.Migrator().HasIndex(&Model{}, "uk_model_active_name"))
	assert.True(t, DB.Migrator().HasIndex(&Vendor{}, "uk_vendor_active_name"))

	var activeModel Model
	require.NoError(t, DB.Where("model_name = ?", "duplicate").First(&activeModel).Error)
	assert.Equal(t, secondModel.Id, activeModel.Id)
	var allModels []Model
	require.NoError(t, DB.Unscoped().Where("model_name = ?", "duplicate").Order("id").Find(&allModels).Error)
	require.Len(t, allModels, 2)
	assert.True(t, allModels[0].DeletedAt.Valid)
	assert.False(t, allModels[1].DeletedAt.Valid)

	var activeVendor Vendor
	require.NoError(t, DB.Where("name = ?", "duplicate-vendor").First(&activeVendor).Error)
	assert.Equal(t, secondVendor.Id, activeVendor.Id)
	var related Model
	require.NoError(t, DB.First(&related, vendorUser.Id).Error)
	assert.Equal(t, activeVendor.Id, related.VendorID)
}

func TestDeleteModelMetadataReleasesActiveName(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "delete.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Model{}, &Vendor{}))
	setRegistryTestDB(t, db)
	require.NoError(t, ensureRegistryActiveNames())

	metadata := Model{ModelName: "reusable", Status: 1}
	require.NoError(t, CreateModelMetadata(&metadata))
	require.NoError(t, DeleteModelMetadata(metadata.Id))

	replacement := Model{ModelName: "reusable", Status: 1}
	require.NoError(t, CreateModelMetadata(&replacement))
	assert.NotEqual(t, metadata.Id, replacement.Id)
}

func TestRegistryCreateHooksPopulateDatabaseUniquenessKeys(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "create-hooks.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Model{}, &Vendor{}))
	setRegistryTestDB(t, db)
	require.NoError(t, ensureRegistryActiveNames())

	metadata := Model{ModelName: "direct-model", Status: 1}
	require.NoError(t, db.Create(&metadata).Error)
	require.NotNil(t, metadata.ActiveName)
	assert.Equal(t, metadata.ModelName, *metadata.ActiveName)
	assert.Error(t, db.Create(&Model{ModelName: metadata.ModelName, Status: 1}).Error,
		"the database must reject a duplicate even when callers bypass the service mutex")

	vendor := Vendor{Name: "direct-vendor", Status: 1}
	require.NoError(t, db.Create(&vendor).Error)
	require.NotNil(t, vendor.ActiveName)
	assert.Equal(t, vendor.Name, *vendor.ActiveName)
	assert.Error(t, db.Create(&Vendor{Name: vendor.Name, Status: 1}).Error,
		"vendor uniqueness must also be enforced by the database")
}

func TestConcurrentModelNameUniqueness(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "concurrent.db") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Model{}, &Vendor{}))
	setRegistryTestDB(t, db)
	require.NoError(t, ensureRegistryActiveNames())

	const workers = 16
	start := make(chan struct{})
	errorsSeen := make(chan error, workers)
	var succeeded atomic.Int32
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			candidate := Model{ModelName: "only-once", Status: 1}
			err := CreateModelMetadata(&candidate)
			if err == nil {
				succeeded.Add(1)
				return
			}
			if !errors.Is(err, ErrModelNameExists) {
				errorsSeen <- err
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("unexpected create error: %v", err)
	}
	assert.Equal(t, int32(1), succeeded.Load())
	var count int64
	require.NoError(t, DB.Model(&Model{}).Where("model_name = ?", "only-once").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}
