package model

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSQLPoolConfigDefaultsAndExplicitBounds(t *testing.T) {
	for _, name := range []string{"SQL_MAX_IDLE_CONNS", "SQL_MAX_OPEN_CONNS", "SQL_MAX_LIFETIME"} {
		t.Setenv(name, "")
	}
	config, err := loadSQLPoolConfig()
	require.NoError(t, err)
	assert.Equal(t, sqlPoolConfig{
		maxIdleConns:       defaultSQLMaxIdleConns,
		maxOpenConns:       defaultSQLMaxOpenConns,
		maxLifetimeSeconds: defaultSQLMaxLifetimeSeconds,
	}, config)

	t.Setenv("SQL_MAX_IDLE_CONNS", "0")
	t.Setenv("SQL_MAX_OPEN_CONNS", strconv.Itoa(maxSQLPoolConnections))
	t.Setenv("SQL_MAX_LIFETIME", "0")
	config, err = loadSQLPoolConfig()
	require.NoError(t, err)
	assert.Equal(t, 0, config.maxIdleConns)
	assert.Equal(t, maxSQLPoolConnections, config.maxOpenConns)
	assert.Equal(t, 0, config.maxLifetimeSeconds)

	t.Setenv("SQL_MAX_LIFETIME", strconv.Itoa(maxSQLMaxLifetimeSeconds))
	config, err = loadSQLPoolConfig()
	require.NoError(t, err)
	assert.Equal(t, maxSQLMaxLifetimeSeconds, config.maxLifetimeSeconds)
}

func TestSQLPoolConfigRejectsUnsafeValuesBeforeOpeningDatabase(t *testing.T) {
	tests := []struct {
		name     string
		idle     string
		open     string
		lifetime string
	}{
		{name: "negative idle", idle: "-1", open: "10", lifetime: "60"},
		{name: "unlimited open", idle: "0", open: "0", lifetime: "60"},
		{name: "oversized open", idle: "0", open: "10001", lifetime: "60"},
		{name: "oversized lifetime", idle: "0", open: "10", lifetime: strconv.Itoa(maxSQLMaxLifetimeSeconds + 1)},
		{name: "idle exceeds open", idle: "11", open: "10", lifetime: "60"},
		{name: "malformed", idle: "many", open: "10", lifetime: "60"},
		{name: "surrounding whitespace", idle: " 1", open: "10", lifetime: "60"},
		{name: "integer overflow", idle: "0", open: "9223372036854775808", lifetime: "60"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SQL_MAX_IDLE_CONNS", test.idle)
			t.Setenv("SQL_MAX_OPEN_CONNS", test.open)
			t.Setenv("SQL_MAX_LIFETIME", test.lifetime)
			_, err := loadSQLPoolConfig()
			require.Error(t, err)
		})
	}
}

func TestApplySQLPoolConfigBoundsEveryDatabaseHandle(t *testing.T) {
	config := sqlPoolConfig{maxIdleConns: 2, maxOpenConns: 7, maxLifetimeSeconds: 30}
	for _, name := range []string{"primary", "log"} {
		t.Run(name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), name+".db")), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, applySQLPoolConfig(db, config))
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			assert.Equal(t, config.maxOpenConns, sqlDB.Stats().MaxOpenConnections)
		})
	}
	require.Error(t, applySQLPoolConfig(nil, config))
}

func TestInitDBRejectsUnsafePoolConfigBeforeReplacingGlobalHandles(t *testing.T) {
	previousDB, previousLogDB := DB, LOG_DB
	t.Cleanup(func() { DB, LOG_DB = previousDB, previousLogDB })
	t.Setenv("SQL_DSN", "")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "must-not-open.db"))
	t.Setenv("LOG_SQL_DSN", "")
	t.Setenv("SQL_MAX_IDLE_CONNS", "0")
	t.Setenv("SQL_MAX_OPEN_CONNS", "0")
	t.Setenv("SQL_MAX_LIFETIME", "60")

	require.Error(t, InitDB())
	assert.Same(t, previousDB, DB)
	assert.Same(t, previousLogDB, LOG_DB)
}
