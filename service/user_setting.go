package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
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

// SQLite cannot lock an individual settings row with SELECT FOR UPDATE. The
// process-local lock prevents read-modify-write races there; production
// MySQL/PostgreSQL deployments additionally take a database row lock below so
// separate application nodes cannot clobber one another.
var userSettingsUpdateMu sync.Mutex

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
	QuotaWarningThreshold            int    `json:"quota_warning_threshold,omitempty"`
	QuotaWarningType                 string `json:"notify_type,omitempty"`
	BillingPreference                string `json:"billing_preference,omitempty"`
	UpstreamModelUpdateNotifyEnabled bool   `json:"upstream_model_update_notify_enabled,omitempty"`
	SidebarModules                   string `json:"sidebar_modules,omitempty"`
	Language                         string `json:"language,omitempty"`
	NotificationEmail                string `json:"notification_email,omitempty"`
	WebhookURL                       string `json:"webhook_url,omitempty"`
	WebhookSecret                    string `json:"webhook_secret,omitempty"`
	BarkURL                          string `json:"bark_url,omitempty"`
	GotifyURL                        string `json:"gotify_url,omitempty"`
	GotifyToken                      string `json:"gotify_token,omitempty"`
	GotifyPriority                   int    `json:"gotify_priority,omitempty"`
	AcceptUnsetRatioModel            bool   `json:"accept_unset_model_ratio_model,omitempty"`
	RecordIPLog                      bool   `json:"record_ip_log,omitempty"`
}

func parseUserSettings(raw string) UserSettings {
	var s UserSettings
	if raw == "" {
		return s
	}
	// Unparsable legacy content degrades to defaults rather than erroring.
	_ = common.UnmarshalJsonStr(raw, &s)
	// TokenRouter previously persisted quota_warning_type. Read it during the
	// rolling upgrade, but all subsequent writes use the reference notify_type
	// spelling.
	if s.QuotaWarningType == "" {
		var legacy struct {
			QuotaWarningType string `json:"quota_warning_type"`
		}
		if common.UnmarshalJsonStr(raw, &legacy) == nil {
			s.QuotaWarningType = legacy.QuotaWarningType
		}
	}
	return s
}

// UserSettingsFromRaw returns the bounded typed view used internally by
// self-profile responses and preference workflows. Browser-visible responses
// must use SafeUserSettingsJSON so delivery credentials stay write-only.
func UserSettingsFromRaw(raw string) UserSettings {
	if len(raw) > 1<<20 {
		return UserSettings{}
	}
	return parseUserSettings(raw)
}

// SafeUserSettingsJSON returns the owner's browser-visible preference JSON.
// Delivery credentials remain write-only; the booleans let the UI distinguish
// an intentionally blank field from an already configured secret.
func SafeUserSettingsJSON(raw string) string {
	return safeUserSettingsJSON(raw, false)
}

// AdminSafeUserSettingsJSON returns non-secret preference state for user
// administration. Notification destinations are owner-private capability and
// contact data, so administrators receive only configured-state booleans.
func AdminSafeUserSettingsJSON(raw string) string {
	return safeUserSettingsJSON(raw, true)
}

func safeUserSettingsJSON(raw string, hideDestinations bool) string {
	settings := UserSettingsFromRaw(raw)
	webhookSecretConfigured := settings.WebhookSecret != ""
	gotifyTokenConfigured := settings.GotifyToken != ""
	notificationEmailConfigured := settings.NotificationEmail != ""
	webhookURLConfigured := settings.WebhookURL != ""
	barkURLConfigured := settings.BarkURL != ""
	gotifyURLConfigured := settings.GotifyURL != ""
	settings.WebhookSecret = ""
	settings.GotifyToken = ""
	if hideDestinations {
		settings.NotificationEmail = ""
		settings.WebhookURL = ""
		settings.BarkURL = ""
		settings.GotifyURL = ""
	}
	encoded, err := common.Marshal(settings)
	if err != nil {
		return `{"webhook_secret_configured":false,"gotify_token_configured":false}`
	}
	fields := make(map[string]any)
	if err := common.Unmarshal(encoded, &fields); err != nil {
		return `{"webhook_secret_configured":false,"gotify_token_configured":false}`
	}
	fields["webhook_secret_configured"] = webhookSecretConfigured
	fields["gotify_token_configured"] = gotifyTokenConfigured
	if hideDestinations {
		fields["notification_email_configured"] = notificationEmailConfigured
		fields["webhook_url_configured"] = webhookURLConfigured
		fields["bark_url_configured"] = barkURLConfigured
		fields["gotify_url_configured"] = gotifyURLConfigured
	}
	encoded, err = common.Marshal(fields)
	if err != nil || len(encoded) > 1<<20 {
		return `{"webhook_secret_configured":false,"gotify_token_configured":false}`
	}
	return string(encoded)
}

func loadUserSettings(userId int) (UserSettings, error) {
	var user model.User
	if err := model.DB.Select("setting").Where("id = ?", userId).First(&user).Error; err != nil {
		return UserSettings{}, err
	}
	return parseUserSettings(user.Setting), nil
}

func updateUserSettings(userId int, mutate func(*UserSettings, int)) error {
	if mutate == nil {
		return errors.New("invalid user settings update")
	}
	return updateUserSettingsChecked(userId, func(settings *UserSettings, role int) error {
		mutate(settings, role)
		return nil
	})
}

func updateUserSettingsChecked(userId int, mutate func(*UserSettings, int) error) error {
	if userId <= 0 || mutate == nil {
		return errors.New("invalid user settings update")
	}
	userSettingsUpdateMu.Lock()
	defer userSettingsUpdateMu.Unlock()
	return model.DB.Transaction(func(tx *gorm.DB) error {
		query := tx.Select("id", "role", "setting").Where("id = ?", userId)
		if model.UsingMySQL() || model.UsingPostgreSQL() {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var user model.User
		if err := query.First(&user).Error; err != nil {
			return err
		}
		if len(user.Setting) > 1<<20 {
			return errors.New("user settings are too large")
		}
		settings := parseUserSettings(user.Setting)
		if err := mutate(&settings, user.Role); err != nil {
			return err
		}

		rawFields := make(map[string]json.RawMessage)
		if raw := strings.TrimSpace(user.Setting); raw != "" {
			if err := common.Unmarshal([]byte(raw), &rawFields); err != nil || rawFields == nil {
				rawFields = make(map[string]json.RawMessage)
			}
		}
		for _, key := range []string{
			"quota_warning_threshold", "quota_warning_type", "notify_type", "billing_preference",
			"upstream_model_update_notify_enabled", "sidebar_modules", "language",
			"notification_email", "webhook_url", "webhook_secret", "bark_url",
			"gotify_url", "gotify_token", "gotify_priority",
			"accept_unset_model_ratio_model", "record_ip_log",
		} {
			delete(rawFields, key)
		}
		knownJSON, err := common.Marshal(settings)
		if err != nil {
			return err
		}
		knownFields := make(map[string]json.RawMessage)
		if err := common.Unmarshal(knownJSON, &knownFields); err != nil {
			return err
		}
		for key, value := range knownFields {
			rawFields[key] = value
		}
		encoded, err := common.Marshal(rawFields)
		if err != nil {
			return err
		}
		if len(encoded) > 1<<20 {
			return errors.New("user settings are too large")
		}
		result := tx.Model(&model.User{}).Where("id = ?", userId).Update("setting", string(encoded))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 1 {
			return fmt.Errorf("user settings update affected %d rows", result.RowsAffected)
		}
		return nil
	})
}

var supportedUserLanguages = map[string]struct{}{
	"en": {}, "fr": {}, "ja": {}, "ru": {}, "vi": {}, "zh": {}, "zh-TW": {},
}

var sidebarModuleKeys = map[string]map[string]struct{}{
	"chat": {
		"enabled": {}, "playground": {}, "chat": {},
	},
	"console": {
		"enabled": {}, "detail": {}, "token": {}, "log": {}, "midjourney": {}, "task": {},
	},
	"personal": {
		"enabled": {}, "topup": {}, "personal": {},
	},
}

func normalizeSidebarModules(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > 16<<10 || !json.Valid([]byte(raw)) {
		return "", errors.New("invalid sidebar modules")
	}
	var modules map[string]map[string]bool
	if err := common.UnmarshalJsonStr(raw, &modules); err != nil || modules == nil || len(modules) > len(sidebarModuleKeys) {
		return "", errors.New("invalid sidebar modules")
	}
	for section, values := range modules {
		allowed, ok := sidebarModuleKeys[section]
		if !ok || values == nil || len(values) > len(allowed) {
			return "", errors.New("invalid sidebar modules")
		}
		for key := range values {
			if _, ok := allowed[key]; !ok {
				return "", errors.New("invalid sidebar modules")
			}
		}
	}
	encoded, err := common.Marshal(modules)
	if err != nil || len(encoded) > 16<<10 {
		return "", errors.New("invalid sidebar modules")
	}
	return string(encoded), nil
}

// UpdateUserProfilePreferences persists reference-compatible language and
// sidebar preferences without clobbering billing or notification settings.
func UpdateUserProfilePreferences(userID int, language, sidebarModules *string) error {
	if language == nil && sidebarModules == nil {
		return errors.New("no profile preference supplied")
	}
	var normalizedLanguage string
	if language != nil {
		normalizedLanguage = strings.TrimSpace(*language)
		if normalizedLanguage != *language {
			return errors.New("invalid language")
		}
		if _, ok := supportedUserLanguages[normalizedLanguage]; !ok {
			return errors.New("invalid language")
		}
	}
	var normalizedModules string
	var err error
	if sidebarModules != nil {
		normalizedModules, err = normalizeSidebarModules(*sidebarModules)
		if err != nil {
			return err
		}
	}
	return updateUserSettings(userID, func(settings *UserSettings, _ int) {
		if language != nil {
			settings.Language = normalizedLanguage
		}
		if sidebarModules != nil {
			settings.SidebarModules = normalizedModules
		}
	})
}

// UpdateUserSetting validates and stores the legacy quota-warning subset. New
// request handlers use UpdateUserNotificationSettings for the complete
// email/webhook/Bark/Gotify and privacy contract.
func UpdateUserSetting(userId int, threshold int, notifyType string) error {
	return UpdateUserSettingWithUpstreamNotification(userId, 0, threshold, notifyType, nil)
}

// UpdateUserSettingWithUpstreamNotification persists the admin-only model
// discovery watcher preference alongside the existing quota-warning fields.
// A nil preference leaves the existing value unchanged; non-admin callers
// cannot toggle it even if they forge the JSON field.
func UpdateUserSettingWithUpstreamNotification(userId, role, threshold int, notifyType string, upstreamNotify *bool) error {
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
	return updateUserSettings(userId, func(settings *UserSettings, storedRole int) {
		settings.QuotaWarningThreshold = threshold
		settings.QuotaWarningType = notifyType
		if role >= constant.RoleAdminUser && storedRole >= constant.RoleAdminUser && upstreamNotify != nil {
			settings.UpstreamModelUpdateNotifyEnabled = *upstreamNotify
		}
	})
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
	if err := updateUserSettings(userId, func(settings *UserSettings, _ int) {
		settings.BillingPreference = normalized
	}); err != nil {
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

// UserRecordIPLogEnabled resolves the owner's explicit privacy opt-in. Missing,
// malformed, oversized, or unreadable settings all fail closed to no IP
// retention.
func UserRecordIPLogEnabled(userID int) bool {
	if userID <= 0 || model.DB == nil {
		return false
	}
	settings, err := loadUserSettings(userID)
	return err == nil && settings.RecordIPLog
}
