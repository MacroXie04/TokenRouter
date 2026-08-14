package service

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// CreditInviter applies invite-code bonuses after a successful registration
// (gated on payment-compliance confirmation, matching the reference behavior):
// the invitee receives QuotaForInvitee directly, while the inviter accumulates
// QuotaForInviter as affiliate quota (transferred to usable quota via
// TransferAffQuota), plus referral counters.
func CreditInviter(inviterId, inviteeId int) {
	if inviterId == 0 || !PaymentComplianceConfirmed() {
		return
	}
	if v := setting.GetOptionIntOrDefault(setting.QuotaForInviteeOption, 0); v > 0 {
		_ = IncreaseUserQuota(inviteeId, v)
	}
	if v := setting.GetOptionIntOrDefault(setting.QuotaForInviterOption, 0); v > 0 {
		_ = model.DB.Model(&model.User{}).Where("id = ?", inviterId).Updates(map[string]any{
			"aff_count":         gorm.Expr("aff_count + 1"),
			"aff_quota":         gorm.Expr("aff_quota + ?", v),
			"aff_history_quota": gorm.Expr("aff_history_quota + ?", v),
		}).Error
	}
}

// TransferAffQuota moves accumulated affiliate quota into usable quota. The
// minimum transfer is one QuotaPerUnit, and the transfer runs in a transaction
// with a row lock so concurrent transfers cannot double-spend.
func TransferAffQuota(userId, quota int) error {
	minQuota := setting.GetOptionIntOrDefault(setting.QuotaPerUnitOption, 500000)
	if quota < minQuota {
		return fmt.Errorf("转移额度最小为 %d", minQuota)
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, userId).Error; err != nil {
			return err
		}
		if user.AffQuota < quota {
			return errors.New("邀请额度不足")
		}
		return tx.Model(&model.User{}).Where("id = ?", userId).Updates(map[string]any{
			"aff_quota": gorm.Expr("aff_quota - ?", quota),
			"quota":     gorm.Expr("quota + ?", quota),
		}).Error
	})
}
