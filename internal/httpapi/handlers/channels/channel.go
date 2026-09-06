package channels

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/dto"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/pagination"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	operationssvc "github.com/tokenrouter/tokenrouter/internal/operations"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"strings"
)

const (
	maxChannelUpdateBodyBytes int64 = 1 << 20
	maxChannelCreateBatch           = 1_000
)

type channelCreateEnvelope struct {
	Mode                      string          `json:"mode"`
	MultiKeyMode              string          `json:"multi_key_mode"`
	BatchAddSetKeyPrefix2Name bool            `json:"batch_add_set_key_prefix_2_name"`
	Channel                   *ChannelRequest `json:"channel"`
}

// GetChannels lists channels with pagination.
func GetChannels(c *gin.Context) {
	var p dto.Pagination
	_ = c.ShouldBindQuery(&p)
	p.Normalize()
	var channels []model.Channel
	var total int64
	if err := model.DB.Model(&model.Channel{}).Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询渠道失败"))
		return
	}
	if err := model.DB.Order("id desc").Limit(p.PageSize).Offset(p.Offset()).Find(&channels).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询渠道失败"))
		return
	}
	// Upstream keys are never returned by list/get; disclosure goes through
	// the step-up-guarded POST /api/channel/:id/key endpoint.
	for i := range channels {
		channelssvc.MaskChannelSensitiveFieldsForResponse(&channels[i])
		clearChannelInfo(&channels[i])
	}
	p.Total = total
	p.TotalPages = int((total + int64(p.PageSize) - 1) / int64(p.PageSize))
	c.JSON(http.StatusOK, dto.Ok(gin.H{"items": channels, "pagination": p}))
}

// GetChannel returns a single channel.
func GetChannel(c *gin.Context) {
	ch, err := channelssvc.GetChannelByID(textutil.Str2Int(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("渠道不存在"))
		return
	}
	if auth.Can(requestctx.GetUserId(c), requestctx.GetRole(c), auth.ChannelSensitiveWrite) {
		channelssvc.MaskChannelCredentialsForResponse(ch)
	} else {
		channelssvc.MaskChannelSensitiveFieldsForResponse(ch)
	}
	clearChannelInfo(ch)
	c.JSON(http.StatusOK, dto.Ok(ch))
}

// GetChannelKey discloses a channel's upstream key. The route is guarded by
// RootAuth + SecureVerificationRequired (2FA or passkey step-up).
func GetChannelKey(c *gin.Context) {
	ch, err := channelssvc.GetChannelByID(textutil.Str2Int(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("渠道不存在"))
		return
	}
	billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage,
		"channel.key_view id="+textutil.Int2Str(ch.Id)+" name="+ch.Name)
	c.JSON(http.StatusOK, dto.Ok(gin.H{"key": channelssvc.ChannelCredentialForDisclosure(ch)}))
}

// AddChannel creates a channel.
func AddChannel(c *gin.Context) {
	body, err := httpx.ReadAllLimited(c.Request.Body, maxChannelUpdateBodyBytes)
	if err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, dto.Fail("请求体过大"))
			return
		}
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	var requestObject map[string]any
	if err := jsonutil.Unmarshal(body, &requestObject); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
		return
	}
	mode := "single"
	multiKeyMode := ""
	batchFingerprintNames := false
	var req ChannelRequest
	if _, wrapped := requestObject["channel"]; wrapped {
		var envelope channelCreateEnvelope
		if err := jsonutil.Unmarshal(body, &envelope); err != nil || envelope.Channel == nil {
			c.JSON(http.StatusBadRequest, dto.Fail("参数错误: channel 不能为空"))
			return
		}
		mode = strings.TrimSpace(envelope.Mode)
		multiKeyMode = strings.TrimSpace(envelope.MultiKeyMode)
		batchFingerprintNames = envelope.BatchAddSetKeyPrefix2Name
		req = *envelope.Channel
	} else if err := jsonutil.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
		return
	}
	if err := channelssvc.ValidateChannelOtherSettingsForType(req.Settings, req.Type); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
		return
	}
	channel, err := channelFromRequest(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
		return
	}
	keys, err := channelCreateKeys(mode, channel)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	channels := make([]*model.Channel, 0, len(keys))
	for _, key := range keys {
		candidate := *channel
		candidate.Key = key
		if batchFingerprintNames && len(keys) > 1 {
			candidate.Name = fmt.Sprintf("%s %s", channel.Name, channelKeyFingerprint(key))
		}
		if mode == "multi_to_single" {
			candidate.Key = strings.Join(keys, "\n")
			if err := setMultiKeyCreateInfo(&candidate, len(keys), multiKeyMode); err != nil {
				c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
				return
			}
		}
		candidate.CreatedTime = wallclock.NowTimestamp()
		channels = append(channels, &candidate)
		if mode == "multi_to_single" {
			break
		}
	}
	var createErr error
	if len(channels) == 1 {
		createErr = channelssvc.CreateChannelWithAbilities(channels[0])
	} else {
		createErr = channelssvc.CreateChannelsWithAbilities(channels)
	}
	if err := createErr; err != nil {
		if errors.Is(err, channelssvc.ErrInvalidChannelInput) {
			c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
			return
		}
		c.JSON(http.StatusInternalServerError, dto.Fail("创建失败: "+err.Error()))
		return
	}
	// Creation proves that the submitted key was accepted; it must not become
	// an alternate secret-disclosure endpoint. Reading it back requires the
	// root-only, step-up-guarded channel-key route.
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func channelFromRequest(req ChannelRequest) (*model.Channel, error) {
	channelInfo, err := channelJSONText(req.ChannelInfo)
	if err != nil {
		return nil, fmt.Errorf("channel_info 必须是 JSON 对象: %w", err)
	}
	weight := uint(channelcatalog.DefaultChannelWeight)
	if req.Weight != nil {
		weight = *req.Weight
	}
	status := channelcatalog.ChannelStatusEnabled
	if req.Status != nil {
		if *req.Status != channelcatalog.ChannelStatusEnabled &&
			*req.Status != channelcatalog.ChannelStatusAutoDisabled &&
			*req.Status != channelcatalog.ChannelStatusManuallyDisabled {
			return nil, errors.New("status 超出范围")
		}
		status = *req.Status
	}
	autoBan := 1
	if req.AutoBan != nil {
		autoBan = *req.AutoBan
	}
	return &model.Channel{
		Type: req.Type, Key: req.Key, OpenAIOrganization: req.OpenAIOrganization,
		TestModel: req.TestModel, Status: status, Name: req.Name, Weight: &weight,
		BaseURL: req.BaseURL, Other: req.Other, Models: req.Models, Group: req.Group,
		ModelMapping: req.ModelMapping, StatusCodeMapping: req.StatusCodeMapping,
		Priority: req.Priority, AutoBan: &autoBan, OtherInfo: req.OtherInfo, Tag: req.Tag,
		Setting: req.Setting, ParamOverride: req.ParamOverride, HeaderOverride: req.HeaderOverride,
		Remark: req.Remark, ChannelInfo: channelInfo, OtherSettings: req.Settings,
	}, nil
}

func channelJSONText(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text), nil
	}
	encoded, err := jsonutil.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func channelCreateKeys(mode string, channel *model.Channel) ([]string, error) {
	if channel == nil {
		return nil, errors.New("channel 不能为空")
	}
	if mode != "single" && mode != "batch" && mode != "multi_to_single" {
		return nil, errors.New("不支持的添加模式")
	}
	if mode == "single" {
		return []string{channel.Key}, nil
	}
	keys := make([]string, 0)
	if channel.Type == int(channelcatalog.ChannelTypeVertexAi) && strings.HasPrefix(strings.TrimSpace(channel.Key), "[") {
		var vertexKeys []any
		if err := jsonutil.UnmarshalJsonStr(channel.Key, &vertexKeys); err != nil {
			return nil, errors.New("Vertex AI 批量密钥必须是 JSON 数组")
		}
		for _, value := range vertexKeys {
			var key string
			if text, ok := value.(string); ok {
				key = strings.TrimSpace(text)
			} else if encoded, err := jsonutil.Marshal(value); err == nil {
				key = string(encoded)
			}
			if key != "" {
				keys = append(keys, key)
			}
		}
	} else {
		for _, value := range strings.Split(channel.Key, "\n") {
			if key := strings.TrimSpace(value); key != "" {
				keys = append(keys, key)
			}
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("密钥不能为空")
	}
	if len(keys) > maxChannelCreateBatch {
		return nil, errors.New("批量渠道数量超过限制")
	}
	return keys, nil
}

func setMultiKeyCreateInfo(channel *model.Channel, size int, mode string) error {
	if mode == "" {
		mode = "random"
	}
	if mode != "random" && mode != "polling" {
		return errors.New("不支持的多密钥策略")
	}
	info := map[string]any{}
	if channel.ChannelInfo != "" {
		if err := jsonutil.UnmarshalJsonStr(channel.ChannelInfo, &info); err != nil || info == nil {
			return errors.New("channel_info 必须是 JSON 对象")
		}
	}
	info["is_multi_key"] = true
	info["multi_key_size"] = size
	info["multi_key_status_list"] = map[string]int{}
	info["multi_key_polling_index"] = 0
	info["multi_key_mode"] = mode
	delete(info, "multi_key_disabled_reason")
	delete(info, "multi_key_disabled_time")
	encoded, err := jsonutil.Marshal(info)
	if err != nil {
		return errors.New("channel_info 无法编码")
	}
	channel.ChannelInfo = string(encoded)
	return nil
}

func channelKeyFingerprint(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:4])
}

// UpdateChannel updates a channel (reference contract: id comes from the
// body, sensitive fields require ChannelSensitiveWrite via the fail-closed
// classifier, unknown request fields are treated as sensitive).
func UpdateChannel(c *gin.Context) {
	body, err := httpx.ReadAllLimited(c.Request.Body, maxChannelUpdateBodyBytes)
	if err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, dto.Fail("请求体过大"))
			return
		}
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	var req dtoChannelUpdate
	if err := jsonutil.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
		return
	}
	requestData := map[string]any{}
	_ = jsonutil.Unmarshal(body, &requestData)
	if req.Id <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: id 不能为空"))
		return
	}
	var origin model.Channel
	if err := model.DB.First(&origin, "id = ?", req.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "渠道不存在"})
		} else {
			c.JSON(http.StatusInternalServerError, dto.Fail("读取渠道失败: "+err.Error()))
		}
		return
	}
	if channelHasSensitiveChanges(&req, &origin, requestData) &&
		!auth.Can(requestctx.GetUserId(c), requestctx.GetRole(c), auth.ChannelSensitiveWrite) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "无权限"})
		return
	}
	_, settingsSupplied := requestData["settings"]
	_, typeSupplied := requestData["type"]
	if settingsSupplied || typeSupplied {
		finalSettings := origin.OtherSettings
		if settingsSupplied {
			finalSettings = req.Settings
		}
		finalType := origin.Type
		if typeSupplied {
			finalType = req.Type
		}
		if err := channelssvc.ValidateChannelOtherSettingsForType(finalSettings, finalType); err != nil {
			c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
			return
		}
	}
	updates := map[string]any{}
	stringFields := map[string]string{
		"name":                 req.Name,
		"base_url":             req.BaseURL,
		"open_ai_organization": req.OpenAIOrganization,
		"test_model":           req.TestModel,
		"other":                req.Other,
		"models":               req.Models,
		"group":                req.Group,
		"model_mapping":        req.ModelMapping,
		"status_code_mapping":  req.StatusCodeMapping,
		"other_info":           req.OtherInfo,
		"tag":                  req.Tag,
		"remark":               req.Remark,
		"setting":              req.Setting,
		"param_override":       req.ParamOverride,
		"header_override":      req.HeaderOverride,
		"settings":             req.Settings,
	}
	for field, value := range stringFields {
		requestField := field
		if field == "open_ai_organization" {
			requestField = "openai_organization"
		}
		if _, ok := requestData[requestField]; ok {
			updates[field] = value
		}
	}
	if _, ok := requestData["type"]; ok {
		updates["type"] = req.Type
	}
	// An empty key is the dashboard's masked/unchanged sentinel. Persisting it
	// would let a ChannelWrite-only operator erase a credential even though the
	// sensitive-change classifier correctly treats the sentinel as a no-op.
	if _, ok := requestData["key"]; ok && req.Key != "" {
		updates["key"] = req.Key
	}
	if req.Weight != nil {
		updates["weight"] = *req.Weight
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
	}
	if req.AutoBan != nil {
		updates["auto_ban"] = *req.AutoBan
	}
	if _, ok := requestData["channel_info"]; ok {
		channelInfo, err := channelJSONText(req.ChannelInfo)
		if err != nil {
			c.JSON(http.StatusBadRequest, dto.Fail("参数错误: channel_info 必须是 JSON 对象"))
			return
		}
		updates["channel_info"] = channelInfo
	}
	if _, ok := requestData["multi_key_mode"]; ok {
		if req.MultiKeyMode != "random" && req.MultiKeyMode != "polling" {
			c.JSON(http.StatusBadRequest, dto.Fail("参数错误: 不支持的多密钥策略"))
			return
		}
		rawInfo := origin.ChannelInfo
		if supplied, ok := updates["channel_info"].(string); ok {
			rawInfo = supplied
		}
		info := map[string]any{}
		if err := jsonutil.UnmarshalJsonStr(rawInfo, &info); err != nil || info == nil {
			c.JSON(http.StatusBadRequest, dto.Fail("参数错误: channel_info 必须是 JSON 对象"))
			return
		}
		info["multi_key_mode"] = req.MultiKeyMode
		encoded, err := jsonutil.Marshal(info)
		if err != nil {
			c.JSON(http.StatusBadRequest, dto.Fail("参数错误: channel_info 无法编码"))
			return
		}
		updates["channel_info"] = string(encoded)
	}
	if err := channelssvc.UpdateChannelWithAbilities(req.Id, updates); err != nil {
		if errors.Is(err, channelssvc.ErrInvalidChannelInput) {
			c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
			return
		}
		c.JSON(http.StatusInternalServerError, dto.Fail("更新失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("更新成功"))
}

// DeleteChannel deletes a channel.
func DeleteChannel(c *gin.Context) {
	id := textutil.Str2Int(c.Param("id"))
	if id <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := channelssvc.DeleteChannelWithAbilities(id); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("删除失败: "+err.Error()))
		return
	}
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
	channel, err := channelssvc.GetChannelByID(channelId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	testModel := c.Query("model")
	success, latencyMs, errMsg := channelssvc.TestChannelHealthDetailed(channelId, testModel)
	if !success {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": errMsg, "time": 0.0})
		return
	}
	if err := model.DB.Model(channel).Update("response_time", latencyMs).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "保存渠道测试结果失败: " + err.Error(), "time": 0.0})
		return
	}
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
	task, created, err := operationssvc.EnqueueSystemTask(model.SystemTaskTypeChannelTest, map[string]any{
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
		return channelcatalog.ChannelStatusEnabled
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
	if err := jsonutil.UnmarshalJsonStr(channel.ChannelInfo, &info); err != nil {
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
	if cleaned, err := jsonutil.Marshal(info); err == nil {
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
	sortOptions := channelssvc.NewChannelSortOptions(c.Query("sort_by"), c.Query("sort_order"), idSort)
	enableTagMode, _ := strconv.ParseBool(c.Query("tag_mode"))
	includeKey := auth.Can(requestctx.GetUserId(c), requestctx.GetRole(c), auth.ChannelSensitiveWrite)

	channelData := make([]*model.Channel, 0)
	if enableTagMode {
		tags, err := channelssvc.SearchChannelTags(keyword, group, modelKeyword, idSort, includeKey)
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
		channels, err := channelssvc.SearchChannels(keyword, group, modelKeyword, sortOptions, includeKey)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		channelData = channels
	}

	// Status filter is applied in memory, after the search (reference order).
	if statusFilter == channelcatalog.ChannelStatusEnabled || statusFilter == 0 {
		filtered := make([]*model.Channel, 0, len(channelData))
		for _, ch := range channelData {
			if statusFilter == channelcatalog.ChannelStatusEnabled && ch.Status != channelcatalog.ChannelStatusEnabled {
				continue
			}
			if statusFilter == 0 && ch.Status == channelcatalog.ChannelStatusEnabled {
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

	pageInfo := pagination.FromContext(c)
	pageSize := pageInfo.PageSize
	total := len(channelData)
	startIdx := pageInfo.Offset()
	if startIdx > total {
		startIdx = total
	}
	endIdx := startIdx + pageSize
	if endIdx > total {
		endIdx = total
	}
	pagedData := channelData[startIdx:endIdx]
	for _, datum := range pagedData {
		channelssvc.MaskChannelSensitiveFieldsForResponse(datum)
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
	if err := model.DB.Order("id desc").Find(&models).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "查询模型失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": models})
}

// EnabledListModels returns the union of model names declared on enabled
// channels.
func EnabledListModels(c *gin.Context) {
	names, err := channelssvc.GetEnabledChannelModels()
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
			"retry_times": channelssvc.RetryTimes(),
		},
	})
}
