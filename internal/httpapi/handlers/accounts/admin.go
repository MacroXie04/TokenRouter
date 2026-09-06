package accounts

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/dto"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"strings"
)

// GetUsers lists users (admin).
func GetUsers(c *gin.Context) {
	var p dto.Pagination
	_ = c.ShouldBindQuery(&p)
	p.Normalize()
	var users []model.User
	var total int64
	if err := model.DB.Model(&model.User{}).Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询用户失败"))
		return
	}
	if err := model.DB.Omit("password", "access_token").Order("id desc").
		Limit(p.PageSize).Offset(p.Offset()).Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询用户失败"))
		return
	}
	for i := range users {
		users[i].Setting = userssvc.AdminSafeUserSettingsJSON(users[i].Setting)
	}
	p.Total = total
	c.JSON(http.StatusOK, dto.Ok(gin.H{"items": users, "pagination": p}))
}

// GetUser returns a single user (admin) with the effective fine-grained
// permission matrix attached.
func GetUser(c *gin.Context) {
	user, err := userssvc.GetUserByID(textutil.Str2Int(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("用户不存在"))
		return
	}
	// Password hashes and dashboard PATs are authentication credentials, not
	// admin-profile fields. Never serialize either one.
	user.Password = ""
	user.AccessToken = nil
	user.Setting = userssvc.AdminSafeUserSettingsJSON(user.Setting)
	user.AdminPermissions = auth.Capabilities(user.Id, user.Role)
	c.JSON(http.StatusOK, dto.Ok(user))
}

// UpdateUser updates a user's quota, group, status, or role (admin). A root
// caller may also update the target's fine-grained admin_permissions
// (reference contract: only root can update admin permissions; non-admin
// targets have their overrides cleared).
func UpdateUser(c *gin.Context) {
	var req struct {
		Id               int                        `json:"id"`
		Quota            *int                       `json:"quota"`
		Group            string                     `json:"group"`
		Status           *int                       `json:"status"`
		Role             *int                       `json:"role"`
		DisplayName      *string                    `json:"display_name"`
		Password         string                     `json:"password"`
		Remark           *string                    `json:"remark"`
		AdminPermissions map[string]map[string]bool `json:"admin_permissions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if req.Id <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	var origin model.User
	if err := model.DB.First(&origin, req.Id).Error; err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("用户不存在"))
		return
	}
	// Preserve the reference's role hierarchy. Generic edits may only target a
	// strictly lower role, and role changes themselves must use ManageUser's
	// guarded promote/demote actions.
	if req.AdminPermissions != nil && requestctx.GetRole(c) != roles.RoleRootUser {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "仅 Root 用户可以更新管理权限"})
		return
	}
	if !canManageTargetRole(requestctx.GetRole(c), origin.Role) {
		c.JSON(http.StatusForbidden, dto.Fail("无权操作同级或更高级用户"))
		return
	}
	if req.Role != nil && *req.Role != origin.Role {
		c.JSON(http.StatusBadRequest, dto.Fail("角色变更必须使用提升或降级操作"))
		return
	}
	if req.Quota != nil && (*req.Quota < 0 || !quotamath.QuotaWithinBounds(*req.Quota)) {
		c.JSON(http.StatusBadRequest, dto.Fail("配额值超出允许范围"))
		return
	}
	if req.Status != nil && *req.Status != model.UserStatusEnabled && *req.Status != model.UserStatusDisabled {
		c.JSON(http.StatusBadRequest, dto.Fail("用户状态无效"))
		return
	}
	group := strings.TrimSpace(req.Group)
	if req.Group != "" && (group == "" || len(group) > 64 || strings.ContainsRune(group, '\x00')) {
		c.JSON(http.StatusBadRequest, dto.Fail("用户字段超出允许范围"))
		return
	}
	var groupUpdate *string
	if req.Group != "" {
		groupUpdate = &group
	}
	var displayName *string
	if req.DisplayName != nil {
		clean := strings.TrimSpace(*req.DisplayName)
		if clean == "" || len(clean) > 64 || strings.ContainsRune(clean, '\x00') {
			c.JSON(http.StatusBadRequest, dto.Fail("昵称格式无效"))
			return
		}
		displayName = &clean
	}
	var remark *string
	if req.Remark != nil {
		clean := strings.TrimSpace(*req.Remark)
		if len(clean) > 255 || strings.ContainsRune(clean, '\x00') {
			c.JSON(http.StatusBadRequest, dto.Fail("用户字段超出允许范围"))
			return
		}
		remark = &clean
	}
	if req.Password != "" && (len(req.Password) < 8 || len(req.Password) > 64 || strings.ContainsRune(req.Password, '\x00')) {
		c.JSON(http.StatusBadRequest, dto.Fail("密码长度必须在 8 到 64 个字符之间"))
		return
	}
	managedUpdate := auth.ManagedUserUpdate{
		DisplayName: displayName,
		Group:       groupUpdate,
		Remark:      remark,
		Quota:       req.Quota,
		Status:      req.Status,
		Password:    req.Password,
	}
	authzTouched := false
	authChanged := false
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		changed, err := auth.UpdateManagedUserInTx(tx, req.Id, origin.Role, managedUpdate)
		if err != nil {
			return err
		}
		authChanged = changed
		touched, err := updateAdminPermissionsForUserInTx(c, tx, req.Id, origin.Role, req.AdminPermissions)
		authzTouched = touched
		return err
	})
	if err != nil {
		if errors.Is(err, auth.ErrManagedUserRoleChanged) {
			c.JSON(http.StatusForbidden, dto.Fail("目标用户权限已变化，请刷新后重试"))
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if authzTouched {
		if err := auth.ReloadPermissionPolicy(); err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("权限策略刷新失败"))
			return
		}
	}
	billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage,
		"user.update username="+origin.Username+" id="+textutil.Int2Str(req.Id)+
			" password_reset="+strconv.FormatBool(req.Password != "")+
			" sessions_revoked="+strconv.FormatBool(authChanged))
	c.JSON(http.StatusOK, dto.OkMessage("更新成功"))
}

// updateAdminPermissionsForUserInTx applies the reference permission-touch
// semantics inside a transaction: nil permissions clear overrides for
// non-admin targets (root caller only), non-nil permissions require a root
// caller, and overrides for a non-admin target are cleared instead of set.
func updateAdminPermissionsForUserInTx(c *gin.Context, tx *gorm.DB, userID, userRole int, permissions map[string]map[string]bool) (bool, error) {
	if permissions == nil {
		if userRole < roles.RoleAdminUser && requestctx.GetRole(c) == roles.RoleRootUser {
			return true, auth.ClearUserAuthorizationInTx(tx, userID)
		}
		return false, nil
	}
	if requestctx.GetRole(c) != roles.RoleRootUser {
		return false, fmt.Errorf("仅 Root 用户可以更新管理权限")
	}
	if userRole < roles.RoleAdminUser {
		return true, auth.ClearUserAuthorizationInTx(tx, userID)
	}
	return true, auth.SetUserPermissionsInTx(tx, userID, permissions)
}
