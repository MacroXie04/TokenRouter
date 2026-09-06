package router_test

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

// newRouterTestDatabase owns the database lifetime for an assembled HTTP test.
// These fixtures share process-level application state and must not run in
// parallel. Domain fixtures choose their schema and initialize their services.
func newRouterTestDatabase(t *testing.T, filename string, entities ...any) *gorm.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), filename) + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		billingsvc.ResetQuotaDataCache()
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(entities...))
	model.DB, model.LOG_DB = db, db
	return db
}
