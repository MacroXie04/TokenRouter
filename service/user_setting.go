package service

import (
	"errors"
	"strings"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Billing preferences: the consumption order between subscription quota and
// the wallet balance for relay requests.
const (
	BillingPreferenceSubscriptionFirst = "subscription_first"
	BillingPreferenceWalletFirst       = "wallet_first"
	BillingPreferenceSubscriptionOnly  = "subscription_only"
	BillingPreferenceWalletOnly        = "wallet_only"
)

// NormalizeBillingPreference clamps a billing preference to the valid values;
// unknown values silently fall back to the default (subscription_first),
// matching the reference contract (bad input never errors).
func NormalizeBillingPreference(pref string) string {
	switch strings.TrimSpace(pref) {
	case BillingPreferenceSubscriptionFirst, BillingPreferenceWalletFirst,
		BillingPreferenceSubscriptionOnly, BillingPreferenceWalletOnly:
		return strings.TrimSpace(pref)
	default:
		return BillingPreferenceSubscriptionFirst
	}
}

// UserSettings is the per-user settings JSON stored in users.setting. All
// writers must read-modify-write the whole struct so independent settings
// (quota warning, billing preference) do not clobber each other.
type UserSettings struct {
	QuotaWarningThreshold int    `json:"quota_warning_threshold,omitempty"`
	QuotaWarningType      string `json:"quota_warning_type,omitempty"`
	BillingPreference     string `json:"billing_preference,omitempty"`
}

func parseUserSettings(raw string) UserSettings {
	var s UserSettings
	if raw == "" {
		return s
	}
	// Unparsable legacy content degrades to defaults rather than erroring.
	_ = common.UnmarshalJsonStr(raw, &s)
	return s
}

func loadUserSettings(userId int) (UserSettings, error) {
	var user model.User
	if err := model.DB.Select("setting").Where("id = ?", userId).First(&user).Error; err != nil {
		return UserSettings{}, err
	}
	return parseUserSettings(user.Setting), nil
}

func saveUserSettings(userId int, s UserSettings) error {
	raw, err := common.Marshal(s)
	if err != nil {
		return err
	}
	return model.DB.Model(&model.User{}).Where("id = ?", userId).
		Update("setting", string(raw)).Error
}

// UpdateUserSetting validates and stores the per-user quota warning config.
// Notification types are accepted for parity with the reference; only "email"
// is currently deliverable (Bark/Gotify/webhook channels are not ported).
func UpdateUserSetting(userId int, threshold int, notifyType string) error {
	if threshold <= 0 {
		return errors.New("提醒阈值必须大于 0")
	}
	switch notifyType {
	case "", "email", "webhook", "bark", "gotify":
	default:
		return errors.New("通知类型无效")
	}
	if notifyType == "" {
		notifyType = "email"
	}
	settings, err := loadUserSettings(userId)
	if err != nil {
		return err
	}
	settings.QuotaWarningThreshold = threshold
	settings.QuotaWarningType = notifyType
	return saveUserSettings(userId, settings)
}

// GetUserBillingPreference returns the user's normalized billing preference.
func GetUserBillingPreference(userId int) string {
	settings, err := loadUserSettings(userId)
	if err != nil {
		return NormalizeBillingPreference("")
	}
	return NormalizeBillingPreference(settings.BillingPreference)
}

// UpdateUserBillingPreference normalizes and persists the billing preference,
// preserving the user's other settings, and returns the stored value.
func UpdateUserBillingPreference(userId int, pref string) (string, error) {
	normalized := NormalizeBillingPreference(pref)
	settings, err := loadUserSettings(userId)
	if err != nil {
		return "", err
	}
	settings.BillingPreference = normalized
	if err := saveUserSettings(userId, settings); err != nil {
		return "", err
	}
	return normalized, nil
}

// QuotaWarningThreshold returns the per-user threshold override, or 0 when
// unset/unparsable.
func QuotaWarningThreshold(user *model.User) int {
	if user == nil || user.Setting == "" {
		return 0
	}
	return parseUserSettings(user.Setting).QuotaWarningThreshold
}
