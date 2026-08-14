package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

func setupTaskHistory(t *testing.T, role int) (http.Handler, func(method, path, body string) *httptest.ResponseRecorder, int) {
	t.Helper()
	handler, do, userID := setupChannelRead(t, role)
	require.NoError(t, model.DB.AutoMigrate(&model.Midjourney{}, &model.Task{}))
	return handler, do, userID
}

func taskHistoryItems(t *testing.T, rec *httptest.ResponseRecorder) ([]map[string]any, map[string]any) {
	t.Helper()
	body := decodeBody(t, rec)
	require.Equal(t, true, body["success"], "body: %s", rec.Body.String())
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "page missing: %s", rec.Body.String())
	rawItems, ok := data["items"].([]any)
	require.True(t, ok, "items missing: %s", rec.Body.String())
	items := make([]map[string]any, 0, len(rawItems))
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		require.True(t, ok)
		items = append(items, item)
	}
	return items, data
}

func TestTaskHistorySelfContract(t *testing.T) {
	handler, do, userID := setupTaskHistory(t, constant.RoleCommonUser)
	other := model.User{Username: "task-other", Password: "password", Role: constant.RoleCommonUser,
		Status: model.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&other).Error)

	ownTasks := []model.Task{
		{TaskID: "task-old", Platform: "suno", UserId: userID, Group: "default", ChannelId: 11,
			Quota: 100, Action: "song", Status: "SUCCESS", FailReason: "legacy-result",
			SubmitTime: 100, Progress: "100%", Properties: `{"input":"old"}`, Data: `{"audio":"old.mp3"}`},
		{TaskID: "task-middle", Platform: "video", UserId: userID, Group: "vip", ChannelId: 12,
			Quota: 200, Action: "video", Status: "IN_PROGRESS", SubmitTime: 200, Progress: "50%",
			Properties: "legacy-property", Data: ""},
		{TaskID: "task-new", Platform: "video", UserId: userID, Group: "vip", ChannelId: 13,
			Quota: 300, Action: "video", Status: "SUCCESS", SubmitTime: 300, Progress: "100%",
			Properties: `{"input":"new","origin_model_name":"vid-1"}`, Data: `[1,2]`,
			PrivateData: `{"key":"private-secret","result_url":"https://example.test/result.mp4"}`},
	}
	require.NoError(t, model.DB.Create(&ownTasks).Error)
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID: "task-other", Platform: "video", UserId: other.Id, ChannelId: 99,
		Action: "video", Status: "SUCCESS", SubmitTime: 400,
	}).Error)

	rec := do(http.MethodGet, "/api/task/self?p=1&page_size=2&channel_id=999", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	items, page := taskHistoryItems(t, rec)
	require.Len(t, items, 2)
	assert.Equal(t, float64(3), page["total"])
	assert.Equal(t, float64(1), page["page"])
	assert.Equal(t, float64(2), page["page_size"])
	assert.Equal(t, "task-new", items[0]["task_id"])
	assert.Equal(t, "task-middle", items[1]["task_id"])
	assert.Equal(t, float64(0), items[0]["channel_id"], "self contract suppresses channel id")
	assert.Equal(t, float64(userID), items[0]["user_id"])
	assert.Equal(t, "https://example.test/result.mp4", items[0]["result_url"])
	properties, ok := items[0]["properties"].(map[string]any)
	require.True(t, ok, "properties must be structural JSON")
	assert.Equal(t, "new", properties["input"])
	assert.Equal(t, []any{float64(1), float64(2)}, items[0]["data"])
	assert.NotContains(t, rec.Body.String(), "private-secret")
	assert.NotContains(t, rec.Body.String(), "private_data")
	assert.NotContains(t, items[0], "username")
	legacyProperties, ok := items[1]["properties"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "", legacyProperties["input"], "invalid legacy properties retain the typed object contract")
	assert.Nil(t, items[1]["data"], "blank legacy data is null")

	rec = do(http.MethodGet, "/api/task/self?platform=video&status=SUCCESS&action=video&start_timestamp=250&end_timestamp=350", "")
	items, page = taskHistoryItems(t, rec)
	require.Len(t, items, 1)
	assert.Equal(t, "task-new", items[0]["task_id"])
	assert.Equal(t, float64(1), page["total"])

	rec = do(http.MethodGet, "/api/task/self?task_id=task-old", "")
	items, _ = taskHistoryItems(t, rec)
	require.Len(t, items, 1)
	assert.Equal(t, "legacy-result", items[0]["result_url"])

	rec = do(http.MethodGet, "/api/task/self?platform=missing", "")
	items, page = taskHistoryItems(t, rec)
	assert.Empty(t, items)
	assert.Equal(t, float64(0), page["total"])

	rec = do(http.MethodGet, "/api/task/", "")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	request := httptest.NewRequest(http.MethodGet, "/api/task/self", nil)
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, request)
	assert.Equal(t, http.StatusUnauthorized, anonymous.Code)
}

func TestTaskHistoryAdminContract(t *testing.T) {
	_, do, adminID := setupTaskHistory(t, constant.RoleRootUser)
	other := model.User{Username: "task-admin-other", Password: "password", Role: constant.RoleCommonUser,
		Status: model.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&other).Error)
	tasks := []model.Task{
		{TaskID: "admin-old", Platform: "suno", UserId: adminID, ChannelId: 7, Action: "song", Status: "SUCCESS", SubmitTime: 100},
		{TaskID: "admin-new", Platform: "video", UserId: other.Id, ChannelId: 8, Action: "video", Status: "FAILURE", SubmitTime: 200},
	}
	require.NoError(t, model.DB.Create(&tasks).Error)

	rec := do(http.MethodGet, "/api/task/?channel_id=8&platform=video&task_id=admin-new&status=FAILURE&action=video&start_timestamp=150&end_timestamp=250", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	items, page := taskHistoryItems(t, rec)
	require.Len(t, items, 1)
	assert.Equal(t, float64(1), page["total"])
	assert.Equal(t, "admin-new", items[0]["task_id"])
	assert.Equal(t, float64(8), items[0]["channel_id"])
	assert.Equal(t, "task-admin-other", items[0]["username"])
	emptyProperties, ok := items[0]["properties"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "", emptyProperties["input"], "empty properties retain the reference DTO field")

	rec = do(http.MethodGet, "/api/task/?page_size=1&p=2", "")
	items, page = taskHistoryItems(t, rec)
	require.Len(t, items, 1)
	assert.Equal(t, "admin-old", items[0]["task_id"], "admin history is newest-first")
	assert.Equal(t, float64(2), page["total"])
}

func TestMidjourneyHistoryContracts(t *testing.T) {
	_, do, userID := setupTaskHistory(t, constant.RoleAdminUser)
	require.NoError(t, setting.UpdateOption(setting.ServerAddressOption, "https://router.example/"))
	require.NoError(t, setting.UpdateOption(setting.MjForwardURLEnabledOption, "true"))
	other := model.User{Username: "mj-other", Password: "password", Role: constant.RoleCommonUser,
		Status: model.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&other).Error)
	rows := []model.Midjourney{
		{UserId: userID, MjId: "mj-old", ChannelId: 21, Action: "IMAGINE", SubmitTime: 100,
			Status: "SUCCESS", ImageUrl: "old.png", Buttons: `[{"label":"U1"}]`, VideoUrls: `["old.mp4"]`},
		{UserId: userID, MjId: "mj-new", ChannelId: 22, Action: "IMAGINE", SubmitTime: 200,
			Status: "SUCCESS", ImageUrl: "new.png", Properties: `{"seed":42}`},
		{UserId: other.Id, MjId: "mj-other", ChannelId: 23, Action: "IMAGINE", SubmitTime: 300,
			Status: "SUCCESS", ImageUrl: "other.png"},
	}
	require.NoError(t, model.DB.Create(&rows).Error)

	rec := do(http.MethodGet, "/api/mj/self?channel_id=999&page_size=1&p=1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	items, page := taskHistoryItems(t, rec)
	require.Len(t, items, 1)
	assert.Equal(t, "mj-new", items[0]["mj_id"])
	assert.Equal(t, "https://router.example/mj/image/mj-new", items[0]["image_url"])
	assert.Equal(t, float64(2), page["total"])

	rec = do(http.MethodGet, "/api/mj/self?mj_id=mj-old&start_timestamp=50&end_timestamp=150", "")
	items, _ = taskHistoryItems(t, rec)
	require.Len(t, items, 1)
	assert.Equal(t, "mj-old", items[0]["mj_id"])
	assert.Equal(t, `[{"label":"U1"}]`, items[0]["buttons"], "Midjourney string fields preserve the reference shape")
	assert.Equal(t, `["old.mp4"]`, items[0]["video_urls"])

	require.NoError(t, setting.UpdateOption(setting.MjForwardURLEnabledOption, "false"))
	rec = do(http.MethodGet, "/api/mj/self?mj_id=mj-old", "")
	items, _ = taskHistoryItems(t, rec)
	require.Len(t, items, 1)
	assert.Equal(t, "old.png", items[0]["image_url"])
	require.NoError(t, setting.UpdateOption(setting.MjForwardURLEnabledOption, "true"))

	rec = do(http.MethodGet, "/api/mj/?channel_id=23&mj_id=mj-other&start_timestamp=250&end_timestamp=350", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	items, page = taskHistoryItems(t, rec)
	require.Len(t, items, 1)
	assert.Equal(t, "mj-other", items[0]["mj_id"])
	assert.Equal(t, float64(other.Id), items[0]["user_id"])
	assert.Equal(t, float64(1), page["total"])

	rec = do(http.MethodGet, "/api/mj/?mj_id=missing", "")
	items, page = taskHistoryItems(t, rec)
	assert.Empty(t, items)
	assert.Equal(t, float64(0), page["total"])
}

func TestTaskHistoryDatabaseErrors(t *testing.T) {
	_, do, _ := setupTaskHistory(t, constant.RoleRootUser)
	require.NoError(t, model.DB.Migrator().DropTable(&model.Task{}))
	rec := do(http.MethodGet, "/api/task/", "")
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])

	require.NoError(t, model.DB.Migrator().DropTable(&model.Midjourney{}))
	rec = do(http.MethodGet, "/api/mj/self", "")
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "获取任务列表失败", body["message"])
}

func TestTaskHistoryPageBounds(t *testing.T) {
	_, do, userID := setupTaskHistory(t, constant.RoleCommonUser)
	for i := 0; i < 105; i++ {
		require.NoError(t, model.DB.Create(&model.Task{
			TaskID: fmt.Sprintf("bounded-%03d", i), UserId: userID, SubmitTime: int64(i),
		}).Error)
	}
	rec := do(http.MethodGet, "/api/task/self?p=0&page_size=1000", "")
	items, page := taskHistoryItems(t, rec)
	assert.Len(t, items, 100)
	assert.Equal(t, float64(1), page["page"])
	assert.Equal(t, float64(100), page["page_size"])
	assert.Equal(t, float64(105), page["total"])

	rec = do(http.MethodGet, "/api/task/self?ps=2", "")
	items, page = taskHistoryItems(t, rec)
	assert.Len(t, items, 2)
	assert.Equal(t, float64(2), page["page_size"])
	rec = do(http.MethodGet, "/api/task/self?size=3", "")
	items, page = taskHistoryItems(t, rec)
	assert.Len(t, items, 3)
	assert.Equal(t, float64(3), page["page_size"])
}
