package relay

import (
	"github.com/gin-gonic/gin"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/dto"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/pagination"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	operationssvc "github.com/tokenrouter/tokenrouter/internal/operations"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type taskHistoryPropertiesDTO struct {
	Input             string `json:"input"`
	UpstreamModelName string `json:"upstream_model_name,omitempty"`
	OriginModelName   string `json:"origin_model_name,omitempty"`
}

type taskHistoryDTO struct {
	ID         int64                    `json:"id"`
	CreatedAt  int64                    `json:"created_at"`
	UpdatedAt  int64                    `json:"updated_at"`
	TaskID     string                   `json:"task_id"`
	Platform   string                   `json:"platform"`
	UserID     int                      `json:"user_id"`
	Group      string                   `json:"group"`
	ChannelID  int                      `json:"channel_id"`
	Quota      int                      `json:"quota"`
	Action     string                   `json:"action"`
	Status     string                   `json:"status"`
	FailReason string                   `json:"fail_reason"`
	ResultURL  string                   `json:"result_url,omitempty"`
	SubmitTime int64                    `json:"submit_time"`
	StartTime  int64                    `json:"start_time"`
	FinishTime int64                    `json:"finish_time"`
	Progress   string                   `json:"progress"`
	Properties taskHistoryPropertiesDTO `json:"properties"`
	Username   string                   `json:"username,omitempty"`
	Data       model.JSONValue          `json:"data"`
}

func parseHistoryTimestamp(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func taskHistoryFilterFromQuery(c *gin.Context, includeChannel bool) operationssvc.TaskHistoryFilter {
	filter := operationssvc.TaskHistoryFilter{
		Platform:       c.Query("platform"),
		TaskID:         c.Query("task_id"),
		Status:         c.Query("status"),
		Action:         c.Query("action"),
		StartTimestamp: parseHistoryTimestamp(c.Query("start_timestamp")),
		EndTimestamp:   parseHistoryTimestamp(c.Query("end_timestamp")),
	}
	if includeChannel {
		filter.ChannelID = c.Query("channel_id")
	}
	return filter
}

func midjourneyHistoryFilterFromQuery(c *gin.Context, includeChannel bool) operationssvc.MidjourneyHistoryFilter {
	filter := operationssvc.MidjourneyHistoryFilter{
		MJID:           c.Query("mj_id"),
		StartTimestamp: parseHistoryTimestamp(c.Query("start_timestamp")),
		EndTimestamp:   parseHistoryTimestamp(c.Query("end_timestamp")),
	}
	if includeChannel {
		filter.ChannelID = c.Query("channel_id")
	}
	return filter
}

func taskHistoryProperties(value string) taskHistoryPropertiesDTO {
	var properties taskHistoryPropertiesDTO
	if strings.TrimSpace(value) != "" {
		_ = jsonutil.UnmarshalJsonStr(value, &properties)
	}
	return properties
}

func storedTaskJSON(value, emptyValue string) model.JSONValue {
	if strings.TrimSpace(value) == "" {
		return model.JSONValue(emptyValue)
	}
	var decoded model.JSONValue
	if err := decoded.Scan(value); err != nil {
		return model.JSONValue(emptyValue)
	}
	return decoded
}

func isJimengTaskPlatform(platform string) bool {
	jimengPlatform := strconv.Itoa(int(channelcatalog.ChannelTypeJimeng))
	return platform == jimengPlatform || platform == "47"
}

func isHailuoTaskPlatform(platform string) bool {
	return platform == strconv.Itoa(int(channelcatalog.ChannelTypeMiniMax))
}

func isAliWanTaskPlatform(platform string) bool {
	return model.IsAliWanTaskOperationPlatform(platform)
}

func isVeoTaskPlatform(platform string) bool {
	return model.IsVeoTaskOperationPlatform(platform)
}

type hailuoTaskHistoryData struct {
	State        string `json:"state"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	ResultURL    string `json:"result_url,omitempty"`
	VideoWidth   int    `json:"video_width,omitempty"`
	VideoHeight  int    `json:"video_height,omitempty"`
}

type aliWanTaskHistoryData struct {
	State        string `json:"state"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	ResultURL    string `json:"result_url,omitempty"`
}

type veoTaskHistoryData struct {
	State          string `json:"state"`
	ErrorCode      string `json:"error_code,omitempty"`
	ErrorMessage   string `json:"error_message,omitempty"`
	ResultURL      string `json:"result_url,omitempty"`
	HasInlineVideo bool   `json:"has_inline_video,omitempty"`
	InlineMIMEType string `json:"inline_mime_type,omitempty"`
	InlineBytes    int64  `json:"inline_bytes,omitempty"`
}

func safeAliWanTaskHistoryData(raw string) model.JSONValue {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" || len(raw) > 1<<20 {
		return model.JSONValue("null")
	}
	var data aliWanTaskHistoryData
	if jsonutil.UnmarshalJsonStr(raw, &data) != nil ||
		!validAliWanHistoryState(data.State) || !safeHailuoHistoryText(data.ErrorCode, 4<<10) ||
		!safeHailuoHistoryText(data.ErrorMessage, 4<<10) || !safeHailuoHistoryURL(data.ResultURL) {
		return model.JSONValue("null")
	}
	encoded, err := jsonutil.Marshal(data)
	if err != nil || len(encoded) > 1<<20 {
		return model.JSONValue("null")
	}
	return model.JSONValue(encoded)
}

func validAliWanHistoryState(state string) bool {
	switch state {
	case "submitted", "processing", "succeeded", "failed":
		return true
	default:
		return false
	}
}

func safeVeoTaskHistoryData(raw string) model.JSONValue {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" || len(raw) > 1<<20 {
		return model.JSONValue("null")
	}
	var data veoTaskHistoryData
	if jsonutil.UnmarshalJsonStr(raw, &data) != nil {
		return model.JSONValue("null")
	}
	outputs := 0
	if data.ResultURL != "" {
		outputs++
	}
	if data.HasInlineVideo {
		outputs++
	}
	if !validVeoTaskHistoryState(data.State) || !safeHailuoHistoryText(data.ErrorCode, 4<<10) ||
		!safeHailuoHistoryText(data.ErrorMessage, 4<<10) || !safeHailuoHistoryURL(data.ResultURL) ||
		(data.State == "processing" && outputs != 0) ||
		(data.State == "succeeded" && outputs != 1) ||
		(data.State == "failed" && outputs != 0) ||
		(data.HasInlineVideo && (data.ResultURL != "" || data.InlineMIMEType != "video/mp4" ||
			data.InlineBytes <= 0 || data.InlineBytes > 48<<20)) ||
		(!data.HasInlineVideo && (data.InlineMIMEType != "" || data.InlineBytes != 0)) {
		return model.JSONValue("null")
	}
	encoded, err := jsonutil.Marshal(data)
	if err != nil || len(encoded) > 1<<20 {
		return model.JSONValue("null")
	}
	return model.JSONValue(encoded)
}

func validVeoTaskHistoryState(state string) bool {
	return state == "processing" || state == "succeeded" || state == "failed"
}

func safeHailuoTaskHistoryData(raw string) model.JSONValue {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" || len(raw) > 1<<20 {
		return model.JSONValue("null")
	}
	var data hailuoTaskHistoryData
	if jsonutil.UnmarshalJsonStr(raw, &data) != nil || data.VideoWidth < 0 || data.VideoHeight < 0 ||
		!safeHailuoHistoryText(data.State, 64) || !safeHailuoHistoryText(data.ErrorCode, 4<<10) ||
		!safeHailuoHistoryText(data.ErrorMessage, 4<<10) || !safeHailuoHistoryURL(data.ResultURL) {
		return model.JSONValue("null")
	}
	encoded, err := jsonutil.Marshal(data)
	if err != nil || len(encoded) > 1<<20 {
		return model.JSONValue("null")
	}
	return model.JSONValue(encoded)
}

func safeHailuoHistoryText(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == 0 || (unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t') {
			return false
		}
	}
	return true
}

func safeHailuoHistoryURL(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 8<<10 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\\\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && parsed.User == nil && parsed.Fragment == "" &&
		(parsed.Scheme == "http" || parsed.Scheme == "https")
}

func storedTaskHistoryData(task model.Task) model.JSONValue {
	if isJimengTaskPlatform(task.Platform) {
		// Older Jimeng rows may contain the provider's complete response body,
		// including opaque IDs or echoed credentials. History exposes only the
		// gateway-owned state; the validated result URL has its own typed field.
		encoded, err := jsonutil.Marshal(map[string]string{"status": task.Status})
		if err != nil {
			return model.JSONValue("null")
		}
		return model.JSONValue(encoded)
	}
	if isHailuoTaskPlatform(task.Platform) {
		return safeHailuoTaskHistoryData(task.Data)
	}
	if isAliWanTaskPlatform(task.Platform) {
		return safeAliWanTaskHistoryData(task.Data)
	}
	if isVeoTaskPlatform(task.Platform) {
		return safeVeoTaskHistoryData(task.Data)
	}
	return storedTaskJSON(task.Data, "null")
}

func taskResultURL(task model.Task) string {
	var privateData struct {
		ResultURL string `json:"result_url"`
	}
	// Modern Jimeng private data is deliberately prefixed so pre-upgrade relay
	// nodes fail closed instead of stripping durable-reservation metadata.
	privateValue := strings.TrimPrefix(strings.TrimSpace(task.PrivateData), "jimeng-v2:")
	if jsonutil.UnmarshalJsonStr(privateValue, &privateData) == nil && privateData.ResultURL != "" {
		return privateData.ResultURL
	}
	if isJimengTaskPlatform(task.Platform) {
		// Legacy Jimeng stored provider-controlled response text in FailReason.
		// It is neither a trusted URL nor safe history output.
		return ""
	}
	if isHailuoTaskPlatform(task.Platform) {
		var data hailuoTaskHistoryData
		if jsonutil.UnmarshalJsonStr(task.Data, &data) == nil && safeHailuoHistoryURL(data.ResultURL) {
			return data.ResultURL
		}
		return ""
	}
	if isAliWanTaskPlatform(task.Platform) {
		if string(safeAliWanTaskHistoryData(task.Data)) == "null" {
			return ""
		}
		var data aliWanTaskHistoryData
		if jsonutil.UnmarshalJsonStr(task.Data, &data) == nil && safeHailuoHistoryURL(data.ResultURL) {
			return data.ResultURL
		}
		return ""
	}
	if isVeoTaskPlatform(task.Platform) {
		if string(safeVeoTaskHistoryData(task.Data)) == "null" {
			return ""
		}
		var data veoTaskHistoryData
		if jsonutil.UnmarshalJsonStr(task.Data, &data) == nil && safeHailuoHistoryURL(data.ResultURL) {
			return data.ResultURL
		}
		return ""
	}
	return task.FailReason
}

func taskHistoryDTOs(tasks []model.Task, usernames map[int]string) []taskHistoryDTO {
	items := make([]taskHistoryDTO, 0, len(tasks))
	for _, task := range tasks {
		failReason := task.FailReason
		if isJimengTaskPlatform(task.Platform) && strings.TrimSpace(failReason) != "" {
			failReason = "Jimeng task failed"
		}
		if isHailuoTaskPlatform(task.Platform) && !safeHailuoHistoryText(failReason, 4<<10) {
			failReason = "Hailuo task failed"
		}
		if isAliWanTaskPlatform(task.Platform) && !safeHailuoHistoryText(failReason, 4<<10) {
			failReason = "Alibaba Wan task failed"
		}
		if isVeoTaskPlatform(task.Platform) && !safeHailuoHistoryText(failReason, 4<<10) {
			failReason = "Veo task failed"
		}
		items = append(items, taskHistoryDTO{
			ID: task.ID, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
			TaskID: task.TaskID, Platform: task.Platform, UserID: task.UserId,
			Group: task.Group, ChannelID: task.ChannelId, Quota: task.Quota,
			Action: task.Action, Status: task.Status, FailReason: failReason,
			ResultURL: taskResultURL(task), SubmitTime: task.SubmitTime,
			StartTime: task.StartTime, FinishTime: task.FinishTime,
			Progress: task.Progress, Properties: taskHistoryProperties(task.Properties),
			Username: usernames[task.UserId], Data: storedTaskHistoryData(task),
		})
	}
	return items
}

func respondTaskHistory(c *gin.Context, userID *int) {
	page := pagination.FromContext(c)
	result, err := operationssvc.GetTaskHistory(
		c.Request.Context(), userID, page.Offset(), page.PageSize,
		taskHistoryFilterFromQuery(c, userID == nil),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("获取任务列表失败"))
		return
	}
	page.Total = int(result.Total)
	page.Items = taskHistoryDTOs(result.Items, result.Usernames)
	c.JSON(http.StatusOK, dto.Ok(page))
}

// GetAllTask returns the admin task-history collection.
func GetAllTask(c *gin.Context) {
	respondTaskHistory(c, nil)
}

// GetUserTask returns only the signed-in user's task history.
func GetUserTask(c *gin.Context) {
	userID := requestctx.GetUserId(c)
	respondTaskHistory(c, &userID)
}

func rewriteMidjourneyImageURLs(items []model.Midjourney) {
	if !setting.GetOptionBool(setting.MjForwardURLEnabledOption, true) {
		return
	}
	baseURL := strings.TrimRight(setting.GetOptionOrDefault(setting.ServerAddressOption, "http://localhost:3000"), "/")
	for i := range items {
		items[i].ImageUrl = baseURL + "/mj/image/" + items[i].MjId
	}
}

func respondMidjourneyHistory(c *gin.Context, userID *int) {
	page := pagination.FromContext(c)
	items, total, err := operationssvc.GetMidjourneyHistory(
		c.Request.Context(), userID, page.Offset(), page.PageSize,
		midjourneyHistoryFilterFromQuery(c, userID == nil),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("获取任务列表失败"))
		return
	}
	rewriteMidjourneyImageURLs(items)
	page.Total = int(total)
	page.Items = items
	c.JSON(http.StatusOK, dto.Ok(page))
}

// GetAllMidjourney returns the admin Midjourney task-history collection.
func GetAllMidjourney(c *gin.Context) {
	respondMidjourneyHistory(c, nil)
}

// GetUserMidjourney returns only the signed-in user's Midjourney history.
func GetUserMidjourney(c *gin.Context) {
	userID := requestctx.GetUserId(c)
	respondMidjourneyHistory(c, &userID)
}
