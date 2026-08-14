package controller

import (
	"io"
	"net/http"
	"strconv"
	"strings"

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
	// Upstream keys are never returned by list/get; disclosure goes through
	// the step-up-guarded POST /api/channel/:id/key endpoint.
	for i := range channels {
		channels[i].Key = ""
	}
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
	ch.Key = "" // masked; see GetChannelKey
	c.JSON(http.StatusOK, dto.Ok(ch))
}

// GetChannelKey discloses a channel's upstream key. The route is guarded by
// RootAuth + SecureVerificationRequired (2FA or passkey step-up).
func GetChannelKey(c *gin.Context) {
	ch, err := service.GetChannelByID(common.Str2Int(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("渠道不存在"))
		return
	}
	service.RecordSystemLog(common.GetUserId(c), service.LogTypeManage,
		"channel.key_view id="+common.Int2Str(ch.Id)+" name="+ch.Name)
	c.JSON(http.StatusOK, dto.Ok(gin.H{"key": ch.Key}))
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

// UpdateChannel updates a channel (reference contract: id comes from the
// body, sensitive fields require ChannelSensitiveWrite via the fail-closed
// classifier, unknown request fields are treated as sensitive).
func UpdateChannel(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	var req dtoChannelUpdate
	if err := common.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
		return
	}
	requestData := map[string]any{}
	_ = common.Unmarshal(body, &requestData)
	if req.Id <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: id 不能为空"))
		return
	}
	var origin model.Channel
	if err := model.DB.First(&origin, "id = ?", req.Id).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "渠道不存在"})
		return
	}
	if channelHasSensitiveChanges(&req, &origin, requestData) &&
		!service.Can(common.GetUserId(c), common.GetRole(c), service.ChannelSensitiveWrite) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "无权限"})
		return
	}
	updates := map[string]any{}
	stringFields := map[string]string{
		"name":                req.Name,
		"base_url":            req.BaseURL,
		"models":              req.Models,
		"group":               req.Group,
		"model_mapping":       req.ModelMapping,
		"status_code_mapping": req.StatusCodeMapping,
		"tag":                 req.Tag,
		"remark":              req.Remark,
		"setting":             req.Setting,
	}
	for field, value := range stringFields {
		if _, ok := requestData[field]; ok {
			updates[field] = value
		}
	}
	if _, ok := requestData["type"]; ok {
		updates["type"] = req.Type
	}
	if _, ok := requestData["key"]; ok {
		updates["key"] = req.Key
	}
	if req.Weight != nil {
		updates["weight"] = *req.Weight
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
	}
	if err := model.DB.Model(&model.Channel{}).Where("id = ?", req.Id).Updates(updates).Error; err != nil {
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

// TestChannel tests a single channel and returns the reference contract:
// {success:true,message:"",time:<seconds>} on success, or
// {success:false,message:<err>,time:0.0} on failure. The channel's
// response_time is updated after a successful test.
func TestChannel(c *gin.Context) {
	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channel, err := service.GetChannelByID(channelId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	testModel := c.Query("model")
	success, latencyMs, errMsg := service.TestChannelHealthDetailed(channelId, testModel)
	if !success {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": errMsg, "time": 0.0})
		return
	}
	_ = model.DB.Model(channel).Update("response_time", latencyMs).Error
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"time":    float64(latencyMs) / 1000.0,
	})
}

// TestAllChannels enqueues a channel_test system task that sweeps every
// testable channel. When a task is already pending or running the reference
// conflict response is returned with the existing task's data.
func TestAllChannels(c *gin.Context) {
	task, created, err := service.EnqueueSystemTask(model.SystemTaskTypeChannelTest, map[string]any{
		"mode":   "scheduled_all",
		"notify": true,
	})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if !created {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": "已有通道测试任务正在运行或等待中，不能启动本次手动任务",
			"data": gin.H{
				"task_id": task.TaskID,
				"status":  task.Status,
				"type":    task.Type,
			},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"task_id": task.TaskID,
			"status":  task.Status,
		},
	})
}

// parseStatusFilter maps the reference status filter values: enabled/1 -> 1,
// disabled/0 -> 0, anything else -> -1 (no filter).
func parseStatusFilter(statusParam string) int {
	switch strings.ToLower(statusParam) {
	case "enabled", "1":
		return constant.ChannelStatusEnabled
	case "disabled", "0":
		return 0
	default:
		return -1
	}
}

// clearChannelInfo strips multi-key internals from a channel before it is
// returned by search. TokenRouter stores channel_info as an opaque JSON
// string, so the reference's structured-field clearing is applied to that
// JSON: when is_multi_key is set, the per-key disabled reason/time maps are
// removed from the payload.
func clearChannelInfo(channel *model.Channel) {
	if channel.ChannelInfo == "" {
		return
	}
	var info map[string]any
	if err := common.UnmarshalJsonStr(channel.ChannelInfo, &info); err != nil {
		return
	}
	if isMultiKey, _ := info["is_multi_key"].(bool); !isMultiKey {
		return
	}
	changed := false
	for _, key := range []string{"multi_key_disabled_reason", "multi_key_disabled_time"} {
		if _, ok := info[key]; ok {
			delete(info, key)
			changed = true
		}
	}
	if !changed {
		return
	}
	if cleaned, err := common.Marshal(info); err == nil {
		channel.ChannelInfo = string(cleaned)
	}
}

// SearchChannels implements the reference channel search contract: keyword
// (id/name/exact key/base URL), model substring, group, status and type
// filters, optional tag grouping, the sort whitelist, and the
// {success,message,data:{items,total,type_counts}} response with keys omitted.
func SearchChannels(c *gin.Context) {
	keyword := c.Query("keyword")
	group := c.Query("group")
	modelKeyword := c.Query("model")
	statusParam := c.Query("status")
	statusFilter := parseStatusFilter(statusParam)
	idSort, _ := strconv.ParseBool(c.Query("id_sort"))
	sortOptions := service.NewChannelSortOptions(c.Query("sort_by"), c.Query("sort_order"), idSort)
	enableTagMode, _ := strconv.ParseBool(c.Query("tag_mode"))

	channelData := make([]*model.Channel, 0)
	if enableTagMode {
		tags, err := service.SearchChannelTags(keyword, group, modelKeyword, idSort)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		for _, tag := range tags {
			var tagChannels []*model.Channel
			err := sortOptions.Apply(model.DB.Model(&model.Channel{}).
				Where("tag = ?", tag)).
				Omit("key").
				Find(&tagChannels).Error
			if err != nil {
				c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
				return
			}
			channelData = append(channelData, tagChannels...)
		}
	} else {
		channels, err := service.SearchChannels(keyword, group, modelKeyword, sortOptions)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		channelData = channels
	}

	// Status filter is applied in memory, after the search (reference order).
	if statusFilter == constant.ChannelStatusEnabled || statusFilter == 0 {
		filtered := make([]*model.Channel, 0, len(channelData))
		for _, ch := range channelData {
			if statusFilter == constant.ChannelStatusEnabled && ch.Status != constant.ChannelStatusEnabled {
				continue
			}
			if statusFilter == 0 && ch.Status == constant.ChannelStatusEnabled {
				continue
			}
			filtered = append(filtered, ch)
		}
		channelData = filtered
	}

	// Type counts are computed on the status-filtered set, before the type
	// filter (reference order).
	typeCounts := make(map[int64]int64)
	for _, channel := range channelData {
		typeCounts[int64(channel.Type)]++
	}

	typeFilter := -1
	if typeParam := c.Query("type"); typeParam != "" {
		if tp, err := strconv.Atoi(typeParam); err == nil {
			typeFilter = tp
		}
	}
	if typeFilter >= 0 {
		filtered := make([]*model.Channel, 0, len(channelData))
		for _, ch := range channelData {
			if ch.Type == typeFilter {
				filtered = append(filtered, ch)
			}
		}
		channelData = filtered
	}

	page, _ := strconv.Atoi(c.DefaultQuery("p", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	total := len(channelData)
	startIdx := (page - 1) * pageSize
	if startIdx > total {
		startIdx = total
	}
	endIdx := startIdx + pageSize
	if endIdx > total {
		endIdx = total
	}
	pagedData := channelData[startIdx:endIdx]
	for _, datum := range pagedData {
		clearChannelInfo(datum)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"items":       pagedData,
			"total":       total,
			"type_counts": typeCounts,
		},
	})
}

// ChannelListModels returns the model catalog for channel administration.
func ChannelListModels(c *gin.Context) {
	var models []model.Model
	model.DB.Order("id desc").Find(&models)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": models})
}

// EnabledListModels returns the union of model names declared on enabled
// channels.
func EnabledListModels(c *gin.Context) {
	names, err := service.GetEnabledChannelModels()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": names})
}

// GetChannelOps returns channel-related operation settings.
func GetChannelOps(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"retry_times": service.RetryTimes(),
		},
	})
}
