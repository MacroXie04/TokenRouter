package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/jimeng"
	"github.com/tokenrouter/tokenrouter/service"
)

func setupJimengOperationDB(t *testing.T) (*gorm.DB, model.User, model.Token, model.Channel) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "jimeng-operation.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Token{}, &model.Channel{}, &model.Task{},
		&model.JimengTaskOperation{}, &model.RelayQuotaReservationRecord{},
		&model.RelayQuotaReservationReviewEvent{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{},
		&model.Log{}, &model.AuditLogOutbox{},
	))
	model.DB = db
	model.LOG_DB = db
	user := model.User{Username: "jimeng-operation", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-jimeng-operation", Status: service.TokenStatusEnabled,
		RemainQuota: 100,
	}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{
		Name: "jimeng-operation", Key: "access|secret", Type: int(constant.ChannelTypeJimeng),
		Status: constant.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(&channel).Error)
	return db, user, token, channel
}

func newAtomicJimengTask(user model.User, channel model.Channel) (model.Task, jimengTaskPrivateData) {
	now := common.NowTimestamp()
	return model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: model.GenerateTaskID(),
		Platform: jimengTaskPlatform, UserId: user.Id, ChannelId: channel.Id,
		Group: "default", Quota: 10, Action: "generate",
		Status: model.TaskStatusNotStart, SubmitTime: now, Progress: "0%",
		Properties: `{}`, Data: `null`,
	}, jimengTaskPrivateData{}
}

func TestJimengAtomicCreationRollsBackEveryPredispatchBoundary(t *testing.T) {
	tests := []struct {
		name      string
		callback  string
		table     string
		operation string
	}{
		{name: "wallet hold", callback: "gorm:update", table: "users", operation: "update"},
		{name: "token hold", callback: "gorm:update", table: "tokens", operation: "update"},
		{name: "reservation marker", callback: "gorm:create", table: "relay_quota_reservations", operation: "create"},
		{name: "task", callback: "gorm:create", table: "tasks", operation: "create"},
		{name: "recovery marker", callback: "gorm:create", table: "jimeng_task_operations", operation: "create"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, user, token, channel := setupJimengOperationDB(t)
			callbackName := "test:jimeng_atomic_" + test.table
			inject := func(tx *gorm.DB) {
				if tx.Statement.Table == test.table {
					tx.AddError(errors.New("injected atomic Jimeng creation failure"))
				}
			}
			if test.operation == "update" {
				require.NoError(t, db.Callback().Update().Before(test.callback).Register(callbackName, inject))
				t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })
			} else {
				require.NoError(t, db.Callback().Create().Before(test.callback).Register(callbackName, inject))
				t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
			}

			task, privateData := newAtomicJimengTask(user, channel)
			_, err := createJimengReservedTask(&task, &token, &privateData)
			require.Error(t, err)
			require.NoError(t, db.First(&user, user.Id).Error)
			require.NoError(t, db.First(&token, token.Id).Error)
			assert.Equal(t, 100, user.Quota)
			assert.Equal(t, 100, token.RemainQuota)
			var taskCount, operationCount, reservationCount int64
			require.NoError(t, db.Model(&model.Task{}).Count(&taskCount).Error)
			require.NoError(t, db.Model(&model.JimengTaskOperation{}).Count(&operationCount).Error)
			require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).Count(&reservationCount).Error)
			assert.Zero(t, taskCount)
			assert.Zero(t, operationCount)
			assert.Zero(t, reservationCount)
		})
	}
}

func TestJimengDispatchingRecoverySurvivesRestartWithoutRedispatchOrGuessingCharge(t *testing.T) {
	db, user, token, channel := setupJimengOperationDB(t)
	var providerCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		providerCalls.Add(1)
	}))
	defer upstream.Close()
	channel.BaseURL = upstream.URL
	require.NoError(t, db.Save(&channel).Error)

	task, privateData := newAtomicJimengTask(user, channel)
	privateData.ChannelBaseURL = upstream.URL
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))

	// Simulate a process exit by discarding the in-memory reservation. A later
	// worker uses only primary-database state and must never submit again.
	now := common.NowTimestamp() + jimengDispatchRecoverySeconds + 1
	require.NoError(t, reconcileJimengTaskOperationsAt(now, 10))
	require.NoError(t, reconcileJimengTaskOperationsAt(now+1, 10))
	assert.Zero(t, providerCalls.Load(), "recovery must never retry a possibly accepted submit")

	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	require.NoError(t, db.First(&channel, channel.Id).Error)
	assert.Equal(t, 90, user.Quota, "the funding hold remains reserved for review")
	assert.Zero(t, user.UsedQuota, "absence of a provider id is not evidence of acceptance")
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, 90, token.RemainQuota, "the token hold remains reserved for review")
	assert.Zero(t, token.UsedQuota)
	assert.Zero(t, channel.UsedQuota)
	var storedTask model.Task
	require.NoError(t, db.First(&storedTask, task.ID).Error)
	assert.Equal(t, model.TaskStatusUnknown, storedTask.Status)
	var operation model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationManualReview, operation.State)
	assert.False(t, operation.SettlementPending)
	var record model.RelayQuotaReservationRecord
	require.NoError(t, db.Where("reservation_id = ?", reservation.ReservationID()).First(&record).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, record.Status,
		"the bounded generic expiry/review workflow owns an unresolved hold")
}

func TestJimengDurableUnknownPendingOutcomeSettlesWithoutClientFetch(t *testing.T) {
	t.Setenv("JIMENG_RECOVERY_DIR", t.TempDir())
	db, user, token, channel := setupJimengOperationDB(t)
	task, privateData := newAtomicJimengTask(user, channel)
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))
	require.NoError(t, persistJimengPendingOutcome(
		&task, privateData, []byte(`{"ambiguous":true}`), model.TaskStatusUnknown,
		"provider outcome was durably recorded as unknown",
	))
	require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
		Update("next_attempt_at", 0).Error)

	require.NoError(t, ReconcileJimengTaskOperations())
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 10, token.UsedQuota)
	var operation model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationUnknown, operation.State)
	assert.False(t, operation.SettlementPending)
	require.NoError(t, db.First(&task, task.ID).Error)
	assert.NotContains(t, task.PrivateData, "encrypted_channel_key")
	assert.NotContains(t, task.PrivateData, "channel_base_url")
	assert.Contains(t, task.PrivateData, "relay_reservation_id",
		"billing identity remains durable after recovery credentials are removed")
	var ledger model.RelayQuotaReservationRecord
	require.NoError(t, db.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, ledger.Status)
	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", jimengAuditEventID(reservation.ReservationID())).Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)
}

func TestJimengSubmittedRecoverySettlesAndPollsWithoutClientFetch(t *testing.T) {
	t.Setenv("SSRF_DISABLE", "true")
	t.Cleanup(common.InitSSRF)
	common.InitSSRF()
	db, user, token, channel := setupJimengOperationDB(t)
	var providerFetches atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		providerFetches.Add(1)
		assert.Equal(t, "CVSync2AsyncGetResult", request.URL.Query().Get("Action"))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":10000,"message":"success","data":{"status":"done","video_url":"https://cdn.example.test/autonomous.mp4"}}`))
	}))
	defer upstream.Close()
	channel.BaseURL = upstream.URL
	require.NoError(t, db.Save(&channel).Error)

	task, privateData := newAtomicJimengTask(user, channel)
	task.Properties = `{"upstream_model_name":"jimeng-test","origin_model_name":"jimeng-test"}`
	privateData.ChannelBaseURL = upstream.URL
	encryptedKey, err := common.EncryptByAES(channel.Key)
	require.NoError(t, err)
	privateData.EncryptedChannelKey = encryptedKey
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, task.Properties, privateJSON))
	privateData.UpstreamTaskID = "provider-task-never-logged"
	require.NoError(t, persistJimengPendingOutcome(
		&task, privateData, []byte(`{"accepted":true}`), model.TaskStatusSubmitted, "",
	))
	require.NoError(t, db.Model(&model.JimengTaskOperation{}).
		Where("task_id = ?", task.TaskID).Update("next_attempt_at", 0).Error)

	require.NoError(t, ReconcileJimengTaskOperations())
	require.NoError(t, ReconcileJimengTaskOperations())
	assert.Equal(t, int32(1), providerFetches.Load(), "terminal recovery must be cached")
	require.NoError(t, db.First(&task, task.ID).Error)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	require.NoError(t, db.First(&channel, channel.Id).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 10, token.UsedQuota)
	assert.Equal(t, int64(10), channel.UsedQuota)
	var operation model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationTerminal, operation.State)
	assert.False(t, operation.SettlementPending)
}

func TestJimengPollingHasBoundedLifetimeAndOversizedResultsCloseDurably(t *testing.T) {
	t.Setenv("SSRF_DISABLE", "true")
	t.Cleanup(common.InitSSRF)
	common.InitSSRF()

	t.Run("successful nonterminal polls reach manual review by age", func(t *testing.T) {
		t.Setenv("JIMENG_MAX_POLL_DAYS", "1")
		db, user, token, channel := setupJimengOperationDB(t)
		var fetches atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			fetches.Add(1)
			_, _ = writer.Write([]byte(`{"code":10000,"data":{"status":"running"}}`))
		}))
		defer upstream.Close()
		task, privateData := newAtomicJimengTask(user, channel)
		task.Properties = `{"upstream_model_name":"jimeng-age","origin_model_name":"jimeng-age"}`
		privateData.ChannelBaseURL = upstream.URL
		privateData.EncryptedChannelKey, _ = common.EncryptByAES(channel.Key)
		reservation, err := createJimengReservedTask(&task, &token, &privateData)
		require.NoError(t, err)
		privateJSON, err := marshalJimengTaskPrivateData(privateData)
		require.NoError(t, err)
		require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, task.Properties, privateJSON))
		privateData.UpstreamTaskID = "provider-long-running"
		require.NoError(t, persistJimengPendingOutcome(
			&task, privateData, []byte(`{"accepted":true}`), model.TaskStatusSubmitted, "",
		))
		now, err := model.DatabaseUnixTimestamp(db)
		require.NoError(t, err)
		// Task.CreatedAt is a submitter wall clock and is intentionally skewed
		// forward. The recovery horizon is anchored to the operation's DB time.
		require.NoError(t, db.Model(&model.Task{}).Where("id = ?", task.ID).
			Update("created_at", now+30*24*60*60).Error)
		require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
			Updates(map[string]any{"created_at": now - 2*24*60*60, "next_attempt_at": 0}).Error)

		require.NoError(t, reconcileJimengTaskOperationsAt(now, 1))
		assert.Zero(t, fetches.Load(), "an expired automatic-poll horizon must not issue another fetch")
		require.NoError(t, db.First(&task, task.ID).Error)
		assert.Equal(t, model.TaskStatusUnknown, task.Status)
		assert.Equal(t, jimengPollingReviewReason, task.FailReason)
		var operation model.JimengTaskOperation
		require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		assert.Equal(t, model.JimengTaskOperationManualReview, operation.State)
		require.NoError(t, db.First(&user, user.Id).Error)
		assert.Equal(t, 10, user.UsedQuota, "settled work remains charged at the review boundary")
	})

	t.Run("oversized fetch becomes bounded terminal failure", func(t *testing.T) {
		db, user, token, channel := setupJimengOperationDB(t)
		var fetches atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			fetches.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"code":10000,"data":{"status":"done"},"message":"` +
				strings.Repeat("x", jimeng.MaxDurableResponseBytes) + `"}`))
		}))
		defer upstream.Close()
		task, privateData := newAtomicJimengTask(user, channel)
		task.Properties = `{"upstream_model_name":"jimeng-large","origin_model_name":"jimeng-large"}`
		privateData.ChannelBaseURL = upstream.URL
		privateData.EncryptedChannelKey, _ = common.EncryptByAES(channel.Key)
		reservation, err := createJimengReservedTask(&task, &token, &privateData)
		require.NoError(t, err)
		privateJSON, err := marshalJimengTaskPrivateData(privateData)
		require.NoError(t, err)
		require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, task.Properties, privateJSON))
		privateData.UpstreamTaskID = "provider-large-result"
		require.NoError(t, persistJimengPendingOutcome(
			&task, privateData, []byte(`{"accepted":true}`), model.TaskStatusSubmitted, "",
		))
		require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
			Update("next_attempt_at", 0).Error)
		now, err := model.DatabaseUnixTimestamp(db)
		require.NoError(t, err)

		require.NoError(t, reconcileJimengTaskOperationsAt(now, 1))
		assert.Equal(t, int32(1), fetches.Load())
		require.NoError(t, db.First(&task, task.ID).Error)
		assert.Equal(t, model.TaskStatusFailure, task.Status)
		assert.Equal(t, "provider result exceeds durable storage limit", task.FailReason)
		assert.Less(t, len(task.Data), jimengTaskTextMaxBytes)
		var operation model.JimengTaskOperation
		require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		assert.Equal(t, model.JimengTaskOperationTerminal, operation.State)
		require.NoError(t, db.First(&user, user.Id).Error)
		assert.Equal(t, 10, user.UsedQuota)
	})

	t.Run("oversized server error remains retryable", func(t *testing.T) {
		db, user, token, channel := setupJimengOperationDB(t)
		var fetches atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			fetches.Add(1)
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(strings.Repeat("x", jimeng.MaxDurableResponseBytes+1)))
		}))
		defer upstream.Close()
		task, privateData := newAtomicJimengTask(user, channel)
		task.Properties = `{"upstream_model_name":"jimeng-large-503","origin_model_name":"jimeng-large-503"}`
		privateData.ChannelBaseURL = upstream.URL
		privateData.EncryptedChannelKey, _ = jimengEncrypt(channel.Key)
		reservation, err := createJimengReservedTask(&task, &token, &privateData)
		require.NoError(t, err)
		privateJSON, err := marshalJimengTaskPrivateData(privateData)
		require.NoError(t, err)
		require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, task.Properties, privateJSON))
		privateData.UpstreamTaskID = "provider-large-error"
		require.NoError(t, persistJimengPendingOutcome(
			&task, privateData, []byte(`{"accepted":true}`), model.TaskStatusSubmitted, "",
		))
		require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
			Update("next_attempt_at", 0).Error)
		now, err := model.DatabaseUnixTimestamp(db)
		require.NoError(t, err)

		require.ErrorContains(t, reconcileJimengTaskOperationsAt(now, 1),
			"Jimeng recovery operation failed")
		assert.Equal(t, int32(1), fetches.Load())
		require.NoError(t, db.First(&task, task.ID).Error)
		assert.Equal(t, model.TaskStatusSubmitted, task.Status)
		var operation model.JimengTaskOperation
		require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		assert.Equal(t, model.JimengTaskOperationSubmitted, operation.State)
		assert.Equal(t, 1, operation.Attempts)
		assert.Empty(t, operation.LeaseOwner)
		require.NoError(t, db.First(&user, user.Id).Error)
		assert.Equal(t, 10, user.UsedQuota, "retryable polling errors never refund accepted work")
	})
}

func TestJimengRecoveryPassDoesNotClaimWithoutFullFetchBudget(t *testing.T) {
	t.Setenv("JIMENG_RECOVERY_PASS_SECONDS", "1")
	assert.Equal(t, time.Duration(jimengDefaultPassSeconds)*time.Second, jimengRecoveryPassDuration(),
		"an undersized configured pass must fall back rather than starving provider work")
	db, user, token, channel := setupJimengOperationDB(t)
	task, privateData := newAtomicJimengTask(user, channel)
	privateData.UpstreamTaskID = "provider-budget-id"
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))
	require.NoError(t, settleJimengAcceptedTask(
		&task, reservation, privateJSON, nil, model.TaskStatusSubmitted, "",
		model.JimengTaskOperationDispatching, "",
	))
	require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
		Updates(map[string]any{"next_attempt_at": 0, "attempts": 0}).Error)
	preparedTask, preparedPrivate := newAtomicJimengTask(user, channel)
	_, err = createJimengReservedTask(&preparedTask, &token, &preparedPrivate)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", preparedTask.TaskID).
		Update("next_attempt_at", 0).Error)
	now, err := model.DatabaseUnixTimestamp(db)
	require.NoError(t, err)
	passContext, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	require.NoError(t, reconcileJimengTaskOperationsWithClock(
		passContext, 2, func() (int64, error) { return now, nil },
	))
	var operation model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Zero(t, operation.Attempts,
		"a row skipped for scheduler budget must not consume a provider-failure attempt")
	assert.Empty(t, operation.LeaseOwner)
	assert.Equal(t, model.JimengTaskOperationSubmitted, operation.State)
	var preparedOperation model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", preparedTask.TaskID).First(&preparedOperation).Error)
	assert.Equal(t, model.JimengTaskOperationRefunded, preparedOperation.State,
		"a budget-skipped submitted row must not starve a later safe prepared refund")
}

func TestJimengParentCancellationReleasesClaimWithoutProviderAttempt(t *testing.T) {
	t.Setenv("SSRF_DISABLE", "true")
	t.Cleanup(common.InitSSRF)
	common.InitSSRF()
	db, user, token, channel := setupJimengOperationDB(t)
	requestStarted := make(chan struct{})
	serverRelease := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-serverRelease
	}))
	defer upstream.Close()
	defer close(serverRelease)
	task, privateData := newAtomicJimengTask(user, channel)
	task.Properties = `{"upstream_model_name":"jimeng-cancel","origin_model_name":"jimeng-cancel"}`
	privateData.ChannelBaseURL = upstream.URL
	privateData.EncryptedChannelKey, _ = jimengEncrypt(channel.Key)
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, task.Properties, privateJSON))
	privateData.UpstreamTaskID = "provider-cancel-id"
	require.NoError(t, persistJimengPendingOutcome(
		&task, privateData, []byte(`{"accepted":true}`), model.TaskStatusSubmitted, "",
	))
	require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
		Updates(map[string]any{"next_attempt_at": 0, "attempts": 0}).Error)
	now, err := model.DatabaseUnixTimestamp(db)
	require.NoError(t, err)

	parentContext, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- reconcileJimengTaskOperationsAtContext(parentContext, now, 1)
	}()
	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Jimeng recovery fetch did not start")
	}
	cancel()
	select {
	case reconcileErr := <-done:
		require.NoError(t, reconcileErr)
	case <-time.After(5 * time.Second):
		t.Fatal("Jimeng recovery did not release after parent cancellation")
	}

	var operation model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationSubmitted, operation.State)
	assert.Zero(t, operation.Attempts)
	assert.Empty(t, operation.LeaseOwner)
	assert.Zero(t, operation.LeaseExpiresAt)
	require.NoError(t, db.First(&task, task.ID).Error)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
}

func TestJimengPromotionFailureCannotAgeDeleteAuthoritativeJournal(t *testing.T) {
	recoveryDirectory := t.TempDir()
	t.Setenv("JIMENG_RECOVERY_DIR", recoveryDirectory)
	t.Setenv("JIMENG_RECOVERY_RETENTION_DAYS", "1")
	db, user, token, channel := setupJimengOperationDB(t)
	task, privateData := newAtomicJimengTask(user, channel)
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))
	require.NoError(t, db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status": model.TaskStatusUnknown, "fail_reason": jimengManualReviewReason,
	}).Error)
	require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
		Updates(map[string]any{"state": model.JimengTaskOperationManualReview, "next_attempt_at": 0}).Error)
	require.NoError(t, persistJimengRecovery(jimengRecoveryEnvelope{
		UserID: task.UserId, TaskID: task.TaskID, ChannelID: channel.Id,
		UpstreamTaskID: "provider-retained-after-failure", Status: model.TaskStatusSubmitted,
	}))
	journalPath, err := jimengRecoveryPath(task.TaskID)
	require.NoError(t, err)
	now, err := model.DatabaseUnixTimestamp(db)
	require.NoError(t, err)
	oldTime := time.Unix(now-2*24*60*60, 0)
	require.NoError(t, os.Chtimes(journalPath, oldTime, oldTime))

	const callbackName = "test:jimeng_failed_promotion_retention"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "jimeng_task_operations" {
			tx.AddError(errors.New("injected Jimeng promotion transition failure"))
		}
	}))
	require.Error(t, PromoteJimengTaskRecoveryContext(context.Background()))
	_, err = os.Stat(journalPath)
	assert.NoError(t, err, "cleanup must not erase evidence after a failed promotion")
	require.NoError(t, db.Callback().Update().Remove(callbackName))

	require.NoError(t, PromoteJimengTaskRecoveryContext(context.Background()))
	_, err = os.Stat(journalPath)
	assert.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 10, user.UsedQuota)
}

func TestJimengOperationLeaseContentionStaleTakeoverAndFencing(t *testing.T) {
	db, user, token, channel := setupJimengOperationDB(t)
	task, privateData := newAtomicJimengTask(user, channel)
	privateData.ChannelBaseURL = "https://provider.example.test"
	var err error
	privateData.EncryptedChannelKey, err = jimengEncrypt("worker-refund-secret")
	require.NoError(t, err)
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)

	now := common.NowTimestamp() + jimengOperationRetrySeconds + 1
	var stored model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&stored).Error)
	first, claimed, err := claimJimengTaskOperation(stored.ID, now)
	require.NoError(t, err)
	require.True(t, claimed)
	_, claimed, err = claimJimengTaskOperation(stored.ID, now)
	require.NoError(t, err)
	assert.False(t, claimed, "an unexpired lease must exclude another worker")
	second, claimed, err := claimJimengTaskOperation(stored.ID, now+jimengOperationLeaseSeconds+1)
	require.NoError(t, err)
	require.True(t, claimed, "an expired lease must permit takeover")
	require.NotEqual(t, first.LeaseOwner, second.LeaseOwner)

	err = refundJimengTaskReservation(&task, reservation, "stale worker", first.LeaseOwner)
	assert.ErrorIs(t, err, service.ErrRelayQuotaReservationBusy)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 90, user.Quota, "a fenced stale refund must roll back")
	assert.Equal(t, 90, token.RemainQuota)

	require.NoError(t, refundJimengTaskReservation(&task, reservation, "not dispatched", second.LeaseOwner))
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 100, token.RemainQuota)
	var operation model.JimengTaskOperation
	require.NoError(t, db.First(&operation, stored.ID).Error)
	assert.Equal(t, model.JimengTaskOperationRefunded, operation.State)
	require.NoError(t, db.First(&task, task.ID).Error)
	assert.NotContains(t, task.PrivateData, "encrypted_channel_key")
	assert.NotContains(t, task.PrivateData, "channel_base_url")
}

func TestJimengSafeRefundMarkerHandlesFailureAndPostCommitAmbiguity(t *testing.T) {
	t.Run("storage failure remains a known refund", func(t *testing.T) {
		db, user, token, channel := setupJimengOperationDB(t)
		task, privateData := newAtomicJimengTask(user, channel)
		privateData.ChannelBaseURL = "https://provider.example.test"
		var err error
		privateData.EncryptedChannelKey, err = jimengEncrypt("foreground-refund-secret")
		require.NoError(t, err)
		reservation, err := createJimengReservedTask(&task, &token, &privateData)
		require.NoError(t, err)
		privateJSON, err := marshalJimengTaskPrivateData(privateData)
		require.NoError(t, err)
		require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))

		err = markJimengTaskSafeToRefundWithTransaction(&task, reservation,
			func(func(*gorm.DB) error) error { return errors.New("injected safe-marker storage failure") })
		require.ErrorContains(t, err, "injected safe-marker storage failure")
		require.NoError(t, refundJimengTaskReservation(&task, reservation, "provider rejected", ""),
			"a marker failure must still take the direct atomic refund path")

		require.NoError(t, db.First(&user, user.Id).Error)
		require.NoError(t, db.First(&token, token.Id).Error)
		assert.Equal(t, 100, user.Quota)
		assert.Zero(t, user.UsedQuota)
		assert.Equal(t, 100, token.RemainQuota)
		assert.Zero(t, token.UsedQuota)
		var operation model.JimengTaskOperation
		require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		assert.Equal(t, model.JimengTaskOperationRefunded, operation.State)
		require.NoError(t, db.First(&task, task.ID).Error)
		assert.NotContains(t, task.PrivateData, "encrypted_channel_key")
		assert.NotContains(t, task.PrivateData, "channel_base_url")
	})

	t.Run("post commit error is verified exactly", func(t *testing.T) {
		db, user, token, channel := setupJimengOperationDB(t)
		task, privateData := newAtomicJimengTask(user, channel)
		reservation, err := createJimengReservedTask(&task, &token, &privateData)
		require.NoError(t, err)
		privateJSON, err := marshalJimengTaskPrivateData(privateData)
		require.NoError(t, err)
		require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))

		calls := 0
		err = markJimengTaskSafeToRefundWithTransaction(&task, reservation, func(fn func(*gorm.DB) error) error {
			calls++
			if err := fn(db); err != nil {
				return err
			}
			return errors.New("injected error after commit")
		})
		require.NoError(t, err)
		assert.Equal(t, 1, calls, "exact state verification must not replay an ambiguous committed update")
		var operation model.JimengTaskOperation
		require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		assert.Equal(t, model.JimengTaskOperationPrepared, operation.State)
		assert.False(t, operation.SettlementPending)
	})

	t.Run("double database failure preserves encrypted same-node rejection", func(t *testing.T) {
		sourceRecoveryDirectory := t.TempDir()
		remoteRecoveryDirectory := t.TempDir()
		t.Setenv("JIMENG_RECOVERY_DIR", sourceRecoveryDirectory)
		db, user, token, channel := setupJimengOperationDB(t)
		task, privateData := newAtomicJimengTask(user, channel)
		reservation, err := createJimengReservedTask(&task, &token, &privateData)
		require.NoError(t, err)
		privateJSON, err := marshalJimengTaskPrivateData(privateData)
		require.NoError(t, err)
		require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))

		var failOperationWrites atomic.Bool
		failOperationWrites.Store(true)
		const callbackName = "test:jimeng_rejection_double_db_failure"
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
			if failOperationWrites.Load() && tx.Statement.Table == (model.JimengTaskOperation{}).TableName() {
				tx.AddError(errors.New("injected rejection-state write failure"))
			}
		}))
		t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })
		require.Error(t, markJimengTaskSafeToRefund(&task, reservation))
		require.Error(t, refundJimengTaskReservation(&task, reservation, "provider rejected", ""))
		require.NoError(t, persistJimengRecovery(jimengRecoveryEnvelope{
			UserID: task.UserId, TaskID: task.TaskID, ChannelID: task.ChannelId,
			Status: model.TaskStatusFailure,
		}))
		path, err := jimengRecoveryPath(task.TaskID)
		require.NoError(t, err)
		stored, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.NotContains(t, string(stored), "provider rejected")

		failOperationWrites.Store(false)
		require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
			Update("next_attempt_at", 0).Error)
		reconcileNow, err := model.DatabaseUnixTimestamp(db)
		require.NoError(t, err)
		t.Setenv("JIMENG_RECOVERY_DIR", remoteRecoveryDirectory)
		require.NoError(t, reconcileJimengTaskOperationsAt(reconcileNow, 1))
		var operation model.JimengTaskOperation
		require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		assert.Equal(t, model.JimengTaskOperationManualReview, operation.State)
		require.NoError(t, db.First(&user, user.Id).Error)
		assert.Zero(t, user.UsedQuota)
		_, err = os.Stat(path)
		require.NoError(t, err, "a non-source worker must not destroy unseen rejection evidence")
		// The ordinary two-hour reservation deadline may pass before the source
		// node returns. The generic reconciler must retain the dispatched ledger
		// through the bounded encrypted-journal window so rejection can still win.
		require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
			Where("reservation_id = ?", reservation.ReservationID()).
			Update("expires_at", reconcileNow-1).Error)
		require.NoError(t, service.ReconcileRelayQuotaReservations())
		var ledger model.RelayQuotaReservationRecord
		require.NoError(t, db.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
		assert.Equal(t, model.RelayQuotaReservationStatusDispatched, ledger.Status)

		t.Setenv("JIMENG_RECOVERY_DIR", sourceRecoveryDirectory)
		require.NoError(t, PromoteJimengTaskRecoveryContext(context.Background()))
		require.NoError(t, db.First(&user, user.Id).Error)
		require.NoError(t, db.First(&token, token.Id).Error)
		assert.Equal(t, 100, user.Quota)
		assert.Zero(t, user.UsedQuota)
		assert.Equal(t, 100, token.RemainQuota)
		assert.Zero(t, token.UsedQuota)
		require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		assert.Equal(t, model.JimengTaskOperationRefunded, operation.State)
		_, err = os.Stat(path)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})
}

func TestJimengInteractiveTerminalTransitionIsAtomicAndMonotonic(t *testing.T) {
	db, user, token, channel := setupJimengOperationDB(t)
	task, privateData := newAtomicJimengTask(user, channel)
	privateData.UpstreamTaskID = "provider-id-not-logged"
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))
	require.NoError(t, settleJimengAcceptedTask(
		&task, reservation, privateJSON, nil, model.TaskStatusSubmitted, "",
		model.JimengTaskOperationDispatching, "",
	))
	desired := task
	desired.Status = model.TaskStatusSuccess
	desired.Progress = "100%"
	desired.FinishTime = common.NowTimestamp()
	desired.UpdatedAt = desired.FinishTime
	require.NoError(t, persistJimengTaskResult(&desired, model.TaskStatusSubmitted))
	var storedTask model.Task
	var operation model.JimengTaskOperation
	require.NoError(t, db.First(&storedTask, task.ID).Error)
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskStatusSuccess, storedTask.Status)
	assert.Equal(t, model.JimengTaskOperationTerminal, operation.State)
	assert.Zero(t, operation.NextAttemptAt)

	downgrade := storedTask
	downgrade.Status = model.TaskStatusRunning
	downgrade.Progress = "50%"
	downgrade.UpdatedAt++
	require.Error(t, persistJimengTaskResult(&downgrade, model.TaskStatusSuccess))
	require.NoError(t, db.First(&storedTask, task.ID).Error)
	assert.Equal(t, model.TaskStatusSuccess, storedTask.Status,
		"a terminal task must never be downgraded by a later provider response")
}

func TestJimengTaskCASAcceptsVerifiedMySQLStyleUnchangedRows(t *testing.T) {
	db, user, token, channel := setupJimengOperationDB(t)
	task, privateData := newAtomicJimengTask(user, channel)
	privateData.UpstreamTaskID = "provider-same-second"
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))
	require.NoError(t, settleJimengAcceptedTask(
		&task, reservation, privateJSON, nil, model.TaskStatusSubmitted, "",
		model.JimengTaskOperationDispatching, "",
	))
	var operation model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	now, err := model.DatabaseUnixTimestamp(db)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("id = ?", operation.ID).
		Update("next_attempt_at", now).Error)
	claimed, ok, err := claimJimengTaskOperation(operation.ID, now)
	require.NoError(t, err)
	require.True(t, ok)

	const callbackName = "test:jimeng_mysql_unchanged_rows"
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Task{}).TableName() {
			tx.Statement.RowsAffected = 0
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	// This nonterminal result is byte-for-byte identical to the stored Task.
	// MySQL reports zero changed rows, while the exact read-back proves the CAS.
	same := task
	require.NoError(t, persistJimengTaskResultWithOperation(
		&same, model.TaskStatusSubmitted, claimed,
	))
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationSubmitted, operation.State)
	assert.Empty(t, operation.LeaseOwner)

	// The terminal path has the same portable read-back rule.
	desired := task
	desired.Status = model.TaskStatusSuccess
	desired.Progress = "100%"
	desired.FinishTime = now
	desired.UpdatedAt = now
	require.NoError(t, persistJimengTaskResult(&desired, model.TaskStatusSubmitted))
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationTerminal, operation.State)
}

func TestJimengDispatchCASAcceptsVerifiedMySQLStyleUnchangedRows(t *testing.T) {
	db, user, token, channel := setupJimengOperationDB(t)
	task, privateData := newAtomicJimengTask(user, channel)
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)

	const callbackName = "test:jimeng_dispatch_mysql_unchanged_rows"
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Task{}).TableName() {
			tx.Statement.RowsAffected = 0
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	// Atomic creation already stored these exact values. MySQL can therefore
	// report zero changed rows when creation and dispatch share a DB second.
	require.NoError(t, markJimengTaskDispatching(
		&task, reservation, channel.Id, task.Properties, privateJSON,
	))
	var operation model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationDispatching, operation.State)
	assert.True(t, operation.SettlementPending)
	var ledger model.RelayQuotaReservationRecord
	require.NoError(t, db.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, ledger.Status)
}

func TestJimengRecoveryTerminalizesLegacyTerminalTaskWithoutProviderPoll(t *testing.T) {
	db, user, token, channel := setupJimengOperationDB(t)
	var providerFetches atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		providerFetches.Add(1)
	}))
	defer upstream.Close()
	task, privateData := newAtomicJimengTask(user, channel)
	privateData.UpstreamTaskID = "provider-id-not-polled"
	privateData.ChannelBaseURL = upstream.URL
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))
	require.NoError(t, settleJimengAcceptedTask(
		&task, reservation, privateJSON, nil, model.TaskStatusSubmitted, "",
		model.JimengTaskOperationDispatching, "",
	))
	require.NoError(t, db.Where("event_id = ?", jimengAuditEventID(reservation.ReservationID())).
		Delete(&model.AuditLogOutbox{}).Error, "simulate a terminal task written by an older node")
	require.NoError(t, db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status": model.TaskStatusSuccess, "progress": "100%", "finish_time": common.NowTimestamp(),
	}).Error)
	require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
		Update("next_attempt_at", 0).Error)

	require.NoError(t, ReconcileJimengTaskOperations())
	assert.Zero(t, providerFetches.Load(), "terminal repair must never poll the provider")
	var operation model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationTerminal, operation.State)
	assert.False(t, operation.SettlementPending)
	assert.Zero(t, operation.NextAttemptAt)
	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", jimengAuditEventID(reservation.ReservationID())).Count(&auditCount).Error)
	assert.Equal(t, int64(1), auditCount, "terminal repair must atomically restore the consume audit")
}

func TestJimengTerminalRepairPreservesOriginalConsumeAudit(t *testing.T) {
	db, user, token, channel := setupJimengOperationDB(t)
	var providerFetches atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		providerFetches.Add(1)
	}))
	defer upstream.Close()
	task, privateData := newAtomicJimengTask(user, channel)
	privateData.UpstreamTaskID = "provider-id-not-polled"
	privateData.ChannelBaseURL = upstream.URL
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))
	require.NoError(t, settleJimengAcceptedTask(
		&task, reservation, privateJSON,
		[]byte(`{"code":10000,"request_id":"submit-request-id"}`),
		model.TaskStatusSubmitted, "", model.JimengTaskOperationDispatching, "",
	))
	var originalAudit model.AuditLogOutbox
	require.NoError(t, db.Where("event_id = ?", jimengAuditEventID(reservation.ReservationID())).
		First(&originalAudit).Error)

	// Model a terminal result cached by an older/mixed node that did not update
	// the operation. The fetch fingerprint is intentionally different from the
	// immutable submit-time consume event.
	terminalData := durableJimengProviderPayload(
		[]byte(`{"code":10000,"request_id":"fetch-request-id","data":{"status":"done"}}`),
	)
	require.NoError(t, db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status": model.TaskStatusSuccess, "progress": "100%", "finish_time": common.NowTimestamp(),
		"data": string(terminalData),
	}).Error)
	require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
		Update("next_attempt_at", 0).Error)

	require.NoError(t, ReconcileJimengTaskOperations())
	assert.Zero(t, providerFetches.Load(), "terminal repair must never poll the provider")
	var operation model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationTerminal, operation.State)
	var preservedAudit model.AuditLogOutbox
	require.NoError(t, db.Where("event_id = ?", originalAudit.EventID).First(&preservedAudit).Error)
	assert.Equal(t, originalAudit.Payload, preservedAudit.Payload,
		"terminal repair must preserve the submit-time immutable consume event")
	require.NoError(t, db.First(&task, task.ID).Error)
	assert.NotContains(t, task.PrivateData, "encrypted_upstream_task_id")
	assert.NotContains(t, task.PrivateData, "encrypted_channel_key")
}

func TestJimengDispatchExtendsLowGenericReservationTTL(t *testing.T) {
	t.Setenv("RELAY_RESERVATION_HOLD_SECONDS", "60")
	db, user, token, channel := setupJimengOperationDB(t)
	task, privateData := newAtomicJimengTask(user, channel)
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	databaseNow, err := model.DatabaseUnixTimestamp(db)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))
	var record model.RelayQuotaReservationRecord
	require.NoError(t, db.Where("reservation_id = ?", reservation.ReservationID()).First(&record).Error)
	assert.GreaterOrEqual(t, record.ExpiresAt, databaseNow+jimengReservationSafetySeconds,
		"generic expiry must not preempt Jimeng's dispatch-recovery grace")

	// Even an already-expired row (for example from an older writer or a
	// process-clock-skewed node) remains owned by the active Jimeng operation.
	// The generic job must not move it to manual review before Jimeng recovery.
	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
		Where("id = ?", record.ID).Update("expires_at", databaseNow-1).Error)
	require.NoError(t, service.ReconcileRelayQuotaReservations())
	require.NoError(t, db.First(&record, record.ID).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, record.Status)

	// The protection is bounded. Once a quarantined operation is older than
	// the encrypted journal retention window, generic manual review takes over
	// and the holds remain visible to the root accounting workflow.
	require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
		Updates(map[string]any{
			"state":        model.JimengTaskOperationManualReview,
			"completed_at": databaseNow - 8*24*60*60,
		}).Error)
	require.NoError(t, service.ReconcileRelayQuotaReservations())
	require.NoError(t, db.First(&record, record.ID).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusManualReview, record.Status,
		"expired journal protection must not retain holds forever")
}

func TestJimengZeroPriceSubscriptionPendingSettlementAcceptsOneUnitHold(t *testing.T) {
	db, user, token, channel := setupJimengOperationDB(t)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", user.Id).
		Update("setting", `{"billing_preference":"subscription_only"}`).Error)
	plan := model.SubscriptionPlan{Title: "Jimeng zero price", PriceAmount: "0", Enabled: true}
	require.NoError(t, db.Create(&plan).Error)
	subscription := model.UserSubscription{
		UserId: user.Id, PlanId: plan.Id, AmountTotal: 100,
		EndTime: common.NowTimestamp() + 3600, Status: service.SubscriptionStatusActive,
	}
	require.NoError(t, db.Create(&subscription).Error)
	task, privateData := newAtomicJimengTask(user, channel)
	task.Quota = 0
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	assert.Equal(t, service.BillingSourceSubscription, privateData.BillingSource)
	assert.Equal(t, 1, privateData.FundingReserved)
	assert.Equal(t, subscription.UsageEpoch, privateData.FundingUsageEpoch)
	assert.NotEmpty(t, privateData.FundingRequestID)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))
	privateData.UpstreamTaskID = "zero-price-provider-task"
	require.NoError(t, persistJimengPendingOutcome(
		&task, privateData, nil, model.TaskStatusSubmitted, "",
	))
	privateData, err = decodeJimengTaskPrivateData(task.PrivateData)
	require.NoError(t, err)
	require.NoError(t, settlePendingJimengTask(&task, &privateData))

	require.NoError(t, db.First(&subscription, subscription.Id).Error)
	assert.Zero(t, subscription.AmountUsed, "settling actual zero releases the one-unit subscription hold")
	var record model.RelayQuotaReservationRecord
	require.NoError(t, db.Where("reservation_id = ?", reservation.ReservationID()).First(&record).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	assert.Zero(t, record.ActualQuota)
}

func TestJimengSettlementAndAuditOutboxShareOneCommitAndReplayOnce(t *testing.T) {
	db, user, token, channel := setupJimengOperationDB(t)
	task, privateData := newAtomicJimengTask(user, channel)
	task.Properties = `{"upstream_model_name":"jimeng-audit","origin_model_name":"jimeng-audit"}`
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	dispatchPrivate, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(
		&task, reservation, channel.Id, task.Properties, dispatchPrivate,
	))
	privateData.UpstreamTaskID = "provider-id-must-not-enter-audit"
	acceptedPrivate, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)

	const callbackName = "test:jimeng_audit_atomicity"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AuditLogOutbox{}).TableName() {
			tx.AddError(errors.New("injected audit outbox failure"))
		}
	}))
	err = settleJimengAcceptedTask(
		&task, reservation, acceptedPrivate,
		[]byte(`{"code":10000,"request_id":"request-fingerprint","data":{"task_id":"provider-id-must-not-enter-audit"}}`),
		model.TaskStatusSubmitted, "", model.JimengTaskOperationDispatching, "",
	)
	require.ErrorContains(t, err, "injected audit outbox failure")
	require.NoError(t, db.Callback().Create().Remove(callbackName))

	var record model.RelayQuotaReservationRecord
	require.NoError(t, db.Where("reservation_id = ?", reservation.ReservationID()).First(&record).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, record.Status)
	var storedTask model.Task
	require.NoError(t, db.First(&storedTask, task.ID).Error)
	assert.Equal(t, model.TaskStatusNotStart, storedTask.Status)
	var outboxCount int64
	require.NoError(t, db.Model(&model.AuditLogOutbox{}).Count(&outboxCount).Error)
	assert.Zero(t, outboxCount)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Zero(t, user.UsedQuota, "audit failure must roll back accounting")

	require.NoError(t, settleJimengAcceptedTask(
		&task, reservation, acceptedPrivate,
		[]byte(`{"code":10000,"request_id":"request-fingerprint","data":{"task_id":"provider-id-must-not-enter-audit"}}`),
		model.TaskStatusSubmitted, "", model.JimengTaskOperationDispatching, "",
	))
	// Ambiguous/replayed settlement observes the same immutable event.
	require.NoError(t, settleJimengAcceptedTask(
		&task, reservation, acceptedPrivate,
		[]byte(`{"code":10000,"request_id":"request-fingerprint","data":{"task_id":"provider-id-must-not-enter-audit"}}`),
		model.TaskStatusSubmitted, "", model.JimengTaskOperationDispatching, "",
	))
	require.NoError(t, db.Model(&model.AuditLogOutbox{}).Count(&outboxCount).Error)
	assert.Equal(t, int64(1), outboxCount)
	var outbox model.AuditLogOutbox
	require.NoError(t, db.First(&outbox).Error)
	assert.Equal(t, jimengAuditEventID(reservation.ReservationID()), outbox.EventID)
	assert.NotContains(t, outbox.Payload, privateData.UpstreamTaskID)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
}

func TestJimengForegroundAndWorkerTransitionsAreFencedByStateAndOwner(t *testing.T) {
	t.Run("claimed prepared operation blocks dispatch", func(t *testing.T) {
		db, _, token, channel := setupJimengOperationDB(t)
		task, privateData := newAtomicJimengTask(model.User{Id: token.UserId}, channel)
		reservation, err := createJimengReservedTask(&task, &token, &privateData)
		require.NoError(t, err)
		var operation model.JimengTaskOperation
		require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		now, err := model.DatabaseUnixTimestamp(db)
		require.NoError(t, err)
		claimed, ok, err := claimJimengTaskOperation(operation.ID, now+jimengOperationRetrySeconds+1)
		require.NoError(t, err)
		require.True(t, ok)
		privateJSON, err := marshalJimengTaskPrivateData(privateData)
		require.NoError(t, err)
		require.Error(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))
		require.NoError(t, refundJimengTaskReservation(&task, reservation, "not dispatched", claimed.LeaseOwner))
	})

	t.Run("claimed dispatch operation blocks definite-rejection refund", func(t *testing.T) {
		db, user, token, channel := setupJimengOperationDB(t)
		task, privateData := newAtomicJimengTask(user, channel)
		reservation, err := createJimengReservedTask(&task, &token, &privateData)
		require.NoError(t, err)
		privateJSON, err := marshalJimengTaskPrivateData(privateData)
		require.NoError(t, err)
		require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))
		var operation model.JimengTaskOperation
		require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		now, err := model.DatabaseUnixTimestamp(db)
		require.NoError(t, err)
		claimed, ok, err := claimJimengTaskOperation(operation.ID, now+jimengDispatchRecoverySeconds+1)
		require.NoError(t, err)
		require.True(t, ok)
		require.Error(t, markJimengTaskSafeToRefund(&task, reservation))
		require.ErrorIs(t, refundJimengTaskReservation(&task, reservation, "provider rejected", ""),
			service.ErrRelayQuotaReservationBusy)
		require.NoError(t, settleJimengAcceptedTask(
			&task, reservation, privateJSON, nil, model.TaskStatusUnknown,
			"unknown", model.JimengTaskOperationDispatching, claimed.LeaseOwner,
		))
		require.NoError(t, db.First(&user, user.Id).Error)
		assert.Equal(t, 10, user.UsedQuota)
		assert.Equal(t, 1, user.RequestCount)
	})
}

func TestJimengPermanentAndExhaustedRecoveryMovesToReachableManualReview(t *testing.T) {
	t.Run("corrupt encrypted provider id is permanent", func(t *testing.T) {
		db, user, token, channel := setupJimengOperationDB(t)
		task, privateData := newAtomicJimengTask(user, channel)
		reservation, err := createJimengReservedTask(&task, &token, &privateData)
		require.NoError(t, err)
		privateJSON, err := marshalJimengTaskPrivateData(privateData)
		require.NoError(t, err)
		require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))
		require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
			Updates(map[string]any{
				"state":                      model.JimengTaskOperationSubmitted,
				"encrypted_provider_task_id": "not-a-valid-ciphertext",
				"next_attempt_at":            0,
			}).Error)
		now, err := model.DatabaseUnixTimestamp(db)
		require.NoError(t, err)
		require.Error(t, reconcileJimengTaskOperationsAt(now, 1))
		var operation model.JimengTaskOperation
		require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		assert.Equal(t, model.JimengTaskOperationManualReview, operation.State)
		assert.Zero(t, operation.NextAttemptAt)
	})

	t.Run("client can fence and terminalize paid manual review", func(t *testing.T) {
		db, user, token, channel := setupJimengOperationDB(t)
		task, privateData := newAtomicJimengTask(user, channel)
		privateData.UpstreamTaskID = "manual-provider-id"
		reservation, err := createJimengReservedTask(&task, &token, &privateData)
		require.NoError(t, err)
		privateJSON, err := marshalJimengTaskPrivateData(privateData)
		require.NoError(t, err)
		require.NoError(t, markJimengTaskDispatching(&task, reservation, channel.Id, `{}`, privateJSON))
		require.NoError(t, settleJimengAcceptedTask(
			&task, reservation, privateJSON, nil, model.TaskStatusSubmitted, "",
			model.JimengTaskOperationDispatching, "",
		))
		require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
			Updates(map[string]any{
				"state":      model.JimengTaskOperationManualReview,
				"last_error": "bounded failure",
			}).Error)
		claimed, ok, err := claimJimengTaskOperationForClient(task.TaskID, reservation.ReservationID(),
			[]string{model.JimengTaskOperationManualReview})
		require.NoError(t, err)
		require.True(t, ok)
		desired := task
		desired.Status = model.TaskStatusSuccess
		desired.Progress = "100%"
		desired.FinishTime = common.NowTimestamp()
		desired.UpdatedAt = desired.FinishTime
		require.NoError(t, persistJimengTaskResultWithOperation(&desired, task.Status, claimed))
		require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&claimed).Error)
		assert.Equal(t, model.JimengTaskOperationTerminal, claimed.State)
		assert.Empty(t, claimed.LeaseOwner)
	})
}

func TestModernJimengCredentialSnapshotFailsClosed(t *testing.T) {
	_, user, _, channel := setupJimengOperationDB(t)
	task, privateData := newAtomicJimengTask(user, channel)
	privateData.RelayReservationID = common.BestEffortUUID()
	privateData.ChannelBaseURL = "https://snapshot.example.test"
	privateData.EncryptedChannelKey = "corrupt-ciphertext"
	_, _, err := resolveJimengTaskChannel(task, privateData)
	require.ErrorContains(t, err, "cannot be decrypted")

	privateData.EncryptedChannelKey = ""
	_, _, err = resolveJimengTaskChannel(task, privateData)
	require.ErrorContains(t, err, "incomplete")

	legacyURL, legacyKey, err := resolveJimengTaskChannel(task, jimengTaskPrivateData{})
	require.NoError(t, err)
	assert.Equal(t, jimengChannelBaseURL(&channel), legacyURL)
	assert.Equal(t, channel.Key, legacyKey)
}

func TestJimengMissingRotationKeyRetriesWithoutQuarantine(t *testing.T) {
	t.Setenv("SSRF_DISABLE", "true")
	t.Cleanup(common.InitSSRF)
	common.InitSSRF()
	oldKey := strings.Repeat("old-jimeng-recovery-key-", 2)
	newKey := strings.Repeat("new-jimeng-recovery-key-", 2)
	t.Setenv("JIMENG_ENCRYPTION_KEYS", "old="+oldKey)
	db, user, token, channel := setupJimengOperationDB(t)
	var fetches atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fetches.Add(1)
		_, _ = writer.Write([]byte(`{"code":10000,"data":{"status":"done","video_url":"https://cdn.example.test/rotated.mp4"}}`))
	}))
	defer upstream.Close()
	task, privateData := newAtomicJimengTask(user, channel)
	task.Properties = `{"upstream_model_name":"jimeng-rotation","origin_model_name":"jimeng-rotation"}`
	privateData.ChannelBaseURL = upstream.URL
	privateData.EncryptedChannelKey, _ = jimengEncrypt(channel.Key)
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	dispatchPrivate, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(
		&task, reservation, channel.Id, task.Properties, dispatchPrivate,
	))
	privateData.UpstreamTaskID = "provider-rotation-id"
	acceptedPrivate, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, settleJimengAcceptedTask(
		&task, reservation, acceptedPrivate, nil, model.TaskStatusSubmitted, "",
		model.JimengTaskOperationDispatching, "",
	))
	require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
		Updates(map[string]any{"next_attempt_at": 0, "attempts": 0}).Error)
	now, err := model.DatabaseUnixTimestamp(db)
	require.NoError(t, err)

	// A temporarily stale node cannot decrypt the old id, but it must release
	// the lease without consuming retry budget or making ManualReview terminal.
	t.Setenv("JIMENG_ENCRYPTION_KEYS", "new="+newKey)
	require.NoError(t, reconcileJimengTaskOperationsAt(now, 1))
	assert.Zero(t, fetches.Load())
	var operation model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationSubmitted, operation.State)
	assert.Zero(t, operation.Attempts)
	assert.Empty(t, operation.LeaseOwner)

	// Once the old key is restored cluster-wide, normal autonomous polling
	// resumes without an operator requeue or client fetch.
	t.Setenv("JIMENG_ENCRYPTION_KEYS", "new="+newKey+",old="+oldKey)
	require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("id = ?", operation.ID).
		Update("next_attempt_at", 0).Error)
	now, err = model.DatabaseUnixTimestamp(db)
	require.NoError(t, err)
	require.NoError(t, reconcileJimengTaskOperationsAt(now, 1))
	assert.Equal(t, int32(1), fetches.Load())
	require.NoError(t, db.First(&task, task.ID).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationTerminal, operation.State)
	assert.NotContains(t, task.PrivateData, "encrypted_upstream_task_id")
	assert.NotContains(t, task.PrivateData, "encrypted_channel_key")
	assert.NotContains(t, task.PrivateData, "channel_base_url")

	// Terminal history no longer depends on the retired key. Dropping it after
	// the active recovery window still leaves the safe stored result readable.
	t.Setenv("JIMENG_ENCRYPTION_KEYS", "new="+newKey)
	_, terminalPrivate, err := decodeJimengTerminalTaskMetadata(task)
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example.test/rotated.mp4", terminalPrivate.ResultURL)
	terminalResponse := newJimengVideoResponse(task, "")
	require.NotNil(t, terminalResponse.Metadata)
	assert.Equal(t, "https://cdn.example.test/rotated.mp4", terminalResponse.Metadata["url"])
}

func TestJimengWorkerImportsEncryptedEmergencyJournalBeforeUnknown(t *testing.T) {
	t.Setenv("SSRF_DISABLE", "true")
	sourceRecoveryDirectory := t.TempDir()
	remoteRecoveryDirectory := t.TempDir()
	t.Setenv("JIMENG_RECOVERY_DIR", sourceRecoveryDirectory)
	t.Cleanup(common.InitSSRF)
	common.InitSSRF()
	db, user, token, channel := setupJimengOperationDB(t)
	var fetches atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fetches.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":10000,"data":{"status":"done","video_url":"https://cdn.example.test/emergency.mp4"}}`))
	}))
	defer upstream.Close()
	task, privateData := newAtomicJimengTask(user, channel)
	task.Properties = `{"upstream_model_name":"jimeng-emergency","origin_model_name":"jimeng-emergency"}`
	privateData.ChannelBaseURL = upstream.URL
	privateData.EncryptedChannelKey, _ = common.EncryptByAES(channel.Key)
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(
		&task, reservation, channel.Id, task.Properties, privateJSON,
	))
	require.NoError(t, persistJimengRecovery(jimengRecoveryEnvelope{
		UserID: task.UserId, TaskID: task.TaskID, ChannelID: channel.Id,
		UpstreamTaskID: "provider-emergency-id", Status: model.TaskStatusSubmitted,
	}))
	require.NoError(t, db.Model(&model.JimengTaskOperation{}).Where("task_id = ?", task.TaskID).
		Update("next_attempt_at", 0).Error)

	now, err := model.DatabaseUnixTimestamp(db)
	require.NoError(t, err)
	// A different node cannot see the source node's journal. It must retain the
	// holds for review instead of charging UNKNOWN and destroying recoverability.
	t.Setenv("JIMENG_RECOVERY_DIR", remoteRecoveryDirectory)
	require.NoError(t, reconcileJimengTaskOperationsAt(now, 1))
	assert.Zero(t, fetches.Load())
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Zero(t, user.UsedQuota)
	var operation model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationManualReview, operation.State)
	sourceJournal := filepath.Join(sourceRecoveryDirectory, task.TaskID+".json")
	_, err = os.Stat(sourceJournal)
	require.NoError(t, err, "a remote node cannot delete an unseen source-node journal")
	// Crossing the normal reservation horizon must not close the accounting
	// transition while the quarantined operation is still within the bounded
	// encrypted-journal recovery window.
	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ?", reservation.ReservationID()).
		Update("expires_at", now-1).Error)
	require.NoError(t, service.ReconcileRelayQuotaReservations())
	var ledger model.RelayQuotaReservationRecord
	require.NoError(t, db.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, ledger.Status)

	// When the source filesystem returns, its pre-reconcile promotion commits
	// the encrypted accepted id to the primary database before any provider poll.
	t.Setenv("JIMENG_RECOVERY_DIR", sourceRecoveryDirectory)
	require.NoError(t, ReconcileJimengTaskOperations())
	assert.Equal(t, int32(1), fetches.Load())
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	require.NoError(t, db.First(&task, task.ID).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationTerminal, operation.State)
	_, err = loadJimengRecovery(task.TaskID)
	assert.True(t, jimengRecoveryNotFound(err))
}

func TestJimengRecoveryPromotionCursorPreventsBadPrefixStarvation(t *testing.T) {
	recoveryDirectory := t.TempDir()
	require.NoError(t, os.Chmod(recoveryDirectory, 0o700))
	t.Setenv("JIMENG_RECOVERY_DIR", recoveryDirectory)
	jimengRecoveryPromotionCursor.Store(0)
	db, user, token, channel := setupJimengOperationDB(t)
	task, privateData := newAtomicJimengTask(user, channel)
	task.Properties = `{"upstream_model_name":"jimeng-promotion","origin_model_name":"jimeng-promotion"}`
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(
		&task, reservation, channel.Id, task.Properties, privateJSON,
	))

	baseTime := time.Unix(common.NowTimestamp()-2*24*60*60, 0)
	for index := 0; index < jimengRecoveryCleanupLimit; index++ {
		path := filepath.Join(recoveryDirectory, fmt.Sprintf("task_aaa_bad_%03d.json", index))
		require.NoError(t, os.WriteFile(path, []byte(`{"invalid":true}`), 0o600))
		require.NoError(t, os.Chtimes(path, baseTime, baseTime))
	}
	require.NoError(t, persistJimengRecovery(jimengRecoveryEnvelope{
		UserID: task.UserId, TaskID: task.TaskID, ChannelID: task.ChannelId,
		UpstreamTaskID: "provider-promoted-after-window", Status: model.TaskStatusSubmitted,
	}))
	targetPath, err := jimengRecoveryPath(task.TaskID)
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(targetPath, baseTime.Add(time.Hour), baseTime.Add(time.Hour)))

	now, err := model.DatabaseUnixTimestamp(db)
	require.NoError(t, err)
	require.Error(t, promoteJimengRecoveryRecords(now, jimengRecoveryCleanupLimit))
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Zero(t, user.UsedQuota)

	// The next bounded pass starts after the prior window, so the valid 101st
	// record is promoted even though the first 100 remain unreadable.
	require.Error(t, promoteJimengRecoveryRecords(now, jimengRecoveryCleanupLimit))
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 10, user.UsedQuota)
	var operation model.JimengTaskOperation
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationSubmitted, operation.State)
	_, err = os.Stat(targetPath)
	assert.ErrorIs(t, err, os.ErrNotExist)
}
