package service

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

func setupRelayTrustQuotaDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupRelayQuotaReservationDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	require.NoError(t, setting.Init())
	return db
}

func seedRelayTrustWallet(t *testing.T, db *gorm.DB, userQuota, tokenQuota int, unlimited bool) (model.User, model.Token) {
	t.Helper()
	user := model.User{
		Username: "ordinary-trust-wallet", Status: model.UserStatusEnabled, Quota: userQuota,
	}
	require.NoError(t, db.Create(&user).Error)
	_, err := UpdateUserBillingPreference(user.Id, BillingPreferenceWalletOnly)
	require.NoError(t, err)
	token := model.Token{
		UserId: user.Id, Key: "sk-ordinary-trust-wallet", Status: TokenStatusEnabled,
		RemainQuota: tokenQuota, UnlimitedQuota: unlimited,
	}
	require.NoError(t, db.Create(&token).Error)
	return user, token
}

func TestOrdinaryRelayTrustQuotaStrictThresholdAndFundingPolicy(t *testing.T) {
	const (
		trustQuota = trustQuotaUnits * common.QuotaPerUnit
		estimate   = 10
	)
	tests := []struct {
		name           string
		userQuota      int
		tokenQuota     int
		tokenUnlimited bool
		subscription   bool
		bypassed       bool
	}{
		{name: "user exactly at threshold", userQuota: trustQuota, tokenQuota: trustQuota + 1},
		{name: "token exactly at threshold", userQuota: trustQuota + 1, tokenQuota: trustQuota},
		{name: "both strictly above threshold", userQuota: trustQuota + 1, tokenQuota: trustQuota + 1, bypassed: true},
		{name: "unlimited token and funded wallet", userQuota: trustQuota + 1, tokenUnlimited: true, bypassed: true},
		{name: "subscription never bypasses", userQuota: trustQuota + 1, tokenQuota: trustQuota + 1, subscription: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := setupRelayTrustQuotaDB(t)
			user, token := seedRelayTrustWallet(t, db, test.userQuota, test.tokenQuota, test.tokenUnlimited)
			var subscription *model.UserSubscription
			if test.subscription {
				_, err := UpdateUserBillingPreference(user.Id, BillingPreferenceSubscriptionOnly)
				require.NoError(t, err)
				plan := seedRawPlan(t, nil)
				subscription = seedSub(t, user.Id, plan.Id, 1_000, 0, nil)
			}

			reservation, err := NewOrdinaryRelayQuotaReservation(user.Id, &token, estimate)
			require.NoError(t, err)
			record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
			assert.Equal(t, test.bypassed, record.TrustQuotaBypassed)
			assert.Equal(t, estimate, record.RequestedQuota)
			assert.Equal(t, test.bypassed, reservation.BillingLogFields()["trust_quota_bypassed"] == true)

			var gotUser model.User
			var gotToken model.Token
			require.NoError(t, db.First(&gotUser, user.Id).Error)
			require.NoError(t, db.First(&gotToken, token.Id).Error)
			switch {
			case test.subscription:
				assert.Equal(t, BillingSourceSubscription, record.FundingSource)
				assert.Equal(t, estimate, record.ReservedQuota)
				assert.Equal(t, estimate, record.TokenReserved)
				assert.Equal(t, test.userQuota, gotUser.Quota)
				assert.Equal(t, test.tokenQuota-estimate, gotToken.RemainQuota)
				require.NotNil(t, subscription)
				assert.Equal(t, int64(estimate), loadSub(t, subscription.Id).AmountUsed)
			case test.bypassed:
				assert.Equal(t, BillingSourceWallet, record.FundingSource)
				assert.Zero(t, record.ReservedQuota)
				assert.Zero(t, record.TokenReserved)
				assert.Equal(t, test.userQuota, gotUser.Quota)
				assert.Equal(t, test.tokenQuota, gotToken.RemainQuota)
			default:
				assert.Equal(t, BillingSourceWallet, record.FundingSource)
				assert.Equal(t, estimate, record.ReservedQuota)
				assert.Equal(t, estimate, record.TokenReserved)
				assert.Equal(t, test.userQuota-estimate, gotUser.Quota)
				assert.Equal(t, test.tokenQuota-estimate, gotToken.RemainQuota)
			}
		})
	}

	t.Run("forced reservation callers remain fully held", func(t *testing.T) {
		db := setupRelayTrustQuotaDB(t)
		user, token := seedRelayTrustWallet(t, db, trustQuota+1, trustQuota+1, false)
		reservation, err := NewRelayQuotaReservation(user.Id, &token, estimate)
		require.NoError(t, err)
		record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
		assert.False(t, record.TrustQuotaBypassed)
		assert.Equal(t, estimate, record.ReservedQuota)
		assert.Equal(t, estimate, record.TokenReserved)
		gotUser, gotToken := loadRelayQuotaBalances(t, user.Id, token.Id)
		assert.Equal(t, trustQuota+1-estimate, gotUser.Quota)
		assert.Equal(t, trustQuota+1-estimate, gotToken.RemainQuota)
	})

	t.Run("wallet must still cover the estimate", func(t *testing.T) {
		db := setupRelayTrustQuotaDB(t)
		user, token := seedRelayTrustWallet(t, db, trustQuota+1, 500, false)
		_, err := NewOrdinaryRelayQuotaReservation(user.Id, &token, trustQuota+2)
		require.ErrorIs(t, err, ErrInsufficientQuota)
		gotUser, gotToken := loadRelayQuotaBalances(t, user.Id, token.Id)
		assert.Equal(t, trustQuota+1, gotUser.Quota)
		assert.Equal(t, 500, gotToken.RemainQuota)
		var records int64
		require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).Count(&records).Error)
		assert.Zero(t, records)
	})

	t.Run("trusted token need not cover the estimate until final settlement", func(t *testing.T) {
		db := setupRelayTrustQuotaDB(t)
		user, token := seedRelayTrustWallet(t, db, trustQuota+500, trustQuota+1, false)
		reservation, err := NewOrdinaryRelayQuotaReservation(user.Id, &token, trustQuota+150)
		require.NoError(t, err)
		record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
		assert.True(t, record.TrustQuotaBypassed)
		assert.Zero(t, record.ReservedQuota)
		assert.Zero(t, record.TokenReserved)
		gotUser, gotToken := loadRelayQuotaBalances(t, user.Id, token.Id)
		assert.Equal(t, trustQuota+500, gotUser.Quota)
		assert.Equal(t, trustQuota+1, gotToken.RemainQuota)
	})
}

func TestOrdinaryRelayTrustQuotaFinalChargeRefundAndShortfall(t *testing.T) {
	const (
		trustQuota = trustQuotaUnits * common.QuotaPerUnit
		estimate   = 40
	)
	tests := []struct {
		name              string
		initial           int
		actual            int
		bypassed          bool
		refund            bool
		expectedRemaining int
	}{
		{name: "trusted final charge", initial: trustQuota + 50, actual: 25, bypassed: true, expectedRemaining: trustQuota + 25},
		{name: "held overestimate refund", initial: trustQuota, actual: 25, expectedRemaining: trustQuota - 25},
		{name: "held underestimate shortfall", initial: trustQuota, actual: 55, expectedRemaining: trustQuota - 55},
		{name: "trusted upstream failure refund", initial: trustQuota + 50, bypassed: true, refund: true, expectedRemaining: trustQuota + 50},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := setupRelayTrustQuotaDB(t)
			user, token := seedRelayTrustWallet(t, db, test.initial, test.initial, false)
			reservation, err := NewOrdinaryRelayQuotaReservation(user.Id, &token, estimate)
			require.NoError(t, err)
			require.NoError(t, reservation.MarkDispatched())
			dispatched := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
			assert.Equal(t, test.bypassed, dispatched.TrustQuotaBypassed)
			if test.bypassed {
				assert.Equal(t, estimate, dispatched.ActualQuota,
					"trusted dispatch fallback must retain the requested estimate despite zero holds")
			}

			if test.refund {
				require.NoError(t, reservation.Refund())
				require.NoError(t, reservation.Refund(), "terminal refund is idempotent")
			} else {
				require.NoError(t, reservation.Settle(test.actual))
				require.NoError(t, reservation.Settle(test.actual), "terminal settlement is idempotent")
			}

			gotUser, gotToken := loadRelayQuotaBalances(t, user.Id, token.Id)
			assert.Equal(t, test.expectedRemaining, gotUser.Quota)
			assert.Equal(t, test.expectedRemaining, gotToken.RemainQuota)
			record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
			if test.refund {
				assert.Equal(t, model.RelayQuotaReservationStatusRefunded, record.Status)
				assert.Zero(t, gotUser.UsedQuota)
				assert.Zero(t, gotUser.RequestCount)
				assert.Zero(t, gotToken.UsedQuota)
			} else {
				assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
				assert.Equal(t, test.actual, record.ActualQuota)
				assert.Equal(t, test.actual, gotUser.UsedQuota)
				assert.Equal(t, 1, gotUser.RequestCount)
				assert.Equal(t, test.actual, gotToken.UsedQuota)
			}
		})
	}
}

func TestOrdinaryRelayTrustQuotaInsufficientFinalBalanceIsAtomicAndRecoverable(t *testing.T) {
	const trustQuota = trustQuotaUnits * common.QuotaPerUnit

	t.Run("user shortfall", func(t *testing.T) {
		db := setupRelayTrustQuotaDB(t)
		user, token := seedRelayTrustWallet(t, db, trustQuota+1, trustQuota+50, false)
		reservation, err := NewOrdinaryRelayQuotaReservation(user.Id, &token, 10)
		require.NoError(t, err)
		require.NoError(t, reservation.MarkDispatched())
		require.ErrorIs(t, reservation.Settle(trustQuota+2), ErrInsufficientQuota)
		gotUser, gotToken := loadRelayQuotaBalances(t, user.Id, token.Id)
		assert.Equal(t, trustQuota+1, gotUser.Quota)
		assert.Equal(t, trustQuota+50, gotToken.RemainQuota)
		assert.Zero(t, gotUser.UsedQuota)
		assert.Zero(t, gotToken.UsedQuota)
		assert.Equal(t, model.RelayQuotaReservationStatusPendingSettlement,
			loadRelayQuotaReservationRecord(t, reservation.ReservationID()).Status)
	})

	t.Run("token shortfall recovers after topup", func(t *testing.T) {
		db := setupRelayTrustQuotaDB(t)
		user, token := seedRelayTrustWallet(t, db, trustQuota+50, trustQuota+1, false)
		reservation, err := NewOrdinaryRelayQuotaReservation(user.Id, &token, 10)
		require.NoError(t, err)
		require.NoError(t, reservation.MarkDispatched())
		require.ErrorIs(t, reservation.Settle(trustQuota+2), ErrInsufficientTokenQuota)
		gotUser, gotToken := loadRelayQuotaBalances(t, user.Id, token.Id)
		assert.Equal(t, trustQuota+50, gotUser.Quota, "token failure must roll the user charge back")
		assert.Equal(t, trustQuota+1, gotToken.RemainQuota)
		assert.Zero(t, gotUser.UsedQuota)
		assert.Zero(t, gotToken.UsedQuota)

		require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Update("remain_quota", trustQuota+100).Error)
		require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
			Where("reservation_id = ?", reservation.ReservationID()).
			Updates(map[string]any{"next_attempt_at": 0, "lease_owner": "", "lease_expires_at": 0}).Error)
		require.NoError(t, ReconcileRelayQuotaReservations())
		require.NoError(t, ReconcileRelayQuotaReservations(), "recovery is idempotent")

		gotUser, gotToken = loadRelayQuotaBalances(t, user.Id, token.Id)
		assert.Equal(t, 48, gotUser.Quota)
		assert.Equal(t, 98, gotToken.RemainQuota)
		assert.Equal(t, trustQuota+2, gotUser.UsedQuota)
		assert.Equal(t, 1, gotUser.RequestCount)
		assert.Equal(t, trustQuota+2, gotToken.UsedQuota)
		record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
		assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
		assert.True(t, record.TrustQuotaBypassed)
		assert.Equal(t, trustQuota+2, record.ActualQuota)
	})
}

func TestOrdinaryRelayTrustQuotaConcurrentSettlementCannotOverspend(t *testing.T) {
	const (
		trustQuota = trustQuotaUnits * common.QuotaPerUnit
		actual     = trustQuota + 5
	)
	db := setupRelayTrustQuotaDB(t)
	user, token := seedRelayTrustWallet(t, db, trustQuota+11, trustQuota+11, false)
	reservations := make([]*RelayQuotaReservation, 2)
	for index := range reservations {
		var err error
		reservations[index], err = NewOrdinaryRelayQuotaReservation(user.Id, &token, 1)
		require.NoError(t, err)
		require.True(t, loadRelayQuotaReservationRecord(t, reservations[index].ReservationID()).TrustQuotaBypassed)
		require.NoError(t, reservations[index].MarkDispatched())
	}

	start := make(chan struct{})
	results := make([]error, len(reservations))
	var wg sync.WaitGroup
	for index := range reservations {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index] = reservations[index].Settle(actual)
		}(index)
	}
	close(start)
	wg.Wait()

	succeeded := make([]bool, len(reservations))
	successCount := 0
	for index, err := range results {
		if err == nil {
			succeeded[index] = true
			successCount++
		}
	}
	assert.LessOrEqual(t, successCount, 1, "concurrent zero-hold requests must never both spend the same quota")
	// A transient SQLite writer conflict may reject both first attempts. Retrying
	// serially proves one can commit and the other then fails on live balances.
	if successCount == 0 {
		for index := range reservations {
			if err := reservations[index].Settle(actual); err == nil {
				succeeded[index] = true
				successCount++
				break
			}
		}
	}
	require.Equal(t, 1, successCount)
	for index := range reservations {
		if succeeded[index] {
			continue
		}
		err := reservations[index].Settle(actual)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInsufficientQuota) || errors.Is(err, ErrInsufficientTokenQuota), err)
	}

	gotUser, gotToken := loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, 6, gotUser.Quota)
	assert.Equal(t, 6, gotToken.RemainQuota)
	assert.Equal(t, actual, gotUser.UsedQuota)
	assert.Equal(t, 1, gotUser.RequestCount)
	assert.Equal(t, actual, gotToken.UsedQuota)
	var settled int64
	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
		Where("status = ?", model.RelayQuotaReservationStatusSettled).Count(&settled).Error)
	assert.Equal(t, int64(1), settled)
}

func TestRelayTrustQuotaIgnoresMutableCompatibilityOption(t *testing.T) {
	db := setupRelayTrustQuotaDB(t)
	quota, valid := RelayTrustQuota()
	require.True(t, valid)
	assert.Equal(t, 10*common.QuotaPerUnit, quota)

	require.NoError(t, setting.UpdateOption(setting.QuotaPerUnitOption, "1"))
	quota, valid = RelayTrustQuota()
	require.True(t, valid)
	assert.Equal(t, 10*common.QuotaPerUnit, quota)

	require.NoError(t, db.Model(&model.Option{}).
		Where("key = ?", setting.QuotaPerUnitOption).Update("value", "not-a-number").Error)
	quota, valid = RelayTrustQuota()
	require.True(t, valid)
	assert.Equal(t, 10*common.QuotaPerUnit, quota)
	require.NoError(t, setting.Sync())
	quota, valid = RelayTrustQuota()
	require.True(t, valid)
	assert.Equal(t, 10*common.QuotaPerUnit, quota,
		"database corruption must not change the immutable accounting threshold")

	user, token := seedRelayTrustWallet(t, db, 1_000, 1_000, false)
	reservation, err := NewOrdinaryRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.False(t, record.TrustQuotaBypassed)
	assert.Equal(t, 10, record.ReservedQuota)
	assert.Equal(t, 10, record.TokenReserved)
}

func TestOrdinaryRelayTrustQuotaZeroHoldRequiresDurableMarker(t *testing.T) {
	const trustQuota = trustQuotaUnits * common.QuotaPerUnit
	db := setupRelayTrustQuotaDB(t)
	user, token := seedRelayTrustWallet(t, db, trustQuota+1, trustQuota+1, false)
	reservation, err := NewOrdinaryRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	require.True(t, record.TrustQuotaBypassed)
	require.Zero(t, record.ReservedQuota)
	require.Zero(t, record.TokenReserved)

	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ?", reservation.ReservationID()).
		Update("trust_quota_bypassed", false).Error)
	_, err = RestoreRelayQuotaReservation(reservation.ReservationID())
	require.Error(t, err)
	require.Error(t, reservation.MarkDispatched(),
		"a zero hold without its explicit durable marker must fail before upstream dispatch")

	record = loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusHeld, record.Status)
	gotUser, gotToken := loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, trustQuota+1, gotUser.Quota)
	assert.Equal(t, trustQuota+1, gotToken.RemainQuota)
}

func TestOrdinaryRelayTrustQuotaSettlementOverflowFailsClosed(t *testing.T) {
	const trustQuota = trustQuotaUnits * common.QuotaPerUnit
	db := setupRelayTrustQuotaDB(t)
	user, token := seedRelayTrustWallet(t, db, trustQuota+11, trustQuota+11, false)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", user.Id).
		Update("used_quota", int(common.MaxQuota)).Error)
	reservation, err := NewOrdinaryRelayQuotaReservation(user.Id, &token, 1)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	require.ErrorIs(t, reservation.Settle(1), ErrUserUsageOverflow)

	gotUser, gotToken := loadRelayQuotaBalances(t, user.Id, token.Id)
	assert.Equal(t, trustQuota+11, gotUser.Quota)
	assert.Equal(t, int(common.MaxQuota), gotUser.UsedQuota)
	assert.Zero(t, gotUser.RequestCount)
	assert.Equal(t, trustQuota+11, gotToken.RemainQuota)
	assert.Zero(t, gotToken.UsedQuota)
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.True(t, record.TrustQuotaBypassed)
	assert.Equal(t, model.RelayQuotaReservationStatusPendingSettlement, record.Status)
}
