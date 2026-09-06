package relay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

type jimengManualReviewFixture struct {
	db          *gorm.DB
	user        model.User
	token       model.Token
	channel     model.Channel
	task        model.Task
	reservation *service.RelayQuotaReservation
}

func createJimengManualReviewFixture(t *testing.T) jimengManualReviewFixture {
	t.Helper()
	t.Setenv("JIMENG_RECOVERY_DIR", t.TempDir())
	db, user, token, channel := setupJimengOperationDB(t)
	task, privateData := newAtomicJimengTask(user, channel)
	task.Properties = `{"upstream_model_name":"jimeng-review","origin_model_name":"jimeng-review"}`
	privateData.ChannelBaseURL = "https://provider.example.invalid"
	var err error
	privateData.EncryptedChannelKey, err = jimengEncrypt(channel.Key)
	require.NoError(t, err)
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(
		&task, reservation, channel.Id, task.Properties, task.PrivateData,
	))
	now, err := model.DatabaseUnixTimestamp(db)
	require.NoError(t, err)
	require.NoError(t, reconcileJimengTaskOperationsAt(now+jimengDispatchRecoverySeconds+1, 1))
	require.NoError(t, db.First(&task, task.ID).Error)
	var operation model.JimengTaskOperation
	require.NoError(t, db.Where("reservation_id = ?", reservation.ReservationID()).First(&operation).Error)
	require.Equal(t, model.TaskStatusUnknown, task.Status)
	require.Equal(t, jimengManualReviewReason, task.FailReason)
	require.Equal(t, model.JimengTaskOperationManualReview, operation.State)
	return jimengManualReviewFixture{
		db: db, user: user, token: token, channel: channel, task: task, reservation: reservation,
	}
}

func TestResolveJimengManualReviewSettleIsAtomicAuditedAndIdempotent(t *testing.T) {
	fixture := createJimengManualReviewFixture(t)
	result, err := ResolveJimengRelayQuotaReservationReview(
		fixture.reservation.ReservationID(), 7001, service.RelayQuotaReviewResolutionSettle,
	)
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Equal(t, fixture.task.TaskID, result.TaskID)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, result.ReservationStatus)
	assert.Equal(t, model.TaskStatusUnknown, result.TaskStatus)
	assert.Equal(t, model.JimengTaskOperationUnknown, result.OperationState)
	assert.NotEmpty(t, result.AuditEventID)

	require.NoError(t, fixture.db.First(&fixture.user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&fixture.token, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&fixture.channel, fixture.channel.Id).Error)
	assert.Equal(t, 90, fixture.user.Quota)
	assert.Equal(t, 10, fixture.user.UsedQuota)
	assert.Equal(t, 1, fixture.user.RequestCount)
	assert.Equal(t, 90, fixture.token.RemainQuota)
	assert.Equal(t, 10, fixture.token.UsedQuota)
	assert.Equal(t, int64(10), fixture.channel.UsedQuota)

	var record model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("reservation_id = ?", fixture.reservation.ReservationID()).First(&record).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	assert.Equal(t, model.RelayQuotaReservationOperationSettle, record.Operation)
	assert.Equal(t, fixture.task.Quota, record.ActualQuota)
	assert.Equal(t, fixture.channel.Id, record.ChannelID)
	require.NoError(t, fixture.db.First(&fixture.task, fixture.task.ID).Error)
	assert.Equal(t, jimengManualSettlementReason, fixture.task.FailReason)
	assert.NotContains(t, fixture.task.PrivateData, "channel_base_url")
	assert.NotContains(t, fixture.task.PrivateData, "encrypted_channel_key")
	var operation model.JimengTaskOperation
	require.NoError(t, fixture.db.Where("reservation_id = ?", record.ReservationID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationUnknown, operation.State)
	assert.Empty(t, operation.LastError)
	assert.Empty(t, operation.LeaseOwner)

	var reviewEvent model.RelayQuotaReservationReviewEvent
	require.NoError(t, fixture.db.Where("event_id = ?", result.AuditEventID).First(&reviewEvent).Error)
	assert.Equal(t, 7001, reviewEvent.OperatorUserID)
	assert.Equal(t, service.RelayQuotaReviewActionResolveSettle, reviewEvent.Action)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reviewEvent.FromStatus)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reviewEvent.ToStatus)
	assert.Equal(t, fixture.task.Quota, reviewEvent.ActualQuota)
	assert.Equal(t, fixture.channel.Id, reviewEvent.ChannelID)
	var consumeAuditCount int64
	require.NoError(t, fixture.db.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", jimengAuditEventID(record.ReservationID)).Count(&consumeAuditCount).Error)
	assert.EqualValues(t, 1, consumeAuditCount)

	replay, err := ResolveJimengRelayQuotaReservationReview(
		record.ReservationID, 7002, service.RelayQuotaReviewResolutionSettle,
	)
	require.NoError(t, err)
	assert.False(t, replay.Changed)
	assert.Equal(t, result.AuditEventID, replay.AuditEventID)
	_, err = ResolveJimengRelayQuotaReservationReview(
		record.ReservationID, 7002, service.RelayQuotaReviewResolutionRefund,
	)
	assert.ErrorIs(t, err, service.ErrRelayQuotaReviewInvalidState)
	assertJimengResolutionEventCounts(t, fixture.db, record.ReservationID, 1, 1)
	require.NoError(t, fixture.db.First(&fixture.user, fixture.user.Id).Error)
	assert.Equal(t, 10, fixture.user.UsedQuota, "replay and conflict cannot charge twice")
}

func TestResolveJimengManualReviewRefundIsAtomicAuditedAndIdempotent(t *testing.T) {
	fixture := createJimengManualReviewFixture(t)
	result, err := ResolveJimengRelayQuotaReservationReview(
		fixture.reservation.ReservationID(), 7101, service.RelayQuotaReviewResolutionRefund,
	)
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, result.ReservationStatus)
	assert.Equal(t, model.TaskStatusFailure, result.TaskStatus)
	assert.Equal(t, model.JimengTaskOperationRefunded, result.OperationState)

	require.NoError(t, fixture.db.First(&fixture.user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&fixture.token, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&fixture.channel, fixture.channel.Id).Error)
	assert.Equal(t, 100, fixture.user.Quota)
	assert.Zero(t, fixture.user.UsedQuota)
	assert.Zero(t, fixture.user.RequestCount)
	assert.Equal(t, 100, fixture.token.RemainQuota)
	assert.Zero(t, fixture.token.UsedQuota)
	assert.Zero(t, fixture.channel.UsedQuota)

	var record model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("reservation_id = ?", fixture.reservation.ReservationID()).First(&record).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, record.Status)
	assert.Equal(t, model.RelayQuotaReservationOperationRefund, record.Operation)
	assert.Zero(t, record.ActualQuota)
	assert.Zero(t, record.ChannelID)
	require.NoError(t, fixture.db.First(&fixture.task, fixture.task.ID).Error)
	assert.Equal(t, model.TaskStatusFailure, fixture.task.Status)
	assert.Equal(t, jimengManualRefundReason, fixture.task.FailReason)
	assert.NotContains(t, fixture.task.PrivateData, "channel_base_url")
	assert.NotContains(t, fixture.task.PrivateData, "encrypted_channel_key")
	var operation model.JimengTaskOperation
	require.NoError(t, fixture.db.Where("reservation_id = ?", record.ReservationID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationRefunded, operation.State)
	assert.Empty(t, operation.LastError)

	var reviewEvent model.RelayQuotaReservationReviewEvent
	require.NoError(t, fixture.db.Where("event_id = ?", result.AuditEventID).First(&reviewEvent).Error)
	assert.Equal(t, 7101, reviewEvent.OperatorUserID)
	assert.Equal(t, service.RelayQuotaReviewActionResolveRefund, reviewEvent.Action)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reviewEvent.ToStatus)
	assert.Zero(t, reviewEvent.ActualQuota)
	assert.Equal(t, fixture.channel.Id, reviewEvent.ChannelID,
		"the audit preserves the immutable provider channel even though refund clears it from the ledger")

	replay, err := ResolveJimengRelayQuotaReservationReview(
		record.ReservationID, 7102, service.RelayQuotaReviewResolutionRefund,
	)
	require.NoError(t, err)
	assert.False(t, replay.Changed)
	assert.Equal(t, result.AuditEventID, replay.AuditEventID)
	_, err = ResolveJimengRelayQuotaReservationReview(
		record.ReservationID, 7102, service.RelayQuotaReviewResolutionSettle,
	)
	assert.ErrorIs(t, err, service.ErrRelayQuotaReviewInvalidState)
	assertJimengResolutionEventCounts(t, fixture.db, record.ReservationID, 1, 0)
}

func TestResolveJimengManualReviewRejectsDriftAndAuditFailureRollsBack(t *testing.T) {
	t.Run("immutable funding snapshot mismatch", func(t *testing.T) {
		fixture := createJimengManualReviewFixture(t)
		privateData, err := decodeJimengTaskPrivateDataStored(fixture.task.PrivateData)
		require.NoError(t, err)
		privateData.FundingReserved++
		fixture.task.PrivateData, err = marshalJimengTaskPrivateData(privateData)
		require.NoError(t, err)
		require.NoError(t, fixture.db.Model(&model.Task{}).Where("id = ?", fixture.task.ID).
			Update("private_data", fixture.task.PrivateData).Error)

		_, err = ResolveJimengRelayQuotaReservationReview(
			fixture.reservation.ReservationID(), 7201, service.RelayQuotaReviewResolutionSettle,
		)
		assert.ErrorIs(t, err, service.ErrRelayQuotaReviewUnsafeRetry)
		assertJimengUnresolvedHold(t, fixture)
		assertJimengResolutionEventCounts(t, fixture.db, fixture.reservation.ReservationID(), 0, 0)
	})

	t.Run("review audit append failure", func(t *testing.T) {
		fixture := createJimengManualReviewFixture(t)
		require.NoError(t, fixture.db.Migrator().DropTable(&model.RelayQuotaReservationReviewEvent{}))
		_, err := ResolveJimengRelayQuotaReservationReview(
			fixture.reservation.ReservationID(), 7202, service.RelayQuotaReviewResolutionSettle,
		)
		require.Error(t, err)
		assertJimengUnresolvedHold(t, fixture)
		var outboxCount int64
		require.NoError(t, fixture.db.Model(&model.AuditLogOutbox{}).Count(&outboxCount).Error)
		assert.Zero(t, outboxCount, "consume audit must roll back with the failed operator audit")
	})
}

func assertJimengResolutionEventCounts(
	t *testing.T,
	db *gorm.DB,
	reservationID string,
	reviewEvents, consumeEvents int64,
) {
	t.Helper()
	var reviewCount int64
	require.NoError(t, db.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ? AND action IN ?", reservationID,
			[]string{service.RelayQuotaReviewActionResolveSettle, service.RelayQuotaReviewActionResolveRefund}).
		Count(&reviewCount).Error)
	assert.EqualValues(t, reviewEvents, reviewCount)
	var consumeCount int64
	require.NoError(t, db.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", jimengAuditEventID(reservationID)).Count(&consumeCount).Error)
	assert.EqualValues(t, consumeEvents, consumeCount)
}

func assertJimengUnresolvedHold(t *testing.T, fixture jimengManualReviewFixture) {
	t.Helper()
	var record model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("reservation_id = ?", fixture.reservation.ReservationID()).First(&record).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, record.Status)
	var task model.Task
	require.NoError(t, fixture.db.First(&task, fixture.task.ID).Error)
	assert.Equal(t, model.TaskStatusUnknown, task.Status)
	assert.Equal(t, jimengManualReviewReason, task.FailReason)
	var operation model.JimengTaskOperation
	require.NoError(t, fixture.db.Where("reservation_id = ?", record.ReservationID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationManualReview, operation.State)
	require.NoError(t, fixture.db.First(&fixture.user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&fixture.token, fixture.token.Id).Error)
	assert.Equal(t, 90, fixture.user.Quota)
	assert.Zero(t, fixture.user.UsedQuota)
	assert.Equal(t, 90, fixture.token.RemainQuota)
	assert.Zero(t, fixture.token.UsedQuota)
}
