package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestSystemInstanceHeartbeat(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SystemInstance{}))
	model.DB = db
	model.LOG_DB = db

	// First registration creates the node.
	require.NoError(t, RegisterSystemInstance())
	instances := GetSystemInstances()
	require.Len(t, instances, 1)
	assert.Equal(t, NodeName(), instances[0].NodeName)
	firstSeen := instances[0].LastSeenAt

	// Heartbeat updates LastSeenAt.
	require.NoError(t, RegisterSystemInstance())
	instances = GetSystemInstances()
	require.Len(t, instances, 1, "heartbeat must not create duplicate nodes")
	assert.GreaterOrEqual(t, instances[0].LastSeenAt, firstSeen)

	// A fresh instance is not stale.
	assert.False(t, IsInstanceStale(&instances[0], 90))
}
