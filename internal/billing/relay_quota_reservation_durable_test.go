package billing

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func loadRelayQuotaReservationRecord(t *testing.T, reservationId string) model.RelayQuotaReservationRecord {
	t.Helper()
	var record model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("reservation_id = ?", reservationId).First(&record).Error)
	return record
}

func loadRelayQuotaBalances(t *testing.T, userId, tokenId int) (model.User, model.Token) {
	t.Helper()
	var user model.User
	var token model.Token
	require.NoError(t, model.DB.First(&user, userId).Error)
	require.NoError(t, model.DB.First(&token, tokenId).Error)
	return user, token
}

func TestRelayQuotaReservationCreationRollsBackHoldsWhenLedgerWriteFails(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-create-failure", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-durable-create-failure", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)

	injected := errors.New("injected durable ledger create failure")
	callbackName := "test:fail_relay_reservation_create"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.RelayQuotaReservationRecord{}).TableName() {
			tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	_, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.ErrorIs(t, err, injected)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 100, token.RemainQuota)
	var records int64
	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).Count(&records).Error)
	assert.Zero(t, records)
}

func TestSubscriptionTokenRelayReservationRollsBackEveryCreationBoundary(t *testing.T) {
	cases := []struct {
		name      string
		operation string
		table     string
	}{
		{name: "subscription update", operation: "update", table: (model.UserSubscription{}).TableName()},
		{name: "subscription ledger create", operation: "create", table: (model.SubscriptionPreConsumeRecord{}).TableName()},
		{name: "token update", operation: "update", table: (model.Token{}).TableName()},
		{name: "reservation ledger create", operation: "create", table: (model.RelayQuotaReservationRecord{}).TableName()},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db := setupRelayQuotaReservationDB(t)
			user := model.User{
				Username: "subscription-token-rollback", Status: model.UserStatusEnabled,
				Setting: `{"billing_preference":"subscription_only"}`,
			}
			require.NoError(t, db.Create(&user).Error)
			plan := model.SubscriptionPlan{Title: "Rollback", PriceAmount: "0", Enabled: true}
			require.NoError(t, db.Create(&plan).Error)
			subscription := model.UserSubscription{
				UserId: user.Id, PlanId: plan.Id, AmountTotal: 100, AmountUsed: 0,
				EndTime: wallclock.NowTimestamp() + 3600, Status: SubscriptionStatusActive,
				AllowWalletOverflow: false,
			}
			require.NoError(t, db.Create(&subscription).Error)
			token := model.Token{
				UserId: user.Id, Key: "sk-subscription-token-rollback", Status: TokenStatusEnabled,
				RemainQuota: 100,
			}
			require.NoError(t, db.Create(&token).Error)

			injected := errors.New("injected " + test.name + " failure")
			callbackName := "test:fail_subscription_token_creation_" + strings.ReplaceAll(test.name, " ", "_")
			callback := func(tx *gorm.DB) {
				if tx.Statement.Table == test.table {
					tx.AddError(injected)
				}
			}
			if test.operation == "update" {
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, callback))
				t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })
			} else {
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, callback))
				t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
			}

			_, err := NewRelayQuotaReservation(user.Id, &token, 10)
			require.ErrorIs(t, err, injected)
			require.NoError(t, db.First(&subscription, subscription.Id).Error)
			require.NoError(t, db.First(&token, token.Id).Error)
			assert.Zero(t, subscription.AmountUsed)
			assert.Equal(t, 100, token.RemainQuota)
			var preConsumeRows, reservationRows int64
			require.NoError(t, db.Model(&model.SubscriptionPreConsumeRecord{}).Count(&preConsumeRows).Error)
			require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).Count(&reservationRows).Error)
			assert.Zero(t, preConsumeRows)
			assert.Zero(t, reservationRows)
		})
	}
}

func TestRelayQuotaReservationSupportsPlaygroundSyntheticUnlimitedToken(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-playground", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)

	reservation, err := NewRelayQuotaReservation(user.Id, &model.Token{
		UserId: user.Id, Name: "playground", UnlimitedQuota: true,
	}, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.Settle(7))

	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Zero(t, record.TokenID)
	assert.True(t, record.TokenUnlimited)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 93, user.Quota)
	assert.Equal(t, 7, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
}

func TestRelayQuotaReservationAmbiguousCreationRecoversByUniqueMarker(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-ambiguous-create", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-durable-ambiguous-create", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)

	var injected atomic.Bool
	reservation, err := createDurableRelayQuotaReservationWithTransaction(
		user.Id,
		&token,
		10,
		func(fn func(tx *gorm.DB) error) error {
			err := db.Transaction(fn)
			if err == nil && injected.CompareAndSwap(false, true) {
				return errors.New("injected ambiguous creation commit result")
			}
			return err
		},
	)
	require.NoError(t, err)
	require.NotEmpty(t, reservation.ReservationID())
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusHeld, record.Status)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 90, token.RemainQuota)
	var records int64
	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).Count(&records).Error)
	assert.Equal(t, int64(1), records)
}

func TestRelayQuotaReservationAmbiguousDispatchUsesDurableMarker(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-ambiguous-dispatch", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-durable-ambiguous-dispatch", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)

	var injected atomic.Bool
	reservation.transact = func(fn func(tx *gorm.DB) error) error {
		err := db.Transaction(fn)
		if err == nil && injected.CompareAndSwap(false, true) {
			return errors.New("injected ambiguous dispatch commit result")
		}
		return err
	}
	require.NoError(t, reservation.MarkDispatched())
	reservation.transact = nil
	require.NoError(t, reservation.MarkDispatched(), "dispatch transition is idempotent")

	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, record.Status)
	assert.Equal(t, model.RelayQuotaReservationOperationSettle, record.Operation)
	assert.Equal(t, 10, record.ActualQuota)
	assert.Positive(t, record.DispatchedAt)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 90, token.RemainQuota)
}

func TestRelayQuotaSettlementFailureIsRecoveredFromDurableIntent(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-settle-retry", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-durable-settle-retry", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_durable_settle_user_update"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.User{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected user settlement failure"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	err = reservation.Settle(14)
	require.ErrorContains(t, err, "injected user settlement failure")
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusPendingSettlement, record.Status)
	assert.Equal(t, 14, record.ActualQuota)
	assert.Equal(t, 1, record.Attempts)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, token.UsedQuota)

	now := wallclock.NowTimestamp()
	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ?", reservation.ReservationID()).
		Update("next_attempt_at", now-1).Error)
	require.NoError(t, reconcileRelayQuotaReservationsAt(now, 10))
	record = loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 86, user.Quota)
	assert.Equal(t, 14, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 86, token.RemainQuota)
	assert.Equal(t, 14, token.UsedQuota)
}

func TestRelayQuotaChannelFailureRollsBackAndRecoversAllAccounting(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-channel-retry", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-durable-channel-retry", Status: TokenStatusEnabled,
		UnlimitedQuota: true, RemainQuota: 37, UsedQuota: 5,
	}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{
		Name: "saturated-channel", Key: "upstream", Status: channelcatalog.ChannelStatusEnabled,
		UsedQuota: math.MaxInt64,
	}
	require.NoError(t, db.Create(&channel).Error)

	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	err = reservation.SettleWithChannel(14, channel.Id)
	require.ErrorIs(t, err, ErrChannelUsageOverflow)

	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusPendingSettlement, record.Status)
	assert.Equal(t, channel.Id, record.ChannelID)
	assert.True(t, record.TokenUnlimited)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	require.NoError(t, db.First(&channel, channel.Id).Error)
	assert.Equal(t, 90, user.Quota, "the original wallet hold remains after rollback")
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, 37, token.RemainQuota)
	assert.Equal(t, 5, token.UsedQuota)
	assert.Equal(t, int64(math.MaxInt64), channel.UsedQuota)

	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channel.Id).
		Update("used_quota", int64(7)).Error)
	now := wallclock.NowTimestamp()
	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ?", reservation.ReservationID()).
		Update("next_attempt_at", now-1).Error)
	require.NoError(t, reconcileRelayQuotaReservationsAt(now, 10))

	record = loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	require.NoError(t, db.First(&channel, channel.Id).Error)
	assert.Equal(t, 86, user.Quota)
	assert.Equal(t, 14, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 37, token.RemainQuota)
	assert.Equal(t, 19, token.UsedQuota)
	assert.Equal(t, int64(21), channel.UsedQuota)
}

func TestRelayQuotaRefundFailureRollsBackAndWorkerRetries(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-refund-retry", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-durable-refund-retry", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_durable_refund_token_update"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Token{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected token refund failure"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	err = reservation.Refund()
	require.ErrorContains(t, err, "injected token refund failure")
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusPendingRefund, record.Status)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 90, user.Quota, "wallet refund must roll back with token failure")
	assert.Equal(t, 90, token.RemainQuota)

	now := wallclock.NowTimestamp()
	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ?", reservation.ReservationID()).
		Update("next_attempt_at", now-1).Error)
	require.NoError(t, reconcileRelayQuotaReservationsAt(now, 10))
	record = loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, record.Status)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 100, token.RemainQuota)
}

func TestExpiredRelayQuotaHoldIsRefundedByRecoverySweep(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-stale-hold", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-durable-stale-hold", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)

	now := wallclock.NowTimestamp()
	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ?", reservation.ReservationID()).
		Update("expires_at", now-1).Error)
	require.NoError(t, reconcileRelayQuotaReservationsAt(now, 10))
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, record.Status)
	assert.Equal(t, model.RelayQuotaReservationOperationRefund, record.Operation)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 100, token.RemainQuota)
}

func TestExpiredDispatchedRelayQuotaHoldRequiresManualReviewWithoutRefund(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-stale-dispatched", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-durable-stale-dispatched", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())

	now := wallclock.NowTimestamp()
	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ?", reservation.ReservationID()).
		Update("expires_at", now-1).Error)
	require.NoError(t, reconcileRelayQuotaReservationsAt(now, 10))
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusManualReview, record.Status)
	assert.Equal(t, model.RelayQuotaReservationOperationSettle, record.Operation)
	assert.Equal(t, 10, record.ActualQuota, "reserved quota remains the conservative settlement candidate")
	assert.Contains(t, record.LastError, "dispatched reservation expired")
	assert.Positive(t, record.CompletedAt)
	assert.ErrorIs(t, reservation.Refund(), ErrRelayQuotaManualReview)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 90, user.Quota, "possibly consumed funding remains held")
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 90, token.RemainQuota, "possibly consumed token quota remains held")
	assert.Zero(t, token.UsedQuota)
}

func TestRelayQuotaReservationManualReviewCapsPersistedError(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-error-cap", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-durable-error-cap", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())

	longError := errors.New(strings.Repeat("界", maxRelayReservationErrorBytes))
	require.Error(t, markInvalidRelayQuotaReservationForReview(&record, longError, wallclock.NowTimestamp()))
	record = loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.LessOrEqual(t, len(record.LastError), maxRelayReservationErrorBytes)
	assert.True(t, utf8.ValidString(record.LastError))
	assert.True(t, strings.HasSuffix(record.LastError, "... [truncated]"))
}

func TestRelayQuotaReservationConcurrentDurableClaimsSettleOnce(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-concurrent-claims", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-durable-concurrent-claims", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, prepareRelayQuotaOperation(
		reservation.ReservationID(), model.RelayQuotaReservationOperationSettle, 10, 0,
	))
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())

	const workers = 8
	var group sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		workerReservation, loadErr := relayReservationFromRecord(&record)
		require.NoError(t, loadErr)
		group.Add(1)
		go func() {
			defer group.Done()
			errs <- reconcileRelayQuotaSettlement(workerReservation, true)
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	record = loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
}

func TestRelayQuotaReservationExpiredProcessingLeaseIsRecovered(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-expired-processing-lease", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-durable-expired-processing-lease", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, prepareRelayQuotaOperation(
		reservation.ReservationID(), model.RelayQuotaReservationOperationSettle, 10, 0,
	))

	now := wallclock.NowTimestamp()
	record, claimed, err := claimRelayQuotaReservation(
		reservation.ReservationID(),
		model.RelayQuotaReservationStatusPendingSettlement,
		"crashed-worker",
		now,
		true,
	)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, 1, record.Attempts, "claiming consumes an attempt even if the process dies")
	recoveryTime := record.LeaseExpiresAt + 1
	require.NoError(t, reconcileRelayQuotaReservationsAt(recoveryTime, 10))

	stored := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, stored.Status)
	assert.Equal(t, 2, stored.Attempts)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
}

func TestRelayQuotaReservationInterruptedFinalAttemptBecomesManualReview(t *testing.T) {
	t.Setenv("RELAY_RESERVATION_MAX_ATTEMPTS", "1")
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-interrupted-final-attempt", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-durable-interrupted-final-attempt", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, prepareRelayQuotaOperation(
		reservation.ReservationID(), model.RelayQuotaReservationOperationSettle, 10, 0,
	))

	now := wallclock.NowTimestamp()
	record, claimed, err := claimRelayQuotaReservation(
		reservation.ReservationID(),
		model.RelayQuotaReservationStatusPendingSettlement,
		"crashed-final-worker",
		now,
		true,
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, reconcileRelayQuotaReservationsAt(record.LeaseExpiresAt+1, 10))

	stored := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusManualReview, stored.Status)
	assert.Contains(t, stored.LastError, "attempt limit")
	assert.Positive(t, stored.CompletedAt)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 90, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
}

func TestRelayQuotaReservationAmbiguousCommitUsesTerminalMarker(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-ambiguous-settle", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-durable-ambiguous-settle", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)

	var injected atomic.Bool
	reservation.transact = func(fn func(tx *gorm.DB) error) error {
		err := db.Transaction(fn)
		if err == nil && injected.CompareAndSwap(false, true) {
			return errors.New("injected ambiguous commit result")
		}
		return err
	}
	require.NoError(t, reservation.Settle(10), "the committed terminal marker resolves the ambiguous result")
	reservation.transact = nil
	require.NoError(t, reservation.Settle(10), "retry must not apply accounting twice")

	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
}

func TestRelayQuotaReservationAmbiguousRefundUsesTerminalMarker(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "durable-ambiguous-refund", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-durable-ambiguous-refund", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)

	var injected atomic.Bool
	reservation.transact = func(fn func(tx *gorm.DB) error) error {
		err := db.Transaction(fn)
		if err == nil && injected.CompareAndSwap(false, true) {
			return errors.New("injected ambiguous refund commit result")
		}
		return err
	}
	require.NoError(t, reservation.Refund())
	reservation.transact = nil
	require.NoError(t, reservation.Refund())

	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, record.Status)
	user, token = loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 100, token.RemainQuota)
}

func TestExpiredSubscriptionRelayHoldIsDurablyRefunded(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{
		Username: "durable-subscription-stale", Status: model.UserStatusEnabled,
		Setting: `{"billing_preference":"subscription_only"}`,
	}
	require.NoError(t, db.Create(&user).Error)
	plan := model.SubscriptionPlan{Title: "Durable", PriceAmount: "0", Enabled: true}
	require.NoError(t, db.Create(&plan).Error)
	subscription := model.UserSubscription{
		UserId: user.Id, PlanId: plan.Id, AmountTotal: 100,
		EndTime: wallclock.NowTimestamp() + 3600, Status: SubscriptionStatusActive,
		AllowWalletOverflow: true,
	}
	require.NoError(t, db.Create(&subscription).Error)
	unlimited := model.Token{UserId: user.Id, Key: "sk-durable-subscription-stale", UnlimitedQuota: true, Status: TokenStatusEnabled}
	require.NoError(t, db.Create(&unlimited).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &unlimited, 10)
	require.NoError(t, err)
	require.NoError(t, db.First(&subscription, subscription.Id).Error)
	assert.Equal(t, int64(10), subscription.AmountUsed)

	now := wallclock.NowTimestamp()
	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ?", reservation.ReservationID()).
		Update("expires_at", now-1).Error)
	require.NoError(t, reconcileRelayQuotaReservationsAt(now, 10))
	require.NoError(t, db.First(&subscription, subscription.Id).Error)
	assert.Zero(t, subscription.AmountUsed)
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, record.Status)
	var ledger model.SubscriptionPreConsumeRecord
	require.NoError(t, db.Where("request_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, SubscriptionPreConsumeStatusRefunded, ledger.Status)
}

func TestSubscriptionRelayRefundRecoversWhenAuxiliaryLedgerWasPruned(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{
		Username: "durable-subscription-pruned-ledger", Status: model.UserStatusEnabled,
		Setting: `{"billing_preference":"subscription_only"}`,
	}
	require.NoError(t, db.Create(&user).Error)
	plan := model.SubscriptionPlan{Title: "Durable", PriceAmount: "0", Enabled: true}
	require.NoError(t, db.Create(&plan).Error)
	subscription := model.UserSubscription{
		UserId: user.Id, PlanId: plan.Id, AmountTotal: 100,
		EndTime: wallclock.NowTimestamp() + 3600, Status: SubscriptionStatusActive,
		AllowWalletOverflow: true,
	}
	require.NoError(t, db.Create(&subscription).Error)
	unlimited := model.Token{UserId: user.Id, Key: "sk-durable-subscription-pruned", UnlimitedQuota: true, Status: TokenStatusEnabled}
	require.NoError(t, db.Create(&unlimited).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &unlimited, 10)
	require.NoError(t, err)
	require.NoError(t, db.Where("request_id = ?", reservation.ReservationID()).
		Delete(&model.SubscriptionPreConsumeRecord{}).Error)

	require.NoError(t, reservation.Refund())
	require.NoError(t, reservation.Refund(), "terminal relay marker keeps fallback refund idempotent")
	require.NoError(t, db.First(&subscription, subscription.Id).Error)
	assert.Zero(t, subscription.AmountUsed)
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, record.Status)
}
