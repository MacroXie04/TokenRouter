package relay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/midjourney"
	"github.com/tokenrouter/tokenrouter/service"
)

func createMidjourneyPollManualReviewFixture(t *testing.T) (
	midjourneyTaskFixture, model.Task, model.Midjourney,
	model.RelayQuotaReservationRecord, model.TaskOperation,
) {
	t.Helper()
	fixture := newMidjourneyTaskFixture(t, &mockMidjourneyProvider{})
	require.NoError(t, fixture.db.AutoMigrate(
		&model.RelayQuotaReservationReviewEvent{},
		&model.JimengTaskOperation{},
	))
	task, reservation := createPreparedMidjourneyTaskForRecovery(t, fixture, midjourney.ActionImagine)
	require.NoError(t, markMidjourneyTaskDispatching(task, reservation))
	require.NoError(t, persistAcceptedMidjourneyTask(task, reservation.ReservationID(), &midjourney.TaskResult{
		ProviderTaskID: "provider-midjourney-review", Action: string(midjourney.ActionImagine),
		Status: model.TaskStatusSubmitted, Progress: "0%",
	}, 1, model.TaskOperationDispatching, ""))
	var operation model.TaskOperation
	var record model.RelayQuotaReservationRecord
	var mirror model.Midjourney
	require.NoError(t, fixture.db.Where("task_id = ? AND platform = ?", task.TaskID,
		midjourneyTaskPlatform).First(&operation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&record).Error)
	require.NoError(t, fixture.db.Where("mj_id = ?", task.TaskID).First(&mirror).Error)
	now, err := model.DatabaseUnixTimestamp(fixture.db)
	require.NoError(t, err)
	require.NoError(t, fixture.db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status": model.TaskStatusUnknown, "fail_reason": midjourneyPollingManualReviewReason,
		"progress": "100%", "finish_time": now, "updated_at": now,
	}).Error)
	require.NoError(t, fixture.db.Model(&model.Midjourney{}).Where("id = ?", mirror.Id).Updates(map[string]any{
		"status": model.TaskStatusUnknown, "fail_reason": midjourneyPollingManualReviewReason,
		"progress": "100%", "finish_time": now * 1000,
	}).Error)
	require.NoError(t, fixture.db.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
		"state": model.TaskOperationManualReview, "attempts": midjourneyOperationMaxAttempts,
		"next_attempt_at": 0, "completed_at": now, "updated_at": now,
		"last_error": midjourneyPollingManualReviewReason, "lease_owner": "", "lease_expires_at": 0,
	}).Error)
	require.NoError(t, fixture.db.First(task, task.ID).Error)
	require.NoError(t, fixture.db.First(&mirror, mirror.Id).Error)
	require.NoError(t, fixture.db.First(&operation, operation.ID).Error)
	return fixture, *task, mirror, record, operation
}

func TestRetryMidjourneyTaskManualReviewAtomicallyFencesMirrorAndAccounting(t *testing.T) {
	fixture, task, mirror, beforeRecord, beforeOperation := createMidjourneyPollManualReviewFixture(t)
	var beforeUser model.User
	var beforeToken model.Token
	var beforeChannel model.Channel
	require.NoError(t, fixture.db.First(&beforeUser, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&beforeToken, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&beforeChannel, fixture.channel.Id).Error)
	assert.Zero(t, beforeRecord.ChannelID, "an unsettled hold must not claim a settlement channel")
	assert.Equal(t, fixture.channel.Id, beforeOperation.ChannelID)
	review, err := service.GetManualReviewRelayQuotaReservation(beforeRecord.ReservationID)
	require.NoError(t, err)
	assert.Equal(t, service.RelayQuotaReviewKindMidjourneyTask, review.ReviewKind)
	assert.True(t, review.Retryable)
	assert.Equal(t, model.TaskOperationSubmitted, review.RetryTargetStatus)

	result, err := RetryMidjourneyTaskManualReview(beforeRecord.ReservationID, 8801)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Changed)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, result.ReservationStatus)
	assert.Equal(t, model.TaskStatusSubmitted, result.TaskStatus)
	assert.Equal(t, model.TaskOperationSubmitted, result.OperationState)

	var afterRecord model.RelayQuotaReservationRecord
	var afterTask model.Task
	var afterMirror model.Midjourney
	var afterOperation model.TaskOperation
	var afterUser model.User
	var afterToken model.Token
	var afterChannel model.Channel
	require.NoError(t, fixture.db.Where("reservation_id = ?", beforeRecord.ReservationID).First(&afterRecord).Error)
	require.NoError(t, fixture.db.First(&afterTask, task.ID).Error)
	require.NoError(t, fixture.db.First(&afterMirror, mirror.Id).Error)
	require.NoError(t, fixture.db.First(&afterOperation, beforeOperation.ID).Error)
	require.NoError(t, fixture.db.First(&afterUser, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&afterToken, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&afterChannel, fixture.channel.Id).Error)
	assert.Equal(t, beforeRecord, afterRecord)
	assert.Equal(t, beforeUser, afterUser)
	assert.Equal(t, beforeToken, afterToken)
	assert.Equal(t, beforeChannel, afterChannel)
	assert.Equal(t, model.TaskStatusSubmitted, afterTask.Status)
	assert.Empty(t, afterTask.FailReason)
	assert.Zero(t, afterTask.FinishTime)
	assert.Equal(t, model.TaskStatusSubmitted, afterMirror.Status)
	assert.Empty(t, afterMirror.FailReason)
	assert.Equal(t, "0%", afterMirror.Progress)
	assert.Zero(t, afterMirror.FinishTime)
	assert.Equal(t, model.TaskOperationSubmitted, afterOperation.State)
	assert.True(t, afterOperation.SettlementPending)
	assert.Zero(t, afterOperation.Attempts)
	assert.Equal(t, beforeOperation.EncryptedProviderTaskID, afterOperation.EncryptedProviderTaskID)

	var event model.RelayQuotaReservationReviewEvent
	require.NoError(t, fixture.db.Where("event_id = ?", result.AuditEventID).First(&event).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, event.FromStatus)
	assert.Zero(t, event.ChannelID)
	replay, err := RetryMidjourneyTaskManualReview(beforeRecord.ReservationID, 8802)
	require.NoError(t, err)
	assert.False(t, replay.Changed)
	assert.Equal(t, result.AuditEventID, replay.AuditEventID)
}

func TestRetryMidjourneyTaskManualReviewRejectsAmbiguousMirrorAtomically(t *testing.T) {
	fixture, task, mirror, record, operation := createMidjourneyPollManualReviewFixture(t)
	duplicate := mirror
	duplicate.Id = 0
	duplicate.UserId++
	duplicate.ChannelId++
	require.NoError(t, fixture.db.Create(&duplicate).Error)

	result, err := RetryMidjourneyTaskManualReview(record.ReservationID, 8803)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, service.ErrRelayQuotaReviewUnsafeRetry)
	require.NoError(t, fixture.db.First(&task, task.ID).Error)
	require.NoError(t, fixture.db.First(&mirror, mirror.Id).Error)
	require.NoError(t, fixture.db.First(&operation, operation.ID).Error)
	assert.Equal(t, model.TaskStatusUnknown, task.Status)
	assert.Equal(t, model.TaskStatusUnknown, mirror.Status)
	assert.Equal(t, model.TaskOperationManualReview, operation.State)
	var eventCount int64
	require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ?", record.ReservationID).Count(&eventCount).Error)
	assert.Zero(t, eventCount)
}
