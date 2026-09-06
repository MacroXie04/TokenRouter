package tasks

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/sora"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"testing"
	"time"
)

func createVideoPollManualReviewFixture(
	t *testing.T,
	platform string,
) (videoTaskFixture, *model.Task, *billingsvc.RelayQuotaReservation, model.TaskOperation, int64) {
	t.Helper()
	require.True(t, model.IsOpenAIVideoTaskOperationPlatform(platform))
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	require.NoError(t, fixture.db.AutoMigrate(&model.RelayQuotaReservationReviewEvent{}))
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	require.NoError(t, markVideoTaskDispatching(task, reservation))
	accepted := &sora.Response{ID: "provider-manual-review", Status: "queued", Progress: 17}
	require.NoError(t, settleAcceptedVideoTask(
		task, reservation, accepted, accepted.ID, model.TaskOperationDispatching, "",
	))
	if task.Platform != platform {
		require.NoError(t, fixture.db.Model(&model.Task{}).Where("id = ?", task.ID).
			Update("platform", platform).Error)
		require.NoError(t, fixture.db.Model(&model.TaskOperation{}).Where("task_id = ?", task.TaskID).
			Update("platform", platform).Error)
		task.Platform = platform
	}
	now, err := model.DatabaseUnixTimestamp(fixture.db)
	require.NoError(t, err)
	oldCreatedAt := now - int64(8*24*time.Hour/time.Second)
	require.NoError(t, fixture.db.Model(&model.Task{}).Where("id = ?", task.ID).
		Updates(map[string]any{
			"status": model.TaskStatusUnknown, "fail_reason": videoPollingManualReviewReason,
			"finish_time": now, "updated_at": now,
		}).Error)
	require.NoError(t, fixture.db.Model(&model.TaskOperation{}).Where("task_id = ?", task.TaskID).
		Updates(map[string]any{
			"state": model.TaskOperationManualReview, "settlement_pending": false,
			"attempts": videoOperationMaxAttempts, "next_attempt_at": 0,
			"lease_owner": "", "lease_expires_at": 0,
			"last_error": videoPollingManualReviewReason, "created_at": oldCreatedAt,
			"updated_at": now, "completed_at": now,
		}).Error)
	require.NoError(t, fixture.db.First(task, task.ID).Error)
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	return fixture, task, reservation, operation, oldCreatedAt
}

func TestRetryVideoTaskManualReviewIsAtomicAuditedAndIdempotent(t *testing.T) {
	fixture, task, reservation, beforeOperation, oldCreatedAt := createVideoPollManualReviewFixture(
		t, model.TaskOperationPlatformSora,
	)
	var beforeUser model.User
	var beforeToken model.Token
	var beforeChannel model.Channel
	require.NoError(t, fixture.db.First(&beforeUser, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&beforeToken, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&beforeChannel, fixture.channel.Id).Error)

	result, err := RetryVideoTaskManualReview(reservation.ReservationID(), 8101)
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Equal(t, task.TaskID, result.TaskID)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, result.ReservationStatus)
	assert.Equal(t, model.TaskStatusSubmitted, result.TaskStatus)
	assert.Equal(t, model.TaskOperationSubmitted, result.OperationState)
	assert.NotEmpty(t, result.AuditEventID)

	var record model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("reservation_id = ?", reservation.ReservationID()).First(&record).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	assert.Equal(t, model.RelayQuotaReservationOperationSettle, record.Operation)
	require.NoError(t, fixture.db.First(task, task.ID).Error)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
	assert.Empty(t, task.FailReason)
	assert.Zero(t, task.FinishTime)
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.Zero(t, operation.Attempts)
	assert.Zero(t, operation.CompletedAt)
	assert.Empty(t, operation.LastError)
	assert.Empty(t, operation.LeaseOwner)
	assert.Greater(t, operation.CreatedAt, oldCreatedAt,
		"operator retry must reopen the bounded polling horizon")
	assert.Equal(t, operation.CreatedAt, operation.NextAttemptAt)

	var event model.RelayQuotaReservationReviewEvent
	require.NoError(t, fixture.db.Where("event_id = ?", result.AuditEventID).First(&event).Error)
	assert.Equal(t, 8101, event.OperatorUserID)
	assert.Equal(t, model.RelayQuotaReservationReviewActionRetryTaskPoll, event.Action)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, event.FromStatus)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, event.ToStatus)
	assert.Equal(t, task.TaskID, event.TaskID)
	assert.Equal(t, model.TaskStatusUnknown, event.TaskFromStatus)
	assert.Equal(t, model.TaskStatusSubmitted, event.TaskToStatus)
	assert.Equal(t, model.TaskOperationManualReview, event.TaskOperationFromState)
	assert.Equal(t, model.TaskOperationSubmitted, event.TaskOperationToState)
	assert.Equal(t, oldCreatedAt, event.TaskOperationCreatedAt)
	assert.Equal(t, beforeOperation.Attempts, event.TaskOperationAttempts)

	replay, err := RetryVideoTaskManualReview(reservation.ReservationID(), 8102)
	require.NoError(t, err)
	assert.False(t, replay.Changed)
	assert.Equal(t, result.AuditEventID, replay.AuditEventID)
	var eventCount int64
	require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ? AND action = ?", reservation.ReservationID(),
			model.RelayQuotaReservationReviewActionRetryTaskPoll).Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount)

	var afterUser model.User
	var afterToken model.Token
	var afterChannel model.Channel
	require.NoError(t, fixture.db.First(&afterUser, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&afterToken, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&afterChannel, fixture.channel.Id).Error)
	assert.Equal(t, beforeUser.Quota, afterUser.Quota)
	assert.Equal(t, beforeUser.UsedQuota, afterUser.UsedQuota)
	assert.Equal(t, beforeUser.RequestCount, afterUser.RequestCount)
	assert.Equal(t, beforeToken.RemainQuota, afterToken.RemainQuota)
	assert.Equal(t, beforeToken.UsedQuota, afterToken.UsedQuota)
	assert.Equal(t, beforeChannel.UsedQuota, afterChannel.UsedQuota)
}

func TestRetryVideoTaskManualReviewRejectsUnrecoverableProviderID(t *testing.T) {
	fixture, task, reservation, operation, _ := createVideoPollManualReviewFixture(
		t, model.TaskOperationPlatformSora,
	)
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	require.NoError(t, err)
	privateData.EncryptedUpstreamTaskID = ""
	privateJSON, err := marshalVideoTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, fixture.db.Model(&model.Task{}).Where("id = ?", task.ID).
		Update("private_data", privateJSON).Error)
	require.NoError(t, fixture.db.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).
		Update("encrypted_provider_task_id", "").Error)

	result, err := RetryVideoTaskManualReview(reservation.ReservationID(), 8201)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	require.NoError(t, fixture.db.First(task, task.ID).Error)
	assert.Equal(t, model.TaskStatusUnknown, task.Status)
	require.NoError(t, fixture.db.First(&operation, operation.ID).Error)
	assert.Equal(t, model.TaskOperationManualReview, operation.State)
	assert.Empty(t, operation.EncryptedProviderTaskID)
	var eventCount int64
	require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ?", reservation.ReservationID()).Count(&eventCount).Error)
	assert.Zero(t, eventCount)

	require.NoError(t, fixture.db.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).
		Update("encrypted_provider_task_id", "async-task-v2:video-test:not-authenticated").Error)
	result, err = RetryVideoTaskManualReview(reservation.ReservationID(), 8202)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ?", reservation.ReservationID()).Count(&eventCount).Error)
	assert.Zero(t, eventCount, "malformed ciphertext cannot create an operator audit or transition")
}

func TestRetryVideoTaskManualReviewAcceptsOpenAIPlatform(t *testing.T) {
	fixture, task, reservation, _, _ := createVideoPollManualReviewFixture(
		t, model.TaskOperationPlatformOpenAI,
	)
	result, err := RetryVideoTaskManualReview(reservation.ReservationID(), 8301)
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Equal(t, task.TaskID, result.TaskID)
	assert.Equal(t, model.TaskStatusSubmitted, result.TaskStatus)
	assert.Equal(t, model.TaskOperationSubmitted, result.OperationState)
	var event model.RelayQuotaReservationReviewEvent
	require.NoError(t, fixture.db.Where("event_id = ?", result.AuditEventID).First(&event).Error)
	assert.Equal(t, model.TaskOperationPlatformOpenAI, event.TaskPlatform)
}
