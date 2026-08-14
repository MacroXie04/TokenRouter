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
	if service.TwoFAStatus(userId) {
		if !middleware.RequireSecurityProof(c, service.SecurityProofScopePasskeyDelete, []string{service.SecurityProofMethod2FA}) {
			return
		}
	} else if !service.PasskeyEnabled(userId) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "该用户尚未绑定 Passkey"})
		return
	}
	if err := service.DeleteAllPasskeys(userId); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("解绑失败"))
		return
	}
	if sid, ok := currentSid(c); ok {
		_ = service.BumpAuthVersionKeepSession(userId, sid)
	}
	c.JSON(http.StatusOK, dto.OkMessage("Passkey 已解绑"))
}

// RegenerateBackupCodes replaces the user's 2FA backup codes with a fresh set.
func RegenerateBackupCodes(c *gin.Context) {
	userId := common.GetUserId(c)
	codes, err := service.RegenerateBackupCodes(userId)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	if sid, ok := currentSid(c); ok {
		_ = service.BumpAuthVersionKeepSession(userId, sid)
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"backup_codes": codes}))
}

// UpdateUserSetting stores the per-user quota warning configuration.
func UpdateUserSetting(c *gin.Context) {
	var req struct {
		QuotaWarningThreshold int    `json:"quota_warning_threshold"`
		QuotaWarningType      string `json:"quota_warning_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := service.UpdateUserSetting(common.GetUserId(c), req.QuotaWarningThreshold, req.QuotaWarningType); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
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
	if err := service.UnbindOAuth(common.GetUserId(c), c.Param("provider_id")); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("解绑成功"))
}
