package relay

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

func TestVideoRecoveryJournalEncryptsAndBindsProviderIdentity(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("VIDEO_TASK_RECOVERY_DIR", directory)
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "journal=0123456789abcdef0123456789abcdef")
	task := videoRecoveryJournalTestTask("task_" + strings.Repeat("a", 32))
	providerTaskID := "provider/plaintext/MUST_NOT_APPEAR"
	reservationID := "reservation-0123456789abcdef"

	require.NoError(t, persistAcceptedVideoRecoveryJournal(&task, reservationID, providerTaskID))
	require.NoError(t, persistAcceptedVideoRecoveryJournal(&task, reservationID, providerTaskID),
		"atomic replacement with the same accepted identity must remain readable")
	path, err := videoRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)
	stored, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(stored), providerTaskID)
	assert.NotContains(t, string(stored), reservationID)
	assert.Contains(t, string(stored), `"version":1`)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	directoryInfo, err := os.Stat(directory)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), directoryInfo.Mode().Perm())

	record, err := loadVideoRecoveryJournal(task.TaskID)
	require.NoError(t, err)
	assert.Equal(t, task.TaskID, record.TaskID)
	assert.Equal(t, reservationID, record.ReservationID)
	assert.Equal(t, task.UserId, record.UserID)
	assert.Equal(t, task.ChannelId, record.ChannelID)
	assert.Equal(t, providerTaskID, record.ProviderTaskID)

	require.NoError(t, removeVideoRecoveryJournal(task.TaskID))
	require.NoError(t, removeVideoRecoveryJournal(task.TaskID), "journal removal must be replay-safe")
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)

	openAICompatible := videoRecoveryJournalTestTask("task_" + strings.Repeat("c", 32))
	openAICompatible.Platform = videoTaskOpenAIPlatform
	require.NoError(t, persistAcceptedVideoRecoveryJournal(
		&openAICompatible, "reservation-openai-compatible", "provider/openai-compatible",
	))
	require.NoError(t, removeVideoRecoveryJournal(openAICompatible.TaskID))
}

func TestVideoRecoveryJournalRejectsFilenameSwap(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("VIDEO_TASK_RECOVERY_DIR", directory)
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "journal=0123456789abcdef0123456789abcdef")
	first := videoRecoveryJournalTestTask("task_" + strings.Repeat("a", 32))
	second := videoRecoveryJournalTestTask("task_" + strings.Repeat("b", 32))
	require.NoError(t, persistAcceptedVideoRecoveryJournal(
		&first, "reservation-first", "provider/first/MUST_NOT_APPEAR",
	))
	require.NoError(t, persistAcceptedVideoRecoveryJournal(
		&second, "reservation-second", "provider/second/MUST_NOT_APPEAR",
	))
	firstPath, err := videoRecoveryJournalPath(first.TaskID)
	require.NoError(t, err)
	secondPath, err := videoRecoveryJournalPath(second.TaskID)
	require.NoError(t, err)
	firstCiphertext, err := os.ReadFile(firstPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(secondPath, firstCiphertext, 0o600))

	_, err = loadVideoRecoveryJournal(second.TaskID)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "provider/first/MUST_NOT_APPEAR")
	record, err := loadVideoRecoveryJournal(first.TaskID)
	require.NoError(t, err)
	assert.Equal(t, "provider/first/MUST_NOT_APPEAR", record.ProviderTaskID)
}

func TestVideoRecoveryJournalSurvivesDatabaseOutageAndPromotes(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("VIDEO_TASK_RECOVERY_DIR", directory)
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	require.NoError(t, markVideoTaskDispatching(task, reservation))
	providerTaskID := "provider-journal-outage"
	require.NoError(t, persistAcceptedVideoRecoveryJournal(task, reservation.ReservationID(), providerTaskID))
	path, err := videoRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)
	oldTime := time.Unix(common.NowTimestamp()-30*24*60*60, 0)
	require.NoError(t, os.Chtimes(path, oldTime, oldTime))

	database := model.DB
	model.DB = nil
	promotionErr := PromoteVideoTaskRecoveryJournalsContext(context.Background())
	model.DB = database
	require.Error(t, promotionErr)
	assert.NotContains(t, promotionErr.Error(), providerTaskID)
	_, err = os.Stat(path)
	assert.NoError(t, err, "database outage and journal age must not erase accepted-provider evidence")

	require.NoError(t, PromoteVideoTaskRecoveryJournalsContext(context.Background()))
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.True(t, operation.SettlementPending)
	storedProviderID, err := asyncTaskDecryptBound(
		operation.EncryptedProviderTaskID,
		videoProviderTaskBinding(task.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID),
	)
	require.NoError(t, err)
	assert.Equal(t, providerTaskID, storedProviderID)

	require.NoError(t, reconcileAsyncVideoTasks(context.Background()))
	var ledger model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, ledger.Status)
}

func TestVideoRecoveryJournalRecoversSettledUnknownWithoutDoubleCharge(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("VIDEO_TASK_RECOVERY_DIR", directory)
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	record := videoRecoveryJournalRecord{
		TaskID: task.TaskID, ReservationID: reservation.ReservationID(),
		UserID: task.UserId, ChannelID: task.ChannelId,
		ProviderTaskID: "provider-accepted-before-unknown-settlement",
	}
	require.NoError(t, markVideoTaskDispatching(task, reservation))
	require.NoError(t, persistVideoRecoveryJournal(record))
	require.NoError(t, settleUnknownVideoDispatch(
		task, reservation, videoUnknownDispatchReason, model.TaskOperationDispatching, "",
	))

	var beforeLedger model.RelayQuotaReservationRecord
	var beforeUser model.User
	var beforeToken model.Token
	var beforeChannel model.Channel
	var beforeAuditCount int64
	require.NoError(t, fixture.db.Where("reservation_id = ?", record.ReservationID).First(&beforeLedger).Error)
	require.NoError(t, fixture.db.First(&beforeUser, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&beforeToken, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&beforeChannel, fixture.channel.Id).Error)
	require.NoError(t, fixture.db.Model(&model.AuditLogOutbox{}).Count(&beforeAuditCount).Error)
	require.Equal(t, int64(1), beforeAuditCount)
	require.Equal(t, model.RelayQuotaReservationStatusSettled, beforeLedger.Status)

	require.NoError(t, PromoteVideoTaskRecoveryJournalsContext(context.Background()))
	path, err := videoRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)

	var recoveredTask model.Task
	var recoveredOperation model.TaskOperation
	var afterLedger model.RelayQuotaReservationRecord
	var afterUser model.User
	var afterToken model.Token
	var afterChannel model.Channel
	var afterAuditCount int64
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&recoveredTask).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&recoveredOperation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", record.ReservationID).First(&afterLedger).Error)
	require.NoError(t, fixture.db.First(&afterUser, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&afterToken, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&afterChannel, fixture.channel.Id).Error)
	require.NoError(t, fixture.db.Model(&model.AuditLogOutbox{}).Count(&afterAuditCount).Error)

	assert.Equal(t, model.TaskStatusSubmitted, recoveredTask.Status)
	assert.Equal(t, "0%", recoveredTask.Progress)
	assert.Empty(t, recoveredTask.FailReason)
	assert.Zero(t, recoveredTask.FinishTime)
	assert.Equal(t, model.TaskOperationSubmitted, recoveredOperation.State)
	assert.False(t, recoveredOperation.SettlementPending)
	assert.Positive(t, recoveredOperation.NextAttemptAt)
	assert.Zero(t, recoveredOperation.CompletedAt)
	privateData, err := decodeVideoTaskPrivateData(recoveredTask.PrivateData)
	require.NoError(t, err)
	assert.False(t, privateData.SettlementPending)
	binding := videoProviderTaskBinding(
		record.TaskID, record.ReservationID, record.UserID, record.ChannelID,
	)
	operationProviderID, err := asyncTaskDecryptBound(recoveredOperation.EncryptedProviderTaskID, binding)
	require.NoError(t, err)
	privateProviderID, err := asyncTaskDecryptBound(privateData.EncryptedUpstreamTaskID, binding)
	require.NoError(t, err)
	assert.Equal(t, record.ProviderTaskID, operationProviderID)
	assert.Equal(t, record.ProviderTaskID, privateProviderID)
	assert.True(t, videoRecoveryProviderIDIsDurable(record))

	assert.Equal(t, beforeLedger, afterLedger, "journal recovery must not rewrite settled accounting")
	assert.Equal(t, beforeUser.Quota, afterUser.Quota)
	assert.Equal(t, beforeUser.UsedQuota, afterUser.UsedQuota)
	assert.Equal(t, beforeUser.RequestCount, afterUser.RequestCount)
	assert.Equal(t, beforeToken.RemainQuota, afterToken.RemainQuota)
	assert.Equal(t, beforeToken.UsedQuota, afterToken.UsedQuota)
	assert.Equal(t, beforeChannel.UsedQuota, afterChannel.UsedQuota)
	assert.Equal(t, beforeAuditCount, afterAuditCount, "recovery must not duplicate the consume audit")
}

func TestVideoRecoveryJournalRetainsUnprovenUnknownEvidence(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("VIDEO_TASK_RECOVERY_DIR", directory)
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	providerTaskID := "provider-unproven-unknown"
	require.NoError(t, markVideoTaskDispatching(task, reservation))
	require.NoError(t, persistAcceptedVideoRecoveryJournal(
		task, reservation.ReservationID(), providerTaskID,
	))
	require.NoError(t, settleUnknownVideoDispatch(
		task, reservation, videoUnknownDispatchReason, model.TaskOperationDispatching, "",
	))
	require.NoError(t, fixture.db.Where("event_id = ?", "video:"+reservation.ReservationID()).
		Delete(&model.AuditLogOutbox{}).Error)

	require.Error(t, PromoteVideoTaskRecoveryJournalsContext(context.Background()))
	path, err := videoRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)
	_, err = os.Stat(path)
	assert.NoError(t, err, "missing settlement proof must retain accepted-provider evidence")
	var recoveredTask model.Task
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&recoveredTask).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskStatusUnknown, recoveredTask.Status)
	assert.Equal(t, model.TaskOperationUnknown, operation.State)
	assert.Empty(t, operation.EncryptedProviderTaskID)
}

func TestVideoRecoveryJournalPromotionCursorPreventsBadPrefixStarvation(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("VIDEO_TASK_RECOVERY_DIR", directory)
	videoRecoveryJournalPromotionCursor.Store(0)
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	require.NoError(t, markVideoTaskDispatching(task, reservation))
	require.NoError(t, persistAcceptedVideoRecoveryJournal(
		task, reservation.ReservationID(), "provider-after-corrupt-prefix",
	))
	targetPath, err := videoRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)

	badTaskID := "task_" + strings.Repeat("0", 32)
	badPath, err := videoRecoveryJournalPath(badTaskID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(badPath, []byte(`{"version":1,"ciphertext":"corrupt"}`), 0o600))
	baseTime := time.Unix(common.NowTimestamp()-3600, 0)
	require.NoError(t, os.Chtimes(badPath, baseTime, baseTime))
	require.NoError(t, os.Chtimes(targetPath, baseTime.Add(time.Minute), baseTime.Add(time.Minute)))

	require.Error(t, promoteVideoRecoveryJournalRecords(context.Background(), 1))
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskOperationDispatching, operation.State,
		"the first bounded pass processes only the oldest corrupt record")
	require.NoError(t, promoteVideoRecoveryJournalRecords(context.Background(), 1))
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State,
		"the rotating window must reach a valid record after a bad sorted prefix")
	_, err = os.Stat(targetPath)
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(badPath)
	assert.NoError(t, err, "unreadable evidence is retained for operator repair")
}

func videoRecoveryJournalTestTask(taskID string) model.Task {
	return model.Task{
		TaskID: taskID, Platform: videoTaskPlatform,
		UserId: 7, ChannelId: 11, PrivateData: videoTaskPrivateDataPrefix + "test",
	}
}
