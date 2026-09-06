package billing

import (
	"encoding/json"
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type violationFeeFixture struct {
	db          *gorm.DB
	user        model.User
	token       model.Token
	channel     model.Channel
	reservation *RelayQuotaReservation
}

func setupViolationFeeDB(t *testing.T) *gorm.DB {
	t.Helper()
	oldDB, oldLogDB := model.DB, model.LOG_DB
	dsn := "file:" + filepath.Join(t.TempDir(), "violation-fee.db") +
		"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(16)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Token{}, &model.Channel{}, &model.Option{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{},
		&model.RelayQuotaReservationRecord{}, &model.RelayQuotaReservationReviewEvent{},
		&model.AuditLogOutbox{}, &model.Log{},
		&model.Task{}, &model.JimengTaskOperation{}, &model.TaskOperation{},
	))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.GrokViolationDeductionEnabledOption: "true",
		setting.GrokViolationDeductionAmountOption:  "0.00002", // 10 quota at ratio 1
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(setting.GrokOptionDefaults())
		model.DB = oldDB
		model.LOG_DB = oldLogDB
	})
	return db
}

func newWalletViolationFeeFixture(t *testing.T, channelType channelcatalog.ChannelType) violationFeeFixture {
	t.Helper()
	db := setupViolationFeeDB(t)
	user := model.User{Username: "violation-wallet", Status: model.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-violation-wallet", Name: "violation-token",
		Status: TokenStatusEnabled, RemainQuota: 100,
	}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{
		Name: "violation-xai", Type: int(channelType), Key: "upstream",
		Status: channelcatalog.ChannelStatusEnabled, UsedQuota: 7,
	}
	require.NoError(t, db.Create(&channel).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 5)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	require.NoError(t, reservation.Refund())
	return violationFeeFixture{db: db, user: user, token: token, channel: channel, reservation: reservation}
}

func violationFeeInput(fixture violationFeeFixture) GrokViolationFeeInput {
	return GrokViolationFeeInput{
		ReservationID: fixture.reservation.ReservationID(),
		ChannelID:     fixture.channel.Id, ModelName: "grok-4", Group: userssvc.GroupDefault,
		RequestID: "00000000-0000-4000-8000-000000000001", StatusCode: 400,
		UseTime: 2, GroupRatio: 1,
	}
}

func loadViolationFeeBalances(t *testing.T, fixture violationFeeFixture) (model.User, model.Token, model.Channel, model.RelayQuotaReservationRecord) {
	t.Helper()
	var user model.User
	var token model.Token
	var channel model.Channel
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.Unscoped().First(&token, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&channel, fixture.channel.Id).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", fixture.reservation.ReservationID()).First(&reservation).Error)
	return user, token, channel, reservation
}

func TestCalculateGrokViolationFeeQuotaUsesExactScaleAndRejectsOverflow(t *testing.T) {
	assertQuota := func(amount, ratio float64, expected int) {
		t.Helper()
		quota, err := CalculateGrokViolationFeeQuota(setting.GrokSetting{ViolationDeductionAmount: amount}, ratio)
		require.NoError(t, err)
		assert.Equal(t, expected, quota)
	}
	assertQuota(0.05, 1, 25_000)
	assertQuota(0.000001, 1, 1) // 0.5 rounds half away from zero
	assertQuota(0.05, 0, 0)
	assertQuota(0, 1, 0)

	for _, ratio := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1), 1e100} {
		_, err := CalculateGrokViolationFeeQuota(setting.GrokSetting{ViolationDeductionAmount: 0.05}, ratio)
		require.Error(t, err)
	}
}

func TestChargeGrokViolationFeeRejectsUnsafeAuditMetadataBeforeIntent(t *testing.T) {
	fixture := newWalletViolationFeeFixture(t, channelcatalog.ChannelTypeXai)
	valid := violationFeeInput(fixture)
	tests := map[string]func(*GrokViolationFeeInput){
		"reservation leading whitespace":  func(input *GrokViolationFeeInput) { input.ReservationID = " " + input.ReservationID },
		"reservation trailing whitespace": func(input *GrokViolationFeeInput) { input.ReservationID += " " },
		"model control":                   func(input *GrokViolationFeeInput) { input.ModelName = "grok\nforged" },
		"model bidi override":             func(input *GrokViolationFeeInput) { input.ModelName = "grok\u202eforged" },
		"group zero width":                func(input *GrokViolationFeeInput) { input.Group = "def\u200bault" },
		"request trimmed":                 func(input *GrokViolationFeeInput) { input.RequestID = " request" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			_, err := ChargeGrokViolationFee(input)
			require.Error(t, err)
		})
	}
	_, _, _, reservation := loadViolationFeeBalances(t, fixture)
	assert.True(t, emptyGrokViolationFeeIntent(&reservation))
}

func TestGrokViolationFeePendingIntentIsVisibleAndReplayRecoverable(t *testing.T) {
	fixture := newWalletViolationFeeFixture(t, channelcatalog.ChannelTypeXai)
	input := violationFeeInput(fixture)
	plan := grokViolationFeePlan{
		quota: 10, amount: "0.00002", groupRatio: "1",
		auditEventID: "violation_fee_" + cryptoutil.SHA256Hex(input.ReservationID)[:48],
	}
	prepared, err := prepareGrokViolationFeeIntent(input, plan)
	require.NoError(t, err)
	assert.False(t, prepared.Charged)

	user, token, channel, reservation := loadViolationFeeBalances(t, fixture)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Equal(t, int64(7), channel.UsedQuota)
	assert.Equal(t, model.RelayQuotaViolationFeeStatusPending, reservation.ViolationFeeStatus)
	assert.Equal(t, fixture.channel.Id, reservation.ViolationFeeChannelID)
	items, total, err := ListManualReviewRelayQuotaReservations(RelayQuotaReservationReviewFilter{
		ReservationID: input.ReservationID,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	if assert.Len(t, items, 1) {
		assert.Equal(t, "grok_violation_fee_pending", items[0].DiagnosticCode)
		assert.Equal(t, model.RelayQuotaViolationFeeStatusPending, items[0].ViolationFeeStatus)
	}

	replayed, err := ChargeGrokViolationFee(input)
	require.NoError(t, err)
	assert.True(t, replayed.Charged)
	_, _, _, reservation = loadViolationFeeBalances(t, fixture)
	assert.Equal(t, model.RelayQuotaViolationFeeStatusCharged, reservation.ViolationFeeStatus)
}

func TestGrokViolationFeeManualReviewCanRecoverWithoutDoubleCharge(t *testing.T) {
	fixture := newWalletViolationFeeFixture(t, channelcatalog.ChannelTypeXai)
	input := violationFeeInput(fixture)
	require.NoError(t, fixture.db.Model(&model.User{}).Where("id = ?", fixture.user.Id).Update("quota", 9).Error)
	_, err := ChargeGrokViolationFee(input)
	require.ErrorIs(t, err, ErrInsufficientQuota)

	_, _, _, reservation := loadViolationFeeBalances(t, fixture)
	assert.Equal(t, model.RelayQuotaViolationFeeStatusManualReview, reservation.ViolationFeeStatus)
	assert.Equal(t, violationFeeFailureInsufficient, reservation.ViolationFeeFailureCode)
	assert.Equal(t, 1, reservation.ViolationFeeAttempts)

	require.NoError(t, fixture.db.Model(&model.User{}).Where("id = ?", fixture.user.Id).Update("quota", 100).Error)
	result, err := ChargeGrokViolationFee(input)
	require.NoError(t, err)
	assert.True(t, result.Charged)
	result, err = ChargeGrokViolationFee(input)
	require.NoError(t, err)
	assert.True(t, result.AlreadyCharged)

	user, token, channel, reservation := loadViolationFeeBalances(t, fixture)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
	assert.Equal(t, int64(17), channel.UsedQuota)
	assert.Equal(t, model.RelayQuotaViolationFeeStatusCharged, reservation.ViolationFeeStatus)
	assert.Equal(t, 2, reservation.ViolationFeeAttempts)
	var outboxes int64
	require.NoError(t, fixture.db.Model(&model.AuditLogOutbox{}).Count(&outboxes).Error)
	assert.Equal(t, int64(1), outboxes)
}

func TestRetryGrokViolationFeeUsesStoredPlanWhenPolicyChangesAndIsIdempotent(t *testing.T) {
	fixture := newWalletViolationFeeFixture(t, channelcatalog.ChannelTypeXai)
	input := violationFeeInput(fixture)
	require.NoError(t, fixture.db.Model(&model.User{}).Where("id = ?", fixture.user.Id).Update("quota", 9).Error)
	_, err := ChargeGrokViolationFee(input)
	require.ErrorIs(t, err, ErrInsufficientQuota)
	require.NoError(t, fixture.db.Model(&model.User{}).Where("id = ?", fixture.user.Id).Update("quota", 100).Error)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.GrokViolationDeductionEnabledOption: "false",
		setting.GrokViolationDeductionAmountOption:  "1.25",
	}))

	result, err := RetryManualReviewRelayQuotaReservation(input.ReservationID, 77)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Changed)
	assert.Equal(t, model.RelayQuotaViolationFeeStatusCharged, result.Reservation.ViolationFeeStatus)
	assert.Equal(t, 10, result.Reservation.ViolationFeeQuota, "operator retry must use the stored fee, not current policy")
	assert.NotEmpty(t, result.AuditEventID)

	replay, err := RetryManualReviewRelayQuotaReservation(input.ReservationID, 88)
	require.NoError(t, err)
	require.NotNil(t, replay)
	assert.False(t, replay.Changed)
	assert.Equal(t, result.AuditEventID, replay.AuditEventID)

	user, token, channel, reservation := loadViolationFeeBalances(t, fixture)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
	assert.Equal(t, int64(17), channel.UsedQuota)
	assert.Equal(t, 2, reservation.ViolationFeeAttempts)
	var events []model.RelayQuotaReservationReviewEvent
	require.NoError(t, fixture.db.Where("reservation_id = ? AND action = ?", input.ReservationID,
		model.RelayQuotaReservationReviewActionRetryFee).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, 77, events[0].OperatorUserID)
	assert.Equal(t, model.RelayQuotaViolationFeeStatusManualReview, events[0].ViolationFeeFromStatus)
	assert.Equal(t, model.RelayQuotaViolationFeeStatusCharged, events[0].ViolationFeeToStatus)
	assert.Equal(t, 10, events[0].ViolationFeeQuota)
	assert.Equal(t, fixture.channel.Id, events[0].ViolationFeeChannelID)
}

func TestRetryGrokViolationFeeConcurrentRequestsChargeAndAuditOnce(t *testing.T) {
	fixture := newWalletViolationFeeFixture(t, channelcatalog.ChannelTypeXai)
	input := violationFeeInput(fixture)
	require.NoError(t, fixture.db.Model(&model.User{}).Where("id = ?", fixture.user.Id).Update("quota", 9).Error)
	_, err := ChargeGrokViolationFee(input)
	require.ErrorIs(t, err, ErrInsufficientQuota)
	require.NoError(t, fixture.db.Model(&model.User{}).Where("id = ?", fixture.user.Id).Update("quota", 100).Error)
	require.NoError(t, setting.UpdateOption(setting.GrokViolationDeductionEnabledOption, "false"))

	const workers = 12
	results := make(chan *RelayQuotaReservationRetryResult, workers)
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := RetryManualReviewRelayQuotaReservation(input.ReservationID, 100+worker)
			results <- result
			errs <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	changed := 0
	eventID := ""
	for result := range results {
		require.NotNil(t, result)
		if result.Changed {
			changed++
		}
		if eventID == "" {
			eventID = result.AuditEventID
		}
		assert.Equal(t, eventID, result.AuditEventID)
	}
	assert.Equal(t, 1, changed)
	user, token, channel, _ := loadViolationFeeBalances(t, fixture)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
	assert.Equal(t, int64(17), channel.UsedQuota)
	var eventCount int64
	require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ? AND action = ?", input.ReservationID,
			model.RelayQuotaReservationReviewActionRetryFee).Count(&eventCount).Error)
	assert.Equal(t, int64(1), eventCount)
}

func TestRetryGrokViolationFeeInsufficientBalanceStaysActionableAndAudited(t *testing.T) {
	fixture := newWalletViolationFeeFixture(t, channelcatalog.ChannelTypeXai)
	input := violationFeeInput(fixture)
	require.NoError(t, fixture.db.Model(&model.User{}).Where("id = ?", fixture.user.Id).Update("quota", 9).Error)
	_, err := ChargeGrokViolationFee(input)
	require.ErrorIs(t, err, ErrInsufficientQuota)

	result, err := RetryManualReviewRelayQuotaReservation(input.ReservationID, 79)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Changed)
	assert.Equal(t, model.RelayQuotaViolationFeeStatusManualReview, result.Reservation.ViolationFeeStatus)
	assert.Equal(t, violationFeeFailureInsufficient, result.Reservation.DiagnosticCode)
	assert.True(t, result.Reservation.Retryable)
	assert.Equal(t, model.RelayQuotaViolationFeeStatusCharged, result.Reservation.RetryTargetStatus)

	user, token, channel, reservation := loadViolationFeeBalances(t, fixture)
	assert.Equal(t, 9, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Equal(t, int64(7), channel.UsedQuota)
	assert.Equal(t, 2, reservation.ViolationFeeAttempts)
	var event model.RelayQuotaReservationReviewEvent
	require.NoError(t, fixture.db.Where("event_id = ?", result.AuditEventID).First(&event).Error)
	assert.Equal(t, 79, event.OperatorUserID)
	assert.Equal(t, model.RelayQuotaViolationFeeStatusManualReview, event.ViolationFeeToStatus)
	assert.Equal(t, violationFeeFailureInsufficient, event.ViolationFeeFailureCode)
}

func TestRetryGrokViolationFeeRejectsTamperedIntentAndNonXAIChannel(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(violationFeeFixture)
	}{
		{
			name: "tampered quota",
			mutate: func(fixture violationFeeFixture) {
				require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationRecord{}).
					Where("reservation_id = ?", fixture.reservation.ReservationID()).
					Update("violation_fee_quota", 11).Error)
			},
		},
		{
			name: "channel no longer xAI",
			mutate: func(fixture violationFeeFixture) {
				require.NoError(t, fixture.db.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
					Update("type", int(channelcatalog.ChannelTypeOpenAI)).Error)
			},
		},
		{
			name: "malformed ratio exponent",
			mutate: func(fixture violationFeeFixture) {
				require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationRecord{}).
					Where("reservation_id = ?", fixture.reservation.ReservationID()).
					Update("violation_fee_group_ratio", "1e2147483647").Error)
			},
		},
		{
			name: "hostile negative ratio exponent",
			mutate: func(fixture violationFeeFixture) {
				require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationRecord{}).
					Where("reservation_id = ?", fixture.reservation.ReservationID()).
					Update("violation_fee_group_ratio", "1e-2147483648").Error)
			},
		},
		{
			name: "hostile negative amount exponent",
			mutate: func(fixture violationFeeFixture) {
				require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationRecord{}).
					Where("reservation_id = ?", fixture.reservation.ReservationID()).
					Update("violation_fee_amount", "1e-2147483648").Error)
			},
		},
		{
			name: "oversized amount coefficient",
			mutate: func(fixture violationFeeFixture) {
				require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationRecord{}).
					Where("reservation_id = ?", fixture.reservation.ReservationID()).
					Update("violation_fee_amount", strings.Repeat("1", 64)).Error)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWalletViolationFeeFixture(t, channelcatalog.ChannelTypeXai)
			input := violationFeeInput(fixture)
			plan := grokViolationFeePlan{
				quota: 10, amount: "0.00002", groupRatio: "1",
				auditEventID: "violation_fee_" + cryptoutil.SHA256Hex(input.ReservationID)[:48],
			}
			_, err := prepareGrokViolationFeeIntent(input, plan)
			require.NoError(t, err)
			test.mutate(fixture)

			_, err = RetryManualReviewRelayQuotaReservation(input.ReservationID, 81)
			require.ErrorIs(t, err, ErrRelayQuotaReviewUnsafeRetry)
			user, token, channel, reservation := loadViolationFeeBalances(t, fixture)
			assert.Equal(t, 100, user.Quota)
			assert.Zero(t, user.UsedQuota)
			assert.Equal(t, 100, token.RemainQuota)
			assert.Zero(t, token.UsedQuota)
			assert.Equal(t, int64(7), channel.UsedQuota)
			assert.Zero(t, reservation.ViolationFeeChargedAt)
			var eventCount int64
			require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationReviewEvent{}).
				Where("reservation_id = ? AND action = ?", input.ReservationID,
					model.RelayQuotaReservationReviewActionRetryFee).Count(&eventCount).Error)
			assert.Zero(t, eventCount)
		})
	}
}

func TestRetryGrokViolationFeeOperatorAuditFailureRollsBackCharge(t *testing.T) {
	fixture := newWalletViolationFeeFixture(t, channelcatalog.ChannelTypeXai)
	input := violationFeeInput(fixture)
	require.NoError(t, fixture.db.Model(&model.User{}).Where("id = ?", fixture.user.Id).Update("quota", 9).Error)
	_, err := ChargeGrokViolationFee(input)
	require.ErrorIs(t, err, ErrInsufficientQuota)
	require.NoError(t, fixture.db.Model(&model.User{}).Where("id = ?", fixture.user.Id).Update("quota", 100).Error)
	require.NoError(t, fixture.db.Migrator().DropTable(&model.RelayQuotaReservationReviewEvent{}))

	_, err = RetryManualReviewRelayQuotaReservation(input.ReservationID, 82)
	require.Error(t, err)
	user, token, channel, reservation := loadViolationFeeBalances(t, fixture)
	assert.Equal(t, 100, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Equal(t, int64(7), channel.UsedQuota)
	assert.Equal(t, model.RelayQuotaViolationFeeStatusManualReview, reservation.ViolationFeeStatus)
	assert.Equal(t, 1, reservation.ViolationFeeAttempts)
	assert.Zero(t, reservation.ViolationFeeChargedAt)
	var outboxCount int64
	require.NoError(t, fixture.db.Model(&model.AuditLogOutbox{}).Count(&outboxCount).Error)
	assert.Zero(t, outboxCount)
}

func TestChargeGrokViolationFeeIsAtomicIdempotentAndAudited(t *testing.T) {
	fixture := newWalletViolationFeeFixture(t, channelcatalog.ChannelTypeXai)
	input := violationFeeInput(fixture)

	first, err := ChargeGrokViolationFee(input)
	require.NoError(t, err)
	assert.True(t, first.Charged)
	assert.False(t, first.AlreadyCharged)
	assert.Equal(t, 10, first.Quota)
	assert.NotEmpty(t, first.AuditEventID)

	second, err := ChargeGrokViolationFee(input)
	require.NoError(t, err)
	assert.True(t, second.Charged)
	assert.True(t, second.AlreadyCharged)
	assert.Equal(t, first.AuditEventID, second.AuditEventID)

	user, token, channel, reservation := loadViolationFeeBalances(t, fixture)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
	assert.Equal(t, int64(17), channel.UsedQuota)
	assert.Equal(t, model.RelayQuotaViolationFeeStatusCharged, reservation.ViolationFeeStatus)
	assert.Empty(t, reservation.ViolationFeeFailureCode)
	assert.Equal(t, 1, reservation.ViolationFeeAttempts)
	assert.Equal(t, relaycommon.GrokViolationErrorCode, reservation.ViolationFeeCode)
	assert.Equal(t, 10, reservation.ViolationFeeQuota)
	assert.Equal(t, "0.00002", reservation.ViolationFeeAmount)
	assert.Equal(t, "1", reservation.ViolationFeeGroupRatio)
	assert.Equal(t, fixture.channel.Id, reservation.ViolationFeeChannelID)
	assert.Equal(t, first.AuditEventID, reservation.ViolationFeeAuditEventID)
	assert.Positive(t, reservation.ViolationFeeUpdatedAt)
	assert.Positive(t, reservation.ViolationFeeChargedAt)

	var outboxes, logs int64
	require.NoError(t, fixture.db.Model(&model.AuditLogOutbox{}).Count(&outboxes).Error)
	require.NoError(t, fixture.db.Model(&model.Log{}).Count(&logs).Error)
	assert.Equal(t, int64(1), outboxes)
	assert.Equal(t, int64(1), logs)
	var log model.Log
	require.NoError(t, fixture.db.Where("audit_event_id = ?", first.AuditEventID).First(&log).Error)
	assert.Equal(t, "Violation fee charged", log.Content)
	assert.Equal(t, "grok-4", log.ModelName)
	assert.Equal(t, 10, log.Quota)
	assert.NotContains(t, log.Other, relaycommon.GrokCSAMViolationMarker)
	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
	assert.Equal(t, relaycommon.GrokViolationErrorCode, other["violation_fee_code"])
	assert.Equal(t, fixture.reservation.ReservationID(), other["relay_reservation_id"])
}

func TestChargeGrokViolationFeeConcurrentReplayChargesOnce(t *testing.T) {
	fixture := newWalletViolationFeeFixture(t, channelcatalog.ChannelTypeXai)
	input := violationFeeInput(fixture)
	const workers = 16
	var wait sync.WaitGroup
	errs := make(chan error, workers)
	results := make(chan GrokViolationFeeResult, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := ChargeGrokViolationFee(input)
			results <- result
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	close(results)
	for err := range errs {
		require.NoError(t, err)
	}
	for result := range results {
		assert.True(t, result.Charged)
		assert.Equal(t, 10, result.Quota)
	}
	user, token, channel, _ := loadViolationFeeBalances(t, fixture)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
	assert.Equal(t, int64(17), channel.UsedQuota)
	var outboxes int64
	require.NoError(t, fixture.db.Model(&model.AuditLogOutbox{}).Count(&outboxes).Error)
	assert.Equal(t, int64(1), outboxes)
}

func TestChargeGrokViolationFeeResolvesAmbiguousCommit(t *testing.T) {
	fixture := newWalletViolationFeeFixture(t, channelcatalog.ChannelTypeXai)
	input := violationFeeInput(fixture)
	plan := grokViolationFeePlan{
		quota: 10, amount: "0.00002", groupRatio: "1",
		auditEventID: "violation_fee_" + cryptoutil.SHA256Hex(input.ReservationID)[:48],
	}
	injected := errors.New("injected ambiguous commit result")
	runner := func(fn func(tx *gorm.DB) error) error {
		if err := fixture.db.Transaction(fn); err != nil {
			return err
		}
		return injected
	}

	result, err := chargeGrokViolationFeeWithRunner(input, plan, runner)
	require.NoError(t, err)
	assert.True(t, result.Charged)
	assert.True(t, result.AlreadyCharged)
	user, token, channel, _ := loadViolationFeeBalances(t, fixture)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
	assert.Equal(t, int64(17), channel.UsedQuota)
}

func TestChargeGrokViolationFeeRollsBackEveryCriticalFailure(t *testing.T) {
	for _, test := range []struct {
		name        string
		operation   string
		table       string
		failureCode string
	}{
		{name: "user accounting", operation: "update", table: (model.User{}).TableName(), failureCode: violationFeeFailureTransaction},
		{name: "token accounting", operation: "update", table: (model.Token{}).TableName(), failureCode: violationFeeFailureTransaction},
		{name: "channel accounting", operation: "update", table: (model.Channel{}).TableName(), failureCode: violationFeeFailureTransaction},
		{name: "audit enqueue", operation: "create", table: (model.AuditLogOutbox{}).TableName(), failureCode: violationFeeFailureAudit},
		{name: "durable fee marker", operation: "update", table: (model.RelayQuotaReservationRecord{}).TableName(), failureCode: violationFeeFailureTransaction},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWalletViolationFeeFixture(t, channelcatalog.ChannelTypeXai)
			injected := errors.New("injected " + test.name + " failure")
			callbackName := "test:violation_fee_" + test.operation + "_" + strings.ReplaceAll(test.name, " ", "_")
			callback := func(tx *gorm.DB) {
				if tx.Statement.Table != test.table {
					return
				}
				// Reservation writes also prepare and preserve the review intent.
				// Inject only into the final charged marker so the separate review
				// transaction remains available to prove rollback visibility.
				if test.table == (model.RelayQuotaReservationRecord{}).TableName() {
					updates, ok := tx.Statement.Dest.(map[string]any)
					if !ok || updates["violation_fee_status"] != model.RelayQuotaViolationFeeStatusCharged {
						return
					}
				}
				tx.AddError(injected)
			}
			if test.operation == "create" {
				require.NoError(t, fixture.db.Callback().Create().Before("gorm:create").Register(callbackName, callback))
				t.Cleanup(func() { _ = fixture.db.Callback().Create().Remove(callbackName) })
			} else {
				require.NoError(t, fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, callback))
				t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })
			}

			_, err := ChargeGrokViolationFee(violationFeeInput(fixture))
			require.ErrorIs(t, err, injected)
			user, token, channel, reservation := loadViolationFeeBalances(t, fixture)
			assert.Equal(t, 100, user.Quota)
			assert.Zero(t, user.UsedQuota)
			assert.Zero(t, user.RequestCount)
			assert.Equal(t, 100, token.RemainQuota)
			assert.Zero(t, token.UsedQuota)
			assert.Equal(t, int64(7), channel.UsedQuota)
			assert.Zero(t, reservation.ViolationFeeChargedAt)
			assert.Equal(t, model.RelayQuotaViolationFeeStatusManualReview, reservation.ViolationFeeStatus)
			assert.Equal(t, test.failureCode, reservation.ViolationFeeFailureCode)
			assert.Equal(t, 1, reservation.ViolationFeeAttempts)
			assert.Equal(t, relaycommon.GrokViolationErrorCode, reservation.ViolationFeeCode)
			assert.Equal(t, 10, reservation.ViolationFeeQuota)
			assert.Equal(t, fixture.channel.Id, reservation.ViolationFeeChannelID)
			var outboxes int64
			require.NoError(t, fixture.db.Model(&model.AuditLogOutbox{}).Count(&outboxes).Error)
			assert.Zero(t, outboxes)
			items, total, listErr := ListManualReviewRelayQuotaReservations(RelayQuotaReservationReviewFilter{
				ReservationID: fixture.reservation.ReservationID(),
			})
			require.NoError(t, listErr)
			assert.Equal(t, int64(1), total)
			if assert.Len(t, items, 1) {
				assert.Equal(t, RelayQuotaReviewKindGrokViolationFee, items[0].ReviewKind)
				assert.Equal(t, test.failureCode, items[0].DiagnosticCode)
				assert.Equal(t, fixture.channel.Id, items[0].ViolationFeeChannelID)
				assert.False(t, items[0].ResolutionRequired)
				assert.True(t, items[0].Retryable)
				assert.Equal(t, model.RelayQuotaViolationFeeStatusCharged, items[0].RetryTargetStatus)
			}
		})
	}
}

func TestChargeGrokViolationFeeRejectsNonXAIAndInsufficientFundsAtomically(t *testing.T) {
	for name, mutate := range map[string]func(violationFeeFixture){
		"non-xAI channel": func(_ violationFeeFixture) {},
		"insufficient wallet": func(fixture violationFeeFixture) {
			require.NoError(t, fixture.db.Model(&model.User{}).Where("id = ?", fixture.user.Id).Update("quota", 9).Error)
		},
		"insufficient token": func(fixture violationFeeFixture) {
			require.NoError(t, fixture.db.Model(&model.Token{}).Where("id = ?", fixture.token.Id).Update("remain_quota", 9).Error)
		},
	} {
		t.Run(name, func(t *testing.T) {
			channelType := channelcatalog.ChannelTypeXai
			if name == "non-xAI channel" {
				channelType = channelcatalog.ChannelTypeOpenAI
			}
			fixture := newWalletViolationFeeFixture(t, channelType)
			mutate(fixture)
			_, err := ChargeGrokViolationFee(violationFeeInput(fixture))
			require.Error(t, err)
			user, token, channel, reservation := loadViolationFeeBalances(t, fixture)
			assert.Zero(t, user.UsedQuota)
			assert.Zero(t, user.RequestCount)
			assert.Zero(t, token.UsedQuota)
			assert.Equal(t, int64(7), channel.UsedQuota)
			assert.Zero(t, reservation.ViolationFeeChargedAt)
			if name == "non-xAI channel" {
				assert.Empty(t, reservation.ViolationFeeStatus)
				assert.Empty(t, reservation.ViolationFeeCode)
				assert.Zero(t, reservation.ViolationFeeChannelID)
				items, total, listErr := ListManualReviewRelayQuotaReservations(RelayQuotaReservationReviewFilter{
					ReservationID: fixture.reservation.ReservationID(),
				})
				require.NoError(t, listErr)
				assert.Zero(t, total)
				assert.Empty(t, items)
			} else {
				assert.Equal(t, model.RelayQuotaViolationFeeStatusManualReview, reservation.ViolationFeeStatus)
				expectedFailure := violationFeeFailureInsufficient
				if name == "insufficient token" {
					expectedFailure = violationFeeFailureToken
				}
				assert.Equal(t, expectedFailure, reservation.ViolationFeeFailureCode)
				assert.Equal(t, fixture.channel.Id, reservation.ViolationFeeChannelID)
			}
		})
	}
}

func TestChargeGrokViolationFeeUsesOriginalSubscriptionEpoch(t *testing.T) {
	db := setupViolationFeeDB(t)
	user := model.User{
		Username: "violation-subscription", Status: model.UserStatusEnabled, Quota: 80,
		Setting: `{"billing_preference":"subscription_only"}`,
	}
	require.NoError(t, db.Create(&user).Error)
	plan := model.SubscriptionPlan{Title: "Grok", PriceAmount: "0", Enabled: true}
	require.NoError(t, db.Create(&plan).Error)
	subscription := model.UserSubscription{
		UserId: user.Id, PlanId: plan.Id, AmountTotal: 100, AmountUsed: 0, UsageEpoch: 3,
		StartTime: wallclock.NowTimestamp() - 60, EndTime: wallclock.NowTimestamp() + 3600,
		Status: SubscriptionStatusActive, AllowWalletOverflow: false,
	}
	require.NoError(t, db.Create(&subscription).Error)
	token := model.Token{UserId: user.Id, Key: "sk-violation-sub", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: "xai-sub", Type: int(channelcatalog.ChannelTypeXai), Key: "upstream", Status: channelcatalog.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	reservation, err := NewRelayQuotaReservation(user.Id, &token, 5)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	require.NoError(t, reservation.Refund())
	fixture := violationFeeFixture{db: db, user: user, token: token, channel: channel, reservation: reservation}

	result, err := ChargeGrokViolationFee(violationFeeInput(fixture))
	require.NoError(t, err)
	assert.True(t, result.Charged)
	require.NoError(t, db.First(&subscription, subscription.Id).Error)
	assert.Equal(t, int64(10), subscription.AmountUsed)
	loadedUser, loadedToken, loadedChannel, _ := loadViolationFeeBalances(t, fixture)
	assert.Equal(t, 80, loadedUser.Quota, "subscription funding must not touch wallet quota")
	assert.Equal(t, 10, loadedUser.UsedQuota)
	assert.Equal(t, 90, loadedToken.RemainQuota)
	assert.Equal(t, int64(10), loadedChannel.UsedQuota)

	// A reset between refund and fee must fail closed rather than charging a
	// different usage window or falling back to the wallet.
	secondUser := model.User{
		Username: "violation-subscription-reset", Status: model.UserStatusEnabled, Quota: 80,
		Setting: `{"billing_preference":"subscription_only"}`,
	}
	require.NoError(t, db.Create(&secondUser).Error)
	secondSub := model.UserSubscription{
		UserId: secondUser.Id, PlanId: plan.Id, AmountTotal: 100, UsageEpoch: 5,
		StartTime: wallclock.NowTimestamp() - 60, EndTime: wallclock.NowTimestamp() + 3600,
		Status: SubscriptionStatusActive, AllowWalletOverflow: false,
	}
	require.NoError(t, db.Create(&secondSub).Error)
	secondToken := model.Token{UserId: secondUser.Id, Key: "sk-violation-sub-reset", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&secondToken).Error)
	secondReservation, err := NewRelayQuotaReservation(secondUser.Id, &secondToken, 5)
	require.NoError(t, err)
	require.NoError(t, secondReservation.MarkDispatched())
	require.NoError(t, secondReservation.Refund())
	require.NoError(t, db.Model(&model.UserSubscription{}).Where("id = ?", secondSub.Id).Update("usage_epoch", 6).Error)
	secondFixture := violationFeeFixture{db: db, user: secondUser, token: secondToken, channel: channel, reservation: secondReservation}
	_, err = ChargeGrokViolationFee(violationFeeInput(secondFixture))
	require.ErrorIs(t, err, ErrViolationFeeSubscriptionEpoch)
	require.NoError(t, db.First(&secondSub, secondSub.Id).Error)
	assert.Zero(t, secondSub.AmountUsed)
	secondLoadedUser, secondLoadedToken, _, secondRecord := loadViolationFeeBalances(t, secondFixture)
	assert.Equal(t, 80, secondLoadedUser.Quota)
	assert.Zero(t, secondLoadedUser.UsedQuota)
	assert.Equal(t, 100, secondLoadedToken.RemainQuota)
	assert.Zero(t, secondLoadedToken.UsedQuota)
	assert.Zero(t, secondRecord.ViolationFeeChargedAt)
	assert.Equal(t, model.RelayQuotaViolationFeeStatusManualReview, secondRecord.ViolationFeeStatus)
	assert.Equal(t, violationFeeFailureEpoch, secondRecord.ViolationFeeFailureCode)
	assert.Equal(t, channel.Id, secondRecord.ViolationFeeChannelID)
}
