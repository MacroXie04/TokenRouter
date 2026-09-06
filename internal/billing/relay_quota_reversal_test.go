package billing

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"testing"
)

func TestReverseSettledRelayQuotaReservationRestoresWalletAndTokenExactlyOnce(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "video-reversal-wallet", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-video-reversal-wallet", Status: TokenStatusEnabled, RemainQuota: 100,
	}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: "video-reversal", Key: "upstream", Status: channelcatalog.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)

	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	require.NoError(t, reservation.SettleWithChannel(14, channel.Id))

	persistCalls := 0
	reverse := func(tx *gorm.DB) error {
		persistCalls++
		return nil
	}
	require.NoError(t, ReverseSettledRelayQuotaReservationWithPersistence(reservation.ReservationID(), reverse))
	require.NoError(t, ReverseSettledRelayQuotaReservationWithPersistence(reservation.ReservationID(), reverse),
		"replaying a terminal provider failure must not double-credit")
	assert.Equal(t, 2, persistCalls, "the caller's idempotent task transition is replayed for ambiguous commits")

	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.Unscoped().First(&token, token.Id).Error)
	require.NoError(t, db.First(&channel, channel.Id).Error)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 14, user.UsedQuota, "lifetime usage remains an append-only audit counter")
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Equal(t, 14, token.UsedQuota)
	assert.Equal(t, int64(14), channel.UsedQuota)
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusReversed, record.Status)
	assert.Equal(t, model.RelayQuotaReservationOperationReverse, record.Operation)
	assert.Equal(t, 14, record.ActualQuota)
}

func TestReverseSettledRelayQuotaReservationRestoresSubscriptionEpochUsage(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	now := wallclock.NowTimestamp()
	user := model.User{
		Username: "video-reversal-subscription", Status: model.UserStatusEnabled, Quota: 100,
		Setting: `{"billing_preference":"subscription_only"}`,
	}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-video-reversal-subscription", Status: TokenStatusEnabled, RemainQuota: 100,
	}
	require.NoError(t, db.Create(&token).Error)
	plan := model.SubscriptionPlan{
		Title: "Video", PriceAmount: "1.000000", Currency: "USD", DurationUnit: "month",
		DurationValue: 1, Enabled: true, TotalAmount: 100, QuotaResetPeriod: SubscriptionResetNever,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&plan).Error)
	subscription := model.UserSubscription{
		UserId: user.Id, PlanId: plan.Id, AmountTotal: 100, AmountUsed: 0, UsageEpoch: 7,
		StartTime: now - 60, EndTime: now + 3600, Status: SubscriptionStatusActive,
		Source: "test", AllowWalletOverflow: false, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&subscription).Error)
	channel := model.Channel{Name: "video-sub-reversal", Key: "upstream", Status: channelcatalog.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)

	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	require.NoError(t, reservation.SettleWithChannel(14, channel.Id))
	require.NoError(t, ReverseSettledRelayQuotaReservationWithPersistence(
		reservation.ReservationID(), func(*gorm.DB) error { return nil },
	))

	require.NoError(t, db.First(&subscription, subscription.Id).Error)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.Unscoped().First(&token, token.Id).Error)
	assert.Zero(t, subscription.AmountUsed)
	assert.Equal(t, int64(7), subscription.UsageEpoch)
	assert.Equal(t, 100, user.Quota, "subscription-funded reversal never touches the wallet")
	assert.Equal(t, 14, user.UsedQuota)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Equal(t, 14, token.UsedQuota)
}

func TestReverseSettledRelayQuotaReservationRollsBackCallerPersistenceFailure(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "video-reversal-rollback", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-video-reversal-rollback", Status: TokenStatusEnabled, RemainQuota: 100,
	}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: "video-reversal-rollback", Key: "upstream", Status: channelcatalog.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	require.NoError(t, reservation.SettleWithChannel(14, channel.Id))

	injected := errors.New("injected task transition failure")
	err = ReverseSettledRelayQuotaReservationWithPersistence(
		reservation.ReservationID(), func(*gorm.DB) error { return injected },
	)
	require.ErrorIs(t, err, injected)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.Unscoped().First(&token, token.Id).Error)
	assert.Equal(t, 86, user.Quota)
	assert.Equal(t, 86, token.RemainQuota)
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	assert.Equal(t, model.RelayQuotaReservationOperationSettle, record.Operation)
}
