package settings

import (
	"errors"
	"sync/atomic"
)

const DefaultCollapseSidebarOption = "DefaultCollapseSidebar"

var operationsConfig atomic.Pointer[OperationsSetting]

// OperationsSetting contains public behavior and delivery configuration that
// must change without restarting the process.
type OperationsSetting struct {
	SMTP                   SMTPSetting
	DefaultCollapseSidebar bool
}

func buildOperationsSetting(values map[string]string) (OperationsSetting, error) {
	smtp, err := ParseSMTPSetting(values)
	if err != nil {
		return OperationsSetting{}, err
	}
	collapse, err := parseOperationsBool(values, DefaultCollapseSidebarOption, false)
	if err != nil {
		return OperationsSetting{}, err
	}
	return OperationsSetting{SMTP: smtp, DefaultCollapseSidebar: collapse}, nil
}

// GetOperationsSetting returns the current immutable operations snapshot.
func GetOperationsSetting() OperationsSetting {
	if current := operationsConfig.Load(); current != nil {
		return *current
	}
	fallback, _ := buildOperationsSetting(nil)
	return fallback
}

// OperationsOptionDefaults supplies the public operations settings surface.
// SMTP secrets remain redacted by the HTTP presentation layer.
func OperationsOptionDefaults() map[string]string {
	values := SMTPOptionDefaults()
	values[DefaultCollapseSidebarOption] = "false"
	return values
}
func parseOperationsBool(values map[string]string, key string, fallback bool) (bool, error) {
	raw, present := values[key]
	if !present {
		return fallback, nil
	}
	if raw == "true" {
		return true, nil
	}
	if raw == "false" {
		return false, nil
	}
	return false, errors.New(key + " must be true or false")
}
