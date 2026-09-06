package model

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSystemInstanceHeartbeatRequiresAuthoritativeTimestamp(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&SystemInstance{}))
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })

	require.ErrorIs(t,
		UpsertSystemInstanceContext(context.Background(), "node-zero", nil, 0, 0),
		ErrInvalidSystemInstance,
	)
	const databaseNow int64 = 1_000_000
	require.NoError(t,
		UpsertSystemInstanceContext(context.Background(), "node-fixed", nil, 0, databaseNow),
	)
	var stored SystemInstance
	require.NoError(t, db.Where("node_name = ?", "node-fixed").First(&stored).Error)
	assert.Equal(t, databaseNow, stored.StartedAt)
	assert.Equal(t, databaseNow, stored.LastSeenAt)
	assert.Equal(t, databaseNow, stored.CreatedAt)
	assert.Equal(t, databaseNow, stored.UpdatedAt)
}

func TestSystemInstanceHeartbeatCannotRegressWhenWritesCompleteOutOfOrder(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&SystemInstance{}))
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })

	require.NoError(t, UpsertSystemInstanceContext(context.Background(), "node-race",
		map[string]any{"generation": "new"}, 90, 101))
	require.NoError(t, UpsertSystemInstanceContext(context.Background(), "node-race",
		map[string]any{"generation": "stale"}, 80, 100))
	require.NoError(t, UpsertSystemInstanceContext(context.Background(), "node-race",
		map[string]any{"generation": "equal"}, 70, 101))

	var stored SystemInstance
	require.NoError(t, db.Where("node_name = ?", "node-race").First(&stored).Error)
	assert.Equal(t, int64(90), stored.StartedAt)
	assert.Equal(t, int64(101), stored.LastSeenAt)
	assert.Equal(t, int64(101), stored.CreatedAt)
	assert.Equal(t, int64(101), stored.UpdatedAt)
	assert.JSONEq(t, `{"generation":"new"}`, stored.Info)

	require.NoError(t, UpsertSystemInstanceContext(context.Background(), "node-race",
		map[string]any{"generation": "newest"}, 95, 102))
	require.NoError(t, db.Where("node_name = ?", "node-race").First(&stored).Error)
	assert.Equal(t, int64(95), stored.StartedAt)
	assert.Equal(t, int64(102), stored.LastSeenAt)
	assert.Equal(t, int64(101), stored.CreatedAt)
	assert.Equal(t, int64(102), stored.UpdatedAt)
	assert.JSONEq(t, `{"generation":"newest"}`, stored.Info)

	// A substantially delayed write is still only a delayed write. The future
	// tolerance used for status classification must never weaken ordering.
	require.NoError(t, UpsertSystemInstanceContext(context.Background(), "node-race",
		map[string]any{"generation": "very-stale"}, 1, 1))
	require.NoError(t, db.Where("node_name = ?", "node-race").First(&stored).Error)
	assert.Equal(t, int64(102), stored.LastSeenAt)
	assert.Equal(t, int64(102), stored.UpdatedAt)
	assert.JSONEq(t, `{"generation":"newest"}`, stored.Info)

	// A row from the former process-clock implementation can be far ahead of
	// the database. It stays fenced from an older write, but is classified as
	// invalid and removed by cleanup before the next registration.
	const legacyFuture = int64(102 + 365*24*60*60)
	require.NoError(t, db.Model(&SystemInstance{}).Where("node_name = ?", "node-race").Updates(map[string]any{
		"info":         `{"generation":"legacy-fast-clock"}`,
		"last_seen_at": legacyFuture,
		"updated_at":   legacyFuture,
	}).Error)
	require.NoError(t, UpsertSystemInstanceContext(context.Background(), "node-race",
		map[string]any{"generation": "authoritative"}, 96, 103))
	require.NoError(t, db.Where("node_name = ?", "node-race").First(&stored).Error)
	assert.Equal(t, legacyFuture, stored.LastSeenAt)
	assert.JSONEq(t, `{"generation":"legacy-fast-clock"}`, stored.Info)
	deleted, err := DeleteStaleSystemInstance("node-race", 103)
	require.NoError(t, err)
	require.True(t, deleted)
	require.NoError(t, UpsertSystemInstanceContext(context.Background(), "node-race",
		map[string]any{"generation": "re-registered"}, 96, 104))
	require.NoError(t, db.Where("node_name = ?", "node-race").First(&stored).Error)
	assert.Equal(t, int64(104), stored.LastSeenAt)
	assert.JSONEq(t, `{"generation":"re-registered"}`, stored.Info)
}

func TestSystemInstanceStatusAndDeletionUseStrictSharedClockBoundary(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&SystemInstance{}))
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })

	const databaseNow int64 = 1_000_000
	boundary := &SystemInstance{NodeName: "boundary", LastSeenAt: databaseNow - SystemInstanceStaleAfterSeconds}
	stale := &SystemInstance{NodeName: "stale", LastSeenAt: databaseNow - SystemInstanceStaleAfterSeconds - 1}
	future := &SystemInstance{NodeName: "future", LastSeenAt: databaseNow + 1_000_000}
	for _, instance := range []*SystemInstance{boundary, stale, future} {
		instance.StartedAt = instance.LastSeenAt
		instance.CreatedAt = instance.LastSeenAt
		instance.UpdatedAt = instance.LastSeenAt
		require.NoError(t, db.Create(instance).Error)
	}

	assert.Equal(t, SystemInstanceStatusOnline, boundary.ToResponse(databaseNow).Status)
	assert.Equal(t, SystemInstanceStatusStale, stale.ToResponse(databaseNow).Status)
	assert.Equal(t, SystemInstanceStatusStale, future.ToResponse(databaseNow).Status,
		"a far-future legacy process-clock heartbeat must not stay online indefinitely")

	deleted, err := DeleteStaleSystemInstance("boundary", databaseNow)
	require.NoError(t, err)
	assert.False(t, deleted, "exactly 90 seconds is still online")
	deleted, err = DeleteStaleSystemInstance("stale", databaseNow)
	require.NoError(t, err)
	assert.True(t, deleted, "91 seconds is stale")
	deletedCount, err := DeleteStaleSystemInstances(databaseNow)
	require.NoError(t, err)
	assert.EqualValues(t, 1, deletedCount, "far-future legacy heartbeats are invalid and removable")
	var remaining []SystemInstance
	require.NoError(t, db.Order("node_name").Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, "boundary", remaining[0].NodeName)
}

func TestDeleteFutureSystemInstanceUsesStatementDatabaseClock(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&SystemInstance{}))
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })

	databaseNow, err := DatabaseUnixTimestamp(db)
	require.NoError(t, err)
	legacyFuture := &SystemInstance{
		NodeName:   "legacy-future",
		StartedAt:  databaseNow,
		LastSeenAt: databaseNow + 365*24*60*60,
		CreatedAt:  databaseNow,
		UpdatedAt:  databaseNow,
	}
	require.NoError(t, db.Create(legacyFuture).Error)

	deleted, err := DeleteFutureSystemInstanceContext(context.Background(), legacyFuture.NodeName)
	require.NoError(t, err)
	require.True(t, deleted)
	var count int64
	require.NoError(t, db.Model(&SystemInstance{}).Where("node_name = ?", legacyFuture.NodeName).Count(&count).Error)
	assert.Zero(t, count)
}

func TestDeleteFutureSystemInstanceCannotUseDelayedCallerClockAgainstNewerHeartbeat(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&SystemInstance{}))
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })

	databaseNow, err := DatabaseUnixTimestamp(db)
	require.NoError(t, err)
	delayedCallerTimestamp := databaseNow - SystemInstanceFutureToleranceSeconds - 1
	require.NoError(t, UpsertSystemInstanceContext(context.Background(), "delayed-writer",
		map[string]any{"generation": "newer"}, databaseNow, databaseNow))

	// A repair based on delayedCallerTimestamp would regard the current row as
	// implausibly future. The repair API intentionally accepts no caller clock;
	// its statement-time database comparison must preserve the newer heartbeat.
	deleted, err := DeleteFutureSystemInstanceContext(context.Background(), "delayed-writer")
	require.NoError(t, err)
	assert.False(t, deleted)
	require.NoError(t, UpsertSystemInstanceContext(context.Background(), "delayed-writer",
		map[string]any{"generation": "delayed"}, delayedCallerTimestamp, delayedCallerTimestamp))

	var stored SystemInstance
	require.NoError(t, db.Where("node_name = ?", "delayed-writer").First(&stored).Error)
	assert.Equal(t, databaseNow, stored.LastSeenAt)
	assert.JSONEq(t, `{"generation":"newer"}`, stored.Info)
}
