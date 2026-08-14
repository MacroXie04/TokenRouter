package service

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestImportLegacyOneAPI(t *testing.T) {
	// Build a legacy one-api SQLite DB.
	legacyPath := filepath.Join(t.TempDir(), "legacy.db")
	legacyDB, err := gorm.Open(sqlite.Open(legacyPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, legacyDB.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Option{}))
	require.NoError(t, legacyDB.Create(&model.User{Username: "legacy-user", Password: "hash", Role: 1, Status: 1, Quota: 100, Group: "default"}).Error)
	require.NoError(t, legacyDB.Create(&model.Token{UserId: 1, Key: "sk-legacy", Name: "legacy", Status: TokenStatusEnabled}).Error)
	require.NoError(t, legacyDB.Create(&model.Channel{Name: "legacy-chan", Type: 1, Key: "k", Status: 1}).Error)
	require.NoError(t, legacyDB.Create(&model.Option{Key: "SystemName", Value: "Legacy"}).Error)

	// TokenRouter target DB.
	targetPath := filepath.Join(t.TempDir(), "target.db")
	targetDB, err := gorm.Open(sqlite.Open(targetPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, targetDB.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Option{}))
	model.DB = targetDB
	model.LOG_DB = targetDB

	counts, err := ImportLegacyOneAPI(legacyPath)
	require.NoError(t, err)
	assert.Equal(t, 1, counts["users"])
	assert.Equal(t, 1, counts["tokens"])
	assert.Equal(t, 1, counts["channels"])
	assert.Equal(t, 1, counts["options"])

	var user model.User
	require.NoError(t, model.DB.Where("username = ?", "legacy-user").First(&user).Error)
	assert.Equal(t, 100, user.Quota)

	// Idempotent: a second import skips existing rows.
	counts2, err := ImportLegacyOneAPI(legacyPath)
	require.NoError(t, err)
	assert.Equal(t, 0, counts2["users"])
	assert.Equal(t, 0, counts2["tokens"])
	assert.Equal(t, 0, counts2["channels"])
	assert.Equal(t, 0, counts2["options"])
}
