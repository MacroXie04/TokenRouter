package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

var (
	ErrInvalidReferralCredit  = errors.New("invalid referral credit")
	ErrReferralCreditOverflow = errors.New("referral credit overflow")
)

// CreateUserWithReferral creates a user and applies every configured referral
// credit in one transaction. A failed or overflowing credit therefore cannot
// leave a partially registered account or partially updated inviter counters.
func CreateUserWithReferral(user *model.User) error {
	if user == nil {
		return fmt.Errorf("%w: user is nil", ErrInvalidReferralCredit)
	}
	plan, err := PlanRegistrationMutationFromEnvironment(user.Username)
	if err != nil {
		return err
	}
	return createUserWithReferralPlan(user, plan)
}

func createUserWithReferralPlan(user *model.User, plan RegistrationMutationPlan) error {
	if user == nil {
		return fmt.Errorf("%w: user is nil", ErrInvalidReferralCredit)
	}
	candidate := *user
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := requirePasswordRegistrationEnabledWithTx(tx); err != nil {
			return err
		}
		if err := InsertPlannedRegistrationUserWithTx(tx, &candidate, plan); err != nil {
			return err
		}
		if err := creditInviterTx(tx, candidate.InviterId, candidate.Id); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	*user = candidate
	return nil
}

// CreditInviter applies invite-code bonuses atomically to an already-created
// invitee. New-account paths should use CreateUserWithReferral (or call the
// transaction helper from their larger identity-binding transaction).
func CreditInviter(inviterId, inviteeId int) error {
	if inviterId == 0 || !PaymentComplianceConfirmed() {
		return nil
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		return creditInviterTx(tx, inviterId, inviteeId)
	})
}

func referralCredits() (inviteeQuota, inviterQuota int, err error) {
	values := setting.GetOptions(setting.QuotaForInviteeOption, setting.QuotaForInviterOption)
	inviteeQuota, err = parseReferralCreditOption(values, setting.QuotaForInviteeOption)
	if err != nil {
		return 0, 0, err
	}
	inviterQuota, err = parseReferralCreditOption(values, setting.QuotaForInviterOption)
	if err != nil {
		return 0, 0, err
	}
	return inviteeQuota, inviterQuota, nil
}

func parseReferralCreditOption(values map[string]string, key string) (int, error) {
	raw, present := values[key]
	if !present || raw == "" {
		return 0, nil
	}
	if raw != strings.TrimSpace(raw) || len(raw) > 10 {
		return 0, fmt.Errorf("%w: %s is outside 0..%d", ErrInvalidReferralCredit, key, common.MaxQuota)
	}
	for _, character := range raw {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("%w: %s is outside 0..%d", ErrInvalidReferralCredit, key, common.MaxQuota)
		}
	}
	parsed, parseErr := strconv.ParseInt(raw, 10, 64)
	if parseErr != nil || parsed < 0 || parsed > common.MaxQuota {
		return 0, fmt.Errorf("%w: %s is outside 0..%d", ErrInvalidReferralCredit, key, common.MaxQuota)
	}
	return int(parsed), nil
}

func creditInviterTx(tx *gorm.DB, inviterId, inviteeId int) error {
	if inviterId == 0 || !PaymentComplianceConfirmed() {
		return nil
	}
	if tx == nil || inviterId < 1 || inviteeId < 1 || inviterId == inviteeId {
		return fmt.Errorf("%w: invalid inviter or invitee", ErrInvalidReferralCredit)
	}
	inviteeQuota, inviterQuota, err := referralCredits()
	if err != nil {
		return err
	}

	// Lock and update the shared inviter row first so concurrent registrations
	// cannot lose increments. Absolute-value writes plus old-value predicates
	// retain the same guarantee on SQLite, where FOR UPDATE is unavailable.
	if inviterQuota > 0 {
		var inviter model.User
		if err := subscriptionLockForUpdate(tx).
			Select("id", "aff_count", "aff_quota", "aff_history_quota").
			First(&inviter, inviterId).Error; err != nil {
			return err
		}
		newCount, countOK := common.AddQuotaWithinBounds(inviter.AffCount, 1)
		newAffQuota, quotaOK := common.AddQuotaWithinBounds(inviter.AffQuota, inviterQuota)
		newHistoryQuota, historyOK := common.AddQuotaWithinBounds(inviter.AffHistoryQuota, inviterQuota)
		if !countOK || !quotaOK || !historyOK {
			return ErrReferralCreditOverflow
		}
		result := tx.Model(&model.User{}).
			Where("id = ? AND aff_count = ? AND aff_quota = ? AND aff_history_quota = ?",
				inviterId, inviter.AffCount, inviter.AffQuota, inviter.AffHistoryQuota).
			Updates(map[string]any{
				"aff_count":         newCount,
				"aff_quota":         newAffQuota,
				"aff_history_quota": newHistoryQuota,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("concurrent inviter credit update")
		}
	}

	if inviteeQuota > 0 {
		var invitee model.User
		if err := subscriptionLockForUpdate(tx).Select("id", "quota").First(&invitee, inviteeId).Error; err != nil {
			return err
		}
		newQuota, ok := common.AddQuotaWithinBounds(invitee.Quota, inviteeQuota)
		if !ok {
			return ErrReferralCreditOverflow
		}
		result := tx.Model(&model.User{}).
			Where("id = ? AND quota = ?", inviteeId, invitee.Quota).
			Update("quota", newQuota)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("concurrent invitee credit update")
		}
	}
	return nil
}

// TransferAffQuota moves accumulated affiliate quota into usable quota. The
// minimum transfer is one QuotaPerUnit, and the transfer runs in a transaction
// with a row lock so concurrent transfers cannot double-spend.
func TransferAffQuota(userId, quota int) error {
	// QuotaPerUnit is an immutable accounting invariant, not a mutable option.
	// The compatibility option remains visible to administrators, but even a
	// stale or directly-corrupted database row must never lower this boundary.
	minQuota := common.QuotaPerUnit
	if userId < 1 || quota <= 0 || int64(quota) > common.MaxQuota || minQuota <= 0 {
		return fmt.Errorf("%w: invalid transfer", ErrInvalidReferralCredit)
	}
	// Affiliate credit is funded by the payment-adjacent referral program.
	// Keep the invariant below the HTTP layer so no internal caller can turn it
	// into spendable quota while the current compliance terms are unconfirmed.
	if !PaymentComplianceConfirmed() {
		return ErrPaymentComplianceRequired
	}
	if quota < minQuota {
		return fmt.Errorf("转移额度最小为 %d", minQuota)
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		query := tx
		if model.UsingPostgreSQL() || model.UsingMySQL() {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.Select("id", "quota", "aff_quota").First(&user, userId).Error; err != nil {
			return err
		}
		if !common.QuotaWithinBounds(user.AffQuota) || !common.QuotaWithinBounds(user.Quota) {
			return ErrReferralCreditOverflow
		}
		if user.AffQuota < quota {
			return errors.New("邀请额度不足")
		}
		newQuota, ok := common.AddQuotaWithinBounds(user.Quota, quota)
		if !ok {
			return ErrReferralCreditOverflow
		}
		result := tx.Model(&model.User{}).
			Where("id = ? AND quota = ? AND aff_quota = ?", userId, user.Quota, user.AffQuota).
			Updates(map[string]any{
				"aff_quota": user.AffQuota - quota,
				"quota":     newQuota,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("concurrent affiliate transfer")
		}
		return nil
	})
}
