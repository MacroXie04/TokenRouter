package accounts

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/dto"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/pagination"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"strings"
)

var adminCreateUserValidator = validator.New()

// CreateUser creates a user from an administrator-supplied account identity.
// Submitted account state is deliberately whitelisted: quota, group, status,
// email, affiliate fields and credentials cannot be injected by this endpoint.
func CreateUser(c *gin.Context) {
	var submitted model.User
	err := c.ShouldBindJSON(&submitted)
	submitted.Username = strings.TrimSpace(submitted.Username)
	if err != nil || submitted.Username == "" || submitted.Password == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "无效的参数"})
		return
	}
	if err := adminCreateUserValidator.Struct(&submitted); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "输入不合法 " + err.Error()})
		return
	}
	if submitted.DisplayName == "" {
		submitted.DisplayName = submitted.Username
	}
	if submitted.Role >= requestctx.GetRole(c) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "无法创建权限大于等于自己的用户"})
		return
	}

	cleanUser := model.User{
		Username: submitted.Username, Password: submitted.Password,
		DisplayName: submitted.DisplayName, Role: submitted.Role,
	}
	authzTouched := false
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := auth.InsertAdminUserWithTx(tx, &cleanUser); err != nil {
			return err
		}
		touched, err := updateAdminPermissionsForUserInTx(c, tx, cleanUser.Id, cleanUser.Role, submitted.AdminPermissions)
		authzTouched = touched
		return err
	}); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if authzTouched {
		if err := auth.ReloadPermissionPolicy(); err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
	}
	auth.FinishAdminUserCreation(&cleanUser)
	billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage,
		"user.create username="+cleanUser.Username+" role="+textutil.Int2Str(cleanUser.Role)+
			" target_user_id="+textutil.Int2Str(cleanUser.Id))
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

// SearchUsers searches users for the admin console (reference contract:
// keyword matches username/email/display-name and numeric ids; group/role/
// status filters with status=-1 selecting soft-deleted users; sort whitelist;
// pageInfo shape; password and access_token omitted).
func SearchUsers(c *gin.Context) {
	keyword := c.Query("keyword")
	group := c.Query("group")
	var role *int
	if roleStr := c.Query("role"); roleStr != "" {
		if parsed, err := strconv.Atoi(roleStr); err == nil {
			role = &parsed
		}
	}
	var status *int
	if statusStr := c.Query("status"); statusStr != "" {
		if parsed, err := strconv.Atoi(statusStr); err == nil {
			status = &parsed
		}
	}
	page := pagination.FromContext(c)
	users, total, err := userssvc.SearchUsers(keyword, group, role, status,
		page.Offset(), page.PageSize, c.Query("sort_by"), c.Query("sort_order"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("搜索用户失败"))
		return
	}
	page.Total = int(total)
	page.Items = users
	c.JSON(http.StatusOK, dto.Ok(page))
}

// ManageUser applies admin actions to a target user (reference contract:
// disable/enable/delete/promote/demote/quota changes with role guards and the
// reference's Chinese validation messages).
func ManageUser(c *gin.Context) {
	var req struct {
		Id     int    `json:"id"`
		Action string `json:"action"`
		Value  int    `json:"value"`
		Mode   string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的请求参数"))
		return
	}
	var target model.User
	if err := model.DB.Unscoped().First(&target, req.Id).Error; err != nil || target.Id == 0 {
		c.JSON(http.StatusNotFound, dto.Fail("用户不存在"))
		return
	}
	myRole := requestctx.GetRole(c)
	if !canManageTargetRole(myRole, target.Role) {
		c.JSON(http.StatusForbidden, dto.Fail("无权操作同级或更高级用户"))
		return
	}

	switch req.Action {
	case "disable":
		if target.Role == roles.RoleRootUser {
			c.JSON(http.StatusForbidden, dto.Fail("不能禁用 Root 用户"))
			return
		}
		target.Status = model.UserStatusDisabled
	case "enable":
		target.Status = model.UserStatusEnabled
	case "delete":
		if target.Role == roles.RoleRootUser {
			c.JSON(http.StatusForbidden, dto.Fail("不能删除 Root 用户"))
			return
		}
		if err := auth.DeleteUser(target.Id); err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage,
			"user.manage action=delete username="+target.Username+" id="+textutil.Int2Str(target.Id))
		c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
		return
	case "promote":
		if myRole != roles.RoleRootUser {
			c.JSON(http.StatusForbidden, dto.Fail("无权将用户提升为管理员"))
			return
		}
		if target.Role >= roles.RoleAdminUser {
			c.JSON(http.StatusBadRequest, dto.Fail("该用户已是管理员"))
			return
		}
		target.Role = roles.RoleAdminUser
	case "demote":
		if target.Role == roles.RoleRootUser {
			c.JSON(http.StatusForbidden, dto.Fail("不能降级 Root 用户"))
			return
		}
		if target.Role == roles.RoleCommonUser {
			c.JSON(http.StatusBadRequest, dto.Fail("该用户已是普通用户"))
			return
		}
		target.Role = roles.RoleCommonUser
		if err := model.DB.Model(&model.User{}).Where("id = ?", target.Id).
			Update("role", roles.RoleCommonUser).Error; err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("降级失败"))
			return
		}
		if err := auth.RevokeAllUserSessions(target.Id); err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("降级失败"))
			return
		}
		billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage,
			"user.manage action=demote username="+target.Username+" id="+textutil.Int2Str(target.Id))
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "",
			"data": gin.H{"role": target.Role, "status": target.Status}})
		return
	case "add_quota":
		switch req.Mode {
		case "add":
			if req.Value <= 0 {
				c.JSON(http.StatusBadRequest, dto.Fail("配额变更值必须大于0"))
				return
			}
			if err := billingsvc.IncreaseUserQuota(target.Id, req.Value); err != nil {
				c.JSON(http.StatusInternalServerError, dto.Fail("配额变更失败"))
				return
			}
			billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage,
				"user.quota_add id="+textutil.Int2Str(target.Id)+" quota="+textutil.Int2Str(req.Value))
		case "subtract":
			if req.Value <= 0 {
				c.JSON(http.StatusBadRequest, dto.Fail("配额变更值必须大于0"))
				return
			}
			if err := billingsvc.DecreaseUserQuota(target.Id, req.Value); err != nil {
				c.JSON(http.StatusInternalServerError, dto.Fail("配额变更失败"))
				return
			}
			billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage,
				"user.quota_subtract id="+textutil.Int2Str(target.Id)+" quota="+textutil.Int2Str(req.Value))
		case "override":
			if req.Value < 0 || int64(req.Value) > quotamath.MaxQuota {
				c.JSON(http.StatusBadRequest, dto.Fail("配额值超出允许范围"))
				return
			}
			if err := billingsvc.SetUserQuota(target.Id, req.Value); err != nil {
				c.JSON(http.StatusInternalServerError, dto.Fail("配额变更失败"))
				return
			}
			billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage,
				"user.quota_override id="+textutil.Int2Str(target.Id)+" quota="+textutil.Int2Str(req.Value))
		default:
			c.JSON(http.StatusBadRequest, dto.Fail("无效的请求参数"))
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
		return
	default:
		c.JSON(http.StatusBadRequest, dto.Fail("无效的请求参数"))
		return
	}

	if err := model.DB.Model(&model.User{}).Where("id = ?", target.Id).
		Updates(map[string]any{"role": target.Role, "status": target.Status}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("操作失败"))
		return
	}
	billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage,
		"user.manage action="+req.Action+" username="+target.Username+" id="+textutil.Int2Str(target.Id))
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "",
		"data": gin.H{"role": target.Role, "status": target.Status}})
}

// AdminClearUserBinding clears one built-in identity binding for a strictly
// lower-role user (DELETE /api/user/:id/bindings/:binding_type).
func AdminClearUserBinding(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的用户 ID"))
		return
	}
	bindingType := strings.ToLower(strings.TrimSpace(c.Param("binding_type")))
	if bindingType == "" {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的绑定类型"))
		return
	}
	target, err := userssvc.GetUserByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("用户不存在"))
		return
	}
	if !canManageTargetRole(requestctx.GetRole(c), target.Role) {
		c.JSON(http.StatusForbidden, dto.Fail("无权操作同级或更高级用户"))
		return
	}
	if err := auth.ClearUserIdentityBinding(id, bindingType); err != nil {
		if errors.Is(err, auth.ErrInvalidIdentityBindingType) {
			c.JSON(http.StatusBadRequest, dto.Fail("无效的绑定类型"))
			return
		}
		c.JSON(http.StatusInternalServerError, dto.Fail("解除绑定失败"))
		return
	}
	billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage,
		"user.binding_clear id="+textutil.Int2Str(id)+" type="+bindingType)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "success"})
}

// AdminDeleteUser exposes the reference DELETE route while preserving
// TokenRouter's recoverable soft-delete and credential revocation semantics.
func AdminDeleteUser(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的用户 ID"))
		return
	}
	target, err := userssvc.GetUserByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("用户不存在"))
		return
	}
	if !canManageTargetRole(requestctx.GetRole(c), target.Role) {
		c.JSON(http.StatusForbidden, dto.Fail("无权操作同级或更高级用户"))
		return
	}
	if err := auth.DeleteUser(id); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("删除用户失败"))
		return
	}
	billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage,
		"user.delete username="+target.Username+" id="+textutil.Int2Str(id))
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}
