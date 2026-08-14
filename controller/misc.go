package controller

import (
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// GetModels returns the dashboard model registry.
func GetModels(c *gin.Context) {
	var models []model.Model
	model.DB.Order("id desc").Find(&models)
	c.JSON(http.StatusOK, dto.Ok(models))
}

// GetUserModels returns models available to the authenticated user's group.
func GetUserModels(c *gin.Context) {
	user, err := service.GetUserByID(common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusUnauthorized, dto.Fail("用户不存在"))
		return
	}
	group := user.Group
	if group == "" {
		group = service.GroupDefault
	}
	models := service.GetGroupModels(group)
	names := make([]string, 0, len(models))
	for m := range models {
		names = append(names, m)
	}
	sort.Strings(names)
	c.JSON(http.StatusOK, dto.Ok(names))
}

// GetUserGroups returns the distinct routing groups (public metadata).
func GetUserGroups(c *gin.Context) {
	var abilities []model.Ability
	model.DB.Distinct("group").Find(&abilities)
	groups := make([]string, 0, len(abilities))
	for _, a := range abilities {
		if a.Group != "" {
			groups = append(groups, a.Group)
		}
	}
	if len(groups) == 0 {
		groups = append(groups, service.GroupDefault)
	}
	c.JSON(http.StatusOK, dto.Ok(groups))
}

// GetRatioConfig returns the model price registry and group ratios for pricing.
func GetRatioConfig(c *gin.Context) {
	c.JSON(http.StatusOK, dto.Ok(gin.H{
		"model_prices": service.ExportedModelPrices(),
		"group_ratios": service.ExportedGroupRatios(),
	}))
}

// GetRankings returns model usage rankings.
func GetRankings(c *gin.Context) {
	// Rankings aggregate usage from quota_data; a simple recent-usage ranking.
	type rank struct {
		ModelName string `json:"model_name"`
		Count     int64  `json:"count"`
		Quota     int64  `json:"quota"`
	}
	var rows []rank
	model.DB.Model(&model.QuotaData{}).
		Select("model_name, SUM(count) as count, SUM(quota) as quota").
		Group("model_name").
		Order("count desc").
		Limit(50).
		Scan(&rows)
	if rows == nil {
		rows = []rank{}
	}
	c.JSON(http.StatusOK, dto.Ok(rows))
}
