package accounts

// RegisterRequest is the sign-up payload.
type RegisterRequest struct {
	Username         string `json:"username" binding:"required,min=3,max=64"`
	Password         string `json:"password" binding:"required,min=8,max=64"`
	Email            string `json:"email"`
	AffCode          string `json:"aff_code"`
	VerificationCode string `json:"verification_code"`
	Turnstile        string `json:"turnstile_token"`
	WeChatCode       string `json:"wechat_code"`
}

// LoginRequest is the sign-in payload.
type LoginRequest struct {
	Username   string `json:"username" binding:"required"`
	Password   string `json:"password" binding:"required"`
	Email      string `json:"email"`
	Turnstile  string `json:"turnstile_token"`
	WeChatCode string `json:"wechat_code"`
}

// Login2FARequest completes a 2FA login.
type Login2FARequest struct {
	FlowToken string `json:"flow_token" binding:"required"`
	Code      string `json:"code" binding:"required"`
}

// ResetPasswordRequest accepts the reference token contract and TokenRouter's
// existing code/new-password extension. The controller validates the two
// alternative credential fields explicitly so conflicting values fail closed.
type ResetPasswordRequest struct {
	Email       string `json:"email" binding:"required"`
	Token       string `json:"token"`
	Code        string `json:"code"`
	NewPassword string `json:"new_password"`
}

// UpdateUserRequest updates self profile.
type UpdateUserRequest struct {
	DisplayName    string  `json:"display_name" binding:"omitempty,max=64"`
	Email          string  `json:"email" binding:"omitempty,max=50"`
	Password       string  `json:"password" binding:"omitempty,min=8,max=64"`
	OldPassword    string  `json:"old_password"`
	Language       *string `json:"language"`
	SidebarModules *string `json:"sidebar_modules"`
}
