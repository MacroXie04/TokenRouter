package billing

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestRelayAccountingExternalDatabaseConcurrency verifies the real
// MySQL/PostgreSQL locking path when TOKENROUTER_TEST_SQL_DSN points at an
// isolated database. Ordinary unit-test runs skip it instead of presenting a
// SQLite result as server-database evidence.
func TestRelayAccountingExternalDatabaseConcurrency(t *testing.T) {
	dsn := os.Getenv("TOKENROUTER_TEST_SQL_DSN")
	if dsn == "" {
		t.Skip("set TOKENROUTER_TEST_SQL_DSN to an isolated MySQL or PostgreSQL database")
	}

	previousDB, previousLogDB := model.DB, model.LOG_DB
	t.Setenv("SQL_DSN", dsn)
	t.Setenv("SQLITE_PATH", "")
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitDB())
	externalDB := model.DB
	t.Cleanup(func() {
		if sqlDB, err := externalDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})
	require.Contains(t, []string{"mysql", "postgres"}, model.DB.Dialector.Name())

	suffix := cryptoutil.BestEffortRandomAlphanumeric(10)
	user := model.User{
		Username: "db-int-" + suffix,
		Status:   model.UserStatusEnabled,
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-db-int-" + suffix,
		Status: TokenStatusEnabled, RemainQuota: 100,
	}
	require.NoError(t, model.DB.Create(&token).Error)

	const attempts = 20
	start := make(chan struct{})
	results := make(chan struct {
		reservation *RelayQuotaReservation
		err         error
	}, attempts)
	var ready sync.WaitGroup
	ready.Add(attempts)
	for range attempts {
		go func() {
			ready.Done()
			<-start
			reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
			results <- struct {
				reservation *RelayQuotaReservation
				err         error
			}{reservation: reservation, err: err}
		}()
	}
	ready.Wait()
	close(start)

	reservations := make([]*RelayQuotaReservation, 0, 10)
	for range attempts {
		result := <-results
		if result.err == nil {
			reservations = append(reservations, result.reservation)
			continue
		}
		assert.True(t, errors.Is(result.err, ErrInsufficientQuota) ||
			errors.Is(result.err, ErrInsufficientTokenQuota), "unexpected reservation error: %v", result.err)
	}
	require.Len(t, reservations, 10, "the database locks must admit exactly the available quota")

	var settleFailures atomic.Int32
	var settleWG sync.WaitGroup
	settleWG.Add(len(reservations))
	for _, reservation := range reservations {
		go func(reservation *RelayQuotaReservation) {
			defer settleWG.Done()
			if err := reservation.Settle(10); err != nil {
				settleFailures.Add(1)
			}
		}(reservation)
	}
	settleWG.Wait()
	assert.Zero(t, settleFailures.Load())

	var userAfter model.User
	var tokenAfter model.Token
	require.NoError(t, model.DB.First(&userAfter, user.Id).Error)
	require.NoError(t, model.DB.First(&tokenAfter, token.Id).Error)
	assert.Zero(t, userAfter.Quota)
	assert.Equal(t, 100, userAfter.UsedQuota)
	assert.Equal(t, 10, userAfter.RequestCount)
	assert.Zero(t, tokenAfter.RemainQuota)
	assert.Equal(t, 100, tokenAfter.UsedQuota)

	var settled int64
	reservationIDs := make([]string, 0, len(reservations))
	for _, reservation := range reservations {
		reservationIDs = append(reservationIDs, reservation.ReservationID())
	}
	require.NoError(t, model.DB.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id IN ? AND user_id = ? AND status = ?", reservationIDs, user.Id,
			model.RelayQuotaReservationStatusSettled).
		Count(&settled).Error)
	assert.EqualValues(t, len(reservations), settled,
		"the manifest must count only reservations created by this run when reusing a disposable schema")
}

func TestAuditOutboxExternalDatabaseLeaseAndDelivery(t *testing.T) {
	dsn := os.Getenv("TOKENROUTER_TEST_SQL_DSN")
	if dsn == "" {
		t.Skip("set TOKENROUTER_TEST_SQL_DSN to an isolated MySQL or PostgreSQL database")
	}

	previousDB, previousLogDB := model.DB, model.LOG_DB
	t.Setenv("SQL_DSN", dsn)
	t.Setenv("SQLITE_PATH", "")
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitDB())
	externalDB := model.DB
	t.Cleanup(func() {
		if sqlDB, err := externalDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})
	require.Contains(t, []string{"mysql", "postgres"}, model.DB.Dialector.Name())

	// An unmatched signed provider event has no accounting ledger row to lock.
	// Its stable audit event id must still make concurrent transaction replays
	// idempotent, including on PostgreSQL where a plain uniqueness failure would
	// abort the losing transaction.
	concurrentEventID := cryptoutil.BestEffortUUID()
	concurrentEntry := &model.Log{
		AuditEventId: &concurrentEventID,
		Type:         LogTypeSystem,
		Content:      "external-concurrent-enqueue",
		CreatedAt:    1,
	}
	startEnqueue := make(chan struct{})
	enqueueErrors := make(chan error, 2)
	for range 2 {
		go func() {
			<-startEnqueue
			enqueueErrors <- model.DB.Transaction(func(tx *gorm.DB) error {
				return EnqueueAuditLogTx(tx, concurrentEntry)
			})
		}()
	}
	close(startEnqueue)
	require.NoError(t, <-enqueueErrors)
	require.NoError(t, <-enqueueErrors)
	var concurrentRows int64
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", concurrentEventID).Count(&concurrentRows).Error)
	assert.EqualValues(t, 1, concurrentRows)

	content := "external-outbox-" + cryptoutil.BestEffortRandomAlphanumeric(12)
	// Exercise the full portable indexed width. The reference/MySQL schema is
	// varchar(191), so the fixture must stay at that exact portable boundary
	// under strict SQL mode.
	longModelName := strings.Repeat("m", 191)
	eventID := cryptoutil.BestEffortUUID()
	model.LOG_DB = nil
	require.NoError(t, persistAuditLogWithOutbox(&model.Log{
		AuditEventId: &eventID,
		Type:         LogTypeSystem,
		Content:      content,
		ModelName:    longModelName,
		CreatedAt:    wallclock.NowTimestamp(),
	}))
	var queued model.AuditLogOutbox
	require.NoError(t, model.DB.Where("event_id = ?", eventID).First(&queued).Error)

	model.LOG_DB = externalDB
	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			errs <- DeliverAuditLogOutbox()
		}()
	}
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)

	require.NoError(t, model.DB.First(&queued, queued.ID).Error)
	assert.Equal(t, model.AuditLogOutboxStatusDelivered, queued.Status)
	assert.True(t, queued.PayloadScrubbed)
	assert.Equal(t, 1, queued.Attempts, "exactly one database-fenced worker may claim the event")
	assert.True(t, isAuditLogPayloadReceipt(queued.Payload))
	assert.NotContains(t, queued.Payload, content)
	var delivered int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("audit_event_id = ? AND content = ? AND model_name = ?", queued.EventID, content, longModelName).
		Count(&delivered).Error)
	assert.EqualValues(t, 1, delivered)
}

// TestSubscriptionUsageEpochExternalDatabaseLifecycle proves the reset fence
// on real row-locking databases. A reservation from a prior quota window must
// reach a terminal state without changing the current window's usage.
func TestSubscriptionUsageEpochExternalDatabaseLifecycle(t *testing.T) {
	dsn := os.Getenv("TOKENROUTER_TEST_SQL_DSN")
	if dsn == "" {
		t.Skip("set TOKENROUTER_TEST_SQL_DSN to an isolated MySQL or PostgreSQL database")
	}

	previousDB, previousLogDB := model.DB, model.LOG_DB
	t.Setenv("SQL_DSN", dsn)
	t.Setenv("SQLITE_PATH", "")
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitDB())
	externalDB := model.DB
	t.Cleanup(func() {
		if sqlDB, err := externalDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})
	require.Contains(t, []string{"mysql", "postgres"}, model.DB.Dialector.Name())

	suffix := cryptoutil.BestEffortRandomAlphanumeric(10)
	user := model.User{
		Username: "epoch-db-int-" + suffix, Status: model.UserStatusEnabled,
		Setting: `{"billing_preference":"subscription_only"}`,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	plan := model.SubscriptionPlan{
		Title: "Epoch external " + suffix, PriceAmount: "0", Enabled: true, TotalAmount: 1_000,
		DurationUnit: SubscriptionDurationDay, DurationValue: 1, QuotaResetPeriod: SubscriptionResetNever,
	}
	require.NoError(t, model.DB.Create(&plan).Error)
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	sub := model.UserSubscription{
		UserId: user.Id, PlanId: plan.Id, AmountTotal: 1_000, UsageEpoch: 31,
		StartTime: now - 60, EndTime: now + 3600, Status: SubscriptionStatusActive,
		EntitlementVersion:        UserSubscriptionEntitlementVersion,
		EntitlementMigrationState: SubscriptionEntitlementMigrationBackfilled,
		QuotaResetPeriodSnapshot:  SubscriptionResetNever, AllowWalletOverflow: true,
	}
	require.NoError(t, model.DB.Create(&sub).Error)
	token := model.Token{UserId: user.Id, Key: "sk-epoch-db-int-" + suffix, Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, model.DB.Create(&token).Error)

	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	_, err = AdminResetUserSubscriptionsByPlan(user.Id, plan.Id, false)
	require.NoError(t, err)
	_, err = PreConsumeUserSubscription("epoch-db-current-"+suffix, user.Id, 7)
	require.NoError(t, err)
	require.NoError(t, reservation.Settle(16))

	var stored model.UserSubscription
	require.NoError(t, model.DB.First(&stored, sub.Id).Error)
	assert.Equal(t, int64(32), stored.UsageEpoch)
	assert.Equal(t, int64(7), stored.AmountUsed)
	var durable model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("reservation_id = ?", reservation.ReservationID()).First(&durable).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, durable.Status)
	assert.Equal(t, int64(31), durable.UsageEpoch)
	var ledger model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, SubscriptionPreConsumeStatusSettled, ledger.Status)
	assert.Equal(t, int64(31), ledger.UsageEpoch)
}

func TestTaskOperationAccountingSQLiteLifecycle(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	db := setupRelayQuotaReservationDB(t)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskOperation{}))
	exerciseTaskOperationAccountingLifecycle(t)
}

// TestTaskOperationAccountingExternalDatabaseLifecycle proves that the
// provider-neutral async journal and durable quota ledger share each state
// transition on the real MySQL/PostgreSQL transaction and CAS paths. Ordinary
// unit runs skip this gate instead of presenting SQLite as server evidence.
func TestTaskOperationAccountingExternalDatabaseLifecycle(t *testing.T) {
	dsn := os.Getenv("TOKENROUTER_TEST_SQL_DSN")
	if dsn == "" {
		t.Skip("set TOKENROUTER_TEST_SQL_DSN to an isolated MySQL or PostgreSQL database")
	}

	previousDB, previousLogDB := model.DB, model.LOG_DB
	t.Setenv("SQL_DSN", dsn)
	t.Setenv("SQLITE_PATH", "")
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitDB())
	externalDB := model.DB
	t.Cleanup(func() {
		if sqlDB, err := externalDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})
	require.Contains(t, []string{"mysql", "postgres"}, model.DB.Dialector.Name())

	exerciseTaskOperationAccountingLifecycle(t)
}

func exerciseTaskOperationAccountingLifecycle(t *testing.T) {
	t.Helper()

	const quota = 10
	suffix := cryptoutil.BestEffortRandomAlphanumeric(12)
	platform := strconv.Itoa(int(channelcatalog.ChannelTypeSora))
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	taskID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	user := model.User{
		Username: "sora-db-int-" + suffix,
		Status:   model.UserStatusEnabled,
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-sora-db-int-" + suffix,
		Status: TokenStatusEnabled, RemainQuota: 100,
	}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{
		Name: "sora-db-int-" + suffix, Key: "upstream-test-key",
		Type: int(channelcatalog.ChannelTypeSora), Status: channelcatalog.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(&channel).Error)

	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID,
		Platform: platform, UserId: user.Id, Group: userssvc.GroupDefault, ChannelId: channel.Id,
		Quota: quota, Action: "textGenerate", Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: `{}`, Data: "null",
	}
	reservation, err := NewRelayQuotaReservationWithPersistence(
		user.Id, &token, quota,
		func(tx *gorm.DB, creation RelayQuotaReservationCreation) error {
			task.PrivateData = "encrypted-task-state-" + suffix
			if err := tx.Create(&task).Error; err != nil {
				return err
			}
			return tx.Create(&model.TaskOperation{
				TaskID: task.TaskID, ReservationID: creation.ReservationID,
				Platform: platform, UserID: user.Id, ChannelID: channel.Id,
				State: model.TaskOperationPrepared, NextAttemptAt: now + 45,
				CreatedAt: now, UpdatedAt: now,
			}).Error
		},
	)
	require.NoError(t, err)
	require.NotZero(t, task.ID)

	var operation model.TaskOperation
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	var ledger model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, model.TaskOperationPrepared, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusHeld, ledger.Status)
	assertTaskOperationAccountingBalances(t, user.Id, token.Id, channel.Id, 90, 90, 0, 0)

	require.NoError(t, reservation.MarkDispatchedWithPersistence(func(tx *gorm.DB) error {
		transitionAt, clockErr := model.DatabaseUnixTimestamp(tx)
		if clockErr != nil {
			return clockErr
		}
		result := tx.Model(&model.TaskOperation{}).
			Where("task_id = ? AND reservation_id = ? AND state = ?", task.TaskID,
				reservation.ReservationID(), model.TaskOperationPrepared).
			Updates(map[string]any{
				"state": model.TaskOperationDispatching, "settlement_pending": true,
				"next_attempt_at": transitionAt + 120, "updated_at": transitionAt,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRelayQuotaReservationBusy
		}
		return nil
	}))
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, model.TaskOperationDispatching, operation.State)
	assert.True(t, operation.SettlementPending)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, ledger.Status)

	injectedSettlementFailure := errors.New("injected task settlement persistence failure")
	err = reservation.SettleWithChannelAndPersistence(quota, channel.Id,
		taskOperationSettlementPersistence(&task, reservation.ReservationID(), suffix, injectedSettlementFailure),
	)
	require.ErrorIs(t, err, injectedSettlementFailure)
	require.NoError(t, model.DB.First(&task, task.ID).Error)
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, model.TaskStatusNotStart, task.Status)
	assert.Equal(t, model.TaskOperationDispatching, operation.State)
	assert.True(t, operation.SettlementPending)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, ledger.Status)
	assertTaskOperationAccountingBalances(t, user.Id, token.Id, channel.Id, 90, 90, 0, 0)

	require.NoError(t, reservation.SettleWithChannelAndPersistence(quota, channel.Id,
		taskOperationSettlementPersistence(&task, reservation.ReservationID(), suffix, nil),
	))
	require.NoError(t, model.DB.First(&task, task.ID).Error)
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.False(t, operation.SettlementPending)
	assert.Equal(t, "encrypted-provider-id-"+suffix, operation.EncryptedProviderTaskID)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, ledger.Status)
	assert.Equal(t, model.RelayQuotaReservationOperationSettle, ledger.Operation)
	assert.Equal(t, quota, ledger.ActualQuota)
	assertTaskOperationAccountingBalances(t, user.Id, token.Id, channel.Id, 90, 90, quota, 1)

	injectedReversalFailure := errors.New("injected task reversal persistence failure")
	err = ReverseSettledRelayQuotaReservationWithPersistence(
		reservation.ReservationID(),
		taskOperationReversalPersistence(&task, reservation.ReservationID(), injectedReversalFailure),
	)
	require.ErrorIs(t, err, injectedReversalFailure)
	require.NoError(t, model.DB.First(&task, task.ID).Error)
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
	assert.Equal(t, quota, task.Quota)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, ledger.Status)
	assertTaskOperationAccountingBalances(t, user.Id, token.Id, channel.Id, 90, 90, quota, 1)

	reverse := taskOperationReversalPersistence(&task, reservation.ReservationID(), nil)
	require.NoError(t, ReverseSettledRelayQuotaReservationWithPersistence(reservation.ReservationID(), reverse))
	require.NoError(t, ReverseSettledRelayQuotaReservationWithPersistence(reservation.ReservationID(), reverse),
		"a replayed provider failure must not restore quota twice")
	require.NoError(t, model.DB.First(&task, task.ID).Error)
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationReversed, operation.State)
	assert.False(t, operation.SettlementPending)
	assert.Empty(t, operation.EncryptedProviderTaskID)
	assert.Equal(t, model.RelayQuotaReservationStatusReversed, ledger.Status)
	assert.Equal(t, model.RelayQuotaReservationOperationReverse, ledger.Operation)
	assertTaskOperationAccountingBalances(t, user.Id, token.Id, channel.Id, 100, 100, quota, 1)
}

func taskOperationSettlementPersistence(
	task *model.Task,
	reservationID, suffix string,
	injected error,
) RelayQuotaReservationPersistence {
	return func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND status = ?", task.ID, task.TaskID, model.TaskStatusNotStart).
			Updates(map[string]any{
				"status": model.TaskStatusSubmitted, "progress": "10%", "updated_at": now,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return ErrRelayQuotaReservationBusy
		}
		operationResult := tx.Model(&model.TaskOperation{}).
			Where("task_id = ? AND reservation_id = ? AND state = ?", task.TaskID,
				reservationID, model.TaskOperationDispatching).
			Updates(map[string]any{
				"state": model.TaskOperationSubmitted, "settlement_pending": false,
				"encrypted_provider_task_id": "encrypted-provider-id-" + suffix,
				"next_attempt_at":            now + 45, "updated_at": now,
			})
		if operationResult.Error != nil {
			return operationResult.Error
		}
		if operationResult.RowsAffected != 1 {
			return ErrRelayQuotaReservationBusy
		}
		return injected
	}
}

func taskOperationReversalPersistence(
	task *model.Task,
	reservationID string,
	injected error,
) RelayQuotaReservationPersistence {
	return func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND status IN ?", task.ID, task.TaskID,
				[]string{model.TaskStatusSubmitted, model.TaskStatusFailure}).
			Updates(map[string]any{
				"status": model.TaskStatusFailure, "fail_reason": "provider task failed",
				"quota": 0, "progress": "100%", "finish_time": now, "updated_at": now,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected == 0 {
			var current model.Task
			if err := tx.First(&current, task.ID).Error; err != nil ||
				current.TaskID != task.TaskID || current.Status != model.TaskStatusFailure || current.Quota != 0 {
				return ErrRelayQuotaReservationBusy
			}
		}
		operationResult := tx.Model(&model.TaskOperation{}).
			Where("task_id = ? AND reservation_id = ? AND state IN ?", task.TaskID, reservationID,
				[]string{model.TaskOperationSubmitted, model.TaskOperationReversed}).
			Updates(map[string]any{
				"state": model.TaskOperationReversed, "settlement_pending": false,
				"encrypted_provider_task_id": "", "next_attempt_at": 0,
				"completed_at": now, "updated_at": now, "last_error": "",
				"lease_owner": "", "lease_expires_at": 0,
			})
		if operationResult.Error != nil {
			return operationResult.Error
		}
		if operationResult.RowsAffected == 0 {
			var current model.TaskOperation
			if err := tx.Where("task_id = ? AND reservation_id = ?", task.TaskID, reservationID).
				First(&current).Error; err != nil || current.State != model.TaskOperationReversed ||
				current.SettlementPending || current.EncryptedProviderTaskID != "" {
				return ErrRelayQuotaReservationBusy
			}
		}
		return injected
	}
}

func assertTaskOperationAccountingBalances(
	t *testing.T,
	userID, tokenID, channelID, wantUserQuota, wantTokenQuota, wantUsedQuota, wantRequests int,
) {
	t.Helper()
	var user model.User
	var token model.Token
	var channel model.Channel
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.Unscoped().First(&token, tokenID).Error)
	require.NoError(t, model.DB.First(&channel, channelID).Error)
	assert.Equal(t, wantUserQuota, user.Quota)
	assert.Equal(t, wantTokenQuota, token.RemainQuota)
	assert.Equal(t, wantUsedQuota, user.UsedQuota)
	assert.Equal(t, wantRequests, user.RequestCount)
	assert.Equal(t, wantUsedQuota, token.UsedQuota)
	assert.Equal(t, int64(wantUsedQuota), channel.UsedQuota)
}
