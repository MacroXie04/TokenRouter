package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// AddAbility upserts a channel-model routing ability.
func AddAbility(c *gin.Context) {
	var req struct {
		Group     string `json:"group" binding:"required"`
		Model     string `json:"model" binding:"required"`
		ChannelId int    `json:"channel_id" binding:"required"`
		Enabled   *bool  `json:"enabled"`
		Priority  *int64 `json:"priority"`
		Weight    *uint  `json:"weight"`
		Tag       string `json:"tag"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
		return
	}
	ability := model.Ability{
		Group:     req.Group,
		Model:     req.Model,
		ChannelId: req.ChannelId,
		Enabled:   true,
		Priority:  req.Priority,
		Weight:    1,
		Tag:       req.Tag,
	}
	if req.Enabled != nil {
		ability.Enabled = *req.Enabled
	}
	if req.Weight != nil {
		ability.Weight = *req.Weight
	}
	// Upsert on composite primary key.
	var existing model.Ability
	res := model.DB.Where("`group` = ? AND model = ? AND channel_id = ?", req.Group, req.Model, req.ChannelId).First(&existing)
	if res.Error == nil {
		if err := model.DB.Model(&existing).Updates(map[string]any{
			"enabled": ability.Enabled, "priority": ability.Priority,
			"weight": ability.Weight, "tag": ability.Tag,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("更新失败"))
			return
		}
	} else {
		if err := model.DB.Create(&ability).Error; err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("创建失败: "+err.Error()))
			return
		}
	}
	_ = service.SyncAbilityCache()
	c.JSON(http.StatusOK, dto.OkMessage("保存成功"))
}

// GetAbilities lists abilities.
func GetAbilities(c *gin.Context) {
	var abilities []model.Ability
	model.DB.Order("`group` asc, model asc").Find(&abilities)
	c.JSON(http.StatusOK, dto.Ok(abilities))
}

// DeleteAbility deletes an ability.
func DeleteAbility(c *gin.Context) {
	var req struct {
		Group     string `json:"group"`
		Model     string `json:"model"`
		ChannelId int    `json:"channel_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := model.DB.Where("`group` = ? AND model = ? AND channel_id = ?", req.Group, req.Model, req.ChannelId).Delete(&model.Ability{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("删除失败"))
		return
	}
	_ = service.SyncAbilityCache()
	c.JSON(http.StatusOK, dto.OkMessage("删除成功"))
}
