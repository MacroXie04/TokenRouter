package setting

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/tokenrouter/tokenrouter/common"
)

const (
	DefaultCheckinMinQuota = 1000
	DefaultCheckinMaxQuota = 10000
)

// CheckinSetting is the validated, coherent daily check-in configuration.
// The whole value is published atomically so a concurrent multi-option update
// cannot expose a new minimum with an old maximum.
type CheckinSetting struct {
	Enabled  bool
	MinQuota int
	MaxQuota int
}

var checkinConfig atomic.Pointer[CheckinSetting]

func init() {
	config, _ := buildCheckinSetting(nil)
	checkinConfig.Store(&config)
}

// GetCheckinSetting returns one immutable snapshot of the live configuration.
func GetCheckinSetting() CheckinSetting {
	config := checkinConfig.Load()
	if config == nil {
		return CheckinSetting{MinQuota: DefaultCheckinMinQuota, MaxQuota: DefaultCheckinMaxQuota}
	}
	return *config
}

// CheckinOptionDefaults are the reference defaults exposed to root operators.
func CheckinOptionDefaults() map[string]string {
	return map[string]string{
		CheckinEnabledOption:  "false",
		CheckinMinQuotaOption: strconv.Itoa(DefaultCheckinMinQuota),
		CheckinMaxQuotaOption: strconv.Itoa(DefaultCheckinMaxQuota),
	}
}

func buildCheckinSetting(options map[string]string) (CheckinSetting, error) {
	config := CheckinSetting{
		MinQuota: DefaultCheckinMinQuota,
		MaxQuota: DefaultCheckinMaxQuota,
	}
	if options == nil {
		return config, nil
	}

	if raw := strings.TrimSpace(options[CheckinEnabledOption]); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return CheckinSetting{}, fmt.Errorf("%s must be a boolean", CheckinEnabledOption)
		}
		config.Enabled = enabled
	}
	if raw := strings.TrimSpace(options[CheckinMinQuotaOption]); raw != "" {
		minimum, err := strconv.Atoi(raw)
		if err != nil {
			return CheckinSetting{}, fmt.Errorf("%s must be an integer", CheckinMinQuotaOption)
		}
		config.MinQuota = minimum
	}
	if raw := strings.TrimSpace(options[CheckinMaxQuotaOption]); raw != "" {
		maximum, err := strconv.Atoi(raw)
		if err != nil {
			return CheckinSetting{}, fmt.Errorf("%s must be an integer", CheckinMaxQuotaOption)
		}
		config.MaxQuota = maximum
	}
	if config.MinQuota <= 0 || int64(config.MinQuota) > common.MaxQuota {
		return CheckinSetting{}, fmt.Errorf("%s must be between 1 and %d", CheckinMinQuotaOption, common.MaxQuota)
	}
	if config.MaxQuota < config.MinQuota || int64(config.MaxQuota) > common.MaxQuota {
		return CheckinSetting{}, fmt.Errorf("%s must be between %d and %d", CheckinMaxQuotaOption, config.MinQuota, common.MaxQuota)
	}
	return config, nil
}
