// Package setting manages database-backed options (system settings) with an
// in-memory cache and hot-reload synchronization.
package setting

import (
	"sort"
	"strconv"
	"sync"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Known option keys. Values are stored in the options table and hot-reloaded.
const (
	SystemNameOption                    = "SystemName"
	LogoOption                          = "Logo"
	ServerAddressOption                 = "ServerAddress"
	MjForwardURLEnabledOption           = "MjForwardUrlEnabled"
	FooterHTMLOption                    = "Footer"
	RegistrationEnabledOption           = "RegisterEnabled"
	PasswordLoginEnabledOption          = "PasswordLoginEnabled"
	PasswordRegisterEnabledOption       = "PasswordRegisterEnabled"
	EmailVerificationEnabledOption      = "EmailVerificationEnabled"
	GitHubOAuthEnabledOption            = "GitHubOAuthEnabled"
	DiscordOAuthEnabledOption           = "DiscordOAuthEnabled"
	WeChatAuthEnabledOption             = "WeChatAuthEnabled"
	WeChatServerAddressOption           = "WeChatServerAddress"
	WeChatServerTokenOption             = "WeChatServerToken"
	WeChatQRCodeOption                  = "WeChatAccountQRCodeImageURL"
	TelegramOAuthEnabledOption          = "TelegramOAuthEnabled"
	LinuxDOOAuthEnabledOption           = "LinuxDOOAuthEnabled"
	TurnstileEnabledOption              = "TurnstileEnabled"
	TurnstileCheckEnabledOption         = "TurnstileCheckEnabled"
	TurnstileSiteKeyOption              = "TurnstileSiteKey"
	TurnstileSecretKeyOption            = "TurnstileSecretKey"
	CheckSensitiveEnabledOption         = "CheckSensitiveEnabled"
	CheckSensitiveOnPromptEnabledOption = "CheckSensitiveOnPromptEnabled"
	TopUpMinimumOption                  = "TopUpMinimum"
	InitialQuotaOption                  = "InitialQuota"
	DefaultGroupOption                  = "DefaultGroup"
	QuotaPerUnitOption                  = "QuotaPerUnit"
	QuotaForInviterOption               = "QuotaForInviter"
	QuotaForInviteeOption               = "QuotaForInvitee"
	QuotaRemindThresholdOption          = "QuotaRemindThreshold"
	ModelPriceOption                    = "ModelPrice"
	ModelRatioOption                    = "ModelRatio"
	GroupRatioOption                    = "GroupRatio"
	PerfMetricsEnabledOption            = "perf_metrics_setting.enabled"
	PerfMetricsFlushIntervalOption      = "perf_metrics_setting.flush_interval"
	PerfMetricsBucketTimeOption         = "perf_metrics_setting.bucket_time"
	PerfMetricsRetentionDaysOption      = "perf_metrics_setting.retention_days"
	PaymentComplianceConfirmedOption    = "payment_setting.compliance_confirmed"
	PaymentComplianceTermsVersionOption = "payment_setting.compliance_terms_version"
	PaymentComplianceConfirmedAtOption  = "payment_setting.compliance_confirmed_at"
	PaymentComplianceConfirmedByOption  = "payment_setting.compliance_confirmed_by"
	PaymentComplianceConfirmedIPOption  = "payment_setting.compliance_confirmed_ip"
	LegacyPaymentComplianceOption       = "PaymentComplianceConfirmed"
	RetryTimesOption                    = "RetryTimes"
	AutoGroupConsumeOption              = "AutoGroupConsume"
	MaxTokenAutoGroupsOption            = "MaxTokenAutoGroups"
	MaxUserTokensOption                 = "MaxUserTokens"
	DisplayTokenCountOption             = "DisplayTokenCount"
	DefaultStreamingTimeoutOption       = "DefaultStreamingTimeout"
)

var (
	optionMapMu    sync.RWMutex
	optionUpdateMu sync.Mutex
	optionMap      = map[string]string{}
)

// Init loads all options into memory from the database.
func Init() error {
	optionUpdateMu.Lock()
	defer optionUpdateMu.Unlock()
	var options []*model.Option
	if err := model.DB.Find(&options).Error; err != nil {
		return err
	}
	loaded := make(map[string]string, len(options))
	for _, o := range options {
		loaded[o.Key] = o.Value
	}
	affinityConfig, err := buildChannelAffinitySetting(loaded)
	if err != nil {
		return err
	}
	optionMapMu.Lock()
	optionMap = loaded
	optionMapMu.Unlock()
	channelAffinityConfig.Store(&affinityConfig)
	return nil
}

// Sync reloads options from the database (hot reload; called periodically and
// after any admin update in multi-node deployments).
func Sync() error {
	return Init()
}

// GetOption returns the raw option value (empty string if unset).
func GetOption(key string) string {
	optionMapMu.RLock()
	defer optionMapMu.RUnlock()
	return optionMap[key]
}

// GetOptionOrDefault returns the option or a default when unset.
func GetOptionOrDefault(key, def string) string {
	v := GetOption(key)
	if v == "" {
		return def
	}
	return v
}

// GetOptionBool returns the option parsed as a boolean.
func GetOptionBool(key string, def bool) bool {
	v := GetOption(key)
	if v == "" {
		return def
	}
	switch v {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	}
	return def
}

// GetOptionIntOrDefault returns the option parsed as an int, or a default.
func GetOptionIntOrDefault(key string, def int) int {
	v := GetOption(key)
	if v == "" {
		return def
	}
	if i, err := strconv.Atoi(v); err == nil {
		return i
	}
	return def
}

// GetOptionFloatOrDefault returns the option parsed as a float, or a default.
func GetOptionFloatOrDefault(key string, def float64) float64 {
	v := GetOption(key)
	if v == "" {
		return def
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	return def
}

// GetOptionJSONField reads a JSON-map option (e.g. ModelBillingMode) and returns
// the string value for a field key, or "" when unset.
func GetOptionJSONField(optionKey, field string) string {
	raw := GetOption(optionKey)
	if raw == "" {
		return ""
	}
	var m map[string]string
	if err := common.UnmarshalJsonStr(raw, &m); err != nil {
		return ""
	}
	return m[field]
}

// UpdateOption persists and caches one option value atomically.
func UpdateOption(key, value string) error {
	return UpdateOptions(map[string]string{key: value})
}

// UpdateOptions persists a set of values in one transaction and publishes the
// cache only after commit, preventing partially visible compliance metadata.
func UpdateOptions(updates map[string]string) error {
	optionUpdateMu.Lock()
	defer optionUpdateMu.Unlock()
	keys := make([]string, 0, len(updates))
	for key := range updates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	optionMapMu.RLock()
	candidate := make(map[string]string, len(optionMap)+len(updates))
	for key, value := range optionMap {
		candidate[key] = value
	}
	optionMapMu.RUnlock()
	for key, value := range updates {
		candidate[key] = value
	}
	affinityConfig, err := buildChannelAffinitySetting(candidate)
	if err != nil {
		return err
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		for _, key := range keys {
			var option model.Option
			if err := tx.Where("key = ?", key).
				Assign(model.Option{Value: updates[key]}).
				FirstOrCreate(&option, model.Option{Key: key}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	optionMapMu.Lock()
	for _, key := range keys {
		optionMap[key] = updates[key]
	}
	optionMapMu.Unlock()
	channelAffinityConfig.Store(&affinityConfig)
	return nil
}

// GetSiteName returns the configured site name, defaulting to the product name.
func GetSiteName() string {
	return GetOptionOrDefault(SystemNameOption, common.ProductName)
}

// GetMaxTokenAutoGroups returns the per-token auto-groups limit
// (MaxTokenAutoGroups option, default 5 — reference default).
func GetMaxTokenAutoGroups() int {
	return GetOptionIntOrDefault(MaxTokenAutoGroupsOption, 5)
}

// GetMaxUserTokens returns the per-user token count limit
// (MaxUserTokens option, default 1000 — reference default).
func GetMaxUserTokens() int {
	return GetOptionIntOrDefault(MaxUserTokensOption, 1000)
}
