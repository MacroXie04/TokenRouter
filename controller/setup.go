package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

// GetSetup reports whether an initial root account must be created.
func GetSetup(c *gin.Context) {
	var count int64
	model.DB.Model(&model.User{}).Where("role >= ?", constant.RoleRootUser).Count(&count)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"setup_required": count == 0,
			"site_name":      setting.GetSiteName(),
			"version":        common.Version,
		},
	})
}

// PostSetup creates the initial root account via the setup wizard. TokenRouter
// does not ship a default password; the operator chooses one here.
func PostSetup(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required,min=3,max=64"`
		Password string `json:"password" binding:"required,min=8,max=64"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
		return
	}
	var count int64
	model.DB.Model(&model.User{}).Where("role >= ?", constant.RoleRootUser).Count(&count)
	if count > 0 {
		c.JSON(http.StatusForbidden, dto.Fail("系统已初始化，禁止重复设置"))
		return
	}

	hash, err := common.PasswordHash(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("密码加密失败"))
		return
	}
	user := model.User{
		Username:    req.Username,
		Password:    hash,
		DisplayName: req.Username,
		Role:        constant.RoleRootUser,
		Status:      model.UserStatusEnabled,
		Group:       service.GroupDefault,
		Quota:       setting.GetOptionIntOrDefault(setting.InitialQuotaOption, 500000),
		CreatedAt:   common.NowTimestamp(),
		AuthVersion: 1,
	}
	if err := model.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("创建管理员失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("初始化成功"))
}
