package billing

import (
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"path/filepath"
	"strconv"
	"testing"
)

func setupRelayQuotaReservationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "relay-quota.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Token{}, &model.Channel{}, &model.Log{}, &model.UserSubscription{},
		&model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{},
		&model.RelayQuotaReservationRecord{}, &model.RelayQuotaReservationReviewEvent{},
		&model.Task{}, &model.TaskOperation{}, &model.JimengTaskOperation{},
	))
	model.DB = db
	model.LOG_DB = db
	return db
}

func TestRelayQuotaReservationPersistsUnlimitedTokenAndChannelUsage(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "unlimited-channel-accounting", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-unlimited-channel-accounting", Status: TokenStatusEnabled,
		UnlimitedQuota: true, RemainQuota: 37, UsedQuota: 5,
	}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: "accounted-channel", Key: "upstream", Status: channelcatalog.ChannelStatusEnabled, UsedQuota: 7}
	require.NoError(t, db.Create(&channel).Error)

	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	require.NoError(t, reservation.SettleWithChannel(14, channel.Id))
	require.NoError(t, reservation.SettleWithChannel(14, channel.Id), "terminal settlement is idempotent")

	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	require.NoError(t, db.First(&channel, channel.Id).Error)
	assert.Equal(t, 86, user.Quota)
	assert.Equal(t, 14, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 37, token.RemainQuota, "unlimited tokens never spend remain_quota")
	assert.Equal(t, 19, token.UsedQuota, "unlimited tokens still accrue lifetime usage")
	assert.Equal(t, int64(21), channel.UsedQuota)

	record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
	assert.Equal(t, token.Id, record.TokenID)
	assert.True(t, record.TokenUnlimited)
	assert.Zero(t, record.TokenReserved)
	assert.Equal(t, channel.Id, record.ChannelID)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
}

func TestRelayQuotaReservationSettlesUserAndTokenAtomically(t *testing.T) {
	ResetQuotaDataCache()
	t.Cleanup(ResetQuotaDataCache)
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "realtime-accounting", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-realtime-accounting", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)

	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 90, token.RemainQuota)

	require.NoError(t, reservation.Settle(14))
	require.NoError(t, reservation.Settle(14), "settlement must be idempotent")
	require.NoError(t, reservation.Refund(), "a settled reservation cannot be refunded")
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 86, user.Quota)
	assert.Equal(t, 14, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 86, token.RemainQuota)
	assert.Equal(t, 14, token.UsedQuota)

	var logCount int64
	require.NoError(t, db.Model(&model.Log{}).Count(&logCount).Error)
	assert.Zero(t, logCount, "settlement and durable logging are deliberately separate outcomes")
	require.NoError(t, RecordConsumeLogChecked(
		user.Id, user.Username, token.Name, "gpt-test", 4, 3, 14, 5,
		true, 0, userssvc.GroupDefault, "127.0.0.1", "request-1", "response-1",
		token.Id, map[string]any{"realtime": true},
	))
	require.NoError(t, db.Model(&model.Log{}).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)
}

func TestRelayQuotaReservationInsufficientTokenDoesNotTouchUser(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "realtime-no-token-quota", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-realtime-no-token-quota", Status: TokenStatusEnabled, RemainQuota: 5}
	require.NoError(t, db.Create(&token).Error)

	_, err := NewRelayQuotaReservation(user.Id, &token, 10)
	assert.ErrorIs(t, err, ErrInsufficientTokenQuota)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 5, token.RemainQuota)
}

func TestRelayQuotaReservationSettlementFailureRetainsBothReservations(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "realtime-settle-fail", Status: model.UserStatusEnabled, Quota: 12}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-realtime-settle-fail", Status: TokenStatusEnabled, RemainQuota: 12}
	require.NoError(t, db.Create(&token).Error)

	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	err = reservation.Settle(20)
	assert.True(t, errors.Is(err, ErrInsufficientQuota) || errors.Is(err, ErrInsufficientTokenQuota))
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 2, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 2, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
}

func TestRelayQuotaReservationRefundsUnusedFunding(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "realtime-refund", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-realtime-refund", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)

	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.Refund())
	require.NoError(t, reservation.Refund(), "refund must be idempotent")
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, token.UsedQuota)
}

func TestRelayQuotaReservationRejectsQuotaAndCounterOverflow(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "realtime-overflow", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-realtime-overflow", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)

	_, err := NewRelayQuotaReservation(user.Id, &token, -1)
	assert.ErrorIs(t, err, ErrInvalidQuota)
	if strconv.IntSize > 32 {
		_, err = NewRelayQuotaReservation(user.Id, &token, int(quotamath.MaxQuota+1))
		assert.ErrorIs(t, err, ErrInvalidQuota)
	}

	require.NoError(t, db.Model(&user).Update("used_quota", int(quotamath.MaxQuota)).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	assert.ErrorIs(t, reservation.Settle(1), ErrUserUsageOverflow)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, int(quotamath.MaxQuota), user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
}

func TestRelayQuotaReservationTokenUsageOverflowRollsBackUser(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "realtime-token-overflow", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-realtime-token-overflow", Status: TokenStatusEnabled,
		RemainQuota: 100, UsedQuota: int(quotamath.MaxQuota),
	}
	require.NoError(t, db.Create(&token).Error)

	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	assert.ErrorIs(t, reservation.Settle(1), ErrTokenQuotaOverflow)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 90, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Equal(t, int(quotamath.MaxQuota), token.UsedQuota)
}

func TestRelayQuotaReservationRefundRejectsTokenBalanceOverflow(t *testing.T) {
	t.Setenv("RELAY_RESERVATION_MAX_ATTEMPTS", "1")
	db := setupRelayQuotaReservationDB(t)
	user := model.User{Username: "realtime-refund-overflow", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-realtime-refund-overflow", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)

	reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, db.Model(&token).Update("remain_quota", int(quotamath.MaxQuota)).Error)
	assert.ErrorIs(t, reservation.Refund(), ErrTokenQuotaOverflow)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 90, user.Quota, "funding and token refunds must roll back together")
	assert.Equal(t, int(quotamath.MaxQuota), token.RemainQuota, "token quota must never wrap or exceed the cap")
	var record model.RelayQuotaReservationRecord
	require.NoError(t, db.Where("reservation_id = ?", reservation.ReservationID()).First(&record).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusManualReview, record.Status)
	assert.Contains(t, record.LastError, ErrTokenQuotaOverflow.Error())
	assert.Zero(t, record.NextAttemptAt)
}

func TestRecordConsumeLogCheckedFailsClosedWithoutAuthoritativeClock(t *testing.T) {
	t.Setenv("LOG_SQL_DSN", "")
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = nil, nil
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })

	err := RecordConsumeLogChecked(
		1, "alice", "token", "gpt-test", 1, 1, 1, 1, true,
		0, userssvc.GroupDefault, "127.0.0.1", "request", "response", 1, nil,
	)
	assert.ErrorContains(t, err, "primary database is nil")
}
