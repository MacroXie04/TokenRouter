package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// Background task types.
const (
	TaskTypeSyncAbilityCache    = "sync_ability_cache"
	TaskTypeCleanupLogs         = "cleanup_logs"
	TaskTypeCleanupAuthFlows    = "cleanup_auth_flows"
	TaskTypeCleanupUserSessions = "cleanup_user_sessions"
	TaskTypeResetSubscriptions  = "reset_subscriptions"
	TaskTypeChannelHealth       = "channel_health"
	TaskTypeInstanceHeartbeat   = "instance_heartbeat"
	TaskTypeRelayQuotaReconcile = "relay_quota_reconcile"
	TaskTypeAuditLogDelivery    = "audit_log_delivery"
	TaskTypeJimengTaskRecovery  = "jimeng_task_recovery"
	TaskTypeAsyncTaskRecovery   = "async_task_recovery"
	TaskTypeStripeReconcile     = "stripe_checkout_reconcile"
	TaskTypeChannelModelUpdate  = "channel_model_update"
	TaskTypeChannelBalance      = "channel_balance_update"
)

const (
	defaultSyncFrequencySeconds       = 60
	maxSyncFrequencySeconds           = 24 * 60 * 60
	defaultModelUpdateIntervalMinutes = 30
	maxScheduledIntervalMinutes       = 365 * 24 * 60
	defaultLogRetentionDays           = 30
	maxLogRetentionDays               = 100 * 365
	periodicPreparationTimeout        = 30 * time.Second
	periodicMaintenancePollInterval   = 30 * time.Second
	systemInstanceHeartbeatInterval   = 30 * time.Second
)

type backgroundJobSchedule struct {
	syncInterval        time.Duration
	modelUpdateEnabled  bool
	modelUpdateInterval time.Duration
}

type backgroundJobStartupHooks struct {
	startSystemTaskRunner  func()
	startQuotaDataFlusher  func()
	startPerfMetricFlusher func()
	startPermissionSync    func(int)
}

type backgroundJobCadences struct {
	runtimeSync       time.Duration
	maintenancePoll   time.Duration
	instanceHeartbeat time.Duration
}

// StartBackgroundJobs launches the periodic background jobs in goroutines.
// Cluster-wide jobs use distributed leases; node-local option and ability
// caches refresh on every node at SYNC_FREQUENCY (default 60s). Maintenance
// admission and instance heartbeats have independent bounded cadences so a
// deliberately slow cache-sync setting cannot make a healthy node appear dead
// or delay critical reconciliation.
// It also starts the system-task runner (channel test sweeps, log cleanup),
// the permission-policy sync loop, and the quota-data histogram flusher.
func StartBackgroundJobs() error {
	return startBackgroundJobsWithHooks(backgroundJobStartupHooks{
		startSystemTaskRunner:  StartSystemTaskRunner,
		startQuotaDataFlusher:  StartQuotaDataFlusher,
		startPerfMetricFlusher: StartPerfMetricFlusher,
		startPermissionSync:    StartPermissionPolicySync,
	})
}

func startBackgroundJobsWithHooks(hooks backgroundJobStartupHooks) error {
	schedule, err := loadBackgroundJobSchedule()
	if err != nil {
		return err
	}
	if _, _, err := channelBalanceUpdateInterval(); err != nil {
		return err
	}
	if _, err := loadUserSessionPolicy(); err != nil {
		return err
	}
	// Every fallible schedule parse happens before any process-long goroutine is
	// started. Invalid startup configuration therefore has no partial effects.
	if hooks.startSystemTaskRunner == nil || hooks.startQuotaDataFlusher == nil ||
		hooks.startPerfMetricFlusher == nil || hooks.startPermissionSync == nil {
		return errors.New("background job startup hooks are incomplete")
	}
	hooks.startSystemTaskRunner()
	hooks.startQuotaDataFlusher()
	hooks.startPerfMetricFlusher()
	hooks.startPermissionSync(int(schedule.syncInterval.Seconds()))
	cadences := cadencesForBackgroundJobs(schedule)
	startBackgroundJobLoop(cadences.runtimeSync, runNodeRuntimeSync)
	startBackgroundJobLoop(cadences.instanceHeartbeat, runSystemInstanceHeartbeat)
	startBackgroundJobLoop(cadences.maintenancePoll, runPeriodicJobs)
	return nil
}

func cadencesForBackgroundJobs(schedule backgroundJobSchedule) backgroundJobCadences {
	return backgroundJobCadences{
		runtimeSync:       schedule.syncInterval,
		maintenancePoll:   periodicMaintenancePollInterval,
		instanceHeartbeat: systemInstanceHeartbeatInterval,
	}
}

func startBackgroundJobLoop(interval time.Duration, run func()) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		runBackgroundJobLoop(run, ticker.C)
	}()
}

func loadBackgroundJobSchedule() (backgroundJobSchedule, error) {
	syncSeconds, err := strictScheduledIntegerEnv(
		"SYNC_FREQUENCY", defaultSyncFrequencySeconds, 1, maxSyncFrequencySeconds,
	)
	if err != nil {
		return backgroundJobSchedule{}, err
	}
	modelUpdateEnabled, err := strictScheduledBoolEnv("CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_ENABLED", true)
	if err != nil {
		return backgroundJobSchedule{}, err
	}
	modelUpdateMinutes, err := strictScheduledIntegerEnv(
		"CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_INTERVAL_MINUTES",
		defaultModelUpdateIntervalMinutes, 1, maxScheduledIntervalMinutes,
	)
	if err != nil {
		return backgroundJobSchedule{}, err
	}
	return backgroundJobSchedule{
		syncInterval:        time.Duration(syncSeconds) * time.Second,
		modelUpdateEnabled:  modelUpdateEnabled,
		modelUpdateInterval: time.Duration(modelUpdateMinutes) * time.Minute,
	}, nil
}

func strictScheduledIntegerEnv(key string, fallback, minimum, maximum int) (int, error) {
	raw, configured := os.LookupEnv(key)
	if !configured || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	trimmed := strings.TrimSpace(raw)
	value, err := strconv.Atoi(trimmed)
	if err != nil || strconv.Itoa(value) != trimmed || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", key, minimum, maximum)
	}
	return value, nil
}

func strictScheduledBoolEnv(key string, fallback bool) (bool, error) {
	raw, configured := os.LookupEnv(key)
	if !configured || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	switch strings.TrimSpace(raw) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be true or false", key)
	}
}

func channelBalanceUpdateInterval() (time.Duration, bool, error) {
	raw, configured := os.LookupEnv("CHANNEL_UPDATE_FREQUENCY")
	raw = strings.TrimSpace(raw)
	if !configured || raw == "" {
		return 0, false, nil
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil || minutes <= 0 || minutes > 365*24*60 {
		return 0, false, errors.New("CHANNEL_UPDATE_FREQUENCY must be an integer from 1 to 525600 minutes")
	}
	return time.Duration(minutes) * time.Minute, true, nil
}

func runPeriodicJobs() {
	runPeriodicJobsWithLease(processPeriodicLeaseRunner)
}

var processPeriodicLeaseRunner = asyncPeriodicLeaseRunner(RunScheduledWithLeaseContext)

// asyncPeriodicLeaseRunner isolates every maintenance job in its own worker.
// A slow or cancellation-broken handler can therefore delay only its own task
// type; the remaining jobs are still offered to their independent DB leases.
func asyncPeriodicLeaseRunner(run periodicLeaseRunner) periodicLeaseRunner {
	var activeMu sync.Mutex
	active := make(map[string]struct{})
	return func(taskType string, interval time.Duration, fn func(context.Context) error) error {
		if run == nil {
			return errors.New("periodic lease runner is nil")
		}
		activeMu.Lock()
		if _, alreadyRunning := active[taskType]; alreadyRunning {
			activeMu.Unlock()
			return nil
		}
		active[taskType] = struct{}{}
		activeMu.Unlock()
		go func() {
			defer func() {
				activeMu.Lock()
				delete(active, taskType)
				activeMu.Unlock()
				if recovered := recover(); recovered != nil {
					common.SysError(fmt.Sprintf("periodic job %s panicked (%T)", taskType, recovered))
				}
			}()
			if err := run(taskType, interval, fn); err != nil {
				common.SysError("periodic job " + taskType + " failed: " + err.Error())
			}
		}()
		return nil
	}
}

func runNodeRuntimeSync() {
	ctx, cancel := context.WithTimeout(context.Background(), periodicPreparationTimeout)
	defer cancel()
	if err := SyncRuntimeOptionsContext(ctx); err != nil {
		common.SysError("failed to synchronize runtime options: " + err.Error())
	}
	if err := InitAbilityCacheContext(ctx); err != nil {
		common.SysError("failed to synchronize ability cache: " + err.Error())
	}
}

func runSystemInstanceHeartbeat() {
	ctx, cancel := context.WithTimeout(context.Background(), periodicPreparationTimeout)
	defer cancel()
	if err := RegisterSystemInstanceContext(ctx); err != nil {
		common.SysError("failed to register system instance: " + err.Error())
	}
}

func runBackgroundJobLoop(run func(), ticks <-chan time.Time) {
	runBackgroundJobPass(run)
	for range ticks {
		runBackgroundJobPass(run)
	}
}

func runBackgroundJobPass(run func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			common.SysError(fmt.Sprintf("background job scheduler panicked (%T)", recovered))
		}
	}()
	if run == nil {
		common.SysError("background job scheduler callback is nil")
		return
	}
	run()
}

type periodicLeaseRunner func(taskType string, ttl time.Duration, fn func(context.Context) error) error

func runPeriodicJobsWithLease(run periodicLeaseRunner) {
	ctx, cancel := context.WithTimeout(context.Background(), periodicPreparationTimeout)
	defer cancel()
	runPeriodicJobsWithLeaseContext(ctx, run)
}

func runPeriodicJobsWithLeaseContext(ctx context.Context, run periodicLeaseRunner) {
	if ctx == nil {
		common.SysError("periodic job preparation context is nil")
		return
	}
	if run == nil {
		common.SysError("periodic lease runner is nil")
		return
	}
	// Every node imports its own encrypted emergency journal before any generic
	// reservation expiry or cluster singleton is chosen. Otherwise a healthy
	// non-source node could win every lease while the only accepted id remains
	// local, or generic expiry could quarantine the hold first.
	recoveryPromotionReady := true
	if err := runRegisteredJimengTaskPromoter(ctx); err != nil {
		recoveryPromotionReady = false
		common.SysError("failed to promote node-local Jimeng recovery state: " + err.Error())
	}
	if err := runRegisteredAsyncTaskPromoter(ctx); err != nil {
		recoveryPromotionReady = false
		common.SysError("failed to promote node-local async-task recovery state: " + err.Error())
	}
	run(TaskTypeCleanupAuthFlows, 5*time.Minute, func(ctx context.Context) error {
		return CleanupAuthFlowsContext(ctx)
	})
	run(TaskTypeCleanupUserSessions, time.Hour, CleanupUserSessions)
	run(TaskTypeResetSubscriptions, 5*time.Minute, func(ctx context.Context) error {
		if err := BackfillLegacySubscriptionEntitlementSnapshotsContext(ctx, 100); err != nil {
			return err
		}
		if err := ExpireDueSubscriptionsContext(ctx, 100); err != nil {
			return err
		}
		if err := ResetDueSubscriptionQuotasContext(ctx); err != nil {
			return err
		}
		// Prune old pre-consume idempotency records alongside the reset sweep
		// (default retention: seven days).
		_, err := CleanupSubscriptionPreConsumeRecordsContext(ctx, 0)
		return err
	})
	run(TaskTypeCleanupLogs, 10*time.Minute, func(ctx context.Context) error {
		return EnqueueExpiredLogCleanupContext(ctx)
	})
	reliability := setting.GetChannelReliabilitySetting()
	if reliability.AutoTestChannelEnabled {
		run(TaskTypeChannelHealth, time.Duration(reliability.AutoTestChannelMinutes)*time.Minute, func(ctx context.Context) error {
			_, _, err := EnqueueSystemTaskContext(ctx, model.SystemTaskTypeChannelTest, channelTestTaskPayload{
				Mode: reliability.ChannelTestMode, Notify: false,
			})
			return err
		})
	}
	schedule, scheduleErr := loadBackgroundJobSchedule()
	if scheduleErr != nil {
		common.SysError("invalid background job schedule: " + scheduleErr.Error())
	} else if schedule.modelUpdateEnabled {
		run(TaskTypeChannelModelUpdate, schedule.modelUpdateInterval, func(ctx context.Context) error {
			_, _, err := EnqueueSystemTaskContext(ctx, model.SystemTaskTypeModelUpdate, nil)
			return err
		})
	}
	if interval, enabled, err := channelBalanceUpdateInterval(); err != nil {
		common.SysError("invalid channel balance update schedule: " + err.Error())
	} else if enabled {
		run(TaskTypeChannelBalance, interval, func(ctx context.Context) error {
			return UpdateAllChannelsBalancesContext(ctx)
		})
	}
	if recoveryPromotionReady {
		run(TaskTypeRelayQuotaReconcile, 2*time.Minute, func(ctx context.Context) error {
			return ReconcileRelayQuotaReservationsContext(ctx)
		})
	}
	run(TaskTypeAuditLogDelivery, 2*time.Minute, func(ctx context.Context) error {
		return DeliverAuditLogOutboxContext(ctx)
	})
	if recoveryPromotionReady {
		run(TaskTypeJimengTaskRecovery, 2*time.Minute, func(ctx context.Context) error {
			return runRegisteredJimengTaskReconciler(ctx)
		})
		run(TaskTypeAsyncTaskRecovery, 2*time.Minute, func(ctx context.Context) error {
			return runRegisteredAsyncTaskReconciler(ctx)
		})
	}
	run(TaskTypeStripeReconcile, 2*time.Minute, func(ctx context.Context) error {
		return ReconcileStripeCheckoutOrders(ctx)
	})
}

// BackfillLegacySubscriptionEntitlementSnapshots copies reset cadence once
// for legacy rows while their source plan still exists. Every examined row is
// moved out of pending (backfilled or review), preventing an orphan at the
// front of the table from starving all later subscriptions.
func BackfillLegacySubscriptionEntitlementSnapshots(limit int) error {
	return BackfillLegacySubscriptionEntitlementSnapshotsContext(context.Background(), limit)
}

// ExpireDueSubscriptions drains all due users in bounded query batches. Each
// user's complete due set is reconciled under one user-row lock, so overlapping
// subscriptions cannot leave the group at an order-dependent intermediate
// baseline.
func ExpireDueSubscriptions(limit int) error {
	return ExpireDueSubscriptionsContext(context.Background(), limit)
}

// ResetDueSubscriptionQuotas resets quota for subscriptions whose reset time has
// passed and are still active.
func ResetDueSubscriptionQuotas() error {
	return ResetDueSubscriptionQuotasContext(context.Background())
}

// CleanupExpiredLogs deletes consumption logs older than the retention period
// (LOG_RETENTION_DAYS, default 30 days; 0 disables cleanup).
func CleanupExpiredLogs() error {
	return CleanupExpiredLogsContext(context.Background())
}

func logRetentionDays() (int64, bool, error) {
	raw, configured := os.LookupEnv("LOG_RETENTION_DAYS")
	if !configured || strings.TrimSpace(raw) == "" {
		return defaultLogRetentionDays, true, nil
	}
	trimmed := strings.TrimSpace(raw)
	days, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || strconv.FormatInt(days, 10) != trimmed || days < 0 || days > maxLogRetentionDays {
		return 0, false, fmt.Errorf("LOG_RETENTION_DAYS must be an integer from 0 to %d", maxLogRetentionDays)
	}
	return days, days > 0, nil
}
