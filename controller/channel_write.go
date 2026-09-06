package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

type fetchModelsRequest struct {
	ChannelID      int     `json:"channel_id"`
	BaseURL        *string `json:"base_url"`
	Type           int     `json:"type"`
	Key            string  `json:"key"`
	AdvancedCustom *string `json:"advanced_custom"`
	HeaderOverride *string `json:"header_override"`
	Proxy          *string `json:"proxy"`
}

// channelAudit records an admin channel-management action in the system log.
func channelAudit(c *gin.Context, action string, payload any) {
	b, _ := common.Marshal(payload)
	service.RecordSystemLog(common.GetUserId(c), service.LogTypeManage,
		action+" "+string(b))
}

// isManageableChannelStatus mirrors the reference: only enabled and
// manually-disabled are valid manual targets.
func isManageableChannelStatus(status int) bool {
	return status == constant.ChannelStatusEnabled || status == constant.ChannelStatusManuallyDisabled
}

// UpdateChannelStatus moves a single channel to a manageable status and
// returns {success,message,data:<changed bool>}.
func UpdateChannelStatus(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "参数错误"})
		return
	}
	var req struct {
		Status int `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !isManageableChannelStatus(req.Status) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "参数错误"})
		return
	}
	changed, err := service.UpdateChannelStatusChecked(id, req.Status, "manual operation")
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channelAudit(c, "channel.status_update", map[string]any{"id": id, "status": req.Status, "changed": changed})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": changed})
}

// BatchUpdateChannelStatus moves a batch of channels and returns the changed
// count.
func BatchUpdateChannelStatus(c *gin.Context) {
	var req struct {
		Ids    []int `json:"ids"`
		Status int   `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Ids) == 0 || !isManageableChannelStatus(req.Status) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "参数错误"})
		return
	}
	changedCount, err := service.UpdateChannelStatusesChecked(req.Ids, req.Status, "manual batch operation")
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channelAudit(c, "channel.status_update_batch", map[string]any{
		"count": changedCount, "total": len(req.Ids), "status": req.Status,
	})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": changedCount})
}

// DeleteDisabledChannel hard-deletes every disabled channel and returns the
// count.
func DeleteDisabledChannel(c *gin.Context) {
	rows, err := service.DeleteDisabledChannels()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channelAudit(c, "channel.delete_disabled", map[string]any{"count": rows})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": rows})
}

// DisableTagChannels manually disables every channel with a tag.
func DisableTagChannels(c *gin.Context) {
	var req struct {
		Tag string `json:"tag"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Tag == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}
	if err := service.DisableChannelsByTag(req.Tag); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channelAudit(c, "channel.tag_disable", map[string]any{"tag": req.Tag})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

// EnableTagChannels enables every channel with a tag.
func EnableTagChannels(c *gin.Context) {
	var req struct {
		Tag string `json:"tag"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Tag == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}
	if err := service.EnableChannelsByTag(req.Tag); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channelAudit(c, "channel.tag_enable", map[string]any{"tag": req.Tag})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

// EditTagChannels edits every channel with a tag (tag rename, models, groups,
// priority, weight, param/header overrides with the reference JSON
// validation).
func EditTagChannels(c *gin.Context) {
	var req struct {
		Tag            string  `json:"tag"`
		NewTag         *string `json:"new_tag"`
		Priority       *int64  `json:"priority"`
		Weight         *uint   `json:"weight"`
		ModelMapping   *string `json:"model_mapping"`
		Models         *string `json:"models"`
		Groups         *string `json:"groups"`
		ParamOverride  *string `json:"param_override"`
		HeaderOverride *string `json:"header_override"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}
	if req.Tag == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "tag不能为空"})
		return
	}
	if (req.ParamOverride != nil || req.HeaderOverride != nil) &&
		!service.Can(common.GetUserId(c), common.GetRole(c), service.ChannelSensitiveWrite) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "无权限"})
		return
	}
	if req.ParamOverride != nil {
		trimmed := strings.TrimSpace(*req.ParamOverride)
		if trimmed != "" && !jsonValid(trimmed) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数覆盖必须是合法的 JSON 格式"})
			return
		}
		req.ParamOverride = &trimmed
	}
	if req.HeaderOverride != nil {
		trimmed := strings.TrimSpace(*req.HeaderOverride)
		if trimmed != "" && !jsonValid(trimmed) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "请求头覆盖必须是合法的 JSON 格式"})
			return
		}
		req.HeaderOverride = &trimmed
	}
	if err := service.EditChannelsByTag(req.Tag, req.NewTag, req.ModelMapping, req.Models, req.Groups,
		req.Priority, req.Weight, req.ParamOverride, req.HeaderOverride); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channelAudit(c, "channel.tag_edit", map[string]any{"tag": req.Tag})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func jsonValid(s string) bool {
	var v any
	return common.Unmarshal([]byte(s), &v) == nil
}

// DeleteChannelBatch hard-deletes the given channels and returns the count.
func DeleteChannelBatch(c *gin.Context) {
	var req struct {
		Ids []int   `json:"ids"`
		Tag *string `json:"tag"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Ids) == 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}
	deletedCount, err := service.BatchDeleteChannels(req.Ids)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channelAudit(c, "channel.delete_batch", map[string]any{"count": deletedCount})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": deletedCount})
}

// FixChannelsAbilities truncates the abilities table and rebuilds it from
// every channel.
func FixChannelsAbilities(c *gin.Context) {
	success, fails, err := service.FixAbilities()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"success": success,
			"fails":   fails,
		},
	})
}

// FetchUpstreamModels fetches the model list from a stored channel's
// upstream.
func FetchUpstreamModels(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channel, err := service.GetChannelByID(id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	ids, err := service.FetchUpstreamModelsForChannel(channel)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取模型列表失败: %s", err.Error()),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": ids})
}

// FetchModels previews the upstream model list for a not-yet-saved channel
// description (type/base_url/key).
func FetchModels(c *gin.Context) {
	var req fetchModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid request"})
		return
	}

	var channel *model.Channel
	if req.Type == int(constant.ChannelTypeAdvancedCustom) || req.ChannelID > 0 {
		var err error
		channel, err = buildAdvancedCustomModelPreviewChannel(req)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
	} else {
		baseURL := ""
		if req.BaseURL != nil {
			baseURL = strings.TrimSpace(*req.BaseURL)
		}
		if baseURL == "" && req.Type > 0 && req.Type < len(constant.ChannelBaseURLs) {
			baseURL = constant.ChannelBaseURLs[req.Type]
		}
		key := strings.TrimSpace(req.Key)
		if req.Type != int(constant.ChannelTypeCodex) {
			key = strings.TrimSpace(strings.Split(key, "\n")[0])
		}
		channel = &model.Channel{Type: req.Type, Key: key, BaseURL: baseURL}
	}

	ids, err := service.FetchUpstreamModelsForChannel(channel)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取模型列表失败: %s", err.Error()),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": ids})
}

func buildAdvancedCustomModelPreviewChannel(req fetchModelsRequest) (*model.Channel, error) {
	var channel *model.Channel
	if req.ChannelID > 0 {
		saved, err := service.GetChannelByID(req.ChannelID)
		if err != nil {
			return nil, err
		}
		if saved.Type != int(constant.ChannelTypeAdvancedCustom) {
			return nil, fmt.Errorf("channel %d is not an advanced custom channel", req.ChannelID)
		}
		channel = saved
	} else {
		if req.Type != int(constant.ChannelTypeAdvancedCustom) {
			return nil, fmt.Errorf("channel type must be advanced custom")
		}
		channel = &model.Channel{
			Type: req.Type,
			Key:  strings.TrimSpace(strings.Split(strings.TrimSpace(req.Key), "\n")[0]),
		}
	}
	if req.BaseURL != nil {
		channel.BaseURL = strings.TrimSpace(*req.BaseURL)
	}

	settings := make(map[string]json.RawMessage)
	if raw := strings.TrimSpace(channel.OtherSettings); raw != "" {
		if err := common.UnmarshalJsonStr(raw, &settings); err != nil || settings == nil {
			return nil, errors.New("channel settings must be a JSON object")
		}
	}
	if req.AdvancedCustom != nil {
		rawConfig := strings.TrimSpace(*req.AdvancedCustom)
		if rawConfig == "" {
			return nil, errors.New("advanced_custom is required")
		}
		var config map[string]json.RawMessage
		if err := common.UnmarshalJsonStr(rawConfig, &config); err != nil || config == nil {
			return nil, errors.New("advanced_custom must be a JSON object")
		}
		settings["advanced_custom"] = json.RawMessage(rawConfig)
	} else if req.ChannelID <= 0 {
		return nil, errors.New("advanced_custom is required")
	}
	encodedSettings, err := common.Marshal(settings)
	if err != nil {
		return nil, errors.New("encode advanced_custom settings")
	}
	channel.OtherSettings = string(encodedSettings)

	if req.HeaderOverride != nil {
		rawHeaderOverride := strings.TrimSpace(*req.HeaderOverride)
		if rawHeaderOverride != "" {
			var headers map[string]any
			if err := common.UnmarshalJsonStr(rawHeaderOverride, &headers); err != nil || headers == nil {
				return nil, errors.New("header_override must be a JSON object")
			}
		}
		channel.HeaderOverride = rawHeaderOverride
	}
	if req.Proxy != nil && strings.TrimSpace(*req.Proxy) != "" {
		return nil, errors.New("per-channel proxy is not supported for model discovery")
	}
	return channel, nil
}

// BatchSetChannelTag sets the tag of the given channels.
func BatchSetChannelTag(c *gin.Context) {
	var req struct {
		Ids []int   `json:"ids"`
		Tag *string `json:"tag"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Ids) == 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}
	if err := service.BatchSetChannelTag(req.Ids, req.Tag); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channelAudit(c, "channel.tag_batch_set", map[string]any{"count": len(req.Ids)})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": len(req.Ids)})
}

// GetTagModels returns the longest model list among channels with the tag.
func GetTagModels(c *gin.Context) {
	tag := c.Query("tag")
	if tag == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "tag不能为空"})
		return
	}
	channels, err := service.GetChannelsByTag(tag)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	var longestModels string
	maxLength := 0
	for _, channel := range channels {
		if channel.Models != "" {
			currentModels := strings.Split(channel.Models, ",")
			if len(currentModels) > maxLength {
				maxLength = len(currentModels)
				longestModels = channel.Models
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": longestModels})
}

// CopyChannel clones a channel (optional ?suffix and ?reset_balance query
// parameters).
func CopyChannel(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid id"})
		return
	}
	suffix := c.DefaultQuery("suffix", "_复制")
	resetBalance := true
	if rbStr := c.DefaultQuery("reset_balance", "true"); rbStr != "" {
		if v, err := strconv.ParseBool(rbStr); err == nil {
			resetBalance = v
		}
	}
	clone, err := service.CopyChannel(id, suffix, resetBalance)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channelAudit(c, "channel.copy", map[string]any{"sourceId": id, "id": clone.Id, "name": clone.Name})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"id": clone.Id}})
}

// ManageMultiKeys implements the reference multi-key management actions.
func ManageMultiKeys(c *gin.Context) {
	var req struct {
		ChannelId int    `json:"channel_id"`
		Action    string `json:"action"`
		KeyIndex  *int   `json:"key_index,omitempty"`
		Page      int    `json:"page,omitempty"`
		PageSize  int    `json:"page_size,omitempty"`
		Status    *int   `json:"status,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channel, err := service.GetChannelByID(req.ChannelId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "渠道不存在"})
		return
	}
	if !service.IsMultiKeyChannel(channel) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "该渠道不是多密钥模式"})
		return
	}
	if (req.Action == "delete_key" || req.Action == "delete_disabled_keys") &&
		!service.Can(common.GetUserId(c), common.GetRole(c), service.ChannelSensitiveWrite) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "无权限"})
		return
	}
	if req.Action != "get_key_status" {
		channelAudit(c, "channel.multi_key_manage", map[string]any{"action": req.Action, "id": channel.Id})
	}

	switch req.Action {
	case "get_key_status":
		resp, err := service.GetMultiKeyStatus(channel, req.Page, req.PageSize, req.Status)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": resp})
	case "disable_key", "enable_key":
		if req.KeyIndex == nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "未指定要操作的密钥索引"})
			return
		}
		status := 2
		verb := "禁用"
		if req.Action == "enable_key" {
			status = 1
			verb = "启用"
		}
		if err := service.SetMultiKeyStatus(channel, *req.KeyIndex, status); err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": fmt.Sprintf("密钥已%s", verb)})
	case "enable_all_keys", "disable_all_keys":
		status := 2
		verb := "禁用"
		if req.Action == "enable_all_keys" {
			status = 1
			verb = "启用"
		}
		changed, err := service.SetAllMultiKeyStatuses(channel, status)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		if changed == 0 {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": fmt.Sprintf("没有可%s的密钥", verb)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": fmt.Sprintf("已%s %d 个密钥", verb, changed)})
	case "delete_key":
		if req.KeyIndex == nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "未指定要删除的密钥索引"})
			return
		}
		if err := service.DeleteMultiKey(channel, *req.KeyIndex); err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "密钥已删除"})
	case "delete_disabled_keys":
		deleted, err := service.DeleteAutoDisabledMultiKeys(channel)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		if deleted == 0 {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "没有需要删除的自动禁用密钥"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": fmt.Sprintf("已删除 %d 个自动禁用的密钥", deleted),
			"data":    deleted,
		})
	default:
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "不支持的操作"})
	}
}

// UpdateChannelBalance refreshes one channel's upstream balance.
func UpdateChannelBalance(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channel, err := service.GetChannelByID(id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if service.IsMultiKeyChannel(channel) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "多密钥渠道不支持余额查询"})
		return
	}
	balance, err := service.FetchChannelBalanceContext(c.Request.Context(), channel)
	if err != nil {
		if errors.Is(err, service.ErrChannelBalanceUnsupported) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "尚未实现"})
			return
		}
		common.SysError("channel balance refresh failed: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "余额查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "balance": balance})
}

// UpdateAllChannelsBalance refreshes the balance of every enabled single-key
// channel; channels that run out of balance are disabled.
func UpdateAllChannelsBalance(c *gin.Context) {
	if err := service.UpdateAllChannelsBalancesContext(c.Request.Context()); err != nil {
		common.SysError("all-channel balance refresh failed: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "余额批量刷新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}
