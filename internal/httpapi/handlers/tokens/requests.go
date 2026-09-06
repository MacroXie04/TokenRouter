package tokens

// TokenRequest is the payload to create/edit a relay token.
type TokenRequest struct {
	Name               string   `json:"name" binding:"required"`
	ExpiredTime        int64    `json:"expired_time"`
	RemainQuota        int      `json:"remain_quota"`
	UnlimitedQuota     bool     `json:"unlimited_quota"`
	ModelLimitsEnabled bool     `json:"model_limits_enabled"`
	ModelLimits        []string `json:"model_limits"`
	AllowIps           string   `json:"allow_ips"`
	Group              string   `json:"group"`
	CrossGroupRetry    bool     `json:"cross_group_retry"`
	AutoGroups         []string `json:"auto_groups"`
}
