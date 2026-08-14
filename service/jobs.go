package service

import (
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// Background task types.
const (
	TaskTypeSyncAbilityCache   = "sync_ability_cache"
	TaskTypeSyncOptions        = "sync_options"
	TaskTypeCleanupLogs        = "cleanup_logs"
	TaskTypeCleanupAuthFlows   = "cleanup_auth_flows"
	TaskTypeResetSubscriptions = "reset_subscriptions"
	TaskTypeChannelHealth      = "channel_health"
	TaskTypeInstanceHeartbeat = "instance_heartbeat"
)

// StartBackgroundJobs launches the periodic background jobs in a goroutine.
// Each job runs under a distributed lease so multiple nodes do not duplicate
// work. The ticker interval is controlled by SYNC_FREQUENCY (default 60s).
func StartBackgroundJobs() {
	interval := time.Duration(common.GetEnvInt("SYNC_FREQUENCY", 60)) * time.Second
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			runPeriodicJobs()
		}
	}()
}

func runPeriodicJobs() {
	RunWithLease(TaskTypeSyncAbilityCache, 2*time.Minute, func() error {
		return InitAbilityCache()
	})
	RunWithLease(TaskTypeSyncOptions, 2*time.Minute, func() error {
		return setting.Sync()
	})
	RunWithLease(TaskTypeCleanupAuthFlows, 5*time.Minute, func() error {
		cutoff := time.Now().Add(-24 * time.Hour)
		return model.DB.Where("consumed_at IS NOT NULL OR expires_at < ?", cutoff).
			Delete(&model.AuthFlow{}).Error
	})
	RunWithLease(TaskTypeResetSubscriptions, 5*time.Minute, func() error {
		return ResetDueSubscriptionQuotas()
	})
	RunWithLease(TaskTypeCleanupLogs, 10*time.Minute, func() error {
		return CleanupExpiredLogs()
	})
	RunWithLease(TaskTypeChannelHealth, 5*time.Minute, func() error {
		return RunChannelHealthTests()
	})
	RunWithLease(TaskTypeInstanceHeartbeat, 1*time.Minute, func() error {
		return RegisterSystemInstance()
	})
}

// ResetDueSubscriptionQuotas resets quota for subscriptions whose reset time has
// passed and are still active.
func ResetDueSubscriptionQuotas() error {
	now := common.NowTimestamp()
	var subs []model.UserSubscription
	if err := model.DB.Where("status = ? AND next_reset_time > 0 AND next_reset_time <= ?",
		SubscriptionStatusActive, now).Find(&subs).Error; err != nil {
		return err
	}
	for i := range subs {
		_ = ResetSubscriptionQuota(&subs[i])
	}
	return nil
}

// CleanupExpiredLogs deletes consumption logs older than the retention period
// (LOG_RETENTION_DAYS, default 30 days; 0 disables cleanup).
func CleanupExpiredLogs() error {
	retention := common.GetEnvInt("LOG_RETENTION_DAYS", 30)
	if retention <= 0 {
		return nil
	}
	cutoff := common.NowTimestamp() - int64(retention*24*60*60)
	return model.LOG_DB.Where("created_at < ?", cutoff).Delete(&model.Log{}).Error
}
