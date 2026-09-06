package public

import (
	"errors"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	catalogsvc "github.com/tokenrouter/tokenrouter/internal/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/dto"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net/http"
	"sort"
)

// GetModels returns the dashboard model registry.
func GetModels(c *gin.Context) {
	var models []model.Model
	if err := model.DB.Order("id desc").Find(&models).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询模型失败"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(models))
}

// GetUserModels returns models available to the authenticated user's group.
func GetUserModels(c *gin.Context) {
	user, err := userssvc.GetUserByID(requestctx.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusUnauthorized, dto.Fail("用户不存在"))
		return
	}
	userGroup := user.Group
	if userGroup == "" {
		userGroup = userssvc.GroupDefault
	}
	usableGroups := billingsvc.GetUserUsableGroups(userGroup)
	requestedGroup := c.Query("group")
	groupsToQuery := make([]string, 0, len(usableGroups))
	switch requestedGroup {
	case "":
		for group := range usableGroups {
			if billingsvc.IsUserSelectableGroup(userGroup, group) {
				groupsToQuery = append(groupsToQuery, group)
			}
		}
	case userssvc.GroupAuto:
		if _, allowed := usableGroups[userssvc.GroupAuto]; allowed {
			groupsToQuery = billingsvc.GetUserDefaultAutoGroups(userGroup)
		}
	default:
		if billingsvc.IsUserSelectableGroup(userGroup, requestedGroup) {
			groupsToQuery = append(groupsToQuery, requestedGroup)
		}
	}
	models := billingsvc.GetGroupsModels(groupsToQuery)
	models, err = billingsvc.FilterModelsByReferencePricing(
		models, userssvc.UserSettingsFromRaw(user.Setting).AcceptUnsetRatioModel,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询模型失败"))
		return
	}
	names := make([]string, 0, len(models))
	for m := range models {
		names = append(names, m)
	}
	sort.Strings(names)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": names})
}

// GetUserGroups returns the reference group-selection metadata. The public
// route exposes globally selectable groups; the authenticated self route also
// includes the user's own group.
func GetUserGroups(c *gin.Context) {
	userGroup := ""
	if userID := requestctx.GetUserId(c); userID > 0 {
		user, err := userssvc.GetUserByID(userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("查询用户分组失败"))
			return
		}
		userGroup = user.Group
		if userGroup == "" {
			userGroup = userssvc.GroupDefault
		}
	}
	usableGroups := billingsvc.GetUserUsableGroups(userGroup)
	groups := make(map[string]gin.H)
	for group := range billingsvc.ExportedGroupRatios() {
		if desc, allowed := usableGroups[group]; allowed {
			ratio, _ := billingsvc.EffectiveGroupRatio(userGroup, group)
			groups[group] = gin.H{"ratio": ratio, "desc": desc}
		}
	}
	if desc, allowed := usableGroups[userssvc.GroupAuto]; allowed {
		groups[userssvc.GroupAuto] = gin.H{"ratio": "自动", "desc": desc}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": groups})
}

// GetRatioConfig returns the model price registry and group ratios for pricing.
func GetRatioConfig(c *gin.Context) {
	if !setting.GetOptionBool(setting.ExposeRatioEnabledOption, false) {
		c.JSON(http.StatusForbidden, dto.Fail("倍率配置接口未启用"))
		return
	}
	data := catalogsvc.ExposedRatioData()
	// Keep the existing TokenRouter names as additive compatibility aliases
	// while exposing the reference fields used by ratio-sync clients.
	data["model_prices"] = billingsvc.ExportedModelPrices()
	data["group_ratios"] = billingsvc.ExportedGroupRatios()
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

// GetRankings returns model usage rankings.
func GetRankings(c *gin.Context) {
	period, hasPeriod := c.GetQuery("period")
	if hasPeriod {
		snapshot, err := catalogsvc.GetRankingsSnapshot(c.Request.Context(), period)
		if err != nil {
			if errors.Is(err, catalogsvc.ErrInvalidRankingPeriod) {
				c.JSON(http.StatusBadRequest, dto.Fail("排行榜周期无效"))
				return
			}
			c.JSON(http.StatusInternalServerError, dto.Fail("查询排行榜失败"))
			return
		}
		c.JSON(http.StatusOK, dto.Ok(snapshot))
		return
	}

	// Rankings aggregate usage from quota_data; a simple recent-usage ranking.
	type rank struct {
		ModelName string `json:"model_name"`
		Count     int64  `json:"count"`
		Quota     int64  `json:"quota"`
	}
	var rows []rank
	if err := model.DB.Model(&model.QuotaData{}).
		Select("model_name, SUM(count) as count, SUM(quota) as quota").
		Group("model_name").
		Order("count desc").
		Limit(50).
		Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询排行榜失败"))
		return
	}
	if rows == nil {
		rows = []rank{}
	}
	c.JSON(http.StatusOK, dto.Ok(rows))
}
