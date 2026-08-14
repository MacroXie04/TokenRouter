package service

import (
	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// NodeName returns the configured node name for this instance.
func NodeName() string {
	return common.GetEnv("NODE_NAME", "tokenrouter-node-1")
}

// RegisterSystemInstance registers (or heartbeats) the current node.
func RegisterSystemInstance() error {
	name := NodeName()
	now := common.NowTimestamp()
	var existing model.SystemInstance
	if err := model.DB.Where("node_name = ?", name).First(&existing).Error; err == nil {
		return model.DB.Model(&existing).Updates(map[string]any{
			"last_seen_at": now,
			"updated_at":   now,
		}).Error
	}
	instance := model.SystemInstance{
		NodeName:  name,
		StartedAt: now,
		LastSeenAt: now,
		CreatedAt: now,
		UpdatedAt: now,
	}
	return model.DB.Create(&instance).Error
}

// GetSystemInstances lists registered nodes, most recently seen first.
func GetSystemInstances() []model.SystemInstance {
	var instances []model.SystemInstance
	model.DB.Order("last_seen_at desc").Find(&instances)
	return instances
}

// IsInstanceStale reports whether a node has not heartbeated within the given
// threshold (default 90s), used to derive online/stale status.
func IsInstanceStale(instance *model.SystemInstance, thresholdSeconds int64) bool {
	if thresholdSeconds <= 0 {
		thresholdSeconds = 90
	}
	return common.NowTimestamp()-instance.LastSeenAt > thresholdSeconds
}
