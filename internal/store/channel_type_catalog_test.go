package store

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func installChannelCatalogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "catalog.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Option{}, &Task{}))
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })
	return db
}

func TestEmptyDatabaseStartsWithReferenceChannelTypeCatalog(t *testing.T) {
	db := installChannelCatalogTestDB(t)
	require.NoError(t, ensureChannelTypeCatalog())
	var marker Option
	require.NoError(t, db.First(&marker, "key = ?", channelTypeCatalogOption).Error)
	assert.Equal(t, channelTypeCatalogReferenceV1, marker.Value)
}

func TestCompactChannelTypeCatalogMigratesExactlyOnce(t *testing.T) {
	db := installChannelCatalogTestDB(t)
	t.Setenv(channelTypeCatalogEnvironment, channelTypeCatalogCompactV0)
	for index, channelType := range []int{28, 29, 30, 31, 32, 39, 47, 57} {
		require.NoError(t, db.Create(&Channel{Name: "legacy-" + string(rune('a'+index)), Key: "key", Type: channelType}).Error)
	}
	require.NoError(t, db.Create(&Task{TaskID: "legacy-jimeng", Platform: "47"}).Error)
	require.NoError(t, db.Create(&Task{TaskID: "named-platform", Platform: "video"}).Error)
	require.NoError(t, ensureChannelTypeCatalog())
	var types []int
	require.NoError(t, db.Model(&Channel{}).Order("id").Pluck("type", &types).Error)
	assert.Equal(t, []int{31, 33, 34, 35, 36, 43, 51, 61}, types)
	var tasks []Task
	require.NoError(t, db.Order("id").Find(&tasks).Error)
	require.Len(t, tasks, 2)
	assert.Equal(t, "51", tasks[0].Platform)
	assert.Equal(t, "video", tasks[1].Platform)
	require.NoError(t, ensureChannelTypeCatalog())
	var replayed []int
	require.NoError(t, db.Model(&Channel{}).Order("id").Pluck("type", &replayed).Error)
	assert.Equal(t, types, replayed, "the durable catalog marker must prevent a second translation")
}

func TestTaskOnlyCompactCatalogRequiresExplicitMigrationAndIsNotMistakenForEmptyDatabase(t *testing.T) {
	db := installChannelCatalogTestDB(t)
	require.NoError(t, db.Create(&Task{TaskID: "orphaned-legacy-jimeng", Platform: "47"}).Error)

	err := ensureChannelTypeCatalog()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop every old TokenRouter node")
	var markers int64
	require.NoError(t, db.Model(&Option{}).Where(&Option{Key: channelTypeCatalogOption}).Count(&markers).Error)
	assert.Zero(t, markers)

	t.Setenv(channelTypeCatalogEnvironment, channelTypeCatalogCompactV0)
	require.NoError(t, ensureChannelTypeCatalog())

	var task Task
	require.NoError(t, db.Where("task_id = ?", "orphaned-legacy-jimeng").First(&task).Error)
	assert.Equal(t, "51", task.Platform)
	var marker Option
	require.NoError(t, db.First(&marker, "key = ?", channelTypeCatalogOption).Error)
	assert.Equal(t, channelTypeCatalogReferenceV1, marker.Value)
}

func TestRecognizableCompactCatalogRequiresExplicitDrainAcknowledgement(t *testing.T) {
	db := installChannelCatalogTestDB(t)
	require.NoError(t, db.Create(&Channel{Name: "legacy-reserved-id", Key: "key", Type: 28}).Error)

	err := ensureChannelTypeCatalog()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop every old TokenRouter node")
	var channel Channel
	require.NoError(t, db.Where("name = ?", "legacy-reserved-id").First(&channel).Error)
	assert.Equal(t, 28, channel.Type)
}

func TestAmbiguousUnmarkedChannelCatalogFailsClosed(t *testing.T) {
	db := installChannelCatalogTestDB(t)
	require.NoError(t, db.Create(&Channel{Name: "ambiguous", Key: "key", Type: 39}).Error)
	err := ensureChannelTypeCatalog()
	require.Error(t, err)
	assert.Contains(t, err.Error(), channelTypeCatalogEnvironment)
	var channel Channel
	require.NoError(t, db.Where("name = ?", "ambiguous").First(&channel).Error)
	assert.Equal(t, 39, channel.Type)
	var markers int64
	require.NoError(t, db.Model(&Option{}).Where(&Option{Key: channelTypeCatalogOption}).Count(&markers).Error)
	assert.Zero(t, markers)
}

func TestAmbiguousCatalogCanBeDeclaredWithoutGuessing(t *testing.T) {
	t.Run("reference", func(t *testing.T) {
		db := installChannelCatalogTestDB(t)
		require.NoError(t, db.Create(&Channel{Name: "cloudflare", Key: "key", Type: 39}).Error)
		t.Setenv(channelTypeCatalogEnvironment, channelTypeCatalogReferenceV1)
		require.NoError(t, ensureChannelTypeCatalog())
		var channel Channel
		require.NoError(t, db.Where("name = ?", "cloudflare").First(&channel).Error)
		assert.Equal(t, 39, channel.Type)
	})

	t.Run("compact", func(t *testing.T) {
		db := installChannelCatalogTestDB(t)
		require.NoError(t, db.Create(&Channel{Name: "deepseek", Key: "key", Type: 39}).Error)
		t.Setenv(channelTypeCatalogEnvironment, channelTypeCatalogCompactV0)
		require.NoError(t, ensureChannelTypeCatalog())
		var channel Channel
		require.NoError(t, db.Where("name = ?", "deepseek").First(&channel).Error)
		assert.Equal(t, 43, channel.Type)
	})
}
