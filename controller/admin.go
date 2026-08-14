package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
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

// GetUser returns a single user (admin).
func GetUser(c *gin.Context) {
	user, err := service.GetUserByID(common.Str2Int(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("用户不存在"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(user))
}

// UpdateUser updates a user's quota, group, status, or role (admin).
func UpdateUser(c *gin.Context) {
	var req struct {
		Id       int    `json:"id"`
		Quota    *int   `json:"quota"`
		Group    string `json:"group"`
		Status   *int   `json:"status"`
		Role     *int   `json:"role"`
		Remark   string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
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
	if err := model.DB.Model(&model.User{}).Where("id = ?", req.Id).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("更新失败"))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("更新成功"))
}

// GetAllTokens lists all relay tokens (admin).
func GetAllTokens(c *gin.Context) {
	var p dto.Pagination
	_ = c.ShouldBindQuery(&p)
	p.Normalize()
	var tokens []model.Token
	var total int64
	model.DB.Model(&model.Token{}).Count(&total)
	model.DB.Order("id desc").Limit(p.PageSize).Offset(p.Offset()).Find(&tokens)
	p.Total = total
	c.JSON(http.StatusOK, dto.Ok(gin.H{"items": tokens, "pagination": p}))
}

// GetLogs lists usage logs (admin) with optional filters.
func GetLogs(c *gin.Context) {
	var p dto.Pagination
	_ = c.ShouldBindQuery(&p)
	p.Normalize()
	query := model.LOG_DB.Model(&model.Log{})
	if userName := c.Query("username"); userName != "" {
		query = query.Where("username = ?", userName)
	}
	if modelName := c.Query("model_name"); modelName != "" {
		query = query.Where("model_name = ?", modelName)
	}
	var total int64
	query.Count(&total)
	var logs []model.Log
	query.Order("id desc").Limit(p.PageSize).Offset(p.Offset()).Find(&logs)
	p.Total = total
	c.JSON(http.StatusOK, dto.Ok(gin.H{"items": logs, "pagination": p}))
}

// GetDashboardData returns admin dashboard aggregation.
func GetDashboardData(c *gin.Context) {
	var userCount, tokenCount, channelCount, requestCount int64
	model.DB.Model(&model.User{}).Count(&userCount)
	model.DB.Model(&model.Token{}).Count(&tokenCount)
	model.DB.Model(&model.Channel{}).Count(&channelCount)
	model.LOG_DB.Model(&model.Log{}).Where("type = ?", service.LogTypeConsume).Count(&requestCount)
	c.JSON(http.StatusOK, dto.Ok(gin.H{
		"user_count":     userCount,
		"token_count":    tokenCount,
		"channel_count":  channelCount,
		"request_count":  requestCount,
	}))
}

// GetOptions returns all system options (admin).
func GetOptions(c *gin.Context) {
	var options []model.Option
	model.DB.Find(&options)
	c.JSON(http.StatusOK, dto.Ok(options))
}

// GetSystemInstances returns the registered cluster nodes (admin).
func GetSystemInstances(c *gin.Context) {
	c.JSON(http.StatusOK, dto.Ok(service.GetSystemInstances()))
}

// UpdateOptions updates system options (admin).
func UpdateOptions(c *gin.Context) {
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	for k, v := range req {
		if err := setting.UpdateOption(k, v); err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("更新选项失败"))
			return
		}
	}
	c.JSON(http.StatusOK, dto.OkMessage("更新成功"))
}
