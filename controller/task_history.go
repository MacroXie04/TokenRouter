package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
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

func taskHistoryFilterFromQuery(c *gin.Context, includeChannel bool) service.TaskHistoryFilter {
	filter := service.TaskHistoryFilter{
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

func midjourneyHistoryFilterFromQuery(c *gin.Context, includeChannel bool) service.MidjourneyHistoryFilter {
	filter := service.MidjourneyHistoryFilter{
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
		_ = common.UnmarshalJsonStr(value, &properties)
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

func taskResultURL(task model.Task) string {
	var privateData struct {
		ResultURL string `json:"result_url"`
	}
	if common.UnmarshalJsonStr(task.PrivateData, &privateData) == nil && privateData.ResultURL != "" {
		return privateData.ResultURL
	}
	return task.FailReason
}

func taskHistoryDTOs(tasks []model.Task, usernames map[int]string) []taskHistoryDTO {
	items := make([]taskHistoryDTO, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, taskHistoryDTO{
			ID: task.ID, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
			TaskID: task.TaskID, Platform: task.Platform, UserID: task.UserId,
			Group: task.Group, ChannelID: task.ChannelId, Quota: task.Quota,
			Action: task.Action, Status: task.Status, FailReason: task.FailReason,
			ResultURL: taskResultURL(task), SubmitTime: task.SubmitTime,
			StartTime: task.StartTime, FinishTime: task.FinishTime,
			Progress: task.Progress, Properties: taskHistoryProperties(task.Properties),
			Username: usernames[task.UserId], Data: storedTaskJSON(task.Data, "null"),
		})
	}
	return items
}

func respondTaskHistory(c *gin.Context, userID *int) {
	page := getPageQuery(c)
	result, err := service.GetTaskHistory(
		c.Request.Context(), userID, (page.Page-1)*page.PageSize, page.PageSize,
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
	userID := common.GetUserId(c)
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
	page := getPageQuery(c)
	items, total, err := service.GetMidjourneyHistory(
		c.Request.Context(), userID, (page.Page-1)*page.PageSize, page.PageSize,
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
	userID := common.GetUserId(c)
	respondMidjourneyHistory(c, &userID)
}
