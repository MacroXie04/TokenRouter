package service

import (
	"fmt"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// CheckAndSendQuotaReminder sends a low-quota email after settlement. The
// threshold is the QuotaRemindThreshold option (default 1000, matching the
// reference; 0 disables). To avoid per-request spam (the reference notifies on
// every request below threshold), a reminder is sent at most once per hour per
// user and only to verified email addresses; delivery is a no-op until SMTP is
// configured.
func CheckAndSendQuotaReminder(userId int) {
	user, err := GetUserByID(userId)
	if err != nil || !user.EmailVerified || user.Email == "" {
		return
	}
	// Global threshold (default 1000), overridable per user via UpdateUserSetting.
	threshold := setting.GetOptionIntOrDefault(setting.QuotaRemindThresholdOption, 1000)
	if override := QuotaWarningThreshold(user); override > 0 {
		threshold = override
	}
	if threshold <= 0 {
		return
	}
	if user.Quota >= threshold {
		return
	}
	now := common.NowTimestamp()
	if now-user.QuotaReminderAt < 3600 {
		return
	}
	body := fmt.Sprintf("您的 TokenRouter 剩余额度为 %d，低于提醒阈值 %d，请及时充值以免影响使用。", user.Quota, threshold)
	if err := Mail.Send(user.Email, "TokenRouter 额度提醒", body); err != nil {
		return
	}
	_ = model.DB.Model(&model.User{}).Where("id = ?", userId).
		Update("quota_reminder_at", now).Error
}
