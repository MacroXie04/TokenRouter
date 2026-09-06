package channels

import (
	"errors"
	"fmt"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxChannelNameBytes              = 191
	maxChannelCredentialBytes        = 512 << 10
	maxChannelBaseURLBytes           = 4096
	maxChannelModelsBytes            = 256 << 10
	maxChannelModelCount             = 10_000
	maxChannelModelNameBytes         = 255
	maxChannelGroupBytes             = 64
	maxChannelMappingBytes           = 256 << 10
	maxChannelStatusCodeMappingBytes = 1024
	maxChannelRemarkBytes            = 255
	maxChannelProviderSettingBytes   = 256 << 10
	maxChannelOpaqueJSONBytes        = 512 << 10
	maxChannelOrganizationBytes      = 512
	maxChannelType                   = 10_000
	maxChannelCopySuffixBytes        = 64
)

// ErrInvalidChannelInput marks a caller-controlled channel value that failed
// the bounded storage contract. Controllers map it to a client error while
// database/cache failures remain server errors.
var ErrInvalidChannelInput = errors.New("invalid channel input")

func invalidChannelInput(field, reason string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidChannelInput, field, reason)
}

func validateChannelForCreate(channel *model.Channel) error {
	if channel == nil {
		return invalidChannelInput("channel", "is required")
	}
	if channel.Type <= 0 || channel.Type > maxChannelType {
		return invalidChannelInput("type", "is out of range")
	}
	if channel.Status == 0 {
		channel.Status = channelcatalog.ChannelStatusEnabled
	}
	if channel.Status != channelcatalog.ChannelStatusEnabled &&
		channel.Status != channelcatalog.ChannelStatusAutoDisabled &&
		channel.Status != channelcatalog.ChannelStatusManuallyDisabled {
		return invalidChannelInput("status", "is out of range")
	}
	if channel.AutoBan != nil && *channel.AutoBan != 0 && *channel.AutoBan != 1 {
		return invalidChannelInput("auto_ban", "is out of range")
	}
	if err := validateChannelText("name", channel.Name, maxChannelNameBytes, false, false); err != nil {
		return err
	}
	if err := validateChannelText("key", channel.Key, maxChannelCredentialBytes, false, true); err != nil {
		return err
	}
	if err := validateChannelRecordFields(channel); err != nil {
		return err
	}
	if err := ValidateChannelOtherSettingsForType(channel.OtherSettings, channel.Type); err != nil {
		return fmt.Errorf("%w: settings are invalid: %v", ErrInvalidChannelInput, err)
	}
	return nil
}

func validateChannelRecordFields(channel *model.Channel) error {
	checks := []struct {
		field         string
		value         string
		maximum       int
		allowNewlines bool
	}{
		{"base_url", channel.BaseURL, maxChannelBaseURLBytes, false},
		{"group", channel.Group, maxChannelGroupBytes, false},
		{"model_mapping", channel.ModelMapping, maxChannelMappingBytes, true},
		{"status_code_mapping", channel.StatusCodeMapping, maxChannelStatusCodeMappingBytes, true},
		{"remark", channel.Remark, maxChannelRemarkBytes, false},
		{"setting", channel.Setting, maxChannelProviderSettingBytes, true},
		{"settings", channel.OtherSettings, maxChannelOpaqueJSONBytes, true},
		{"openai_organization", channel.OpenAIOrganization, maxChannelOrganizationBytes, false},
		{"other", channel.Other, maxChannelProviderSettingBytes, true},
		{"other_info", channel.OtherInfo, maxChannelOpaqueJSONBytes, true},
		{"channel_info", channel.ChannelInfo, maxChannelOpaqueJSONBytes, true},
		{"param_override", channel.ParamOverride, maxChannelMappingBytes, true},
		{"header_override", channel.HeaderOverride, maxChannelMappingBytes, true},
		{"test_model", channel.TestModel, maxChannelModelNameBytes, false},
	}
	for _, check := range checks {
		if err := validateChannelText(check.field, check.value, check.maximum, true, check.allowNewlines); err != nil {
			return err
		}
	}
	if !validChannelTag(channel.Tag, true) {
		return invalidChannelInput("tag", "exceeds safe limits")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"param_override", channel.ParamOverride},
		{"header_override", channel.HeaderOverride},
		{"other_info", channel.OtherInfo},
		{"channel_info", channel.ChannelInfo},
	} {
		if err := validateChannelJSONObject(field.name, field.value); err != nil {
			return err
		}
	}
	return validateChannelModels(channel.Models)
}

func validateChannelUpdateFields(updates map[string]any) error {
	for field, raw := range updates {
		switch field {
		case "name":
			value, ok := raw.(string)
			if !ok {
				return invalidChannelInput(field, "has an invalid type")
			}
			if err := validateChannelText(field, value, maxChannelNameBytes, false, false); err != nil {
				return err
			}
		case "key":
			value, ok := raw.(string)
			if !ok {
				return invalidChannelInput(field, "has an invalid type")
			}
			if err := validateChannelText(field, value, maxChannelCredentialBytes, false, true); err != nil {
				return err
			}
		case "models":
			value, ok := raw.(string)
			if !ok {
				return invalidChannelInput(field, "has an invalid type")
			}
			if err := validateChannelModels(value); err != nil {
				return err
			}
		case "tag":
			value, ok := raw.(string)
			if !ok || !validChannelTag(value, true) {
				return invalidChannelInput(field, "exceeds safe limits")
			}
		case "base_url", "group", "model_mapping", "status_code_mapping", "remark", "setting", "settings",
			"openai_organization", "open_ai_organization", "test_model", "other", "other_info", "channel_info", "param_override", "header_override":
			value, ok := raw.(string)
			if !ok {
				return invalidChannelInput(field, "has an invalid type")
			}
			maximum, allowNewlines := channelStringFieldLimit(field)
			if err := validateChannelText(field, value, maximum, true, allowNewlines); err != nil {
				return err
			}
			if field == "settings" {
				if err := ValidateChannelOtherSettings(value); err != nil {
					return invalidChannelInput(field, "is invalid")
				}
			}
			if field == "other_info" || field == "channel_info" || field == "param_override" || field == "header_override" {
				if err := validateChannelJSONObject(field, value); err != nil {
					return err
				}
			}
		case "type":
			value, ok := raw.(int)
			if !ok || value <= 0 || value > maxChannelType {
				return invalidChannelInput(field, "is out of range")
			}
		case "weight":
			if _, ok := raw.(uint); !ok {
				return invalidChannelInput(field, "has an invalid type")
			}
		case "priority":
			if _, ok := raw.(int64); !ok {
				return invalidChannelInput(field, "has an invalid type")
			}
		case "auto_ban":
			value, ok := raw.(int)
			if !ok || (value != 0 && value != 1) {
				return invalidChannelInput(field, "is out of range")
			}
		default:
			return invalidChannelInput(field, "is not writable")
		}
	}
	return nil
}

func channelStringFieldLimit(field string) (int, bool) {
	switch field {
	case "base_url":
		return maxChannelBaseURLBytes, false
	case "group":
		return maxChannelGroupBytes, false
	case "model_mapping":
		return maxChannelMappingBytes, true
	case "status_code_mapping":
		return maxChannelStatusCodeMappingBytes, true
	case "remark":
		return maxChannelRemarkBytes, false
	case "setting":
		return maxChannelProviderSettingBytes, true
	case "settings":
		return maxChannelOpaqueJSONBytes, true
	case "openai_organization", "open_ai_organization":
		return maxChannelOrganizationBytes, false
	case "test_model":
		return maxChannelModelNameBytes, false
	case "other":
		return maxChannelProviderSettingBytes, true
	case "other_info", "channel_info":
		return maxChannelOpaqueJSONBytes, true
	case "param_override", "header_override":
		return maxChannelMappingBytes, true
	default:
		return 0, false
	}
}

func validateChannelJSONObject(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var object map[string]any
	if err := jsonutil.UnmarshalJsonStr(value, &object); err != nil || object == nil {
		return invalidChannelInput(field, "must be a JSON object")
	}
	return nil
}

func validateChannelModels(raw string) error {
	if err := validateChannelText("models", raw, maxChannelModelsBytes, true, false); err != nil {
		return err
	}
	models := strings.Split(raw, ",")
	if len(models) > maxChannelModelCount {
		return invalidChannelInput("models", "contains too many entries")
	}
	for _, modelName := range models {
		modelName = strings.TrimSpace(modelName)
		if modelName == "" {
			continue
		}
		if err := validateChannelText("model name", modelName, maxChannelModelNameBytes, false, false); err != nil {
			return err
		}
	}
	return nil
}

func validateChannelText(field, value string, maximum int, allowEmpty, allowNewlines bool) error {
	if (!allowEmpty && strings.TrimSpace(value) == "") || len(value) > maximum || !utf8.ValidString(value) {
		return invalidChannelInput(field, "exceeds safe limits")
	}
	for _, character := range value {
		if allowNewlines && (character == '\n' || character == '\r' || character == '\t') {
			continue
		}
		if unicode.IsControl(character) || character == 0x061c || character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return invalidChannelInput(field, "contains unsafe characters")
		}
	}
	return nil
}
