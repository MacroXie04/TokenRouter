package billing

import (
	"errors"
	"fmt"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
)

func refundRelayFundingTx(tx *gorm.DB, record *model.RelayQuotaReservationRecord) error {
	if err := validateQuotaAmount(record.ReservedQuota); err != nil {
		return fmt.Errorf("stored funding reservation: %w", err)
	}
	switch record.FundingSource {
	case BillingSourceWallet:
		var user model.User
		if err := locking.SubscriptionLockForUpdate(tx).Select("id", "quota").First(&user, record.UserID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return userssvc.ErrUserNotFound
			}
			return err
		}
		newQuota, ok := quotamath.AddQuotaWithinBounds(user.Quota, record.ReservedQuota)
		if !ok {
			return ErrUserQuotaOverflow
		}
		if newQuota == user.Quota {
			return nil
		}
		result := tx.Model(&model.User{}).Where("id = ? AND quota = ?", record.UserID, user.Quota).
			UpdateColumn("quota", newQuota)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return userssvc.ErrUserNotFound
		}
		return nil
	case BillingSourceSubscription:
		return refundRelaySubscriptionTx(tx, record)
	case BillingSourceFreeModel:
		if record.ReservedQuota != 0 {
			return errors.New("invalid free-model funding reservation")
		}
		return nil
	default:
		return fmt.Errorf("unsupported funding source %q", record.FundingSource)
	}
}

func refundRelaySubscriptionTx(tx *gorm.DB, record *model.RelayQuotaReservationRecord) error {
	var ledger model.SubscriptionPreConsumeRecord
	ledgerQuery := locking.SubscriptionLockForUpdate(tx).
		Where("request_id = ?", record.ReservationID).Limit(1).Find(&ledger)
	if ledgerQuery.Error != nil {
		return ledgerQuery.Error
	}
	ledgerPresent := ledgerQuery.RowsAffected == 1
	if ledgerPresent {
		if ledger.UserId != record.UserID || ledger.UserSubscriptionId != record.SubscriptionID ||
			ledger.PreConsumed != int64(record.ReservedQuota) || ledger.UsageEpoch != record.UsageEpoch {
			return errors.New("subscription reservation ledger does not match durable reservation")
		}
		if ledger.Status == SubscriptionPreConsumeStatusRefunded {
			return nil
		}
		if ledger.Status != SubscriptionPreConsumeStatusConsumed {
			return fmt.Errorf("unsupported subscription reservation status %q", ledger.Status)
		}
	}
	var subscription model.UserSubscription
	if err := locking.SubscriptionLockForUpdate(tx).
		Where("id = ? AND user_id = ?", record.SubscriptionID, record.UserID).
		First(&subscription).Error; err != nil {
		return err
	}
	epochMatches := subscription.UsageEpoch == record.UsageEpoch
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return err
	}
	if epochMatches {
		used, ok := boundedSubscriptionQuota(subscription.AmountUsed)
		if !ok {
			return ErrSubscriptionQuotaOverflow
		}
		newUsed := used
		if record.ReservedQuota >= used {
			newUsed = 0
		} else {
			newUsed = used - record.ReservedQuota
		}
		if newUsed != used {
			result := tx.Model(&model.UserSubscription{}).
				Where("id = ? AND user_id = ? AND amount_used = ? AND usage_epoch = ?",
					subscription.Id, record.UserID, subscription.AmountUsed, record.UsageEpoch).
				Updates(map[string]any{"amount_used": int64(newUsed), "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("subscription changed during refund")
			}
		}
	}
	if ledgerPresent {
		result := tx.Model(&model.SubscriptionPreConsumeRecord{}).
			Where("id = ? AND status = ? AND usage_epoch = ?", ledger.Id, SubscriptionPreConsumeStatusConsumed, record.UsageEpoch).
			Updates(map[string]any{"status": SubscriptionPreConsumeStatusRefunded, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("subscription reservation ledger changed during refund")
		}
	}
	return nil
}

func refundRelayTokenTx(tx *gorm.DB, record *model.RelayQuotaReservationRecord) error {
	if record.TokenReserved == 0 {
		return nil
	}
	if record.TokenID <= 0 {
		return ErrTokenNotFound
	}
	if err := validateQuotaAmount(record.TokenReserved); err != nil {
		return err
	}
	var token model.Token
	if err := locking.SubscriptionLockForUpdate(tx.Unscoped()).
		Select("id", "user_id", "remain_quota").
		Where("id = ? AND user_id = ?", record.TokenID, record.UserID).
		First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTokenNotFound
		}
		return err
	}
	newRemain, ok := quotamath.AddQuotaWithinBounds(token.RemainQuota, record.TokenReserved)
	if !ok {
		return ErrTokenQuotaOverflow
	}
	result := tx.Unscoped().Model(&model.Token{}).
		Where("id = ? AND user_id = ? AND remain_quota = ?", record.TokenID, record.UserID, token.RemainQuota).
		UpdateColumn("remain_quota", newRemain)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTokenNotFound
	}
	return nil
}

func reverseSettledRelayFundingTx(tx *gorm.DB, record *model.RelayQuotaReservationRecord, now int64) error {
	if record == nil {
		return errors.New("settled relay reservation is nil")
	}
	if err := validateQuotaAmount(record.ActualQuota); err != nil {
		return fmt.Errorf("stored settled quota: %w", err)
	}
	if record.ActualQuota == 0 {
		return nil
	}
	switch record.FundingSource {
	case BillingSourceWallet:
		var user model.User
		if err := locking.SubscriptionLockForUpdate(tx).Select("id", "quota").
			Where("id = ?", record.UserID).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return userssvc.ErrUserNotFound
			}
			return err
		}
		newQuota, ok := quotamath.AddQuotaWithinBounds(user.Quota, record.ActualQuota)
		if !ok {
			return ErrUserQuotaOverflow
		}
		result := tx.Model(&model.User{}).Where("id = ? AND quota = ?", record.UserID, user.Quota).
			UpdateColumn("quota", newQuota)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return userssvc.ErrUserNotFound
		}
		return nil
	case BillingSourceSubscription:
		return postConsumeUserSubscriptionDeltaEpochTx(
			tx, record.SubscriptionID, -int64(record.ActualQuota), now, &record.UsageEpoch,
		)
	default:
		return fmt.Errorf("unsupported funding source %q", record.FundingSource)
	}
}

func reverseSettledRelayTokenTx(tx *gorm.DB, record *model.RelayQuotaReservationRecord) error {
	if record == nil || record.TokenUnlimited || record.TokenID == 0 || record.ActualQuota == 0 {
		return nil
	}
	if record.TokenID < 0 {
		return ErrTokenNotFound
	}
	var token model.Token
	if err := locking.SubscriptionLockForUpdate(tx.Unscoped()).
		Select("id", "user_id", "remain_quota").
		Where("id = ? AND user_id = ?", record.TokenID, record.UserID).
		First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTokenNotFound
		}
		return err
	}
	newRemain, ok := quotamath.AddQuotaWithinBounds(token.RemainQuota, record.ActualQuota)
	if !ok {
		return ErrTokenQuotaOverflow
	}
	result := tx.Unscoped().Model(&model.Token{}).
		Where("id = ? AND user_id = ? AND remain_quota = ?", record.TokenID, record.UserID, token.RemainQuota).
		UpdateColumn("remain_quota", newRemain)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTokenNotFound
	}
	return nil
}
