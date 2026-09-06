package store_test

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	model "github.com/tokenrouter/tokenrouter/internal/store"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "model.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	return db
}

func TestUserBeforeCreateGeneratesUniqueAffCode(t *testing.T) {
	db := newTestDB(t)

	// Two users created with empty aff_code must both succeed and receive
	// distinct codes (the column is unique-indexed).
	u1 := model.User{Username: "first", Password: "x", Status: model.UserStatusEnabled, AuthVersion: 1}
	u2 := model.User{Username: "second", Password: "x", Status: model.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&u1).Error)
	require.NoError(t, db.Create(&u2).Error)
	assert.NotEmpty(t, u1.AffCode)
	assert.NotEmpty(t, u2.AffCode)
	assert.NotEqual(t, u1.AffCode, u2.AffCode)

	// An explicitly provided code is preserved.
	u3 := model.User{Username: "third", Password: "x", Status: model.UserStatusEnabled, AuthVersion: 1, AffCode: "my-code-123"}
	require.NoError(t, db.Create(&u3).Error)
	assert.Equal(t, "my-code-123", u3.AffCode)
}
