package service

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// seedSub inserts an active subscription row directly (bypassing purchase).
func seedSub(t *testing.T, userId, planId int, total, used int64, mutate func(*model.UserSubscription)) *model.UserSubscription {
	t.Helper()
	now := common.NowTimestamp()
	sub := &model.UserSubscription{
		UserId:              userId,
		PlanId:              planId,
		AmountTotal:         total,
		AmountUsed:          used,
		StartTime:           now - 3600,
		EndTime:             now + 86400,
		Status:              SubscriptionStatusActive,
		Source:              "test",
		AllowWalletOverflow: true,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if mutate != nil {
		mutate(sub)
	}
	require.NoError(t, model.DB.Create(sub).Error)
	return sub
}

func loadSub(t *testing.T, id int) *model.UserSubscription {
	t.Helper()
	var sub model.UserSubscription
	require.NoError(t, model.DB.First(&sub, id).Error)
	return &sub
}

func TestPreConsumeSubscriptionValidationAndLedger(t *testing.T) {
	initSubDB(t)
	u := subUser(t, "ledger", 0, "default")
	plan := seedRawPlan(t, nil)

	// Input validation.
	_, err := PreConsumeUserSubscription("req-1", 0, 5)
	assert.EqualError(t, err, "invalid userId")
	_, err = PreConsumeUserSubscription("  ", u.Id, 5)
	assert.EqualError(t, err, "requestId is empty")
	_, err = PreConsumeUserSubscription("req-1", u.Id, 0)
	assert.EqualError(t, err, "amount must be > 0")

	// No subscription at all.
	_, err = PreConsumeUserSubscription("req-1", u.Id, 5)
	assert.EqualError(t, err, "no active subscription")

	sub := seedSub(t, u.Id, plan.Id, 100, 0, nil)

	res, err := PreConsumeUserSubscription("req-1", u.Id, 5)
	require.NoError(t, err)
	assert.Equal(t, sub.Id, res.UserSubscriptionId)
	assert.Equal(t, int64(5), res.PreConsumed)
	assert.Equal(t, int64(100), res.AmountTotal)
	assert.Equal(t, int64(0), res.AmountUsedBefore)
	assert.Equal(t, int64(5), res.AmountUsedAfter)
	assert.Equal(t, int64(5), loadSub(t, sub.Id).AmountUsed)

	var record model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", "req-1").First(&record).Error)
	assert.Equal(t, SubscriptionPreConsumeStatusConsumed, record.Status)
	assert.Equal(t, int64(5), record.PreConsumed)

	// Replaying the same request id returns the original reservation without
	// consuming again.
	replay, err := PreConsumeUserSubscription("req-1", u.Id, 5)
	require.NoError(t, err)
	assert.Equal(t, int64(5), replay.PreConsumed)
	assert.Equal(t, int64(5), loadSub(t, sub.Id).AmountUsed)

	// A refunded id can never be consumed again.
	require.NoError(t, RefundSubscriptionPreConsume("req-1"))
	assert.Equal(t, int64(0), loadSub(t, sub.Id).AmountUsed)
	_, err = PreConsumeUserSubscription("req-1", u.Id, 5)
	assert.EqualError(t, err, "subscription pre-consume already refunded")
}

func TestPreConsumeSubscriptionCandidateSelection(t *testing.T) {
	initSubDB(t)
	u := subUser(t, "candidates", 0, "default")
	plan := seedRawPlan(t, nil)
	now := common.NowTimestamp()

	// Exhausted subscription ends first; the roomy one ends later.
	exhausted := seedSub(t, u.Id, plan.Id, 10, 9, func(s *model.UserSubscription) { s.EndTime = now + 1000 })
	roomy := seedSub(t, u.Id, plan.Id, 100, 0, func(s *model.UserSubscription) { s.EndTime = now + 2000 })

	res, err := PreConsumeUserSubscription("req-pick", u.Id, 5)
	require.NoError(t, err)
	assert.Equal(t, roomy.Id, res.UserSubscriptionId, "insufficient candidate skipped")
	assert.Equal(t, int64(9), loadSub(t, exhausted.Id).AmountUsed, "skipped sub untouched")

	// Every candidate insufficient -> classified error.
	_, err = PreConsumeUserSubscription("req-toobig", u.Id, 500)
	assert.EqualError(t, err, "subscription quota insufficient, need=500")

	// AmountTotal == 0 means unlimited and always accepts.
	unlimited := seedSub(t, u.Id, plan.Id, 0, 0, func(s *model.UserSubscription) { s.EndTime = now + 500 })
	res, err = PreConsumeUserSubscription("req-unlimited", u.Id, 500)
	require.NoError(t, err)
	assert.Equal(t, unlimited.Id, res.UserSubscriptionId, "earliest end_time wins")
}

func TestPreConsumeSubscriptionLazyReset(t *testing.T) {
	initSubDB(t)
	u := subUser(t, "lazyreset", 0, "default")
	now := common.NowTimestamp()

	dailyPlan := seedRawPlan(t, func(p *model.SubscriptionPlan) { p.QuotaResetPeriod = SubscriptionResetDaily })
	sub := seedSub(t, u.Id, dailyPlan.Id, 100, 90, func(s *model.UserSubscription) {
		s.StartTime = now - 3*86400
		s.LastResetTime = now - 3*86400
		s.NextResetTime = now - 2*86400
	})

	// The due reset zeroes usage before the remain check, so a nearly-exhausted
	// subscription still funds the request.
	res, err := PreConsumeUserSubscription("req-reset", u.Id, 50)
	require.NoError(t, err)
	assert.Equal(t, sub.Id, res.UserSubscriptionId)
	assert.Equal(t, int64(0), res.AmountUsedBefore, "usage was reset before consuming")
	got := loadSub(t, sub.Id)
	assert.Equal(t, int64(50), got.AmountUsed)
	assert.Greater(t, got.NextResetTime, now, "schedule caught up past now")

	// A never-period plan leaves a stale schedule untouched on this path
	// (deviation #17: only the periodic job clears it).
	neverPlan := seedRawPlan(t, func(p *model.SubscriptionPlan) { p.QuotaResetPeriod = SubscriptionResetNever })
	staleSub := seedSub(t, u.Id, neverPlan.Id, 100, 40, func(s *model.UserSubscription) {
		s.EndTime = now + 100 // ends before the daily sub so it is picked first
		s.NextResetTime = now - 3600
	})
	res, err = PreConsumeUserSubscription("req-never", u.Id, 10)
	require.NoError(t, err)
	assert.Equal(t, staleSub.Id, res.UserSubscriptionId)
	got = loadSub(t, staleSub.Id)
	assert.Equal(t, int64(50), got.AmountUsed, "40 + 10, no reset applied")
	assert.Equal(t, now-3600, got.NextResetTime, "stale schedule preserved")
}

func TestRefundSubscriptionPreConsumeIdempotent(t *testing.T) {
	initSubDB(t)
	u := subUser(t, "refunder", 0, "default")
	plan := seedRawPlan(t, nil)
	sub := seedSub(t, u.Id, plan.Id, 100, 0, nil)

	assert.EqualError(t, RefundSubscriptionPreConsume(" "), "requestId is empty")
	assert.Error(t, RefundSubscriptionPreConsume("req-unknown"), "unknown ledger id errors")

	_, err := PreConsumeUserSubscription("req-r", u.Id, 30)
	require.NoError(t, err)
	require.Equal(t, int64(30), loadSub(t, sub.Id).AmountUsed)

	require.NoError(t, RefundSubscriptionPreConsume("req-r"))
	assert.Equal(t, int64(0), loadSub(t, sub.Id).AmountUsed)

	// Second refund is a no-op, not a double credit.
	require.NoError(t, RefundSubscriptionPreConsume("req-r"))
	assert.Equal(t, int64(0), loadSub(t, sub.Id).AmountUsed)

	var record model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", "req-r").First(&record).Error)
	assert.Equal(t, SubscriptionPreConsumeStatusRefunded, record.Status)
}

func TestPostConsumeUserSubscriptionDelta(t *testing.T) {
	initSubDB(t)
	u := subUser(t, "delta", 0, "default")
	plan := seedRawPlan(t, nil)
	sub := seedSub(t, u.Id, plan.Id, 100, 50, nil)

	assert.EqualError(t, PostConsumeUserSubscriptionDelta(0, 5), "invalid userSubscriptionId")
	require.NoError(t, PostConsumeUserSubscriptionDelta(sub.Id, 0), "zero delta is a no-op")

	require.NoError(t, PostConsumeUserSubscriptionDelta(sub.Id, 25))
	assert.Equal(t, int64(75), loadSub(t, sub.Id).AmountUsed)

	// Negative overshoot clamps at zero.
	require.NoError(t, PostConsumeUserSubscriptionDelta(sub.Id, -200))
	assert.Equal(t, int64(0), loadSub(t, sub.Id).AmountUsed)

	// Positive overshoot past the total is rejected and changes nothing.
	err := PostConsumeUserSubscriptionDelta(sub.Id, 150)
	assert.EqualError(t, err, "subscription used exceeds total, used=150 total=100")
	assert.Equal(t, int64(0), loadSub(t, sub.Id).AmountUsed)
}

func TestFundingSessionPreferenceDispatch(t *testing.T) {
	newUser := func(t *testing.T, name string, quota int, pref string) *model.User {
		u := subUser(t, name, quota, "default")
		if pref != "" {
			_, err := UpdateUserBillingPreference(u.Id, pref)
			require.NoError(t, err)
		}
		return u
	}
	walletOf := func(t *testing.T, id int) int {
		var u model.User
		require.NoError(t, model.DB.First(&u, id).Error)
		return u.Quota
	}

	t.Run("wallet_only", func(t *testing.T) {
		initSubDB(t)
		u := newUser(t, "wonly", 100, BillingPreferenceWalletOnly)
		s, err := NewFundingSession(u.Id, 40)
		require.NoError(t, err)
		assert.Equal(t, BillingSourceWallet, s.Source())
		assert.Equal(t, 60, walletOf(t, u.Id))

		// Insufficient wallet does not fall back to a subscription.
		plan := seedRawPlan(t, nil)
		seedSub(t, u.Id, plan.Id, 1000, 0, nil)
		_, err = NewFundingSession(u.Id, 1000)
		assert.ErrorIs(t, err, ErrInsufficientQuota)
	})

	t.Run("subscription_only", func(t *testing.T) {
		initSubDB(t)
		u := newUser(t, "sonly", 100, BillingPreferenceSubscriptionOnly)
		_, err := NewFundingSession(u.Id, 40)
		require.Error(t, err)
		assert.True(t, IsSubscriptionFundingErr(err), "no wallet fallback: %v", err)

		plan := seedRawPlan(t, nil)
		sub := seedSub(t, u.Id, plan.Id, 1000, 0, nil)
		s, err := NewFundingSession(u.Id, 40)
		require.NoError(t, err)
		assert.Equal(t, BillingSourceSubscription, s.Source())
		assert.Equal(t, int64(40), loadSub(t, sub.Id).AmountUsed)
		assert.Equal(t, 100, walletOf(t, u.Id), "wallet untouched")
	})

	t.Run("wallet_first falls back to subscription", func(t *testing.T) {
		initSubDB(t)
		u := newUser(t, "wfirst", 10, BillingPreferenceWalletFirst)
		plan := seedRawPlan(t, nil)
		sub := seedSub(t, u.Id, plan.Id, 1000, 0, nil)

		s, err := NewFundingSession(u.Id, 500)
		require.NoError(t, err)
		assert.Equal(t, BillingSourceSubscription, s.Source())
		assert.Equal(t, int64(500), loadSub(t, sub.Id).AmountUsed)
		assert.Equal(t, 10, walletOf(t, u.Id))
	})

	t.Run("subscription_first default", func(t *testing.T) {
		initSubDB(t)
		// No subscription -> wallet.
		u := newUser(t, "sfirst", 200, "")
		s, err := NewFundingSession(u.Id, 40)
		require.NoError(t, err)
		assert.Equal(t, BillingSourceWallet, s.Source())
		assert.Equal(t, 160, walletOf(t, u.Id))

		// Active subscription -> subscription funds it.
		plan := seedRawPlan(t, nil)
		sub := seedSub(t, u.Id, plan.Id, 100, 0, nil)
		s, err = NewFundingSession(u.Id, 40)
		require.NoError(t, err)
		assert.Equal(t, BillingSourceSubscription, s.Source())
		assert.Equal(t, int64(40), loadSub(t, sub.Id).AmountUsed)

		// Exhausted subscription with overflow allowed -> wallet.
		s, err = NewFundingSession(u.Id, 90)
		require.NoError(t, err)
		assert.Equal(t, BillingSourceWallet, s.Source())
	})

	t.Run("subscription_first without overflow", func(t *testing.T) {
		initSubDB(t)
		u := newUser(t, "strict", 10000, "")
		plan := seedRawPlan(t, nil)
		seedSub(t, u.Id, plan.Id, 10, 0, func(s *model.UserSubscription) { s.AllowWalletOverflow = false })

		_, err := NewFundingSession(u.Id, 50)
		require.Error(t, err)
		assert.True(t, IsSubscriptionFundingErr(err))
		assert.Equal(t, 10000, walletOf(t, u.Id), "wallet never touched")
	})

	t.Run("zero reservation still reserves 1 from subscription", func(t *testing.T) {
		initSubDB(t)
		u := newUser(t, "zerores", 0, BillingPreferenceSubscriptionOnly)
		plan := seedRawPlan(t, nil)
		sub := seedSub(t, u.Id, plan.Id, 100, 0, nil)
		s, err := NewFundingSession(u.Id, 0)
		require.NoError(t, err)
		assert.Equal(t, BillingSourceSubscription, s.Source())
		assert.Equal(t, int64(1), loadSub(t, sub.Id).AmountUsed)
	})
}

func TestFundingSessionSettleAndRefund(t *testing.T) {
	t.Run("subscription settle up and down", func(t *testing.T) {
		initSubDB(t)
		u := subUser(t, "settlesub", 0, "default")
		_, err := UpdateUserBillingPreference(u.Id, BillingPreferenceSubscriptionOnly)
		require.NoError(t, err)
		plan := seedRawPlan(t, nil)
		sub := seedSub(t, u.Id, plan.Id, 1000, 0, nil)

		// Actual above the reservation: delta consumed on settle.
		s, err := NewFundingSession(u.Id, 40)
		require.NoError(t, err)
		require.NoError(t, s.Settle(70))
		assert.Equal(t, int64(70), loadSub(t, sub.Id).AmountUsed)

		// Idempotent: second settle is a no-op.
		require.NoError(t, s.Settle(70))
		assert.Equal(t, int64(70), loadSub(t, sub.Id).AmountUsed)

		// Refund after settle is a no-op.
		s.Refund()
		assert.Equal(t, int64(70), loadSub(t, sub.Id).AmountUsed)

		// Actual below the reservation: over-reserve returned on settle.
		s2, err := NewFundingSession(u.Id, 100)
		require.NoError(t, err)
		require.NoError(t, s2.Settle(80))
		assert.Equal(t, int64(150), loadSub(t, sub.Id).AmountUsed, "70 + 80")

		// Lifetime counters track subscription-funded usage.
		var got model.User
		require.NoError(t, model.DB.First(&got, u.Id).Error)
		assert.Equal(t, 150, got.UsedQuota)
		assert.Equal(t, 2, got.RequestCount)
		assert.Equal(t, 0, got.Quota, "wallet untouched")

		// Billing log fields carry the reference key set.
		fields := s2.BillingLogFields()
		assert.Equal(t, BillingSourceSubscription, fields["billing_source"])
		assert.Equal(t, BillingPreferenceSubscriptionOnly, fields["billing_preference"])
		assert.Equal(t, sub.Id, fields["subscription_id"])
		assert.Equal(t, int64(100), fields["subscription_pre_consumed"])
		assert.Equal(t, int64(-20), fields["subscription_post_delta"])
		assert.Equal(t, int64(1000), fields["subscription_total"])
		assert.Equal(t, int64(150), fields["subscription_used"])
		assert.Equal(t, int64(850), fields["subscription_remain"])
		assert.Equal(t, int64(80), fields["subscription_consumed"])
		assert.Equal(t, 0, fields["wallet_quota_deducted"])
		assert.Equal(t, plan.Id, fields["subscription_plan_id"])
		assert.Equal(t, plan.Title, fields["subscription_plan_title"])
	})

	t.Run("subscription refund returns the reservation", func(t *testing.T) {
		initSubDB(t)
		u := subUser(t, "refundsub", 0, "default")
		_, err := UpdateUserBillingPreference(u.Id, BillingPreferenceSubscriptionOnly)
		require.NoError(t, err)
		plan := seedRawPlan(t, nil)
		sub := seedSub(t, u.Id, plan.Id, 1000, 0, nil)

		s, err := NewFundingSession(u.Id, 40)
		require.NoError(t, err)
		require.Equal(t, int64(40), loadSub(t, sub.Id).AmountUsed)

		s.Refund()
		assert.Equal(t, int64(0), loadSub(t, sub.Id).AmountUsed)
		// Refund is idempotent and blocks a later settle.
		s.Refund()
		require.NoError(t, s.Settle(70))
		assert.Equal(t, int64(0), loadSub(t, sub.Id).AmountUsed)

		var got model.User
		require.NoError(t, model.DB.First(&got, u.Id).Error)
		assert.Equal(t, 0, got.UsedQuota, "no usage recorded for refunded request")
	})

	t.Run("wallet settle matches existing behavior", func(t *testing.T) {
		initSubDB(t)
		u := subUser(t, "settlewallet", 100, "default")
		s, err := NewFundingSession(u.Id, 40)
		require.NoError(t, err)
		require.NoError(t, s.Settle(25))
		var got model.User
		require.NoError(t, model.DB.First(&got, u.Id).Error)
		assert.Equal(t, 75, got.Quota)
		assert.Equal(t, 25, got.UsedQuota)
		fields := s.BillingLogFields()
		assert.Equal(t, BillingSourceWallet, fields["billing_source"])
		_, hasSubId := fields["subscription_id"]
		assert.False(t, hasSubId)
	})

	t.Run("wallet refund", func(t *testing.T) {
		initSubDB(t)
		u := subUser(t, "refundwallet", 100, "default")
		s, err := NewFundingSession(u.Id, 40)
		require.NoError(t, err)
		s.Refund()
		var got model.User
		require.NoError(t, model.DB.First(&got, u.Id).Error)
		assert.Equal(t, 100, got.Quota)
	})
}

// TestConcurrentSubscriptionPreConsume exercises the ledger under concurrent
// distinct request ids: with a total of 10, unit consumes never overspend and
// admitted requests exactly match usage and ledger rows. SQLite may reject
// some contenders with BUSY (deferred-tx lock upgrade), so the admitted count
// is <= total rather than exact; MySQL/Postgres row locks admit all 10.
func TestConcurrentSubscriptionPreConsume(t *testing.T) {
	initSubDB(t)
	u := subUser(t, "concsub", 0, "default")
	plan := seedRawPlan(t, nil)
	sub := seedSub(t, u.Id, plan.Id, 10, 0, nil)

	const workers = 20
	var wg sync.WaitGroup
	results := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := PreConsumeUserSubscription(fmt.Sprintf("conc-%d", i), u.Id, 1)
			results[i] = err
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, err := range results {
		if err == nil {
			succeeded++
		}
	}
	assert.GreaterOrEqual(t, succeeded, 1, "at least one contender wins")
	assert.LessOrEqual(t, succeeded, 10, "never admit past the total")
	assert.Equal(t, int64(succeeded), loadSub(t, sub.Id).AmountUsed, "usage matches admitted count")

	var count int64
	model.DB.Model(&model.SubscriptionPreConsumeRecord{}).
		Where("status = ?", SubscriptionPreConsumeStatusConsumed).Count(&count)
	assert.Equal(t, int64(succeeded), count, "one consumed ledger row per admitted request")
}

func TestCleanupSubscriptionPreConsumeRecords(t *testing.T) {
	initSubDB(t)
	u := subUser(t, "cleaner", 0, "default")
	plan := seedRawPlan(t, nil)
	seedSub(t, u.Id, plan.Id, 100, 0, nil)

	_, err := PreConsumeUserSubscription("old-req", u.Id, 1)
	require.NoError(t, err)
	_, err = PreConsumeUserSubscription("new-req", u.Id, 1)
	require.NoError(t, err)

	// Age one record past the seven-day default retention.
	old := common.NowTimestamp() - 8*24*3600
	require.NoError(t, model.DB.Model(&model.SubscriptionPreConsumeRecord{}).
		Where("request_id = ?", "old-req").Update("updated_at", old).Error)

	removed, err := CleanupSubscriptionPreConsumeRecords(0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), removed)
	var remaining []model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, "new-req", remaining[0].RequestId)
}

func TestUserSettingsMergePreservesFields(t *testing.T) {
	initSubDB(t)
	u := subUser(t, "settings", 0, "default")

	// Preference first, then quota warning: both survive.
	pref, err := UpdateUserBillingPreference(u.Id, BillingPreferenceWalletFirst)
	require.NoError(t, err)
	assert.Equal(t, BillingPreferenceWalletFirst, pref)
	require.NoError(t, UpdateUserSetting(u.Id, 12345, "email"))
	assert.Equal(t, BillingPreferenceWalletFirst, GetUserBillingPreference(u.Id))

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 12345, QuotaWarningThreshold(&got))

	// Updating the preference keeps the quota warning too.
	_, err = UpdateUserBillingPreference(u.Id, BillingPreferenceWalletOnly)
	require.NoError(t, err)
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 12345, QuotaWarningThreshold(&got))
	assert.Equal(t, BillingPreferenceWalletOnly, GetUserBillingPreference(u.Id))

	// Unknown values normalize to the default silently.
	pref, err = UpdateUserBillingPreference(u.Id, "bogus")
	require.NoError(t, err)
	assert.Equal(t, BillingPreferenceSubscriptionFirst, pref)
}

func TestNormalizeBillingPreference(t *testing.T) {
	assert.Equal(t, "subscription_first", NormalizeBillingPreference(""))
	assert.Equal(t, "subscription_first", NormalizeBillingPreference("nonsense"))
	assert.Equal(t, "wallet_first", NormalizeBillingPreference(" wallet_first "))
	assert.Equal(t, "subscription_only", NormalizeBillingPreference("subscription_only"))
	assert.Equal(t, "wallet_only", NormalizeBillingPreference("wallet_only"))
}
