package controller

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

// GetUsers lists users (admin).
func GetUsers(c *gin.Context) {
	var p dto.Pagination
	_ = c.ShouldBindQuery(&p)
	p.Normalize()
	var users []model.User
	var total int64
	model.DB.Model(&model.User{}).Count(&total)
	model.DB.Order("id desc").Limit(p.PageSize).Offset(p.Offset()).Find(&users)
	p.Total = total
	c.JSON(http.StatusOK, dto.Ok(gin.H{"items": users, "pagination": p}))
}

// GetUser returns a single user (admin) with the effective fine-grained
// permission matrix attached.
func GetUser(c *gin.Context) {
	user, err := service.GetUserByID(common.Str2Int(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("用户不存在"))
		return
	}
	user.AdminPermissions = service.Capabilities(user.Id, user.Role)
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
		Remark           string                     `json:"remark"`
		AdminPermissions map[string]map[string]bool `json:"admin_permissions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	var origin model.User
	if err := model.DB.First(&origin, req.Id).Error; err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("用户不存在"))
		return
	}
	updates := map[string]any{}
	if req.Quota != nil {
		updates["quota"] = *req.Quota
	}
	if req.Group != "" {
		updates["group"] = req.Group
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.Role != nil {
		updates["role"] = *req.Role
	}
	if req.Remark != "" {
		updates["remark"] = req.Remark
	}
	authzTouched := false
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if len(updates) > 0 {
			if err := tx.Model(&model.User{}).Where("id = ?", req.Id).Updates(updates).Error; err != nil {
				return err
			}
		}
		touched, err := updateAdminPermissionsForUserInTx(c, tx, req.Id, origin.Role, req.AdminPermissions)
		authzTouched = touched
		return err
	})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if authzTouched {
		if err := service.ReloadPermissionPolicy(); err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("权限策略刷新失败"))
			return
		}
	}
	c.JSON(http.StatusOK, dto.OkMessage("更新成功"))
}

// updateAdminPermissionsForUserInTx applies the reference permission-touch
// semantics inside a transaction: nil permissions clear overrides for
// non-admin targets (root caller only), non-nil permissions require a root
// caller, and overrides for a non-admin target are cleared instead of set.
func updateAdminPermissionsForUserInTx(c *gin.Context, tx *gorm.DB, userID, userRole int, permissions map[string]map[string]bool) (bool, error) {
	if permissions == nil {
		if userRole < constant.RoleAdminUser && common.GetRole(c) == constant.RoleRootUser {
			return true, service.ClearUserAuthorizationInTx(tx, userID)
		}
		return false, nil
	}
	if common.GetRole(c) != constant.RoleRootUser {
		return false, fmt.Errorf("仅 Root 用户可以更新管理权限")
	}
	if userRole < constant.RoleAdminUser {
		return true, service.ClearUserAuthorizationInTx(tx, userID)
	}
	return true, service.SetUserPermissionsInTx(tx, userID, permissions)
}

// GetDashboardData returns admin dashboard counters (TokenRouter extension at
// /api/dashboard/stats — the reference /api/data serves the quota histogram).
func GetDashboardData(c *gin.Context) {
	var userCount, tokenCount, channelCount, requestCount int64
	model.DB.Model(&model.User{}).Count(&userCount)
	model.DB.Model(&model.Token{}).Count(&tokenCount)
	model.DB.Model(&model.Channel{}).Count(&channelCount)
	model.LOG_DB.Model(&model.Log{}).Where("type = ?", service.LogTypeConsume).Count(&requestCount)
	c.JSON(http.StatusOK, dto.Ok(gin.H{
		"user_count":    userCount,
		"token_count":   tokenCount,
		"channel_count": channelCount,
		"request_count": requestCount,
	}))
}

// GetOptions returns non-sensitive system options to root operators.
func GetOptions(c *gin.Context) {
	var stored []model.Option
	if err := model.DB.Order("key asc").Find(&stored).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	options := make([]model.Option, 0, len(stored)+6)
	seen := make(map[string]bool, len(stored))
	for _, option := range stored {
		if option.Key == "theme.frontend" || isSensitiveOptionKey(option.Key) ||
			option.Key == setting.LegacyPaymentComplianceOption {
			continue
		}
		options = append(options, option)
		seen[option.Key] = true
	}
	for key, value := range setting.ChannelAffinityOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Key < options[j].Key })
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": options})
}

// GetSystemInstances returns the registered cluster nodes (admin).
func GetSystemInstances(c *gin.Context) {
	c.JSON(http.StatusOK, dto.Ok(service.GetSystemInstances()))
}

// UpdateOptions updates one system option using the reference key/value shape.
func UpdateOptions(c *gin.Context) {
	var request struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "无效的参数"})
		return
	}
	value := fmt.Sprintf("%v", request.Value)
	if isPaymentComplianceOptionKey(request.Key) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "合规确认字段不允许通过通用设置接口修改"})
		return
	}
	if request.Key == setting.QuotaForInviterOption || request.Key == setting.QuotaForInviteeOption {
		if isPositiveOptionValue(value) && !service.PaymentComplianceConfirmed() {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": service.ErrPaymentComplianceRequired.Error()})
			return
		}
	}
	var err error
	switch request.Key {
	case setting.ModelPriceOption:
		err = service.UpdateModelPriceOption(value)
	case setting.GroupRatioOption:
		err = service.UpdateGroupRatioOption(value)
	default:
		err = setting.UpdateOption(request.Key, value)
	}
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	service.RecordSystemLog(common.GetUserId(c), service.LogTypeManage, "option.update key="+request.Key)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func isPaymentComplianceOptionKey(key string) bool {
	return key == setting.LegacyPaymentComplianceOption || strings.HasPrefix(key, "payment_setting.compliance_")
}

func isSensitiveOptionKey(key string) bool {
	return strings.HasSuffix(key, "Token") || strings.HasSuffix(key, "Secret") ||
		strings.HasSuffix(key, "Key") || strings.HasSuffix(key, "secret") ||
		strings.HasSuffix(key, "api_key")
}

func isPositiveOptionValue(value string) bool {
	if integer, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		return integer > 0
	}
	decimal, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return err == nil && decimal > 0
}
