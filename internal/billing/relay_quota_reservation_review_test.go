package billing

import (
	"encoding/base64"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"sync"
	"testing"
)

func createManualReviewReservation(t *testing.T, username, operation string, actual int) (*RelayQuotaReservation, model.User, model.Token, model.Channel) {
	t.Helper()
	user := model.User{Username: username, Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-" + username, Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{Name: "review-" + username, Key: "upstream", Status: channelcatalog.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(&channel).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)

	channelID := 0
	dispatchedAt := int64(0)
	if operation == model.RelayQuotaReservationOperationSettle {
		channelID = channel.Id
		dispatchedAt = wallclock.NowTimestamp()
	}
	now := wallclock.NowTimestamp()
	require.NoError(t, model.DB.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ?", reservation.ReservationID()).Updates(map[string]any{
		"status": model.RelayQuotaReservationStatusManualReview, "operation": operation,
		"actual_quota": actual, "channel_id": channelID, "dispatched_at": dispatchedAt,
		"attempts": 8, "last_error": "Bearer operator-visible-secret password=hunter2",
		"next_attempt_at": 0, "completed_at": now, "updated_at": now,
	}).Error)
	return reservation, user, token, channel
}

func TestManualReviewRelayQuotaListIsBoundedFilteredAndSecretFree(t *testing.T) {
	setupRelayQuotaReservationDB(t)
	settleReservation, settleUser, _, _ := createManualReviewReservation(
		t, "review-list-settle", model.RelayQuotaReservationOperationSettle, 9,
	)
	_, refundUser, _, _ := createManualReviewReservation(
		t, "review-list-refund", model.RelayQuotaReservationOperationRefund, 0,
	)

	items, total, err := ListManualReviewRelayQuotaReservations(RelayQuotaReservationReviewFilter{
		Page: 1, PageSize: 1, Operation: model.RelayQuotaReservationOperationSettle,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	assert.Equal(t, settleReservation.ReservationID(), items[0].ReservationID)
	assert.Equal(t, settleUser.Id, items[0].UserID)
	assert.Equal(t, "reconciliation_failed", items[0].DiagnosticCode)
	assert.Len(t, items[0].DiagnosticFingerprint, relayQuotaReviewFingerprintN)
	assert.True(t, items[0].Retryable)
	assert.Equal(t, model.RelayQuotaReservationStatusPendingSettlement, items[0].RetryTargetStatus)

	items, total, err = ListManualReviewRelayQuotaReservations(RelayQuotaReservationReviewFilter{
		Page: 1, PageSize: 10, UserID: refundUser.Id,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	assert.Equal(t, model.RelayQuotaReservationOperationRefund, items[0].Operation)

	item, err := GetManualReviewRelayQuotaReservation(settleReservation.ReservationID())
	require.NoError(t, err)
	assert.Equal(t, settleReservation.ReservationID(), item.ReservationID)

	for _, filter := range []RelayQuotaReservationReviewFilter{
		{Page: -1, PageSize: 10},
		{Page: maxRelayQuotaReviewPage + 1, PageSize: 10},
		{Page: 1, PageSize: maxRelayQuotaReviewPageSize + 1},
		{Page: 1, PageSize: 10, UserID: -1},
		{Page: 1, PageSize: 10, Operation: "charge_whatever"},
		{Page: 1, PageSize: 10, ReservationID: "bad id"},
	} {
		_, _, err = ListManualReviewRelayQuotaReservations(filter)
		assert.ErrorIs(t, err, ErrRelayQuotaReviewQueryInvalid)
	}
	_, err = GetManualReviewRelayQuotaReservation("bad/id")
	assert.ErrorIs(t, err, ErrRelayQuotaReviewQueryInvalid)
}

func TestManualReviewRelayQuotaListIncludesRetainedJimengDispatch(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "review-list-jimeng", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-review-list-jimeng", Status: TokenStatusEnabled, RemainQuota: 100,
	}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: "review-list-jimeng", Key: "upstream", Status: channelcatalog.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	now := wallclock.NowTimestamp()
	require.NoError(t, db.Create(&model.JimengTaskOperation{
		TaskID: "task_review_list_jimeng", ReservationID: reservation.ReservationID(),
		UserID: user.Id, ChannelID: channel.Id, State: model.JimengTaskOperationManualReview,
		LastError: relayQuotaReviewJimengReason, CreatedAt: now, UpdatedAt: now, CompletedAt: now,
	}).Error)

	items, total, err := ListManualReviewRelayQuotaReservations(RelayQuotaReservationReviewFilter{
		Page: 1, PageSize: 10, ReservationID: reservation.ReservationID(),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, items[0].Status)
	assert.False(t, items[0].Retryable)
	assert.True(t, items[0].ResolutionRequired)
	assert.Equal(t, []string{RelayQuotaReviewResolutionSettle, RelayQuotaReviewResolutionRefund},
		items[0].ResolutionOptions)
	assert.Equal(t, "jimeng_provider_outcome_unresolved", items[0].DiagnosticCode)
	assert.Empty(t, items[0].DiagnosticFingerprint)

	item, err := GetManualReviewRelayQuotaReservation(reservation.ReservationID())
	require.NoError(t, err)
	assert.True(t, item.ResolutionRequired)
	assert.Equal(t, items[0].ResolutionOptions, item.ResolutionOptions)
}

func TestManualReviewRelayQuotaListIncludesOpenAIVideoPollWithoutSecrets(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "review-list-video", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-review-list-video", Status: TokenStatusEnabled, RemainQuota: 100,
	}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: "review-list-video", Key: "upstream", Status: channelcatalog.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	require.NoError(t, reservation.SettleWithChannel(10, channel.Id))

	now := wallclock.NowTimestamp()
	ciphertextFrame := "async-task-v2:review-key:" +
		base64.RawURLEncoding.EncodeToString(make([]byte, 28))
	privateBytes, err := jsonutil.Marshal(map[string]any{
		"relay_reservation_id":  reservation.ReservationID(),
		"channel_base_url":      "https://provider.example.invalid",
		"encrypted_channel_key": ciphertextFrame,
		"billing_source":        BillingSourceWallet,
		"token_id":              token.Id,
	})
	require.NoError(t, err)
	task := model.Task{
		CreatedAt: now - 100, UpdatedAt: now, TaskID: "task_review_list_video",
		Platform: model.TaskOperationPlatformOpenAI, UserId: user.Id,
		ChannelId: channel.Id, Quota: 10, Status: model.TaskStatusUnknown,
		FinishTime: now, Properties: `{}`,
		PrivateData: relayQuotaReviewVideoPrivateDataPrefix + string(privateBytes),
	}
	require.NoError(t, db.Create(&task).Error)
	operation := model.TaskOperation{
		TaskID: task.TaskID, ReservationID: reservation.ReservationID(),
		Platform: model.TaskOperationPlatformOpenAI, UserID: user.Id, ChannelID: channel.Id,
		State: model.TaskOperationManualReview, EncryptedProviderTaskID: ciphertextFrame,
		Attempts: 20_000, LastError: "Bearer never-return-this-video-secret",
		CreatedAt: now - 100, UpdatedAt: now, CompletedAt: now,
	}
	require.NoError(t, db.Create(&operation).Error)

	items, total, err := ListManualReviewRelayQuotaReservations(RelayQuotaReservationReviewFilter{
		Page: 1, PageSize: 10, ReservationID: reservation.ReservationID(),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	item := items[0]
	assert.Equal(t, RelayQuotaReviewKindOpenAIVideoPoll, item.ReviewKind)
	assert.Equal(t, task.TaskID, item.TaskID)
	assert.Equal(t, model.TaskStatusUnknown, item.TaskStatus)
	assert.Equal(t, model.TaskOperationManualReview, item.TaskOperationState)
	assert.Equal(t, 20_000, item.TaskOperationAttempts)
	assert.True(t, item.ProviderTaskIDPresent)
	assert.True(t, item.Retryable)
	assert.Equal(t, model.TaskOperationSubmitted, item.RetryTargetStatus)
	assert.Equal(t, "video_polling_manual_review", item.DiagnosticCode)
	assert.Len(t, item.DiagnosticFingerprint, relayQuotaReviewFingerprintN)
	assert.NotContains(t, fmt.Sprintf("%+v", item), "never-return-this-video-secret")
	assert.NotContains(t, fmt.Sprintf("%+v", item), ciphertextFrame)

	require.NoError(t, db.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).
		Update("encrypted_provider_task_id", "").Error)
	itemPointer, err := GetManualReviewRelayQuotaReservation(reservation.ReservationID())
	require.NoError(t, err)
	assert.False(t, itemPointer.ProviderTaskIDPresent)
	assert.False(t, itemPointer.Retryable)
	assert.Equal(t, "video_provider_id_unavailable", itemPointer.DiagnosticCode)

	// Platform "1" is shared with non-video OpenAI tasks. A row without the
	// video private-data envelope remains visible for diagnosis but is never
	// advertised as a valid/retryable video poll.
	require.NoError(t, db.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).
		Update("encrypted_provider_task_id", ciphertextFrame).Error)
	require.NoError(t, db.Model(&model.Task{}).Where("id = ?", task.ID).
		Update("private_data", `{}`).Error)
	itemPointer, err = GetManualReviewRelayQuotaReservation(reservation.ReservationID())
	require.NoError(t, err)
	assert.False(t, itemPointer.ProviderTaskIDPresent)
	assert.False(t, itemPointer.Retryable)
	assert.Equal(t, "invalid_video_review_record", itemPointer.DiagnosticCode)

	mixedPrivateBytes, err := jsonutil.Marshal(map[string]any{
		"relay_reservation_id":       reservation.ReservationID(),
		"encrypted_upstream_task_id": ciphertextFrame,
		"channel_base_url":           "https://provider.example.invalid",
		"encrypted_channel_key":      ciphertextFrame,
		"billing_source":             BillingSourceWallet,
		"token_id":                   token.Id,
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.Task{}).Where("id = ?", task.ID).
		Update("private_data", relayQuotaReviewVideoPrivateDataPrefix+string(mixedPrivateBytes)).Error)
	require.NoError(t, db.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).
		Update("encrypted_provider_task_id", "async-task-v2:review-key:not-base64!").Error)
	itemPointer, err = GetManualReviewRelayQuotaReservation(reservation.ReservationID())
	require.NoError(t, err)
	assert.False(t, itemPointer.ProviderTaskIDPresent,
		"one malformed durable copy must not be hidden by the other valid copy")
	assert.False(t, itemPointer.Retryable)
}

func TestManualReviewRelayQuotaListUsesExactSunoPlatformAndExposesPollRetry(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "review-list-suno", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-review-list-suno", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: "review-list-suno", Key: "upstream", Status: channelcatalog.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	require.NoError(t, reservation.SettleWithChannel(10, channel.Id))
	var record model.RelayQuotaReservationRecord
	require.NoError(t, db.Where("reservation_id = ?", reservation.ReservationID()).First(&record).Error)

	now := wallclock.NowTimestamp()
	ciphertextFrame := "async-task-v2:review-key:" + base64.RawURLEncoding.EncodeToString(make([]byte, 28))
	privateBytes, err := jsonutil.Marshal(map[string]any{
		"relay_reservation_id":       reservation.ReservationID(),
		"channel_base_url":           "https://provider.example.invalid",
		"encrypted_channel_key":      ciphertextFrame,
		"encrypted_provider_task_id": ciphertextFrame,
		"settlement_pending":         false,
		"billing_source":             record.FundingSource,
		"subscription_id":            record.SubscriptionID,
		"funding_usage_epoch":        record.UsageEpoch,
		"token_id":                   record.TokenID,
	})
	require.NoError(t, err)
	task := model.Task{
		CreatedAt: now - 100, UpdatedAt: now, TaskID: "task_review_list_suno",
		Platform: model.TaskOperationPlatformSuno, UserId: user.Id, ChannelId: channel.Id,
		Quota: 10, Status: model.TaskStatusUnknown, FinishTime: now,
		PrivateData: relayQuotaReviewSunoPrivateDataPrefix + string(privateBytes),
	}
	require.NoError(t, db.Create(&task).Error)
	operation := model.TaskOperation{
		TaskID: task.TaskID, ReservationID: reservation.ReservationID(),
		Platform: model.TaskOperationPlatformSuno, UserID: user.Id, ChannelID: channel.Id,
		State: model.TaskOperationManualReview, EncryptedProviderTaskID: ciphertextFrame,
		Attempts: 20_000, LastError: "Suno polling horizon reached",
		CreatedAt: now - 100, UpdatedAt: now, CompletedAt: now,
	}
	require.NoError(t, db.Create(&operation).Error)

	item, err := GetManualReviewRelayQuotaReservation(reservation.ReservationID())
	require.NoError(t, err)
	assert.Equal(t, RelayQuotaReviewKindSunoTask, item.ReviewKind)
	assert.Equal(t, model.TaskOperationPlatformSuno, item.TaskPlatform)
	assert.True(t, item.ProviderTaskIDPresent)
	assert.True(t, item.Retryable)
	assert.Equal(t, model.TaskOperationSubmitted, item.RetryTargetStatus)
	assert.False(t, item.ResolutionRequired)
	assert.Equal(t, "suno_polling_manual_review", item.DiagnosticCode)

	// Channel type 36 is not the Suno task provenance value. A mismatched task
	// remains visible through its operation but is never treated as a valid row.
	require.NoError(t, db.Model(&model.Task{}).Where("id = ?", task.ID).Update("platform", "36").Error)
	item, err = GetManualReviewRelayQuotaReservation(reservation.ReservationID())
	require.NoError(t, err)
	assert.Equal(t, model.TaskOperationPlatformSuno, item.TaskPlatform)
	assert.False(t, item.ProviderTaskIDPresent)
	assert.Equal(t, "invalid_suno_review_record", item.DiagnosticCode)
}

func TestRetryManualReviewSettlementIsAtomicAuditedAndIdempotent(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	reservation, user, token, channel := createManualReviewReservation(
		t, "review-retry-settle", model.RelayQuotaReservationOperationSettle, 12,
	)

	result, err := RetryManualReviewRelayQuotaReservation(reservation.ReservationID(), 77)
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.NotEmpty(t, result.AuditEventID)
	assert.Equal(t, model.RelayQuotaReservationStatusPendingSettlement, result.Reservation.Status)
	assert.Empty(t, result.Reservation.DiagnosticCode)

	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusPendingSettlement, record.Status)
	assert.Equal(t, model.RelayQuotaReservationOperationSettle, record.Operation)
	assert.Equal(t, 12, record.ActualQuota)
	assert.Equal(t, channel.Id, record.ChannelID)
	assert.Zero(t, record.Attempts)
	assert.Empty(t, record.LastError)
	assert.Zero(t, record.CompletedAt)

	var event model.RelayQuotaReservationReviewEvent
	require.NoError(t, db.Where("event_id = ?", result.AuditEventID).First(&event).Error)
	assert.Equal(t, 77, event.OperatorUserID)
	assert.Equal(t, 1, event.Revision)
	assert.Equal(t, model.RelayQuotaReservationStatusManualReview, event.FromStatus)
	assert.Equal(t, model.RelayQuotaReservationStatusPendingSettlement, event.ToStatus)
	assert.Equal(t, 12, event.ActualQuota)
	assert.Equal(t, channel.Id, event.ChannelID)

	replay, err := RetryManualReviewRelayQuotaReservation(reservation.ReservationID(), 88)
	require.NoError(t, err)
	assert.False(t, replay.Changed)
	assert.Equal(t, result.AuditEventID, replay.AuditEventID)
	var eventCount int64
	require.NoError(t, db.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ?", reservation.ReservationID()).Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount, "a replay must not append a second audit event")

	require.NoError(t, reconcileRelayQuotaReservationsAt(wallclock.NowTimestamp(), 10))
	record = loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	require.NoError(t, db.First(&channel, channel.Id).Error)
	assert.Equal(t, 88, user.Quota)
	assert.Equal(t, 12, user.UsedQuota)
	assert.Equal(t, 88, token.RemainQuota)
	assert.Equal(t, 12, token.UsedQuota)
	assert.Equal(t, int64(12), channel.UsedQuota)

	terminalReplay, err := RetryManualReviewRelayQuotaReservation(reservation.ReservationID(), 99)
	require.NoError(t, err)
	assert.False(t, terminalReplay.Changed)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, terminalReplay.Reservation.Status)
	require.NoError(t, db.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ?", reservation.ReservationID()).Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount)
}

func TestRetryManualReviewRefundReturnsExactHoldAndAuditsOnce(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	reservation, user, token, _ := createManualReviewReservation(
		t, "review-retry-refund", model.RelayQuotaReservationOperationRefund, 0,
	)
	result, err := RetryManualReviewRelayQuotaReservation(reservation.ReservationID(), 71)
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Equal(t, model.RelayQuotaReservationStatusPendingRefund, result.Reservation.Status)

	require.NoError(t, reconcileRelayQuotaReservationsAt(wallclock.NowTimestamp(), 10))
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, record.Status)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 100, token.RemainQuota)
}

func TestRetryManualReviewRejectsCorruptEconomicsAndInvalidStates(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	reservation, _, _, _ := createManualReviewReservation(
		t, "review-invalid-record", model.RelayQuotaReservationOperationRefund, 0,
	)
	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ?", reservation.ReservationID()).
		Updates(map[string]any{"channel_id": 9, "last_error": "password=never-return-this"}).Error)

	item, err := GetManualReviewRelayQuotaReservation(reservation.ReservationID())
	require.NoError(t, err)
	assert.False(t, item.Retryable)
	assert.Equal(t, "invalid_record", item.DiagnosticCode)
	_, err = RetryManualReviewRelayQuotaReservation(reservation.ReservationID(), 72)
	assert.ErrorIs(t, err, ErrRelayQuotaReviewUnsafeRetry)
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusManualReview, record.Status)
	assert.Equal(t, 9, record.ChannelID, "unsafe retry never rewrites accounting fields")
	var count int64
	require.NoError(t, db.Model(&model.RelayQuotaReservationReviewEvent{}).Count(&count).Error)
	assert.Zero(t, count)

	heldUser := model.User{Username: "review-invalid-state", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&heldUser).Error)
	heldToken := model.Token{UserId: heldUser.Id, Key: "sk-review-invalid-state", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&heldToken).Error)
	held, err := NewRelayQuotaReservation(heldUser.Id, &heldToken, 10)
	require.NoError(t, err)
	_, err = RetryManualReviewRelayQuotaReservation(held.ReservationID(), 72)
	assert.ErrorIs(t, err, ErrRelayQuotaReviewInvalidState)
	_, err = RetryManualReviewRelayQuotaReservation("missing-review", 72)
	assert.ErrorIs(t, err, ErrRelayQuotaReviewNotFound)
}

func TestRetryManualReviewAuditFailureRollsBackTransition(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	reservation, _, _, _ := createManualReviewReservation(
		t, "review-audit-rollback", model.RelayQuotaReservationOperationSettle, 10,
	)
	require.NoError(t, db.Migrator().DropTable(&model.RelayQuotaReservationReviewEvent{}))
	_, err := RetryManualReviewRelayQuotaReservation(reservation.ReservationID(), 73)
	require.Error(t, err)
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusManualReview, record.Status)
	assert.Equal(t, 8, record.Attempts)
	assert.NotEmpty(t, record.LastError)
}

func TestRetryManualReviewConcurrentRequestsAppendOneAuditEvent(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	reservation, _, _, _ := createManualReviewReservation(
		t, "review-concurrent", model.RelayQuotaReservationOperationSettle, 10,
	)
	const workers = 12
	results := make(chan *RelayQuotaReservationRetryResult, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		worker := worker
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := RetryManualReviewRelayQuotaReservation(reservation.ReservationID(), 100+worker)
			results <- result
			errs <- err
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	changed := 0
	auditID := ""
	for result := range results {
		require.NotNil(t, result)
		if result.Changed {
			changed++
		}
		if auditID == "" {
			auditID = result.AuditEventID
		}
		assert.Equal(t, auditID, result.AuditEventID)
	}
	assert.Equal(t, 1, changed)
	var eventCount int64
	require.NoError(t, db.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ?", reservation.ReservationID()).Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount)
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusPendingSettlement, record.Status)
	assert.Equal(t, 10, record.ActualQuota)
	assert.Equal(t, 10, record.RequestedQuota)
	assert.NotEmpty(t, fmt.Sprint(auditID))
}
