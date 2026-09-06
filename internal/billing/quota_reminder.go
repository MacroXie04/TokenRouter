package billing

import (
	"errors"
	"fmt"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
)

var errQuotaReminderNotNeeded = errors.New("quota reminder is not needed")

type quotaReminderDelivery struct {
	user         model.User
	settings     userssvc.UserSettings
	previousAt   int64
	reservedAt   int64
	remaining    int64
	subscription bool
}

// CheckAndSendQuotaReminder sends a low-quota notification after settlement. The
// threshold is the QuotaRemindThreshold option (default 1000, matching the
// reference; 0 disables). To avoid per-request spam (the reference notifies on
// every request below threshold), a reminder is reserved atomically and sent
// at most once per hour per user. Failed delivery releases the reservation so a
// later request can retry.
func CheckAndSendQuotaReminder(userId int) {
	checkAndSendQuotaReminder(userId, 0)
}

// CheckAndSendQuotaReminderForReservation selects the balance that actually
// funded a settled request. Subscription reminders re-read the live
// entitlement under a row lock, so a reset or concurrent credit cannot emit a
// stale warning from the reservation snapshot.
func CheckAndSendQuotaReminderForReservation(userID int, reservation *RelayQuotaReservation) {
	subscriptionID := 0
	if reservation != nil {
		reservation.mu.Lock()
		if reservation.settled && reservation.funding != nil {
			reservation.funding.mu.Lock()
			if reservation.funding.source == BillingSourceFreeModel {
				reservation.funding.mu.Unlock()
				reservation.mu.Unlock()
				return
			}
			if reservation.funding.source == BillingSourceSubscription {
				subscriptionID = reservation.funding.subscriptionId
			}
			reservation.funding.mu.Unlock()
		}
		reservation.mu.Unlock()
	}
	checkAndSendQuotaReminder(userID, subscriptionID)
}

func checkAndSendQuotaReminder(userID, subscriptionID int) {
	delivery, threshold, err := reserveQuotaReminder(userID, subscriptionID)
	if err != nil {
		return
	}
	title := "TokenRouter 额度提醒"
	balanceName := "剩余额度"
	if delivery.subscription {
		title = "TokenRouter 订阅额度提醒"
		balanceName = "订阅剩余额度"
	}
	body := fmt.Sprintf("您的 TokenRouter %s为 %d，低于提醒阈值 %d，请及时处理以免影响使用。",
		balanceName, delivery.remaining, threshold)
	if err := userssvc.SendUserNotification(delivery.user.Id, delivery.user.Email, delivery.user.EmailVerified,
		delivery.settings, userssvc.UserNotification{Type: "quota_exceed", Title: title, Content: body}); err != nil {
		// Only release our own reservation. A later successful sender must never
		// be rolled back by this failure path.
		_ = model.DB.Model(&model.User{}).
			Where("id = ? AND quota_reminder_at = ?", delivery.user.Id, delivery.reservedAt).
			Update("quota_reminder_at", delivery.previousAt).Error
	}
}

func reserveQuotaReminder(userID, subscriptionID int) (*quotaReminderDelivery, int, error) {
	if userID <= 0 || model.DB == nil {
		return nil, 0, errQuotaReminderNotNeeded
	}
	globalThreshold := setting.GetOptionIntOrDefault(setting.QuotaRemindThresholdOption, 1000)
	now := wallclock.NowTimestamp()
	delivery := &quotaReminderDelivery{reservedAt: now}
	threshold := 0
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		query := locking.SubscriptionLockForUpdate(tx).Select(
			"id", "email", "email_verified", "quota", "quota_reminder_at", "setting",
		).Where("id = ?", userID)
		if err := query.First(&delivery.user).Error; err != nil {
			return err
		}
		delivery.settings = userssvc.ParseUserSettings(delivery.user.Setting)
		threshold = globalThreshold
		if override := delivery.settings.QuotaWarningThreshold; override > 0 {
			threshold = override
		}
		if threshold <= 0 || !quotamath.QuotaWithinBounds(threshold) || now-delivery.user.QuotaReminderAt < 3600 {
			return errQuotaReminderNotNeeded
		}

		if subscriptionID > 0 {
			var subscription model.UserSubscription
			if err := locking.SubscriptionLockForUpdate(tx).
				Select("id", "user_id", "status", "end_time", "amount_total", "amount_used").
				Where("id = ? AND user_id = ? AND status = ? AND end_time > ?",
					subscriptionID, userID, SubscriptionStatusActive, now).
				First(&subscription).Error; err != nil {
				return errQuotaReminderNotNeeded
			}
			total, totalOK := boundedSubscriptionQuota(subscription.AmountTotal)
			used, usedOK := boundedSubscriptionQuota(subscription.AmountUsed)
			if !totalOK || !usedOK || total == 0 || used > total {
				return errQuotaReminderNotNeeded
			}
			delivery.remaining = int64(total - used)
			delivery.subscription = true
		} else {
			if !quotamath.QuotaWithinBounds(delivery.user.Quota) {
				return errQuotaReminderNotNeeded
			}
			delivery.remaining = int64(delivery.user.Quota)
		}
		if delivery.remaining >= int64(threshold) {
			return errQuotaReminderNotNeeded
		}

		delivery.previousAt = delivery.user.QuotaReminderAt
		update := tx.Model(&model.User{}).
			Where("id = ? AND quota_reminder_at = ?", userID, delivery.previousAt)
		if subscriptionID == 0 {
			update = update.Where("quota = ?", delivery.user.Quota)
		}
		result := update.Update("quota_reminder_at", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errQuotaReminderNotNeeded
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return delivery, threshold, nil
}
