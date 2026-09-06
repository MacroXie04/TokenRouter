package operations

import (
	"context"
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/runtimeinfo"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"math"
	"sync"
	"testing"
)

func TestSystemInstanceHeartbeat(t *testing.T) {
	t.Setenv("NODE_NAME", "test-instance")
	db, err := gorm.Open(sqlite.Open("file:system-instance?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.SystemInstance{}))
	model.DB = db
	model.LOG_DB = db

	// First registration creates the node.
	require.NoError(t, RegisterSystemInstance())
	instances, err := GetSystemInstances()
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, runtimeinfo.NodeName(), instances[0].NodeName)
	firstSeen := instances[0].LastSeenAt
	firstCreated := instances[0].CreatedAt
	var info SystemInstanceInfo
	require.NoError(t, jsonutil.UnmarshalJsonStr(instances[0].Info, &info))
	assert.Equal(t, 1, info.SchemaVersion)
	assert.Equal(t, "test-instance", info.Node.Name)
	assert.Equal(t, "environment", info.Node.Source)
	assert.True(t, info.Node.ManuallyConfigured)
	assert.False(t, info.Node.ShouldConfigureManually)
	assert.True(t, info.Role.IsMaster, "every leased-job-capable TokenRouter node reports its operational role")
	assert.NotEmpty(t, info.Runtime.GOOS)
	assert.NotEmpty(t, info.Runtime.GOARCH)
	assert.Equal(t, systemInstanceStartedAt, info.Runtime.StartedAt)
	assert.GreaterOrEqual(t, info.Resources.Memory.UsagePercent, float64(0))
	assert.LessOrEqual(t, info.Resources.Memory.UsagePercent, float64(100))
	assert.GreaterOrEqual(t, info.Resources.Storage.UsedPercent, float64(0))
	assert.LessOrEqual(t, info.Resources.Storage.UsedPercent, float64(100))

	// Heartbeat updates LastSeenAt.
	require.NoError(t, RegisterSystemInstance())
	instances, err = GetSystemInstances()
	require.NoError(t, err)
	require.Len(t, instances, 1, "heartbeat must not create duplicate nodes")
	assert.GreaterOrEqual(t, instances[0].LastSeenAt, firstSeen)
	assert.Equal(t, firstCreated, instances[0].CreatedAt, "heartbeat preserves first registration time")
	assert.Equal(t, systemInstanceStartedAt, instances[0].StartedAt)

	// A fresh instance is not stale.
	assert.False(t, IsInstanceStale(&instances[0], 90))

	// Overlapping node-local heartbeats converge through one conflict-safe
	// statement rather than racing a read followed by create.
	const writers = 16
	var wait sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- RegisterSystemInstance()
		}()
	}
	wait.Wait()
	close(errs)
	for writeErr := range errs {
		require.NoError(t, writeErr)
	}
	instances, err = GetSystemInstances()
	require.NoError(t, err)
	require.Len(t, instances, 1)

	// Invalid configured identities fail closed instead of being truncated into
	// a colliding database key.
	t.Setenv("NODE_NAME", "   ")
	assert.True(t, errors.Is(RegisterSystemInstance(), model.ErrInvalidSystemInstance))
}

func TestIsInstanceStaleAtRejectsOldAndImplausiblyFutureHeartbeats(t *testing.T) {
	const now int64 = 1_000_000
	assert.False(t, IsInstanceStaleAt(&model.SystemInstance{LastSeenAt: now - 90}, now, 90))
	assert.True(t, IsInstanceStaleAt(&model.SystemInstance{LastSeenAt: now - 91}, now, 90))
	assert.False(t, IsInstanceStaleAt(&model.SystemInstance{LastSeenAt: now + 90}, now, 90))
	assert.True(t, IsInstanceStaleAt(&model.SystemInstance{LastSeenAt: now + 91}, now, 90))
	assert.True(t, IsInstanceStaleAt(&model.SystemInstance{LastSeenAt: math.MaxInt64}, math.MinInt64, 90))
}

func TestRegisterSystemInstanceRepairsLegacyFutureHeartbeat(t *testing.T) {
	t.Setenv("NODE_NAME", "legacy-future-instance")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SystemInstance{}))
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	model.DB = db
	model.LOG_DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
	})

	databaseNow, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.SystemInstance{
		NodeName:   runtimeinfo.NodeName(),
		Info:       `{"schema_version":0}`,
		StartedAt:  databaseNow,
		LastSeenAt: databaseNow + 365*24*60*60,
		CreatedAt:  databaseNow,
		UpdatedAt:  databaseNow,
	}).Error)

	before, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	require.NoError(t, err)
	require.NoError(t, RegisterSystemInstanceContext(context.Background()))
	after, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	require.NoError(t, err)

	var stored model.SystemInstance
	require.NoError(t, db.Where("node_name = ?", runtimeinfo.NodeName()).First(&stored).Error)
	assert.GreaterOrEqual(t, stored.LastSeenAt, before)
	assert.LessOrEqual(t, stored.LastSeenAt, after)
	var info SystemInstanceInfo
	require.NoError(t, jsonutil.UnmarshalJsonStr(stored.Info, &info))
	assert.Equal(t, 1, info.SchemaVersion)
}
