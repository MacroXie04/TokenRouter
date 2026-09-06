package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/service"
)

// GetSelfAff returns the signed-in user's affiliate code and referral stats.
func GetSelfAff(c *gin.Context) {
	user, err := service.GetUserByID(common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("用户不存在"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{
		"aff_code":          user.AffCode,
		"aff_count":         user.AffCount,
		"aff_quota":         user.AffQuota,
		"aff_history_quota": user.AffHistoryQuota,
	}))
}

// TransferAffQuota moves accumulated affiliate quota into usable quota.
func TransferAffQuota(c *gin.Context) {
	var req struct {
		Quota int `json:"quota"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Quota <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := service.TransferAffQuota(common.GetUserId(c), req.Quota); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("转移成功"))
}

// GetSelfPasskeys lists the signed-in user's registered passkeys.
func GetSelfPasskeys(c *gin.Context) {
	creds, err := service.ListPasskeys(common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询失败"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(creds))
}

// DeleteSelfPasskeys removes every passkey of the signed-in user and bumps the
// auth version (other sessions die on their next refresh; this one is kept).
func DeleteSelfPasskeys(c *gin.Context) {
	userId := common.GetUserId(c)
	// When the user has 2FA enabled, a 2FA security proof for the
	// passkey.delete scope is required; otherwise confirm a passkey exists.
	twoFAEnabled, err := service.TwoFAStatusChecked(userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询安全设置失败"))
		return
	}
	if twoFAEnabled {
		if !middleware.RequireSecurityProof(c, service.SecurityProofScopePasskeyDelete, []string{service.SecurityProofMethod2FA}) {
			return
		}
	} else {
		passkeyEnabled, err := service.PasskeyEnabledChecked(userId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("查询安全设置失败"))
			return
		}
		if !passkeyEnabled {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "该用户尚未绑定 Passkey"})
			return
		}
	}
	sid, ok := currentSid(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, dto.Fail("当前认证方式不支持解绑 Passkey"))
		return
	}
	if err := service.DeleteAllPasskeysAndRotateSession(userId, sid); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("解绑失败"))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("Passkey 已解绑"))
}

// RegenerateBackupCodes replaces the user's 2FA backup codes with a fresh set.
func RegenerateBackupCodes(c *gin.Context) {
	userId := common.GetUserId(c)
	twoFAEnabled, err := service.TwoFAStatusChecked(userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询安全设置失败"))
		return
	}
	if !twoFAEnabled {
		c.JSON(http.StatusBadRequest, dto.Fail(service.ErrTwoFANotEnabled.Error()))
		return
	}
	if !middleware.RequireSecurityProof(c, service.SecurityProofScopeBackupCodeReset,
		[]string{service.SecurityProofMethod2FA, service.SecurityProofMethodPasskey}) {
		return
	}
	sid, ok := currentSid(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, dto.Fail("当前认证方式不支持重置备用码"))
		return
	}
	codes, err := service.RegenerateBackupCodesAndRotateSession(userId, sid)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"backup_codes": codes}))
}

// UpdateUserSetting stores the per-user quota warning configuration.
func UpdateUserSetting(c *gin.Context) {
	var req struct {
		QuotaWarningThreshold            int    `json:"quota_warning_threshold"`
		QuotaWarningType                 string `json:"quota_warning_type"`
		NotifyType                       string `json:"notify_type"`
		WebhookURL                       string `json:"webhook_url"`
		WebhookSecret                    string `json:"webhook_secret"`
		NotificationEmail                string `json:"notification_email"`
		BarkURL                          string `json:"bark_url"`
		GotifyURL                        string `json:"gotify_url"`
		GotifyToken                      string `json:"gotify_token"`
		GotifyPriority                   int    `json:"gotify_priority"`
		AcceptUnsetRatioModel            bool   `json:"accept_unset_model_ratio_model"`
		RecordIPLog                      bool   `json:"record_ip_log"`
		UpstreamModelUpdateNotifyEnabled *bool  `json:"upstream_model_update_notify_enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	notifyType := req.QuotaWarningType
	if notifyType == "" {
		notifyType = req.NotifyType
	}
	if req.QuotaWarningType != "" && req.NotifyType != "" && req.QuotaWarningType != req.NotifyType {
		c.JSON(http.StatusBadRequest, dto.Fail("通知设置格式无效"))
		return
	}
	if err := service.UpdateUserNotificationSettings(common.GetUserId(c), common.GetRole(c),
		service.UserNotificationSettingsInput{
			NotifyType:                       notifyType,
			QuotaWarningThreshold:            req.QuotaWarningThreshold,
			WebhookURL:                       req.WebhookURL,
			WebhookSecret:                    req.WebhookSecret,
			NotificationEmail:                req.NotificationEmail,
			BarkURL:                          req.BarkURL,
			GotifyURL:                        req.GotifyURL,
			GotifyToken:                      req.GotifyToken,
			GotifyPriority:                   req.GotifyPriority,
			AcceptUnsetRatioModel:            req.AcceptUnsetRatioModel,
			RecordIPLog:                      req.RecordIPLog,
			UpstreamModelUpdateNotifyEnabled: req.UpstreamModelUpdateNotifyEnabled,
		}); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("通知设置格式无效"))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("设置已保存"))
}

// GetOAuthBindings lists the signed-in user's custom-provider bindings
// (built-in providers live in user columns and are not listed here —
// reference semantics).
func GetOAuthBindings(c *gin.Context) {
	userId := common.GetUserId(c)
	if userId == 0 {
		c.JSON(http.StatusUnauthorized, dto.Fail("未登录"))
		return
	}
	bindings, err := service.ListOAuthBindings(userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": bindings})
}

// UnbindOAuth removes one custom-provider binding for the signed-in user.
// The path carries the numeric provider id; no existence check is performed
// (reference semantics — unbinding a non-bound provider succeeds).
func UnbindOAuth(c *gin.Context) {
	identity, ok := requireLoginSession(c)
	if !ok {
		return
	}
	if err := service.UnbindOAuth(identity.UserID, c.Param("provider_id")); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("解绑成功"))
}
