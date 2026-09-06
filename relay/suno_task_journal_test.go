package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/suno"
)

func TestSunoRecoveryJournalEncryptsAndBindsAcceptedIdentity(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("SUNO_TASK_RECOVERY_DIR", directory)
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "suno-journal=0123456789abcdef0123456789abcdef")
	task := &model.Task{
		TaskID: "task_" + strings.Repeat("a", 32), Platform: sunoTaskPlatform,
		UserId: 11, ChannelId: 22,
	}
	record := sunoRecoveryJournalRecord{
		Family: "suno", Platform: sunoTaskPlatform, Outcome: sunoRecoveryOutcomeAccepted,
		TaskID:        task.TaskID,
		ReservationID: "reservation-0123456789", UserID: task.UserId, ChannelID: task.ChannelId,
		Action: suno.ActionMusic, ProviderTaskID: "provider-accepted-0123456789",
	}
	require.NoError(t, persistSunoRecoveryJournal(record))
	path, err := sunoRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)
	stored, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(stored), record.ReservationID)
	assert.NotContains(t, string(stored), record.ProviderTaskID)
	assert.Contains(t, string(stored), `"version":1`)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	directoryInfo, err := os.Stat(directory)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), directoryInfo.Mode().Perm())

	loaded, err := loadSunoRecoveryJournal(task.TaskID)
	require.NoError(t, err)
	assert.Equal(t, record, loaded)

	otherID := "task_" + strings.Repeat("b", 32)
	otherPath, err := sunoRecoveryJournalPath(otherID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(otherPath, stored, 0o600))
	_, err = loadSunoRecoveryJournal(otherID)
	assert.Error(t, err, "journal ciphertext must be bound to its public task filename")
	assert.NotContains(t, err.Error(), record.ProviderTaskID)

	require.NoError(t, removeSunoRecoveryJournal(task.TaskID))
	require.NoError(t, removeSunoRecoveryJournal(task.TaskID), "cleanup must be replay-safe")
	t.Setenv("SUNO_TASK_RECOVERY_DIR", string(os.PathSeparator))
	_, err = sunoRecoveryJournalPath(task.TaskID)
	assert.Error(t, err, "a broad filesystem root must never be a journal target")
}

func TestSunoRecoveryJournalSurvivesDatabaseOutageAndPromotes(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/suno/fetch" {
			_, _ = writer.Write([]byte(`{"code":"success","message":"","data":[]}`))
			return
		}
		http.NotFound(writer, request)
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	task, reservation := createPreparedSunoTaskForRecovery(t, fixture, suno.ActionMusic)
	require.NoError(t, markSunoTaskDispatching(task, reservation))
	providerID := "provider-journal-outage"
	require.NoError(t, persistAcceptedSunoRecoveryJournal(
		task, reservation.ReservationID(), providerID, suno.ActionMusic,
	))
	path, err := sunoRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)

	database := model.DB
	model.DB = nil
	promotionErr := PromoteSunoTaskRecoveryJournalsContext(context.Background())
	model.DB = database
	require.Error(t, promotionErr)
	assert.NotContains(t, promotionErr.Error(), providerID)
	_, err = os.Stat(path)
	assert.NoError(t, err, "database outage must retain accepted-provider evidence")

	require.NoError(t, PromoteSunoTaskRecoveryJournalsContext(context.Background()))
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.True(t, operation.SettlementPending)
	storedProviderID, err := asyncTaskDecryptBound(
		operation.EncryptedProviderTaskID,
		sunoProviderTaskBinding(task.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID),
	)
	require.NoError(t, err)
	assert.Equal(t, providerID, storedProviderID)

	forceSunoTaskRecoveryDue(t, fixture.db, task.TaskID)
	require.NoError(t, reconcileAsyncSunoTasks(context.Background()))
	var ledger model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, ledger.Status)
}

func TestSunoRecoveryJournalRecoversConservativelySettledUnknownWithoutDoubleCharge(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/suno/fetch" {
			_, _ = writer.Write([]byte(`{"code":"success","message":"","data":[]}`))
			return
		}
		http.NotFound(writer, request)
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	task, reservation := createPreparedSunoTaskForRecovery(t, fixture, suno.ActionLyrics)
	require.NoError(t, markSunoTaskDispatching(task, reservation))
	record := sunoRecoveryJournalRecord{
		Family: "suno", Platform: sunoTaskPlatform, Outcome: sunoRecoveryOutcomeAccepted,
		TaskID:        task.TaskID,
		ReservationID: reservation.ReservationID(), UserID: task.UserId, ChannelID: task.ChannelId,
		Action: suno.ActionLyrics, ProviderTaskID: "provider-accepted-before-unknown",
	}
	require.NoError(t, persistSunoRecoveryJournal(record))
	require.NoError(t, settleUnknownSunoDispatch(task, reservation, sunoUnknownDispatchReason, ""))

	var beforeUser model.User
	var beforeToken model.Token
	var beforeChannel model.Channel
	require.NoError(t, fixture.db.First(&beforeUser, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&beforeToken, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&beforeChannel, fixture.channel.Id).Error)

	require.NoError(t, PromoteSunoTaskRecoveryJournalsContext(context.Background()))
	forceSunoTaskRecoveryDue(t, fixture.db, task.TaskID)
	require.NoError(t, reconcileAsyncSunoTasks(context.Background()))

	var afterUser model.User
	var afterToken model.Token
	var afterChannel model.Channel
	var durableTask model.Task
	var operation model.TaskOperation
	require.NoError(t, fixture.db.First(&afterUser, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&afterToken, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&afterChannel, fixture.channel.Id).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&durableTask).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, beforeUser.Quota, afterUser.Quota)
	assert.Equal(t, beforeUser.UsedQuota, afterUser.UsedQuota)
	assert.Equal(t, beforeToken.RemainQuota, afterToken.RemainQuota)
	assert.Equal(t, beforeToken.UsedQuota, afterToken.UsedQuota)
	assert.Equal(t, beforeChannel.UsedQuota, afterChannel.UsedQuota)
	assert.Equal(t, model.TaskStatusSubmitted, durableTask.Status)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.False(t, operation.SettlementPending)
	assert.NotEmpty(t, operation.EncryptedProviderTaskID)
}

func TestSunoRejectedRecoveryJournalRefundsDurably(t *testing.T) {
	for _, test := range []struct {
		name           string
		settleUnknown  bool
		operationState string
		ledgerStatus   string
	}{
		{
			name: "dispatching rejection", operationState: model.TaskOperationRefunded,
			ledgerStatus: model.RelayQuotaReservationStatusRefunded,
		},
		{
			name: "late journal reverses conservative unknown", settleUnknown: true,
			operationState: model.TaskOperationReversed, ledgerStatus: model.RelayQuotaReservationStatusReversed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("rejected recovery must not contact Suno")
			}))
			defer upstream.Close()
			fixture := newSunoTaskFixture(t, upstream)
			task, reservation := createPreparedSunoTaskForRecovery(t, fixture, suno.ActionMusic)
			require.NoError(t, markSunoTaskDispatching(task, reservation))
			reason := "Suno provider rejected submission"
			require.NoError(t, persistRejectedSunoRecoveryJournal(
				task, reservation.ReservationID(), suno.ActionMusic, reason,
			))
			path, err := sunoRecoveryJournalPath(task.TaskID)
			require.NoError(t, err)
			stored, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.NotContains(t, string(stored), reservation.ReservationID())
			assert.NotContains(t, string(stored), reason)
			if test.settleUnknown {
				require.NoError(t, settleUnknownSunoDispatch(task, reservation, sunoUnknownDispatchReason, ""))
			} else {
				database := model.DB
				model.DB = nil
				promotionErr := PromoteSunoTaskRecoveryJournalsContext(context.Background())
				model.DB = database
				require.Error(t, promotionErr)
				_, err = os.Stat(path)
				require.NoError(t, err, "database outage must retain definitive rejection evidence")
			}

			require.NoError(t, PromoteSunoTaskRecoveryJournalsContext(context.Background()))
			_, err = os.Stat(path)
			assert.ErrorIs(t, err, os.ErrNotExist)
			var durableTask model.Task
			var operation model.TaskOperation
			var ledger model.RelayQuotaReservationRecord
			var user model.User
			var token model.Token
			require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&durableTask).Error)
			require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
			require.NoError(t, fixture.db.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
			require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
			require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
			assert.Equal(t, model.TaskStatusFailure, durableTask.Status)
			assert.Zero(t, durableTask.Quota)
			assert.Equal(t, reason, durableTask.FailReason)
			assert.Equal(t, test.operationState, operation.State)
			assert.Equal(t, test.ledgerStatus, ledger.Status)
			assert.Equal(t, 2_000_000, user.Quota)
			assert.Equal(t, 2_000_000, token.RemainQuota)
		})
	}
}
