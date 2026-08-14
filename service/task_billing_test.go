package service

import (
	"errors"
	"math"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestComputePerCallQuotaAndTokenReservation(t *testing.T) {
	previousPrices := ExportedModelPrices()
	previousRatios := ExportedGroupRatios()
	t.Cleanup(func() {
		SetModelPriceRegistry(previousPrices)
		SetGroupRatios(previousRatios)
	})
	SetModelPriceRegistry(map[string]ModelPrice{
		"jimeng-short": {Prompt: 0.01},
		"free-task":    {Prompt: 0},
		"overflow":     {Prompt: math.MaxFloat64},
	})
	SetGroupRatios(map[string]float64{"default": 1, "vip": 2})

	quota, err := ComputePerCallQuota("jimeng-short", "default", 1)
	require.NoError(t, err)
	assert.Equal(t, 5000, quota)
	quota, err = ComputePerCallQuota("jimeng-short", "vip", 2)
	require.NoError(t, err)
	assert.Equal(t, 20000, quota)
	quota, err = ComputePerCallQuota("free-task", "default", 1)
	require.NoError(t, err)
	assert.Zero(t, quota)
	_, err = ComputePerCallQuota("jimeng-short", "default", 0)
	assert.EqualError(t, err, "per-call billing units must be positive")
	_, err = ComputePerCallQuota("overflow", "default", 2)
	assert.Error(t, err, "saturating task prices must fail closed")

	dsn := "file:" + filepath.Join(t.TempDir(), "token-reservation.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Token{}))
	model.DB = db
	token := model.Token{Key: "sk-task-reservation", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, model.DB.Create(&token).Error)

	require.NoError(t, ReserveTokenQuota(token.Id, 60))
	assert.ErrorIs(t, ReserveTokenQuota(token.Id, 50), ErrInsufficientTokenQuota)
	require.NoError(t, RefundTokenQuotaReservation(token.Id, 60))
	require.NoError(t, ReserveTokenQuota(token.Id, 100))
	require.NoError(t, CommitTokenQuotaReservation(token.Id, 100))
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Zero(t, token.RemainQuota)
	assert.Equal(t, 100, token.UsedQuota)
	assert.True(t, errors.Is(ReserveTokenQuota(token.Id, 1), ErrInsufficientTokenQuota))
}

func TestCommitAcceptedPerCallIsAtomicAndIdempotent(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "accepted-per-call.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Token{}, &model.Task{}, &model.UserSubscription{},
	))
	model.DB = db

	user := model.User{Username: "accepted-task", Quota: 100, Status: model.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-accepted-task", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	funding, err := NewFundingSession(user.Id, 60)
	require.NoError(t, err)
	require.NoError(t, ReserveTokenQuota(token.Id, 60))

	persistErr := errors.New("forced persistence failure")
	err = funding.CommitAcceptedPerCall(60, token.Id, func(tx *gorm.DB) (bool, error) {
		require.NoError(t, tx.Create(&model.Task{TaskID: "rolled-back-task"}).Error)
		return false, persistErr
	})
	require.ErrorIs(t, err, persistErr)
	var taskCount int64
	require.NoError(t, db.Model(&model.Task{}).Count(&taskCount).Error)
	assert.Zero(t, taskCount)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 40, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, 40, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)

	callbackCalls := 0
	err = funding.CommitAcceptedPerCall(60, token.Id, func(tx *gorm.DB) (bool, error) {
		callbackCalls++
		return false, tx.Create(&model.Task{TaskID: "committed-task", Status: model.TaskStatusSubmitted}).Error
	})
	require.NoError(t, err)
	require.NoError(t, funding.CommitAcceptedPerCall(60, token.Id, func(*gorm.DB) (bool, error) {
		callbackCalls++
		return false, nil
	}))
	assert.Equal(t, 1, callbackCalls)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 40, user.Quota)
	assert.Equal(t, 60, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 40, token.RemainQuota)
	assert.Equal(t, 60, token.UsedQuota)

	freeUser := model.User{Username: "free-subscription-task", Status: model.UserStatusEnabled}
	require.NoError(t, db.Create(&freeUser).Error)
	subscription := model.UserSubscription{UserId: freeUser.Id, AmountTotal: 10, AmountUsed: 1}
	require.NoError(t, db.Create(&subscription).Error)
	freeFunding := &FundingSession{
		userId: freeUser.Id, source: BillingSourceSubscription,
		reserved: 1, subscriptionId: subscription.Id, subTotal: 10, subUsedAfter: 1,
	}
	require.NoError(t, freeFunding.CommitAcceptedPerCall(0, 0, nil))
	require.NoError(t, db.First(&subscription, subscription.Id).Error)
	require.NoError(t, db.First(&freeUser, freeUser.Id).Error)
	assert.Zero(t, subscription.AmountUsed)
	assert.Zero(t, freeUser.UsedQuota)
	assert.Equal(t, 1, freeUser.RequestCount)
	assert.Equal(t, int64(-1), freeFunding.postDelta)
}
