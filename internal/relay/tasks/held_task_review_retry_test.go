package tasks

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/kling"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/task/doubao"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"testing"
)

func createKlingPollManualReviewFixture(t *testing.T) (
	relayAccountingFixture, model.Task, model.RelayQuotaReservationRecord, model.TaskOperation,
) {
	t.Helper()
	fixture := configureKlingLifecycleFixture(t, false)
	client := &kling.Client{HTTPClient: &http.Client{Transport: klingRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return klingHTTPResponse(http.StatusOK,
			`{"code":0,"data":{"task_id":"provider-kling-review","task_status":"submitted","created_at":11,"updated_at":12}}`), nil
	})}}
	previousClient := newKlingTaskClient
	newKlingTaskClient = func() *kling.Client { return client }
	t.Cleanup(func() { newKlingTaskClient = previousClient })
	c, recorder := klingLifecycleContext(t, fixture, http.MethodPost, "/kling/v1/videos/text2video",
		`{"model":"kling-v1","prompt":"manual retry"}`, "")
	RelayKlingTask(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeKlingSubmitTaskID(t, recorder)
	var task model.Task
	var operation model.TaskOperation
	var record model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("task_id = ? AND platform = ?", taskID, klingTaskPlatform).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ? AND platform = ?", taskID, klingTaskPlatform).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&record).Error)
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status": model.TaskStatusUnknown, "fail_reason": klingPollingManualReviewReason,
		"progress": "100%", "finish_time": now, "updated_at": now,
	}).Error)
	require.NoError(t, model.DB.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
		"state": model.TaskOperationManualReview, "attempts": klingOperationMaxAttempts,
		"next_attempt_at": 0, "completed_at": now, "updated_at": now,
		"last_error": klingPollingManualReviewReason, "lease_owner": "", "lease_expires_at": 0,
	}).Error)
	require.NoError(t, model.DB.First(&task, task.ID).Error)
	require.NoError(t, model.DB.First(&operation, operation.ID).Error)
	return fixture, task, record, operation
}

func createDoubaoPollManualReviewFixture(t *testing.T, channelType channelcatalog.ChannelType, platform string) (
	relayAccountingFixture, model.Task, model.RelayQuotaReservationRecord, model.TaskOperation,
) {
	t.Helper()
	fixture := configureDoubaoLifecycleFixture(t, false, channelType)
	task, reservation := createPreparedDoubaoTaskForRecovery(t, fixture, platform)
	require.NoError(t, markDoubaoTaskDispatching(task, reservation))
	require.NoError(t, persistAcceptedDoubaoTask(task, reservation.ReservationID(), &doubao.Task{
		ProviderTaskID: "provider-doubao-review", Status: doubao.StatusSubmitted,
	}, model.TaskOperationDispatching, ""))
	var operation model.TaskOperation
	var record model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("task_id = ? AND platform = ?", task.TaskID, platform).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&record).Error)
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status": model.TaskStatusUnknown, "fail_reason": doubaoPollingManualReviewReason,
		"progress": "100%", "finish_time": now, "updated_at": now,
	}).Error)
	require.NoError(t, model.DB.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
		"state": model.TaskOperationManualReview, "attempts": doubaoOperationMaxAttempts,
		"next_attempt_at": 0, "completed_at": now, "updated_at": now,
		"last_error": doubaoPollingManualReviewReason, "lease_owner": "", "lease_expires_at": 0,
	}).Error)
	require.NoError(t, model.DB.First(task, task.ID).Error)
	require.NoError(t, model.DB.First(&operation, operation.ID).Error)
	return fixture, *task, record, operation
}

func TestRetryKlingTaskManualReviewRetainsUsageMeteredHoldAndAudit(t *testing.T) {
	fixture, task, beforeRecord, beforeOperation := createKlingPollManualReviewFixture(t)
	beforeUser, beforeToken, beforeChannel := loadHeldRetryAccounting(t, fixture)
	assert.Zero(t, beforeRecord.ChannelID, "an unsettled hold must not claim a settlement channel")
	assert.Equal(t, fixture.channel.Id, beforeOperation.ChannelID)
	review, err := billingsvc.GetManualReviewRelayQuotaReservation(beforeRecord.ReservationID)
	require.NoError(t, err)
	assert.True(t, review.Retryable)
	assert.Equal(t, model.TaskOperationSubmitted, review.RetryTargetStatus)

	result, err := RetryKlingTaskManualReview(beforeRecord.ReservationID, 8601)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Changed)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, result.ReservationStatus)
	assert.Equal(t, model.TaskOperationSubmitted, result.OperationState)
	assert.NotEmpty(t, result.AuditEventID)
	assertHeldRetryAccountingUnchanged(t, fixture, beforeRecord, beforeUser, beforeToken, beforeChannel)

	var afterTask model.Task
	var afterOperation model.TaskOperation
	require.NoError(t, model.DB.First(&afterTask, task.ID).Error)
	require.NoError(t, model.DB.First(&afterOperation, beforeOperation.ID).Error)
	assert.Equal(t, model.TaskStatusSubmitted, afterTask.Status)
	assert.Empty(t, afterTask.FailReason)
	assert.Zero(t, afterTask.FinishTime)
	assert.Equal(t, model.TaskOperationSubmitted, afterOperation.State)
	assert.True(t, afterOperation.SettlementPending)
	assert.Zero(t, afterOperation.Attempts)
	assert.Equal(t, beforeOperation.EncryptedProviderTaskID, afterOperation.EncryptedProviderTaskID)

	var event model.RelayQuotaReservationReviewEvent
	require.NoError(t, model.DB.Where("event_id = ?", result.AuditEventID).First(&event).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, event.FromStatus)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, event.ToStatus)
	assert.Zero(t, event.ChannelID, "the retry audit must preserve the unsettled hold channel")
	replay, err := RetryKlingTaskManualReview(beforeRecord.ReservationID, 8602)
	require.NoError(t, err)
	assert.False(t, replay.Changed)
	assert.Equal(t, result.AuditEventID, replay.AuditEventID)
}

func TestRetryKlingTaskManualReviewRejectsCredentialBindingDrift(t *testing.T) {
	_, task, record, operation := createKlingPollManualReviewFixture(t)
	privateData, err := decodeKlingTaskPrivateData(task.PrivateData)
	require.NoError(t, err)
	privateData.EncryptedChannelKey = operation.EncryptedProviderTaskID
	encoded, err := marshalKlingTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).
		Update("private_data", encoded).Error)

	result, err := RetryKlingTaskManualReview(record.ReservationID, 8603)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	require.NoError(t, model.DB.First(&operation, operation.ID).Error)
	assert.Equal(t, model.TaskOperationManualReview, operation.State)
	var eventCount int64
	require.NoError(t, model.DB.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ?", record.ReservationID).Count(&eventCount).Error)
	assert.Zero(t, eventCount)
}

func TestRetryDoubaoTaskManualReviewUsesOnlyExactPlatformsAndDoesNotRebill(t *testing.T) {
	tests := []struct {
		name        string
		channelType channelcatalog.ChannelType
		platform    string
	}{
		{"VolcEngine", channelcatalog.ChannelTypeVolcEngine, model.TaskOperationPlatformVolcEngine},
		{"DoubaoVideo", channelcatalog.ChannelTypeDoubaoVideo, model.TaskOperationPlatformDoubaoVideo},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, _, beforeRecord, beforeOperation := createDoubaoPollManualReviewFixture(
				t, test.channelType, test.platform,
			)
			beforeUser, beforeToken, beforeChannel := loadHeldRetryAccounting(t, fixture)
			assert.Zero(t, beforeRecord.ChannelID, "an unsettled hold must not claim a settlement channel")
			assert.Equal(t, fixture.channel.Id, beforeOperation.ChannelID)
			result, err := RetryDoubaoTaskManualReview(beforeRecord.ReservationID, 8701)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.Changed)
			assert.Equal(t, model.RelayQuotaReservationStatusDispatched, result.ReservationStatus)
			assertHeldRetryAccountingUnchanged(t, fixture, beforeRecord, beforeUser, beforeToken, beforeChannel)
			var operation model.TaskOperation
			require.NoError(t, model.DB.First(&operation, beforeOperation.ID).Error)
			assert.Equal(t, test.platform, operation.Platform)
			assert.Equal(t, model.TaskOperationSubmitted, operation.State)
			assert.True(t, operation.SettlementPending)
			assert.Equal(t, beforeOperation.EncryptedProviderTaskID, operation.EncryptedProviderTaskID)
			var event model.RelayQuotaReservationReviewEvent
			require.NoError(t, model.DB.Where("event_id = ?", result.AuditEventID).First(&event).Error)
			assert.Zero(t, event.ChannelID, "the retry audit must preserve the unsettled hold channel")
		})
	}
}

func TestRetryDoubaoTaskManualReviewRejectsCrossPlatformTask(t *testing.T) {
	_, task, record, operation := createDoubaoPollManualReviewFixture(
		t, channelcatalog.ChannelTypeVolcEngine, model.TaskOperationPlatformVolcEngine,
	)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).
		Update("platform", model.TaskOperationPlatformKling).Error)
	result, err := RetryDoubaoTaskManualReview(record.ReservationID, 8702)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	require.NoError(t, model.DB.First(&operation, operation.ID).Error)
	assert.Equal(t, model.TaskOperationManualReview, operation.State)
}

func loadHeldRetryAccounting(t *testing.T, fixture relayAccountingFixture) (model.User, model.Token, model.Channel) {
	t.Helper()
	var user model.User
	var token model.Token
	var channel model.Channel
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.Unscoped().First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
	return user, token, channel
}

func assertHeldRetryAccountingUnchanged(t *testing.T, fixture relayAccountingFixture,
	wantRecord model.RelayQuotaReservationRecord, wantUser model.User, wantToken model.Token, wantChannel model.Channel) {
	t.Helper()
	var record model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("reservation_id = ?", wantRecord.ReservationID).First(&record).Error)
	user, token, channel := loadHeldRetryAccounting(t, fixture)
	assert.Equal(t, wantRecord, record, "poll retry must retain the exact dispatched hold")
	assert.Equal(t, wantUser, user)
	assert.Equal(t, wantToken, token)
	assert.Equal(t, wantChannel, channel)
}

func TestHeldTaskRetryRejectsInvalidReservationAndOperator(t *testing.T) {
	result, err := RetryKlingTaskManualReview("../reservation", 1)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, billingsvc.ErrRelayQuotaReviewQueryInvalid)
	result, err = RetryDoubaoTaskManualReview("valid-reservation", 0)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, billingsvc.ErrRelayQuotaReviewQueryInvalid)
}
