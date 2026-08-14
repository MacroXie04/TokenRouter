package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// GetChannels lists channels with pagination.
func GetChannels(c *gin.Context) {
	var p dto.Pagination
	_ = c.ShouldBindQuery(&p)
	p.Normalize()
	var channels []model.Channel
	var total int64
	model.DB.Model(&model.Channel{}).Count(&total)
	model.DB.Order("id desc").Limit(p.PageSize).Offset(p.Offset()).Find(&channels)
	p.Total = total
	p.TotalPages = int((total + int64(p.PageSize) - 1) / int64(p.PageSize))
	c.JSON(http.StatusOK, dto.Ok(gin.H{"items": channels, "pagination": p}))
}

// GetChannel returns a single channel.
func GetChannel(c *gin.Context) {
	ch, err := service.GetChannelByID(common.Str2Int(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("渠道不存在"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(ch))
}

// AddChannel creates a channel.
func AddChannel(c *gin.Context) {
	var req dto.ChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
		return
	}
	weight := uint(constant.DefaultChannelWeight)
	if req.Weight != nil {
		weight = *req.Weight
	}
	channel := model.Channel{
		Type:              req.Type,
		Key:               req.Key,
		Name:              req.Name,
		BaseURL:           req.BaseURL,
		Models:            req.Models,
		Group:             req.Group,
		Weight:            &weight,
		Priority:          req.Priority,
		ModelMapping:      req.ModelMapping,
		StatusCodeMapping: req.StatusCodeMapping,
		Tag:               req.Tag,
		Remark:            req.Remark,
		Setting:           req.Setting,
		Status:            constant.ChannelStatusEnabled,
		CreatedTime:       common.NowTimestamp(),
	}
	if err := model.DB.Create(&channel).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("创建失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(channel))
}

// UpdateChannel updates a channel.
func UpdateChannel(c *gin.Context) {
	var req dto.ChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
		return
	}
	id := common.Str2Int(c.Param("id"))
	updates := map[string]any{
		"type":               req.Type,
		"name":               req.Name,
		"base_url":           req.BaseURL,
		"models":             req.Models,
		"group":              req.Group,
		"model_mapping":      req.ModelMapping,
		"status_code_mapping": req.StatusCodeMapping,
		"tag":                req.Tag,
		"remark":             req.Remark,
		"setting":            req.Setting,
	}
	if req.Key != "" {
		updates["key"] = req.Key
	}
	if req.Weight != nil {
		updates["weight"] = *req.Weight
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
	}
	if err := model.DB.Model(&model.Channel{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("更新失败"))
		return
	}
	_ = service.SyncAbilityCache()
	c.JSON(http.StatusOK, dto.OkMessage("更新成功"))
}

// DeleteChannel deletes a channel.
func DeleteChannel(c *gin.Context) {
	id := common.Str2Int(c.Param("id"))
	if err := model.DB.Delete(&model.Channel{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("删除失败"))
		return
	}
	_ = model.DB.Where("channel_id = ?", id).Delete(&model.Ability{}).Error
	_ = service.SyncAbilityCache()
	c.JSON(http.StatusOK, dto.OkMessage("删除成功"))
}

// TestChannel pings a channel with a minimal chat-completion test request.
func TestChannel(c *gin.Context) {
	id := common.Str2Int(c.Query("id"))
	ch, err := service.GetChannelByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("渠道不存在"))
		return
	}
	success, latency := service.TestChannelHealth(id)
	c.JSON(http.StatusOK, dto.Ok(gin.H{
		"id":            ch.Id,
		"name":          ch.Name,
		"type":          constant.ChannelTypeName(constant.ChannelType(ch.Type)),
		"success":       success,
		"response_time": latency,
	}))
}
