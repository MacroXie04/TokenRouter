// Package setting manages database-backed options (system settings) with an
// in-memory cache and hot-reload synchronization.
package settings

import (
	"context"
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/buildinfo"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"sort"
	"strconv"
	"sync"
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
	PreConsumedQuotaOption              = "PreConsumedQuota"
	ModelPriceOption                    = "ModelPrice"
	ModelRatioOption                    = "ModelRatio"
	CompletionRatioOption               = "CompletionRatio"
	PerCallModelPriceOption             = "PerCallModelPrice"
	ModelBillingModeOption              = "ModelBillingMode"
	ModelBillingExprOption              = "ModelBillingExpr"
	GroupRatioOption                    = "GroupRatio"
	GroupGroupRatioOption               = "GroupGroupRatio"
	ExposeRatioEnabledOption            = "ExposeRatioEnabled"
	PasskeyEnabledOption                = "passkey.enabled"
	CheckinEnabledOption                = "checkin_setting.enabled"
	CheckinMinQuotaOption               = "checkin_setting.min_quota"
	CheckinMaxQuotaOption               = "checkin_setting.max_quota"
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
	AutoGroupsOption                    = "AutoGroups"
	AutoGroupConsumeOption              = "AutoGroupConsume"
	MaxTokenAutoGroupsOption            = "MaxTokenAutoGroups"
	MaxUserTokensOption                 = "MaxUserTokens"
	DisplayTokenCountOption             = "DisplayTokenCount"
	DefaultStreamingTimeoutOption       = "DefaultStreamingTimeout"
	HeaderNavModulesOption              = "HeaderNavModules"
	SidebarModulesAdminOption           = "SidebarModulesAdmin"
	ConsoleAnnouncementsOption          = "console_setting.announcements"
	ConsoleAnnouncementsEnabledOption   = "console_setting.announcements_enabled"
	ConsoleAPIInfoOption                = "console_setting.api_info"
	ConsoleAPIInfoEnabledOption         = "console_setting.api_info_enabled"
	ConsoleFAQOption                    = "console_setting.faq"
	ConsoleFAQEnabledOption             = "console_setting.faq_enabled"
	ConsoleUptimeKumaGroupsOption       = "console_setting.uptime_kuma_groups"
	ConsoleUptimeKumaEnabledOption      = "console_setting.uptime_kuma_enabled"
	SelfUseModeEnabledOption            = "SelfUseModeEnabled"
	DemoSiteEnabledOption               = "DemoSiteEnabled"
	ModelDeploymentIONetEnabledOption   = "model_deployment.ionet.enabled"
	ModelDeploymentIONetAPIKeyOption    = "model_deployment.ionet.api_key"
)

const (
	DefaultMaxUserTokens = 1000
	MaxMaxUserTokens     = 1_000_000
)

var (
	optionMapMu    sync.RWMutex
	optionUpdateMu sync.Mutex
	optionMap      = map[string]string{}
)

// Init loads all options into memory from the database.
func Init() error {
	return InitContext(context.Background())
}

// InitContext loads and validates one coherent option snapshot while applying
// the caller's cancellation boundary to the database read. Publication occurs
// only after the complete snapshot has been built and cancellation rechecked.
func InitContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("settings context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if model.DB == nil {
		return errors.New("settings database is nil")
	}
	optionUpdateMu.Lock()
	defer optionUpdateMu.Unlock()
	loaded, err := loadBoundedOptionSnapshot(model.DB.WithContext(ctx))
	if err != nil {
		return err
	}
	if _, err := ParseTopUpGroupRatioOption(loaded[TopUpGroupRatioOption]); err != nil {
		return err
	}
	if _, err := parsePayMethods(loaded[PayMethodsOption]); err != nil {
		return err
	}
	consoleContent, err := buildConsoleContentSetting(loaded)
	if err != nil {
		return err
	}
	affinityConfig, err := buildChannelAffinitySetting(loaded)
	if err != nil {
		return err
	}
	groupRouting, err := buildGroupRoutingSetting(loaded)
	if err != nil {
		return err
	}
	registrationGroupPolicy, err := buildRegistrationGroupPolicy(loaded)
	if err != nil {
		return err
	}
	checkin, err := buildCheckinSetting(loaded)
	if err != nil {
		return err
	}
	modelPolicy, err := buildModelPolicySetting(loaded)
	if err != nil {
		return err
	}
	usageRatio, err := buildUsageRatioSetting(loaded)
	if err != nil {
		return err
	}
	authentication, err := buildAuthenticationSetting(loaded)
	if err != nil {
		return err
	}
	modelRequestRateLimit, err := buildModelRequestRateLimitSetting(loaded)
	if err != nil {
		return err
	}
	grok, err := buildGrokSetting(loaded)
	if err != nil {
		return err
	}
	operations, err := buildOperationsSetting(loaded)
	if err != nil {
		return err
	}
	channelReliability, err := buildChannelReliabilitySetting(loaded)
	if err != nil {
		return err
	}
	toolPrices, err := parseToolPriceState(optionValueOrDefault(loaded, ToolPriceOption, "{}"))
	if err != nil {
		return err
	}
	if err := validatePricingConfiguration(loaded); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	optionMapMu.Lock()
	optionMap = loaded
	optionMapMu.Unlock()
	channelAffinityConfig.Store(&affinityConfig)
	groupRoutingConfig.Store(&groupRouting)
	publishRegistrationGroupPolicy(registrationGroupPolicy)
	checkinConfig.Store(&checkin)
	consoleContentConfig.Store(&consoleContent)
	modelPolicyConfig.Store(&modelPolicy)
	usageRatioConfig.Store(&usageRatio)
	authenticationConfig.Store(&authentication)
	modelRequestRateLimitConfig.Store(&modelRequestRateLimit)
	grokConfig.Store(&grok)
	operationsConfig.Store(&operations)
	storeChannelReliabilitySetting(channelReliability)
	publishToolPriceState(toolPrices)
	return nil
}

// Sync reloads options from the database (hot reload; called periodically and
// after any admin update in multi-node deployments).
func Sync() error {
	return Init()
}

// SyncContext is Sync's cancellable form for process-long maintenance loops.
func SyncContext(ctx context.Context) error {
	return InitContext(ctx)
}

// GetOption returns the raw option value (empty string if unset).
func GetOption(key string) string {
	value, _ := LookupOption(key)
	return value
}

// LookupOption returns the raw option value and whether the key is present in
// the hot-reloaded snapshot. This distinguishes an absent option (use a
// documented default) from an explicitly empty value (invalid for settings
// whose safe behavior is to fail closed).
func LookupOption(key string) (string, bool) {
	optionMapMu.RLock()
	defer optionMapMu.RUnlock()
	value, found := optionMap[key]
	return value, found
}

// GetOptions returns one coherent copy of the requested option values. It is
// used when several settings jointly define one runtime decision, so an
// UpdateOptions publication cannot be observed halfway through the copy.
func GetOptions(keys ...string) map[string]string {
	optionMapMu.RLock()
	defer optionMapMu.RUnlock()
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, found := optionMap[key]; found {
			values[key] = value
		}
	}
	return values
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
	if err := jsonutil.UnmarshalJsonStr(raw, &m); err != nil {
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
	if err := validateOptionSnapshot(updates); err != nil {
		return err
	}
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
	if err := validateOptionSnapshot(candidate); err != nil {
		return err
	}
	if _, err := ParseTopUpGroupRatioOption(candidate[TopUpGroupRatioOption]); err != nil {
		return err
	}
	for key := range updates {
		if isCreemOptionKey(key) {
			if _, err := buildCreemConfig(candidate); err != nil {
				return err
			}
			break
		}
	}
	for key := range updates {
		if isWaffoOptionKey(key) {
			if _, err := buildWaffoConfig(candidate); err != nil {
				return err
			}
			break
		}
	}
	for key := range updates {
		if isWaffoPancakeOptionKey(key) {
			if _, err := buildWaffoPancakeConfig(candidate); err != nil {
				return err
			}
			break
		}
	}
	if raw, changed := updates[ChatsOption]; changed {
		if _, err := parseChatPresets(raw); err != nil {
			return err
		}
	}
	if raw, changed := updates[PayMethodsOption]; changed {
		if _, err := parsePayMethods(raw); err != nil {
			return err
		}
	}
	consoleContent, err := buildConsoleContentSetting(candidate)
	if err != nil {
		return err
	}
	affinityConfig, err := buildChannelAffinitySetting(candidate)
	if err != nil {
		return err
	}
	groupRouting, err := buildGroupRoutingSetting(candidate)
	if err != nil {
		return err
	}
	registrationGroupPolicy, err := buildRegistrationGroupPolicy(candidate)
	if err != nil {
		return err
	}
	checkin, err := buildCheckinSetting(candidate)
	if err != nil {
		return err
	}
	modelPolicy, err := buildModelPolicySetting(candidate)
	if err != nil {
		return err
	}
	usageRatio, err := buildUsageRatioSetting(candidate)
	if err != nil {
		return err
	}
	authentication, err := buildAuthenticationSetting(candidate)
	if err != nil {
		return err
	}
	modelRequestRateLimit, err := buildModelRequestRateLimitSetting(candidate)
	if err != nil {
		return err
	}
	grok, err := buildGrokSetting(candidate)
	if err != nil {
		return err
	}
	operations, err := buildOperationsSetting(candidate)
	if err != nil {
		return err
	}
	channelReliability, err := buildChannelReliabilitySetting(candidate)
	if err != nil {
		return err
	}
	toolPrices, err := parseToolPriceState(optionValueOrDefault(candidate, ToolPriceOption, "{}"))
	if err != nil {
		return err
	}
	if err := validatePricingConfiguration(candidate); err != nil {
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
	groupRoutingConfig.Store(&groupRouting)
	publishRegistrationGroupPolicy(registrationGroupPolicy)
	checkinConfig.Store(&checkin)
	consoleContentConfig.Store(&consoleContent)
	modelPolicyConfig.Store(&modelPolicy)
	usageRatioConfig.Store(&usageRatio)
	authenticationConfig.Store(&authentication)
	modelRequestRateLimitConfig.Store(&modelRequestRateLimit)
	grokConfig.Store(&grok)
	operationsConfig.Store(&operations)
	storeChannelReliabilitySetting(channelReliability)
	publishToolPriceState(toolPrices)
	return nil
}

func optionValueOrDefault(options map[string]string, key, fallback string) string {
	if value, present := options[key]; present {
		return value
	}
	return fallback
}

// GetSiteName returns the configured site name, defaulting to the product name.
func GetSiteName() string {
	return GetOptionOrDefault(SystemNameOption, buildinfo.ProductName)
}

// GetMaxUserTokens returns the per-user token count limit
// (MaxUserTokens option, default 1000 — reference default).
func GetMaxUserTokens() int {
	limit := GetOptionIntOrDefault(MaxUserTokensOption, DefaultMaxUserTokens)
	if limit < 1 || limit > MaxMaxUserTokens {
		return DefaultMaxUserTokens
	}
	return limit
}
