package billing

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"testing"
	"time"
)

func epochTestSubscription(t *testing.T, userID, planID int, epoch, used int64) *model.UserSubscription {
	t.Helper()
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	sub := &model.UserSubscription{
		UserId: userID, PlanId: planID, AmountTotal: 1_000, AmountUsed: used, UsageEpoch: epoch,
		StartTime: now - 3600, EndTime: now + 86400, Status: SubscriptionStatusActive,
		EntitlementVersion:        UserSubscriptionEntitlementVersion,
		EntitlementMigrationState: SubscriptionEntitlementMigrationBackfilled,
		QuotaResetPeriodSnapshot:  SubscriptionResetNever,
		AllowWalletOverflow:       true, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, model.DB.Create(sub).Error)
	return sub
}

func TestUsageEpochFencesDelayedSubscriptionRefundAndSettlement(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "epoch-direct", 0, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.QuotaResetPeriod = SubscriptionResetNever
		plan.TotalAmount = 1_000
	})
	sub := epochTestSubscription(t, user.Id, plan.Id, 7, 0)

	oldRefund, err := PreConsumeUserSubscription("epoch-old-refund", user.Id, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(7), oldRefund.UsageEpoch)
	_, err = AdminResetUserSubscriptionsByPlan(user.Id, plan.Id, false)
	require.NoError(t, err)
	newWindow, err := PreConsumeUserSubscription("epoch-new-window", user.Id, 6)
	require.NoError(t, err)
	assert.Equal(t, int64(8), newWindow.UsageEpoch)

	require.NoError(t, RefundSubscriptionPreConsume("epoch-old-refund"))
	stored := loadSub(t, sub.Id)
	assert.Equal(t, int64(8), stored.UsageEpoch)
	assert.Equal(t, int64(6), stored.AmountUsed, "an old-window refund must not erase current usage")
	var refundedLedger model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", "epoch-old-refund").First(&refundedLedger).Error)
	assert.Equal(t, SubscriptionPreConsumeStatusRefunded, refundedLedger.Status)

	oldSettle, err := PreConsumeUserSubscription("epoch-old-settle", user.Id, 10)
	require.NoError(t, err)
	_, err = AdminResetUserSubscriptionsByPlan(user.Id, plan.Id, false)
	require.NoError(t, err)
	_, err = PreConsumeUserSubscription("epoch-current-after-settle", user.Id, 5)
	require.NoError(t, err)
	funding := &FundingSession{
		userId: user.Id, requestId: "epoch-old-settle", source: BillingSourceSubscription,
		reserved: 10, subscriptionId: oldSettle.UserSubscriptionId, usageEpoch: oldSettle.UsageEpoch,
	}
	require.NoError(t, funding.Settle(17))
	stored = loadSub(t, sub.Id)
	assert.Equal(t, int64(9), stored.UsageEpoch)
	assert.Equal(t, int64(5), stored.AmountUsed, "an old-window positive overage must not charge the current window")
	var settledLedger model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", "epoch-old-settle").First(&settledLedger).Error)
	assert.Equal(t, SubscriptionPreConsumeStatusSettled, settledLedger.Status)
}

func TestUsageEpochFencesExactPartialAndOverageSettlements(t *testing.T) {
	for _, actual := range []int{10, 4, 17} {
		t.Run(fmt.Sprintf("actual_%d", actual), func(t *testing.T) {
			initSubDB(t)
			user := subUser(t, fmt.Sprintf("epoch-shape-%d", actual), 0, "default")
			plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
				plan.QuotaResetPeriod = SubscriptionResetNever
				plan.TotalAmount = 1_000
			})
			sub := epochTestSubscription(t, user.Id, plan.Id, 40, 0)
			reserved, err := PreConsumeUserSubscription(fmt.Sprintf("epoch-shape-old-%d", actual), user.Id, 10)
			require.NoError(t, err)
			_, err = AdminResetUserSubscriptionsByPlan(user.Id, plan.Id, false)
			require.NoError(t, err)
			_, err = PreConsumeUserSubscription(fmt.Sprintf("epoch-shape-current-%d", actual), user.Id, 3)
			require.NoError(t, err)
			funding := &FundingSession{
				userId: user.Id, requestId: fmt.Sprintf("epoch-shape-old-%d", actual), source: BillingSourceSubscription,
				reserved: 10, subscriptionId: reserved.UserSubscriptionId, usageEpoch: reserved.UsageEpoch,
			}
			require.NoError(t, funding.Settle(actual))
			stored := loadSub(t, sub.Id)
			assert.Equal(t, int64(41), stored.UsageEpoch)
			assert.Equal(t, int64(3), stored.AmountUsed)
			var ledger model.SubscriptionPreConsumeRecord
			require.NoError(t, model.DB.Where("request_id = ?", funding.requestId).First(&ledger).Error)
			assert.Equal(t, SubscriptionPreConsumeStatusSettled, ledger.Status)
		})
	}
}

func TestDurableUsageEpochFencesRefundAndPositiveOverage(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	user := model.User{
		Username: "epoch-durable", Status: model.UserStatusEnabled,
		Setting: `{"billing_preference":"subscription_only"}`,
	}
	require.NoError(t, db.Create(&user).Error)
	plan := model.SubscriptionPlan{
		Title: "Epoch", PriceAmount: "0", Enabled: true, TotalAmount: 1_000,
		DurationUnit: SubscriptionDurationDay, DurationValue: 1, QuotaResetPeriod: SubscriptionResetNever,
	}
	require.NoError(t, db.Create(&plan).Error)
	sub := epochTestSubscription(t, user.Id, plan.Id, 20, 0)
	token := model.Token{UserId: user.Id, Key: "sk-epoch-durable", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(&token).Error)

	refundReservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	refundRecord := loadRelayQuotaReservationRecord(t, refundReservation.ReservationID())
	assert.Equal(t, int64(20), refundRecord.UsageEpoch)
	_, err = AdminResetUserSubscriptionsByPlan(user.Id, plan.Id, false)
	require.NoError(t, err)
	_, err = PreConsumeUserSubscription("epoch-durable-current-refund", user.Id, 4)
	require.NoError(t, err)
	require.NoError(t, refundReservation.Refund())
	stored := loadSub(t, sub.Id)
	assert.Equal(t, int64(21), stored.UsageEpoch)
	assert.Equal(t, int64(4), stored.AmountUsed)
	refundRecord = loadRelayQuotaReservationRecord(t, refundReservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, refundRecord.Status)
	var refundLedger model.SubscriptionPreConsumeRecord
	require.NoError(t, db.Where("request_id = ?", refundReservation.ReservationID()).First(&refundLedger).Error)
	assert.Equal(t, SubscriptionPreConsumeStatusRefunded, refundLedger.Status)

	settleReservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
	require.NoError(t, err)
	require.NoError(t, settleReservation.MarkDispatched())
	_, err = AdminResetUserSubscriptionsByPlan(user.Id, plan.Id, false)
	require.NoError(t, err)
	_, err = PreConsumeUserSubscription("epoch-durable-current-settle", user.Id, 3)
	require.NoError(t, err)
	require.NoError(t, settleReservation.Settle(18))
	stored = loadSub(t, sub.Id)
	assert.Equal(t, int64(22), stored.UsageEpoch)
	assert.Equal(t, int64(3), stored.AmountUsed)
	settleRecord := loadRelayQuotaReservationRecord(t, settleReservation.ReservationID())
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, settleRecord.Status)
	var settleLedger model.SubscriptionPreConsumeRecord
	require.NoError(t, db.Where("request_id = ?", settleReservation.ReservationID()).First(&settleLedger).Error)
	assert.Equal(t, SubscriptionPreConsumeStatusSettled, settleLedger.Status)
}

func TestDurableUsageEpochFencesEverySettlementShape(t *testing.T) {
	for _, actual := range []int{10, 4, 17} {
		t.Run(fmt.Sprintf("actual_%d", actual), func(t *testing.T) {
			db := setupRelayQuotaReservationDB(t)
			user := model.User{
				Username: fmt.Sprintf("epoch-durable-shape-%d", actual), Status: model.UserStatusEnabled,
				Setting: `{"billing_preference":"subscription_only"}`,
			}
			require.NoError(t, db.Create(&user).Error)
			plan := model.SubscriptionPlan{
				Title: "Epoch shape", PriceAmount: "0", Enabled: true, TotalAmount: 1_000,
				DurationUnit: SubscriptionDurationDay, DurationValue: 1, QuotaResetPeriod: SubscriptionResetNever,
			}
			require.NoError(t, db.Create(&plan).Error)
			sub := epochTestSubscription(t, user.Id, plan.Id, 50, 0)
			token := model.Token{UserId: user.Id, Key: fmt.Sprintf("sk-epoch-shape-%d", actual), Status: TokenStatusEnabled, RemainQuota: 100}
			require.NoError(t, db.Create(&token).Error)
			reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
			require.NoError(t, err)
			require.NoError(t, reservation.MarkDispatched())
			_, err = AdminResetUserSubscriptionsByPlan(user.Id, plan.Id, false)
			require.NoError(t, err)
			_, err = PreConsumeUserSubscription(fmt.Sprintf("epoch-durable-shape-current-%d", actual), user.Id, 3)
			require.NoError(t, err)
			require.NoError(t, reservation.Settle(actual))
			stored := loadSub(t, sub.Id)
			assert.Equal(t, int64(51), stored.UsageEpoch)
			assert.Equal(t, int64(3), stored.AmountUsed)
			record := loadRelayQuotaReservationRecord(t, reservation.ReservationID())
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
			assert.Equal(t, int64(50), record.UsageEpoch)
			var ledger model.SubscriptionPreConsumeRecord
			require.NoError(t, db.Where("request_id = ?", reservation.ReservationID()).First(&ledger).Error)
			assert.Equal(t, SubscriptionPreConsumeStatusSettled, ledger.Status)
			var storedUser model.User
			var storedToken model.Token
			require.NoError(t, db.First(&storedUser, user.Id).Error)
			require.NoError(t, db.First(&storedToken, token.Id).Error)
			assert.Equal(t, actual, storedUser.UsedQuota)
			assert.Equal(t, actual, storedToken.UsedQuota)
		})
	}
}

func TestLazyResetUsesImmutableSnapshotAndUTCConstantTimeCatchup(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "epoch-lazy", 0, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.TotalAmount = 1_000
		plan.QuotaResetPeriod = SubscriptionResetDaily
	})
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	sub := &model.UserSubscription{
		UserId: user.Id, PlanId: plan.Id, AmountTotal: 1_000, AmountUsed: 700, UsageEpoch: 4,
		StartTime: now - 10*86400, EndTime: now + 86400, Status: SubscriptionStatusActive,
		LastResetTime: now - 10*86400, NextResetTime: now - 9*86400,
		EntitlementVersion:        UserSubscriptionEntitlementVersion,
		EntitlementMigrationState: SubscriptionEntitlementMigrationBackfilled,
		QuotaResetPeriodSnapshot:  SubscriptionResetDaily, AllowWalletOverflow: true,
	}
	require.NoError(t, model.DB.Create(sub).Error)
	require.NoError(t, model.DB.Delete(&model.SubscriptionPlan{}, plan.Id).Error)
	result, err := PreConsumeUserSubscription("lazy-deleted-plan", user.Id, 9)
	require.NoError(t, err)
	assert.Equal(t, int64(5), result.UsageEpoch)
	stored := loadSub(t, sub.Id)
	assert.Equal(t, int64(9), stored.AmountUsed)
	assert.Greater(t, stored.NextResetTime, now)

	instant := time.Date(2026, 3, 8, 18, 45, 0, 0, time.UTC)
	shanghai := time.FixedZone("UTC+8", 8*3600)
	utcNext := calcSubscriptionNextResetTime(instant, &model.SubscriptionPlan{QuotaResetPeriod: SubscriptionResetDaily}, 0)
	localNext := calcSubscriptionNextResetTime(instant.In(shanghai), &model.SubscriptionPlan{QuotaResetPeriod: SubscriptionResetDaily}, 0)
	assert.Equal(t, utcNext, localNext, "host/node location must not change reset boundaries")

	last, next, advanced := advanceSubscriptionResetSchedule(1, 1+50_000_000_000,
		1+60_000_000_000, &model.SubscriptionPlan{QuotaResetPeriod: SubscriptionResetCustom, QuotaResetCustomSeconds: 7})
	require.True(t, advanced)
	assert.LessOrEqual(t, last, int64(1+50_000_000_000))
	assert.Greater(t, next, int64(1+50_000_000_000))
}

func TestLegacyEntitlementReviewRequiresRootAndCommitsImmutableAudit(t *testing.T) {
	initSubDB(t)
	target := subUser(t, "legacy-entitlement-target", 0, "vip")
	root := &model.User{Username: "legacy-entitlement-root", Password: "x", Role: roles.RoleRootUser,
		Status: model.UserStatusEnabled, Group: "default"}
	admin := &model.User{Username: "legacy-entitlement-admin", Password: "x", Role: roles.RoleAdminUser,
		Status: model.UserStatusEnabled, Group: "default"}
	require.NoError(t, model.DB.Create(root).Error)
	require.NoError(t, model.DB.Create(admin).Error)
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	sub := &model.UserSubscription{
		UserId: target.Id, PlanId: 987654, AmountTotal: 100, UsageEpoch: 9,
		StartTime: now - 10, EndTime: now + 3600, Status: SubscriptionStatusActive,
		UpgradeGroup: "vip", EntitlementMigrationState: SubscriptionEntitlementMigrationReview,
		UpdatedAt: now,
	}
	require.NoError(t, model.DB.Create(sub).Error)
	resolution := LegacySubscriptionEntitlementResolution{
		ExpectedPlanID: sub.PlanId, ExpectedUsageEpoch: sub.UsageEpoch,
		QuotaResetPeriod: SubscriptionResetCustom, QuotaResetCustomSeconds: 300,
		GroupBaseline: "default", Reason: "verified against archived purchase invoice",
	}
	require.Error(t, ResolveLegacySubscriptionEntitlementReview(admin.Id, sub.Id, resolution))
	require.NoError(t, ResolveLegacySubscriptionEntitlementReview(root.Id, sub.Id, resolution))
	require.NoError(t, ResolveLegacySubscriptionEntitlementReview(root.Id, sub.Id, resolution), "identical root retry must be idempotent")

	var stored model.UserSubscription
	require.NoError(t, model.DB.First(&stored, sub.Id).Error)
	assert.Equal(t, UserSubscriptionEntitlementVersion, stored.EntitlementVersion)
	assert.Equal(t, SubscriptionEntitlementMigrationResolved, stored.EntitlementMigrationState)
	assert.Equal(t, SubscriptionResetCustom, stored.QuotaResetPeriodSnapshot)
	assert.Equal(t, int64(300), stored.QuotaResetCustomSecondsSnapshot)
	assert.Equal(t, "default", stored.GroupBaseline)
	var auditCount int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("content LIKE ?", "%archived purchase invoice%").Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)

	changed := resolution
	changed.QuotaResetCustomSeconds = 600
	require.Error(t, ResolveLegacySubscriptionEntitlementReview(root.Id, sub.Id, changed))
	require.NoError(t, model.DB.First(&stored, sub.Id).Error)
	assert.Equal(t, int64(300), stored.QuotaResetCustomSecondsSnapshot)
}

func TestLegacyEntitlementBackfillMovesOrphansToReviewWithoutStarvation(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "legacy-backfill-progress", 0, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.QuotaResetPeriod = SubscriptionResetMonthly
	})
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	rows := []model.UserSubscription{
		{
			UserId: 987654, PlanId: plan.Id, AmountTotal: 100, StartTime: now,
			EndTime: now + 3600, Status: SubscriptionStatusActive,
			EntitlementMigrationState: SubscriptionEntitlementMigrationPending,
		},
		{
			UserId: user.Id, PlanId: 987654, AmountTotal: 100, StartTime: now,
			EndTime: now + 3600, Status: SubscriptionStatusActive,
			EntitlementMigrationState: SubscriptionEntitlementMigrationPending,
		},
		{
			UserId: user.Id, PlanId: plan.Id, AmountTotal: 100, StartTime: now,
			EndTime: now + 3600, Status: SubscriptionStatusActive,
			EntitlementMigrationState: SubscriptionEntitlementMigrationPending,
		},
	}
	for i := range rows {
		require.NoError(t, model.DB.Create(&rows[i]).Error)
	}
	for range rows {
		require.NoError(t, BackfillLegacySubscriptionEntitlementSnapshots(1))
	}
	for i := range rows {
		require.NoError(t, model.DB.First(&rows[i], rows[i].Id).Error)
	}
	assert.Equal(t, SubscriptionEntitlementMigrationReview, rows[0].EntitlementMigrationState)
	assert.Equal(t, SubscriptionEntitlementMigrationReview, rows[1].EntitlementMigrationState)
	assert.Equal(t, SubscriptionEntitlementMigrationBackfilled, rows[2].EntitlementMigrationState)
	assert.Equal(t, SubscriptionResetMonthly, rows[2].QuotaResetPeriodSnapshot)
}

func TestSubscriptionHardDeleteRejectsNonterminalAccountingReferences(t *testing.T) {
	t.Run("direct preconsume", func(t *testing.T) {
		initSubDB(t)
		user := subUser(t, "delete-guard-direct", 0, "default")
		plan := seedRawPlan(t, nil)
		sub := epochTestSubscription(t, user.Id, plan.Id, 1, 0)
		_, err := PreConsumeUserSubscription("delete-guard-direct-request", user.Id, 10)
		require.NoError(t, err)
		_, err = AdminDeleteUserSubscription(sub.Id)
		require.ErrorIs(t, err, ErrSubscriptionHasAccountingReferences)
		require.NoError(t, RefundSubscriptionPreConsume("delete-guard-direct-request"))
		_, err = AdminDeleteUserSubscription(sub.Id)
		require.NoError(t, err)
	})

	t.Run("durable reservation", func(t *testing.T) {
		db := setupRelayQuotaReservationDB(t)
		user := model.User{
			Username: "delete-guard-durable", Status: model.UserStatusEnabled,
			Setting: `{"billing_preference":"subscription_only"}`,
		}
		require.NoError(t, db.Create(&user).Error)
		plan := model.SubscriptionPlan{
			Title: "Delete guard", PriceAmount: "0", Enabled: true, TotalAmount: 100,
			DurationUnit: SubscriptionDurationDay, DurationValue: 1, QuotaResetPeriod: SubscriptionResetNever,
		}
		require.NoError(t, db.Create(&plan).Error)
		sub := epochTestSubscription(t, user.Id, plan.Id, 2, 0)
		token := model.Token{UserId: user.Id, Key: "sk-delete-guard", Status: TokenStatusEnabled, RemainQuota: 100}
		require.NoError(t, db.Create(&token).Error)
		reservation, err := NewRelayQuotaReservation(user.Id, &token, 10)
		require.NoError(t, err)
		_, err = AdminDeleteUserSubscription(sub.Id)
		require.ErrorIs(t, err, ErrSubscriptionHasAccountingReferences)
		require.NoError(t, reservation.Refund())
		_, err = AdminDeleteUserSubscription(sub.Id)
		require.NoError(t, err)
	})
}

func TestExpireDueSubscriptionsDrainsUsersAndReconcilesUpgradeChainOnce(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "expiry-chain", 0, "default")
	vip := seedPlan(t, 100, "vip")
	pro := seedPlan(t, 100, "pro")
	_, err := AdminBindSubscription(user.Id, vip.Id)
	require.NoError(t, err)
	_, err = AdminBindSubscription(user.Id, pro.Id)
	require.NoError(t, err)
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).
		Updates(map[string]any{"end_time": now - 1}).Error)

	for i := 0; i < 3; i++ {
		extra := subUser(t, "expiry-drain-"+string(rune('a'+i)), 0, "default")
		due := &model.UserSubscription{
			UserId: extra.Id, PlanId: vip.Id, AmountTotal: 1, StartTime: now - 10,
			EndTime: now - 1, Status: SubscriptionStatusActive,
			EntitlementVersion:        UserSubscriptionEntitlementVersion,
			EntitlementMigrationState: SubscriptionEntitlementMigrationBackfilled,
			QuotaResetPeriodSnapshot:  SubscriptionResetNever,
		}
		require.NoError(t, model.DB.Create(due).Error)
	}
	require.NoError(t, ExpireDueSubscriptions(1), "limit bounds each query but must not leave later users starved")
	var storedUser model.User
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, "default", storedUser.Group)
	var activeDue int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).
		Where("status = ? AND end_time <= ?", SubscriptionStatusActive, now).Count(&activeDue).Error)
	assert.Zero(t, activeDue)
}
