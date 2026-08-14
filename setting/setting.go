// Package setting manages database-backed options (system settings) with an
// in-memory cache and hot-reload synchronization.
package setting

import (
	"strconv"
	"sync"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Known option keys. Values are stored in the options table and hot-reloaded.
const (
	SystemNameOption            = "SystemName"
	LogoOption                  = "Logo"
	ServerAddressOption         = "ServerAddress"
	FooterHTMLOption            = "Footer"
	RegistrationEnabledOption   = "RegisterEnabled"
	PasswordLoginEnabledOption  = "PasswordLoginEnabled"
	PasswordRegisterEnabledOption = "PasswordRegisterEnabled"
	EmailVerificationEnabledOption = "EmailVerificationEnabled"
	GitHubOAuthEnabledOption    = "GitHubOAuthEnabled"
	DiscordOAuthEnabledOption   = "DiscordOAuthEnabled"
	WeChatAuthEnabledOption     = "WeChatAuthEnabled"
	TelegramOAuthEnabledOption  = "TelegramOAuthEnabled"
	LinuxDOOAuthEnabledOption   = "LinuxDOOAuthEnabled"
	TurnstileEnabledOption      = "TurnstileEnabled"
	TopUpMinimumOption          = "TopUpMinimum"
	InitialQuotaOption          = "InitialQuota"
	DefaultGroupOption          = "DefaultGroup"
	QuotaPerUnitOption          = "QuotaPerUnit"
	RetryTimesOption            = "RetryTimes"
	AutoGroupConsumeOption      = "AutoGroupConsume"
	DisplayTokenCountOption     = "DisplayTokenCount"
	DefaultStreamingTimeoutOption = "DefaultStreamingTimeout"
)

var (
	optionMapMu sync.RWMutex
	optionMap   = map[string]string{}
)

// Init loads all options into memory from the database.
func Init() error {
	var options []*model.Option
	if err := model.DB.Find(&options).Error; err != nil {
		return err
	}
	optionMapMu.Lock()
	defer optionMapMu.Unlock()
	optionMap = make(map[string]string, len(options))
	for _, o := range options {
		optionMap[o.Key] = o.Value
	}
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

// UpdateOption persists and caches an option value.
func UpdateOption(key, value string) error {
	var o model.Option
	res := model.DB.Where("key = ?", key).First(&o)
	if res.Error != nil {
		o = model.Option{Key: key, Value: value}
		if err := model.DB.Create(&o).Error; err != nil {
			return err
		}
	} else {
		if err := model.DB.Model(&o).Update("value", value).Error; err != nil {
			return err
		}
	}
	optionMapMu.Lock()
	optionMap[key] = value
	optionMapMu.Unlock()
	return nil
}

// GetSiteName returns the configured site name, defaulting to the product name.
func GetSiteName() string {
	return GetOptionOrDefault(SystemNameOption, common.ProductName)
}
