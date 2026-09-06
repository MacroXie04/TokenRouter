package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

func TestIncreaseUserQuotaHonorsPersistedQuotaBound(t *testing.T) {
	initWalletDB(t)
	user := newUser(t, int(common.MaxQuota))

	err := IncreaseUserQuota(user.Id, 1)
	require.ErrorIs(t, err, ErrUserQuotaOverflow)
	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, int(common.MaxQuota), got.Quota)
}

func TestDecreaseUserQuotaUpdatesBalanceAndUsageAtomically(t *testing.T) {
	initWalletDB(t)
	user := newUser(t, 100)
	require.NoError(t, DecreaseUserQuota(user.Id, 30))
	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, 70, got.Quota)
	assert.Equal(t, 30, got.UsedQuota)
}

func TestDecreaseUserQuotaRollsBackWhenUsageWouldOverflow(t *testing.T) {
	initWalletDB(t)
	user := newUser(t, 100)
	require.NoError(t, model.DB.Model(user).Update("used_quota", int(common.MaxQuota)).Error)

	err := DecreaseUserQuota(user.Id, 1)
	require.ErrorIs(t, err, ErrUserUsageOverflow)
	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, 100, got.Quota)
	assert.Equal(t, int(common.MaxQuota), got.UsedQuota)
}

func TestDecreaseUserQuotaRollsBackOnWriteFailure(t *testing.T) {
	initWalletDB(t)
	user := newUser(t, 100)
	injected := errors.New("injected quota update failure")
	const callback = "test:fail_atomic_user_debit"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callback) })

	err := DecreaseUserQuota(user.Id, 30)
	require.ErrorIs(t, err, injected)
	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, 100, got.Quota)
	assert.Zero(t, got.UsedQuota)
}

func TestSetUserQuotaRejectsUnsafeValuesAndMissingRows(t *testing.T) {
	initWalletDB(t)
	user := newUser(t, 10)
	for _, quota := range []int{-1, int(common.MaxQuota) + 1} {
		require.ErrorIs(t, SetUserQuota(user.Id, quota), ErrInvalidQuota)
	}
	require.ErrorIs(t, SetUserQuota(user.Id+999, 1), ErrUserNotFound)
	require.NoError(t, SetUserQuota(user.Id, 0))
	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Zero(t, got.Quota)
}

func TestUpdateUserRequestCountRejectsOverflow(t *testing.T) {
	initWalletDB(t)
	user := newUser(t, 0)
	require.NoError(t, model.DB.Model(user).Update("request_count", int(common.MaxQuota)).Error)
	require.ErrorIs(t, UpdateUserRequestCount(user.Id, 1), ErrUserUsageOverflow)
	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, int(common.MaxQuota), got.RequestCount)
}
