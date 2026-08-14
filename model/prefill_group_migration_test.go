package model

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyPrefillGroup struct {
	Id        int            `gorm:"primaryKey"`
	Name      string         `gorm:"uniqueIndex:uk_prefill_name"`
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (legacyPrefillGroup) TableName() string { return "prefill_groups" }

func TestEnsurePrefillGroupPartialIndexRepairsLegacySQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, DB.AutoMigrate(&legacyPrefillGroup{}))

	var before string
	require.NoError(t, DB.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?", "uk_prefill_name").Scan(&before).Error)
	assert.NotContains(t, strings.ToUpper(before), "WHERE")

	require.NoError(t, DB.AutoMigrate(&PrefillGroup{}))
	require.NoError(t, ensurePrefillGroupPartialIndex())
	var after string
	require.NoError(t, DB.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?", "uk_prefill_name").Scan(&after).Error)
	assert.Contains(t, strings.ToUpper(after), "WHERE")

	first := PrefillGroup{Name: "reusable", Type: "model", Items: JSONValue(`[]`)}
	require.NoError(t, DB.Create(&first).Error)
	require.NoError(t, DB.Delete(&first).Error)
	second := PrefillGroup{Name: "reusable", Type: "model", Items: JSONValue(`[]`)}
	require.NoError(t, DB.Create(&second).Error)
}
