package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

const (
	maxSystemOptionKeyBytes    = setting.MaxOptionKeyBytes
	maxSystemOptionValueBytes  = setting.MaxOptionValueBytes
	maxSystemOptionRows        = setting.MaxOptionRows
	maxSystemOptionResultBytes = setting.MaxOptionAggregateBytes
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
		users[i].Setting = service.AdminSafeUserSettingsJSON(users[i].Setting)
	}
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
	// Password hashes and dashboard PATs are authentication credentials, not
	// admin-profile fields. Never serialize either one.
	user.Password = ""
	user.AccessToken = nil
	user.Setting = service.AdminSafeUserSettingsJSON(user.Setting)
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
	if req.AdminPermissions != nil && common.GetRole(c) != constant.RoleRootUser {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "仅 Root 用户可以更新管理权限"})
		return
	}
	if !canManageTargetRole(common.GetRole(c), origin.Role) {
		c.JSON(http.StatusForbidden, dto.Fail("无权操作同级或更高级用户"))
		return
	}
	if req.Role != nil && *req.Role != origin.Role {
		c.JSON(http.StatusBadRequest, dto.Fail("角色变更必须使用提升或降级操作"))
		return
	}
	if req.Quota != nil && (*req.Quota < 0 || !common.QuotaWithinBounds(*req.Quota)) {
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
	managedUpdate := service.ManagedUserUpdate{
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
		changed, err := service.UpdateManagedUserInTx(tx, req.Id, origin.Role, managedUpdate)
		if err != nil {
			return err
		}
		authChanged = changed
		touched, err := updateAdminPermissionsForUserInTx(c, tx, req.Id, origin.Role, req.AdminPermissions)
		authzTouched = touched
		return err
	})
	if err != nil {
		if errors.Is(err, service.ErrManagedUserRoleChanged) {
			c.JSON(http.StatusForbidden, dto.Fail("目标用户权限已变化，请刷新后重试"))
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if authzTouched {
		if err := service.ReloadPermissionPolicy(); err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("权限策略刷新失败"))
			return
		}
	}
	service.RecordSystemLog(common.GetUserId(c), service.LogTypeManage,
		"user.update username="+origin.Username+" id="+common.Int2Str(req.Id)+
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
	if err := model.DB.Model(&model.User{}).Count(&userCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询仪表盘数据失败"))
		return
	}
	if err := model.DB.Model(&model.Token{}).Count(&tokenCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询仪表盘数据失败"))
		return
	}
	if err := model.DB.Model(&model.Channel{}).Count(&channelCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询仪表盘数据失败"))
		return
	}
	if err := model.LOG_DB.Model(&model.Log{}).Where("type = ?", service.LogTypeConsume).Count(&requestCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询仪表盘数据失败"))
		return
	}
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
	if err := model.DB.Order("key asc").Limit(maxSystemOptionRows + 1).Find(&stored).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if len(stored) > maxSystemOptionRows {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "系统设置数量超出安全范围"})
		return
	}
	options := make([]model.Option, 0, len(stored)+10)
	seen := make(map[string]bool, len(stored))
	resultBytes := 0
	for _, option := range stored {
		if option.Key == "theme.frontend" || option.Key == setting.LegacyPaymentComplianceOption {
			continue
		}
		// The Waffo Pancake key is managed only through its dedicated
		// blank-preserving write endpoint. Omitting the row entirely prevents a
		// generic settings client from learning or accidentally round-tripping
		// this write-only credential identifier.
		if option.Key == setting.WaffoPancakePrivateKeyOption {
			continue
		}
		if isSensitiveOptionKey(option.Key) {
			if !validSystemOptionKey(option.Key) || resultBytes > maxSystemOptionResultBytes-len(option.Key) {
				c.JSON(http.StatusOK, gin.H{"success": false, "message": "系统设置数据超出安全范围"})
				return
			}
			option.Value = ""
			option.Redacted = true
			options = append(options, option)
			seen[option.Key] = true
			resultBytes += len(option.Key)
			continue
		}
		if option.Key == setting.QuotaPerUnitOption {
			option.Value = strconv.Itoa(common.QuotaPerUnit)
		}
		if !validSystemOptionKey(option.Key) || !validSystemOptionValue(option.Value) ||
			resultBytes > maxSystemOptionResultBytes-len(option.Key)-len(option.Value) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "系统设置数据超出安全范围"})
			return
		}
		options = append(options, option)
		seen[option.Key] = true
		resultBytes += len(option.Key) + len(option.Value)
	}
	for key, value := range setting.ChannelAffinityOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	for key, value := range setting.CheckinOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	for key, value := range setting.ModelPolicyOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	for key, value := range setting.UsageRatioOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	for key, value := range setting.GrokOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	for key, value := range setting.BillingOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	for key, value := range setting.AuthenticationOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
			seen[key] = true
		}
	}
	for key, value := range setting.OperationsOptionDefaults() {
		if !seen[key] {
			option := model.Option{Key: key, Value: value}
			if isSensitiveOptionKey(key) {
				option.Value = ""
				option.Redacted = true
			}
			options = append(options, option)
			seen[key] = true
		}
	}
	for key, value := range setting.ModelRequestRateLimitOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
			seen[key] = true
		}
	}
	for key, value := range map[string]string{
		setting.ExposeRatioEnabledOption:          "false",
		setting.PasskeyEnabledOption:              "false",
		setting.GroupGroupRatioOption:             "{}",
		setting.TopUpGroupRatioOption:             service.TopUpGroupRatioOptionDefault(),
		setting.ChatsOption:                       "[]",
		setting.QuotaPerUnitOption:                strconv.Itoa(common.QuotaPerUnit),
		setting.ConsoleAPIInfoOption:              "[]",
		setting.ConsoleAPIInfoEnabledOption:       "true",
		setting.ConsoleFAQOption:                  "[]",
		setting.ConsoleFAQEnabledOption:           "true",
		setting.ConsoleUptimeKumaGroupsOption:     "[]",
		setting.ConsoleUptimeKumaEnabledOption:    "true",
		setting.ConsoleAnnouncementsOption:        "[]",
		setting.ConsoleAnnouncementsEnabledOption: "true",
	} {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	if !seen[setting.UserUsableGroupsOption] {
		options = append(options, model.Option{
			Key:   setting.UserUsableGroupsOption,
			Value: setting.UserUsableGroupsOptionDefault(),
		})
	}
	if !seen[setting.AutoGroupsOption] {
		options = append(options, model.Option{Key: setting.AutoGroupsOption, Value: setting.AutoGroupsOptionDefault()})
	}
	if !seen[setting.MaxTokenAutoGroupsOption] {
		options = append(options, model.Option{Key: setting.MaxTokenAutoGroupsOption, Value: setting.MaxTokenAutoGroupsOptionDefault()})
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Key < options[j].Key })
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": options})
}

// GetSystemInstances returns the registered cluster nodes (admin).
func GetSystemInstances(c *gin.Context) {
	instances, err := service.GetSystemInstances()
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询实例失败"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(instances))
}

// UpdateOptions updates one system option using the reference key/value shape.
func UpdateOptions(c *gin.Context) {
	var request struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "无效的参数"})
		return
	}
	if !validSystemOptionKey(request.Key) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "设置键超出允许范围"})
		return
	}
	value, ok := systemOptionScalarString(request.Value)
	if !ok || !validSystemOptionValue(value) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "设置值超出允许范围"})
		return
	}
	if isPaymentComplianceOptionKey(request.Key) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "合规确认字段不允许通过通用设置接口修改"})
		return
	}
	if request.Key == setting.QuotaPerUnitOption && value != strconv.Itoa(common.QuotaPerUnit) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "QuotaPerUnit is fixed at 500000 and cannot be changed at runtime"})
		return
	}
	if err := setting.ValidateBillingOptionUpdate(request.Key, value); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
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
	case setting.GroupGroupRatioOption:
		err = service.UpdateGroupGroupRatioOption(value)
	case setting.TopUpGroupRatioOption:
		err = service.UpdateTopUpGroupRatioOption(value)
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
	normalized := strings.ToLower(strings.TrimSpace(key))
	return strings.HasSuffix(normalized, "token") || strings.HasSuffix(normalized, "secret") ||
		strings.HasSuffix(normalized, "key") || strings.HasSuffix(normalized, "password") ||
		strings.HasSuffix(normalized, "credential")
}

func validSystemOptionKey(key string) bool {
	if key == "" || len(key) > maxSystemOptionKeyBytes || !utf8.ValidString(key) {
		return false
	}
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func validSystemOptionValue(value string) bool {
	if len(value) > maxSystemOptionValueBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x061c ||
			r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) ||
			(r >= 0x2066 && r <= 0x2069) {
			return false
		}
	}
	return true
}

func systemOptionScalarString(raw json.RawMessage) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return typed, true
	case bool:
		return strconv.FormatBool(typed), true
	case json.Number:
		if _, err := typed.Int64(); err == nil {
			return typed.String(), true
		}
		parsed, err := typed.Float64()
		return typed.String(), err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	default:
		return "", false
	}
}

func isPositiveOptionValue(value string) bool {
	if integer, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		return integer > 0
	}
	decimal, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return err == nil && decimal > 0
}
