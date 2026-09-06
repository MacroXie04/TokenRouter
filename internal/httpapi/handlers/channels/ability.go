package channels

import (
	"errors"
	"github.com/gin-gonic/gin"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/dto"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
)

// AddAbility upserts a channel-model routing ability.
func AddAbility(c *gin.Context) {
	var req struct {
		Group     string  `json:"group" binding:"required"`
		Model     string  `json:"model" binding:"required"`
		ChannelId int     `json:"channel_id" binding:"required"`
		Enabled   *bool   `json:"enabled"`
		Priority  *int64  `json:"priority"`
		Weight    *uint   `json:"weight"`
		Tag       *string `json:"tag"`
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
	identity := map[string]any{"group": req.Group, "model": req.Model, "channel_id": req.ChannelId}
	res := model.DB.Where(identity).First(&existing)
	switch {
	case res.Error == nil:
		if err := model.DB.Model(&existing).Updates(map[string]any{
			"enabled": ability.Enabled, "priority": ability.Priority,
			"weight": ability.Weight, "tag": ability.Tag,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("更新失败"))
			return
		}
	case errors.Is(res.Error, gorm.ErrRecordNotFound):
		if err := model.DB.Create(&ability).Error; err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("创建失败: "+err.Error()))
			return
		}
	default:
		c.JSON(http.StatusInternalServerError, dto.Fail("查询能力失败"))
		return
	}
	if err := channelssvc.SyncAbilityCache(); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("路由缓存刷新失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("保存成功"))
}

// GetAbilities lists abilities.
func GetAbilities(c *gin.Context) {
	var abilities []model.Ability
	if err := model.DB.
		Order(clause.OrderByColumn{Column: clause.Column{Name: "group"}}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "model"}}).
		Find(&abilities).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询能力失败"))
		return
	}
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
	identity := map[string]any{"group": req.Group, "model": req.Model, "channel_id": req.ChannelId}
	if err := model.DB.Where(identity).Delete(&model.Ability{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("删除失败"))
		return
	}
	if err := channelssvc.SyncAbilityCache(); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("路由缓存刷新失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("删除成功"))
}
