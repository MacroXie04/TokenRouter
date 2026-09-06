package channels

import model "github.com/tokenrouter/tokenrouter/internal/store"

// channelHasSensitiveChanges reports whether the update touches a sensitive
// channel field (reference fail-closed classifier): the known sensitive
// fields are compared old-vs-new, and any request field that is not
// explicitly classified as sensitive/non-sensitive/operational/read-only is
// treated as sensitive so a newly introduced channel field cannot silently
// become editable by ChannelWrite-only admins.
func channelHasSensitiveChanges(req *dtoChannelUpdate, origin *model.Channel, requestData map[string]any) bool {
	if _, ok := requestData["type"]; ok && req.Type != origin.Type {
		return true
	}
	if _, ok := requestData["key"]; ok && req.Key != "" && req.Key != origin.Key {
		return true
	}
	if _, ok := requestData["base_url"]; ok && req.BaseURL != origin.BaseURL {
		return true
	}
	if _, ok := requestData["openai_organization"]; ok && req.OpenAIOrganization != origin.OpenAIOrganization {
		return true
	}
	if _, ok := requestData["header_override"]; ok && req.HeaderOverride != origin.HeaderOverride {
		return true
	}
	if _, ok := requestData["param_override"]; ok && req.ParamOverride != origin.ParamOverride {
		return true
	}
	if _, ok := requestData["setting"]; ok && req.Setting != origin.Setting {
		return true
	}
	if _, ok := requestData["other"]; ok && req.Other != origin.Other {
		return true
	}
	if _, ok := requestData["settings"]; ok && req.Settings != origin.OtherSettings {
		return true
	}
	for field := range requestData {
		if _, ok := channelSensitiveFields[field]; ok {
			continue
		}
		if _, ok := channelNonSensitiveFields[field]; ok {
			continue
		}
		if _, ok := channelOperationalFields[field]; ok {
			continue
		}
		if _, ok := channelReadOnlyFields[field]; ok {
			continue
		}
		return true
	}
	return false
}

// channelSensitiveFields lists the channel fields whose modification requires
// ChannelSensitiveWrite. Each is checked individually in
// channelHasSensitiveChanges with a precise old-vs-new comparison; the set
// is also used to exclude them from the fail-closed scan.
var channelSensitiveFields = map[string]struct{}{
	"type":                {},
	"key":                 {},
	"base_url":            {},
	"openai_organization": {},
	"header_override":     {},
	"param_override":      {},
	"setting":             {},
	"other":               {},
	"settings":            {},
}

// channelOperationalFields lists fields managed by operation endpoints
// instead of the general channel edit endpoint.
var channelOperationalFields = map[string]struct{}{
	"status": {},
}

// channelReadOnlyFields lists server-managed/accounting fields that the
// general channel edit endpoint must ignore even if a client sends them.
var channelReadOnlyFields = map[string]struct{}{
	"created_time":         {},
	"test_time":            {},
	"response_time":        {},
	"balance":              {},
	"balance_updated_time": {},
	"used_quota":           {},
}

// channelNonSensitiveFields lists the routing / server-managed channel
// fields a ChannelWrite admin may edit without ChannelSensitiveWrite. When a
// new field is added to the channel update request it must be added to this
// set or channelSensitiveFields; otherwise it falls through to the
// fail-closed branch and is treated as sensitive. TestChannelFieldsAreClassified
// enforces this.
var channelNonSensitiveFields = map[string]struct{}{
	"id":                  {},
	"test_model":          {},
	"name":                {},
	"weight":              {},
	"models":              {},
	"group":               {},
	"model_mapping":       {},
	"status_code_mapping": {},
	"priority":            {},
	"auto_ban":            {},
	"other_info":          {},
	"tag":                 {},
	"remark":              {},
	"channel_info":        {},
	"multi_key_mode":      {},
}

// dtoChannelUpdate mirrors dto.ChannelRequest with an id and pointer-shaped
// optional fields so the update can distinguish absent from zero values.
type dtoChannelUpdate struct {
	Id                 int    `json:"id"`
	Type               int    `json:"type"`
	Key                string `json:"key"`
	OpenAIOrganization string `json:"openai_organization"`
	TestModel          string `json:"test_model"`
	BaseURL            string `json:"base_url"`
	Other              string `json:"other"`
	Setting            string `json:"setting"`
	Settings           string `json:"settings"`
	Name               string `json:"name"`
	Models             string `json:"models"`
	Group              string `json:"group"`
	Weight             *uint  `json:"weight"`
	Priority           *int64 `json:"priority"`
	ModelMapping       string `json:"model_mapping"`
	StatusCodeMapping  string `json:"status_code_mapping"`
	AutoBan            *int   `json:"auto_ban"`
	OtherInfo          string `json:"other_info"`
	Tag                string `json:"tag"`
	Remark             string `json:"remark"`
	ParamOverride      string `json:"param_override"`
	HeaderOverride     string `json:"header_override"`
	ChannelInfo        any    `json:"channel_info"`
	MultiKeyMode       string `json:"multi_key_mode"`
}
