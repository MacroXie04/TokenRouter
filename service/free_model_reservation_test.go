package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func TestFreeModelReservationSkipsWalletAndSubscriptionHoldsDurably(t *testing.T) {
	for _, test := range []struct {
		name       string
		preference string
		withSub    bool
	}{
		{name: "wallet", preference: BillingPreferenceWalletOnly},
		{name: "subscription", preference: BillingPreferenceSubscriptionOnly, withSub: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := setupRelayQuotaReservationDB(t)
			user := model.User{Username: "free-" + test.name, Status: model.UserStatusEnabled, Quota: 17}
			require.NoError(t, db.Create(&user).Error)
			_, err := UpdateUserBillingPreference(user.Id, test.preference)
			require.NoError(t, err)
			var subscription *model.UserSubscription
			if test.withSub {
				plan := seedRawPlan(t, nil)
				subscription = seedSub(t, user.Id, plan.Id, 100, 0, nil)
			}
			token := model.Token{
				UserId: user.Id, Key: "sk-free-" + test.name,
				Status: TokenStatusEnabled, RemainQuota: 11,
			}
			require.NoError(t, db.Create(&token).Error)
			channel := model.Channel{
				Name: "free-channel-" + test.name, Key: "upstream",
				Status: constant.ChannelStatusEnabled, UsedQuota: 3,
			}
			require.NoError(t, db.Create(&channel).Error)

			reservation, err := NewRelayQuotaReservationWithFreeModel(user.Id, &token, 0, true)
			require.NoError(t, err)
			record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
			assert.Equal(t, BillingSourceFreeModel, record.FundingSource)
			assert.Zero(t, record.RequestedQuota)
			assert.Zero(t, record.ReservedQuota)
			assert.Zero(t, record.TokenReserved)
			assert.Zero(t, record.SubscriptionID)
			assert.False(t, record.TrustQuotaBypassed)
			assert.Equal(t, map[string]any{
				"billing_source":       BillingSourceFreeModel,
				"billing_preference":   test.preference,
				"free_model":           true,
				"relay_reservation_id": reservation.ReservationID(),
			}, reservation.BillingLogFields())

			var ledgers int64
			require.NoError(t, db.Model(&model.SubscriptionPreConsumeRecord{}).Count(&ledgers).Error)
			assert.Zero(t, ledgers)
			if subscription != nil {
				assert.Zero(t, loadSub(t, subscription.Id).AmountUsed)
			}

			require.NoError(t, reservation.MarkDispatched())
			require.NoError(t, reservation.SettleWithChannel(0, channel.Id))
			require.NoError(t, db.First(&user, user.Id).Error)
			require.NoError(t, db.First(&token, token.Id).Error)
			require.NoError(t, db.First(&channel, channel.Id).Error)
			assert.Equal(t, 17, user.Quota)
			assert.Zero(t, user.UsedQuota)
			assert.Equal(t, 1, user.RequestCount)
			assert.Equal(t, 11, token.RemainQuota)
			assert.Zero(t, token.UsedQuota)
			assert.Equal(t, int64(3), channel.UsedQuota)
		})
	}
}

func TestFreeModelReservationPolicyFailsClosedAndDefaultZeroStillPreConsumes(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "free-policy", Status: model.UserStatusEnabled, Quota: 5}
	require.NoError(t, db.Create(&user).Error)
	_, err := UpdateUserBillingPreference(user.Id, BillingPreferenceSubscriptionOnly)
	require.NoError(t, err)
	plan := seedRawPlan(t, nil)
	subscription := seedSub(t, user.Id, plan.Id, 100, 0, nil)
	token := model.Token{UserId: user.Id, Key: "sk-free-policy", Status: TokenStatusEnabled, RemainQuota: 5}
	require.NoError(t, db.Create(&token).Error)

	_, err = NewRelayQuotaReservationWithFreeModel(user.Id, &token, 1, true)
	require.ErrorContains(t, err, "free-model reservation requires zero quota")
	var records int64
	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).Count(&records).Error)
	assert.Zero(t, records)

	reservation, err := NewRelayQuotaReservationWithFreeModel(user.Id, &token, 0, false)
	require.NoError(t, err)
	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, BillingSourceSubscription, record.FundingSource)
	assert.Equal(t, 1, record.ReservedQuota,
		"the default/true policy retains the established subscription minimum hold")
	assert.Equal(t, int64(1), loadSub(t, subscription.Id).AmountUsed)
}

func TestFreeModelReservationPersistenceAndRecoveryRejectCorruptNonZeroState(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "free-persist", Status: model.UserStatusEnabled, Quota: 0}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-free-persist", Status: TokenStatusEnabled, RemainQuota: 0}
	require.NoError(t, db.Create(&token).Error)

	var creation RelayQuotaReservationCreation
	reservation, err := NewRelayQuotaReservationWithFreeModelAndPersistence(
		user.Id, &token, 0, true,
		func(_ *gorm.DB, captured RelayQuotaReservationCreation) error {
			creation = captured
			return nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, BillingSourceFreeModel, creation.Funding.Source)
	assert.Zero(t, creation.Funding.Reserved)
	assert.Zero(t, creation.TokenReserved)

	require.NoError(t, db.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ?", reservation.ReservationID()).
		Update("requested_quota", 1).Error)
	assert.Error(t, reservation.MarkDispatched(),
		"recovery must not infer a free-model decision from an inconsistent zero hold")
}
