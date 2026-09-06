package testutil

import (
	"context"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

func OpenPeriodicContextTestDB(t *testing.T, values ...any) *gorm.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "periodic-context.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(values...))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})
	return db
}

func CanceledPeriodicContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
