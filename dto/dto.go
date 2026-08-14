// Package dto defines API request and response transfer objects.
package dto

// Response is the standard JSON envelope for dashboard API responses.
type Response struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// Ok builds a success response.
func Ok(data any) *Response {
	return &Response{Success: true, Data: data}
}

// OkMessage builds a success response with a message.
func OkMessage(message string) *Response {
	return &Response{Success: true, Message: message}
}

// Fail builds a failure response.
func Fail(message string) *Response {
	return &Response{Success: false, Message: message}
}

// Pagination is a standard pagination cursor.
type Pagination struct {
	Page       int `json:"page" form:"page"`
	PageSize   int `json:"page_size" form:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int `json:"total_pages"`
}

// Normalize clamps page/page_size to sane bounds.
func (p *Pagination) Normalize() {
	if p.Page <= 0 {
		p.Page = 1
	}
	if p.PageSize <= 0 || p.PageSize > 100 {
		p.PageSize = 10
	}
}

// Offset returns the SQL offset for the current page.
func (p *Pagination) Offset() int {
	return (p.Page - 1) * p.PageSize
}

// RegisterRequest is the sign-up payload.
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=64"`
	Password string `json:"password" binding:"required,min=8,max=64"`
	Email    string `json:"email"`
	AffCode  string `json:"aff_code"`
	Turnstile string `json:"turnstile_token"`
	WeChatCode string `json:"wechat_code"`
}

// LoginRequest is the sign-in payload.
type LoginRequest struct {
	Username  string `json:"username" binding:"required"`
	Password  string `json:"password" binding:"required"`
	Email     string `json:"email"`
	Turnstile string `json:"turnstile_token"`
	WeChatCode string `json:"wechat_code"`
}

// Login2FARequest completes a 2FA login.
type Login2FARequest struct {
	FlowToken string `json:"flow_token" binding:"required"`
	Code      string `json:"code" binding:"required"`
}

// ResetPasswordRequest resets a password with an email token.
type ResetPasswordRequest struct {
	Email       string `json:"email" binding:"required"`
	ResetToken  string `json:"reset_token" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=8,max=64"`
}

// UpdateUserRequest updates self profile.
type UpdateUserRequest struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	OldPassword string `json:"old_password"`
}

// TokenRequest is the payload to create/edit a relay token.
type TokenRequest struct {
	Name           string `json:"name" binding:"required"`
	ExpiredTime    int64  `json:"expired_time"`
	RemainQuota    int    `json:"remain_quota"`
	UnlimitedQuota bool   `json:"unlimited_quota"`
	ModelLimitsEnabled bool `json:"model_limits_enabled"`
	ModelLimits    []string `json:"model_limits"`
	AllowIps       string `json:"allow_ips"`
	Group          string `json:"group"`
	CrossGroupRetry bool  `json:"cross_group_retry"`
	AutoGroups     []string `json:"auto_groups"`
}

// ChannelRequest is the payload to create/edit a channel.
type ChannelRequest struct {
	Name              string  `json:"name" binding:"required"`
	Type              int     `json:"type" binding:"required"`
	Key               string  `json:"key"`
	BaseURL           string  `json:"base_url"`
	Models            string  `json:"models"`
	Group             string  `json:"group"`
	Weight            *uint   `json:"weight"`
	Priority          *int64  `json:"priority"`
	ModelMapping      string  `json:"model_mapping"`
	StatusCodeMapping string  `json:"status_code_mapping"`
	Tag               string  `json:"tag"`
	Remark            string  `json:"remark"`
	Setting           string  `json:"setting"`
}

// TopUpRequest is a balance top-up request.
type TopUpRequest struct {
	Amount      int    `json:"amount" binding:"required"`
	PaymentMethod string `json:"payment_method"`
}
