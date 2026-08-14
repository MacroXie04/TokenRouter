package service

import (
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Background task types.
const (
	TaskTypeSyncAbilityCache   = "sync_ability_cache"
	TaskTypeCleanupLogs        = "cleanup_logs"
	TaskTypeCleanupAuthFlows   = "cleanup_auth_flows"
	TaskTypeResetSubscriptions = "reset_subscriptions"
	TaskTypeChannelHealth      = "channel_health"
	TaskTypeInstanceHeartbeat  = "instance_heartbeat"
)

// StartBackgroundJobs launches the periodic background jobs in a goroutine.
// Cluster-wide jobs use distributed leases; node-local option caches refresh on
// every node. The ticker interval is controlled by SYNC_FREQUENCY (default 60s).
// It also starts the system-task runner (channel test sweeps, log cleanup),
// the permission-policy sync loop, and the quota-data histogram flusher.
func StartBackgroundJobs() {
	StartSystemTaskRunner()
	StartQuotaDataFlusher()
	StartPerfMetricFlusher()
	interval := time.Duration(common.GetEnvInt("SYNC_FREQUENCY", 60)) * time.Second
	StartPermissionPolicySync(int(interval.Seconds()))
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			runPeriodicJobs()
		}
	}()
}

func runPeriodicJobs() {
	if err := SyncRuntimeOptions(); err != nil {
		common.SysError("failed to synchronize runtime options: " + err.Error())
	}
	RunWithLease(TaskTypeSyncAbilityCache, 2*time.Minute, func() error {
		return InitAbilityCache()
	})
	RunWithLease(TaskTypeCleanupAuthFlows, 5*time.Minute, func() error {
		cutoff := time.Now().Add(-24 * time.Hour)
		return model.DB.Where("consumed_at IS NOT NULL OR expires_at < ?", cutoff).
			Delete(&model.AuthFlow{}).Error
	})
	RunWithLease(TaskTypeResetSubscriptions, 5*time.Minute, func() error {
		if err := ResetDueSubscriptionQuotas(); err != nil {
			return err
		}
		// Prune old pre-consume idempotency records alongside the reset sweep
		// (default retention: seven days).
		_, err := CleanupSubscriptionPreConsumeRecords(0)
		return err
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
