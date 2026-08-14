package model

import (
	"strings"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
)

// Token is a relay/dashboard API access token.
type Token struct {
	Id                 int            `json:"id" gorm:"primaryKey"`
	UserId             int            `json:"user_id" gorm:"index"`
	Key                string         `json:"key" gorm:"type:varchar(128);uniqueIndex"`
	Status             int            `json:"status"`
	Name               string         `json:"name" gorm:"index;type:varchar(64)"`
	CreatedTime        int64          `json:"created_time"`
	AccessedTime       int64          `json:"accessed_time"`
	ExpiredTime        int64          `json:"expired_time"`
	RemainQuota        int            `json:"remain_quota"`
	UnlimitedQuota     bool           `json:"unlimited_quota"`
	ModelLimitsEnabled bool           `json:"model_limits_enabled"`
	ModelLimits        string         `json:"model_limits" gorm:"type:text"`
	AllowIps           string         `json:"allow_ips" gorm:"type:varchar(128)"`
	UsedQuota          int            `json:"used_quota"`
	Group              string         `json:"group" gorm:"type:varchar(64)"`
	CrossGroupRetry    bool           `json:"cross_group_retry"`
	AutoGroups         string         `json:"auto_groups" gorm:"type:text"`
	DeletedAt          gorm.DeletedAt `json:"-" gorm:"index"`
}

func (Token) TableName() string { return "tokens" }

// MaskTokenKey masks a token key for list responses (reference contract:
// prefix + asterisks + suffix; short keys get fewer visible characters).
func MaskTokenKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return strings.Repeat("*", len(key))
	}
	if len(key) <= 8 {
		return key[:2] + "****" + key[len(key)-2:]
	}
	return key[:4] + "**********" + key[len(key)-4:]
}

// GetFullKey returns the unmasked key (only exposed through the
// rate-limited key-disclosure endpoints).
func (token *Token) GetFullKey() string {
	return token.Key
}

// GetMaskedKey returns the key in masked form for list/detail responses.
func (token *Token) GetMaskedKey() string {
	return MaskTokenKey(token.Key)
}

// GetAutoGroups parses the auto-groups field. New rows use the reference's
// JSON-array encoding; legacy TokenRouter rows stored a CSV list and are
// still readable.
func (token *Token) GetAutoGroups() ([]string, error) {
	if token.AutoGroups == "" {
		return nil, nil
	}
	var groups []string
	if err := common.UnmarshalJsonStr(token.AutoGroups, &groups); err == nil {
		return groups, nil
	}
	for _, g := range strings.Split(token.AutoGroups, ",") {
		if g = strings.TrimSpace(g); g != "" {
			groups = append(groups, g)
		}
	}
	return groups, nil
}

// SetAutoGroups stores the auto-groups field (JSON array, reference format).
func (token *Token) SetAutoGroups(groups []string) error {
	if len(groups) == 0 {
		token.AutoGroups = ""
		return nil
	}
	data, err := common.Marshal(groups)
	if err != nil {
		return err
	}
	token.AutoGroups = string(data)
	return nil
}

// GetModelLimits parses the comma-separated model-limits field.
func (token *Token) GetModelLimits() []string {
	if token.ModelLimits == "" {
		return nil
	}
	var out []string
	for _, m := range strings.Split(token.ModelLimits, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// GetModelLimitsMap returns the model-limits set.
func (token *Token) GetModelLimitsMap() map[string]bool {
	m := make(map[string]bool)
	for _, limit := range token.GetModelLimits() {
		m[limit] = true
	}
	return m
}
