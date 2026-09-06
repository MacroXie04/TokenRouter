package controller_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

// setupChannelRead builds an isolated admin session with the channel-read
// surface migrated (catalog, system tasks, locks).
func setupChannelRead(t *testing.T, role int) (http.Handler, func(method, path, body string) *httptest.ResponseRecorder, int) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("CRITICAL_RATE_LIMIT", "1000000")
	t.Setenv("GLOBAL_API_RATE_LIMIT", "1000000")
	t.Cleanup(common.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()
	dsn := "file:" + filepath.Join(t.TempDir(), "channelread.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSession{},
		&model.Channel{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.AuthFlow{}, &model.Log{},
		&model.Option{}, &model.Model{}, &model.SystemTask{}, &model.SystemTaskLock{}, &model.Ability{},
		&model.Redemption{}, &model.AuthzRole{}, &model.CasbinRule{}, &model.TopUp{}, &model.QuotaData{},
		&model.CustomOAuthProvider{}, &model.UserOAuthBinding{}, &model.SystemInstance{}, &model.PerfMetric{},
		&model.PrefillGroup{}, &model.AuditLogOutbox{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, service.InitCasbin())
	require.NoError(t, service.InitPermissionAuthz())
	service.ResetQuotaDataCache()
	require.NoError(t, setting.UpdateOption(setting.QuotaPerUnitOption, "500000"))

	user := model.User{Username: "chreader", Password: "pw", Role: role, Status: model.UserStatusEnabled,
		Quota: 1000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	sid, access, refresh, err := service.CompleteLogin(&user, "127.0.0.1", "ua", "test")
	require.NoError(t, err)

	r := router.SetUpRouter()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: sid + "." + refresh})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	return r, do, user.Id
}

func createSearchChannel(t *testing.T, name string, typ, status int, group, models, tag string, priority int64) model.Channel {
	t.Helper()
	ch := model.Channel{
		Type: typ, Key: "sk-secret-" + name, Name: name, Status: status, Group: group,
		Models: models, Tag: tag, Priority: &priority,
	}
	require.NoError(t, model.DB.Create(&ch).Error)
	return ch
}

func searchItems(t *testing.T, rec *httptest.ResponseRecorder) ([]map[string]any, map[string]any) {
	t.Helper()
	body := decodeBody(t, rec)
	require.Equal(t, true, body["success"], "body: %s", rec.Body.String())
	assert.Equal(t, "", body["message"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "data missing: %s", rec.Body.String())
	items, ok := data["items"].([]any)
	require.True(t, ok, "items missing: %s", rec.Body.String())
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		require.True(t, ok)
		out = append(out, m)
	}
	return out, data
}

func TestChannelSearchContract(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	createSearchChannel(t, "alpha-gateway", 1, constant.ChannelStatusEnabled, "default", "gpt-4o,text-embedding-3-small", "fast", 10)
	beta := createSearchChannel(t, "beta-gateway", 3, constant.ChannelStatusEnabled, "vip", "claude-sonnet-4", "fast", 20)
	createSearchChannel(t, "gamma-gateway", 1, constant.ChannelStatusManuallyDisabled, "vip", "gpt-4o-mini", "slow", 5)

	// Full list with type counts.
	rec := do(http.MethodGet, "/api/channel/search", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	items, data := searchItems(t, rec)
	require.Len(t, items, 3)
	assert.Equal(t, float64(3), data["total"])
	typeCounts, ok := data["type_counts"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(2), typeCounts["1"])
	assert.Equal(t, float64(1), typeCounts["3"])
	// Keys must never appear in search results.
	assert.NotContains(t, rec.Body.String(), "sk-secret-")

	// Keyword matches name substring.
	rec = do(http.MethodGet, "/api/channel/search?keyword=alpha", "")
	items, data = searchItems(t, rec)
	require.Len(t, items, 1)
	assert.Equal(t, "alpha-gateway", items[0]["name"])
	assert.Equal(t, float64(1), data["total"])

	// Numeric keyword matches the id.
	rec = do(http.MethodGet, "/api/channel/search?keyword="+common.Int2Str(beta.Id), "")
	items, _ = searchItems(t, rec)
	require.Len(t, items, 1)
	assert.Equal(t, "beta-gateway", items[0]["name"])

	// Model substring filter.
	rec = do(http.MethodGet, "/api/channel/search?model=gpt-4o", "")
	items, _ = searchItems(t, rec)
	require.Len(t, items, 2)
	for _, it := range items {
		assert.NotEqual(t, "beta-gateway", it["name"])
	}

	// Group filter.
	rec = do(http.MethodGet, "/api/channel/search?group=vip", "")
	items, _ = searchItems(t, rec)
	require.Len(t, items, 2)
	for _, it := range items {
		assert.NotEqual(t, "alpha-gateway", it["name"])
	}

	// Status filters: enabled vs disabled.
	rec = do(http.MethodGet, "/api/channel/search?status=enabled", "")
	items, _ = searchItems(t, rec)
	require.Len(t, items, 2)
	rec = do(http.MethodGet, "/api/channel/search?status=0", "")
	items, _ = searchItems(t, rec)
	require.Len(t, items, 1)
	assert.Equal(t, "gamma-gateway", items[0]["name"])
	// status=1 is the enabled alias.
	rec = do(http.MethodGet, "/api/channel/search?status=1", "")
	items, _ = searchItems(t, rec)
	require.Len(t, items, 2)

	// Type filter.
	rec = do(http.MethodGet, "/api/channel/search?type=3", "")
	items, _ = searchItems(t, rec)
	require.Len(t, items, 1)
	assert.Equal(t, "beta-gateway", items[0]["name"])

	// Sort whitelist.
	rec = do(http.MethodGet, "/api/channel/search?sort_by=name&sort_order=asc", "")
	items, _ = searchItems(t, rec)
	require.Len(t, items, 3)
	assert.Equal(t, "alpha-gateway", items[0]["name"])
	// Unknown sort column falls back to the default priority-desc ordering.
	rec = do(http.MethodGet, "/api/channel/search?sort_by=bogus", "")
	items, _ = searchItems(t, rec)
	require.Len(t, items, 3)
	assert.Equal(t, "beta-gateway", items[0]["name"], "highest priority first")
	// id_sort orders by id desc.
	rec = do(http.MethodGet, "/api/channel/search?id_sort=true", "")
	items, _ = searchItems(t, rec)
	require.Len(t, items, 3)
	assert.Equal(t, "gamma-gateway", items[0]["name"], "highest id first")

	// Paging.
	rec = do(http.MethodGet, "/api/channel/search?page_size=1&p=2&sort_by=name&sort_order=asc", "")
	items, data = searchItems(t, rec)
	require.Len(t, items, 1)
	assert.Equal(t, float64(3), data["total"])

	// Tag mode groups results by the distinct tags of matching channels
	// (the keyword matches name/id/key/base_url, never the tag itself).
	rec = do(http.MethodGet, "/api/channel/search?tag_mode=true&keyword=gateway", "")
	items, _ = searchItems(t, rec)
	require.Len(t, items, 3)
	// Grouped by tag: the two "fast" channels first.
	assert.Equal(t, "fast", items[0]["tag"])
	assert.Equal(t, "fast", items[1]["tag"])
	assert.Equal(t, "slow", items[2]["tag"])
	// A keyword that matches one channel returns that channel's whole tag
	// group (the per-tag fetch applies group/status/type filters, not the
	// keyword — reference semantics).
	rec = do(http.MethodGet, "/api/channel/search?tag_mode=true&keyword=beta", "")
	items, _ = searchItems(t, rec)
	require.Len(t, items, 2)
	for _, it := range items {
		assert.Equal(t, "fast", it["tag"])
	}
}

func TestChannelSearchRequiresAdmin(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleCommonUser)
	rec := do(http.MethodGet, "/api/channel/search", "")
	assert.NotEqual(t, http.StatusOK, rec.Code)
}

func TestChannelReadResponseRedactsSensitiveFieldsByCapability(t *testing.T) {
	_, doAdmin, _ := setupChannelRead(t, constant.RoleAdminUser)
	priority := int64(1)
	weight := uint(1)
	channel := model.Channel{
		Name: "sensitive-wire", Type: int(constant.ChannelTypeOpenAI), Key: "sk-wire-secret",
		Status: constant.ChannelStatusEnabled, Models: "gpt-4o", Group: "default",
		Priority: &priority, Weight: &weight, BaseURL: "https://internal.example.test",
		OpenAIOrganization: "org-secret", Other: "provider-secret",
		Setting:       `{"balance_url":"https://balance.example.test"}`,
		OtherSettings: `{"advanced_custom":{"auth":"secret"}}`,
		ParamOverride: `{"prompt":"secret"}`, HeaderOverride: `{"Authorization":"secret"}`,
	}
	require.NoError(t, model.DB.Create(&channel).Error)

	rec := doAdmin(http.MethodGet, "/api/channel/"+common.Int2Str(channel.Id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeBody(t, rec)["data"].(map[string]any)
	for _, field := range []string{
		"key", "base_url", "openai_organization", "other", "setting", "settings", "param_override", "header_override",
	} {
		assert.Equal(t, "", data[field], field)
	}
	assert.NotContains(t, rec.Body.String(), "wire-secret")
	assert.NotContains(t, rec.Body.String(), "internal.example.test")

	for _, path := range []string{"/api/channel", "/api/channel/search?keyword=sensitive-wire"} {
		rec = doAdmin(http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.NotContains(t, rec.Body.String(), "wire-secret", path)
		assert.NotContains(t, rec.Body.String(), "internal.example.test", path)
		assert.NotContains(t, rec.Body.String(), "Authorization", path)
	}

	_, doRoot, _ := setupChannelRead(t, constant.RoleRootUser)
	rootChannel := channel
	rootChannel.Id = 0
	require.NoError(t, model.DB.Create(&rootChannel).Error)
	rec = doRoot(http.MethodGet, "/api/channel/"+common.Int2Str(rootChannel.Id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rootData := decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, "", rootData["key"], "even root detail uses the dedicated key-reveal endpoint")
	assert.Equal(t, "https://internal.example.test", rootData["base_url"])
	assert.Equal(t, `{"Authorization":"secret"}`, rootData["header_override"])
}

func TestChannelListModelsAndEnabledModels(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	require.NoError(t, model.DB.Create(&model.Model{ModelName: "gpt-4o"}).Error)
	require.NoError(t, model.DB.Create(&model.Model{ModelName: "claude-sonnet-4"}).Error)
	createSearchChannel(t, "en1", 1, constant.ChannelStatusEnabled, "default", "gpt-4o, text-embedding-3-small", "", 0)
	createSearchChannel(t, "dis1", 1, constant.ChannelStatusManuallyDisabled, "default", "secret-model", "", 0)

	rec := do(http.MethodGet, "/api/channel/models", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	catalog, ok := body["data"].([]any)
	require.True(t, ok, "catalog missing: %s", rec.Body.String())
	assert.Len(t, catalog, 2)

	rec = do(http.MethodGet, "/api/channel/models_enabled", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	names, ok := body["data"].([]any)
	require.True(t, ok, "names missing: %s", rec.Body.String())
	require.Len(t, names, 2)
	joined := ""
	for _, n := range names {
		joined += n.(string) + ","
	}
	assert.Contains(t, joined, "gpt-4o")
	assert.Contains(t, joined, "text-embedding-3-small")
	assert.NotContains(t, joined, "secret-model")
}

func TestChannelOpsContract(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	require.NoError(t, setting.UpdateOption(setting.RetryTimesOption, "7"))
	rec := do(http.MethodGet, "/api/channel/ops", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(7), data["retry_times"])
}

func TestChannelTestSingleContract(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer sk-live", r.Header.Get("Authorization"))
		time.Sleep(30 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[]}`))
	}))
	defer up.Close()

	ch := createSearchChannel(t, "okch", 1, constant.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&ch).Updates(map[string]any{"base_url": up.URL, "key": "sk-live"}).Error)

	rec := do(http.MethodGet, "/api/channel/test/"+common.Int2Str(ch.Id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"], "body: %s", rec.Body.String())
	assert.Equal(t, "", body["message"])
	elapsed, ok := body["time"].(float64)
	require.True(t, ok, "time missing: %s", rec.Body.String())
	assert.GreaterOrEqual(t, elapsed, 0.0)
	// The reference contract has no "key" in the test response.
	assert.NotContains(t, rec.Body.String(), "sk-live")

	// response_time is persisted after a successful test.
	var refreshed model.Channel
	require.NoError(t, model.DB.First(&refreshed, ch.Id).Error)
	assert.Greater(t, refreshed.ResponseTime, 0)

	// A failing upstream yields the failure shape with time 0.0.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()
	badCh := createSearchChannel(t, "badch", 1, constant.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&badCh).Updates(map[string]any{"base_url": down.URL, "key": "k"}).Error)
	rec = do(http.MethodGet, "/api/channel/test/"+common.Int2Str(badCh.Id), "")
	require.Equal(t, http.StatusOK, rec.Code)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.NotEmpty(t, body["message"])
	assert.Equal(t, float64(0), body["time"])

	// Invalid and missing ids return the reference success:false shape.
	rec = do(http.MethodGet, "/api/channel/test/abc", "")
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.NotEmpty(t, body["message"])

	rec = do(http.MethodGet, "/api/channel/test/999999", "")
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
}

func TestChannelTestAllSystemTask(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)

	// A slow upstream keeps the task pending/running long enough to observe
	// the conflict contract.
	slowUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[]}`))
	}))
	defer slowUp.Close()
	slowCh := createSearchChannel(t, "slowch", 1, constant.ChannelStatusEnabled, "default", "", "", 0)
	require.NoError(t, model.DB.Model(&slowCh).Updates(map[string]any{"base_url": slowUp.URL, "key": "k", "test_model": "gpt-4o"}).Error)
	// A channel without any test model is skipped by the sweep.
	createSearchChannel(t, "nomodel", 1, constant.ChannelStatusEnabled, "default", "", "", 0)

	// First call enqueues the task.
	rec := do(http.MethodGet, "/api/channel/test", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"], "body: %s", rec.Body.String())
	assert.Equal(t, "", body["message"])
	firstData, ok := body["data"].(map[string]any)
	require.True(t, ok, "data missing: %s", rec.Body.String())
	firstTaskID, ok := firstData["task_id"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, firstTaskID)
	assert.Equal(t, model.SystemTaskStatusPending, firstData["status"])

	// While active, a second enqueue is rejected with the reference 409.
	rec = do(http.MethodGet, "/api/channel/test", "")
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "已有通道测试任务正在运行或等待中，不能启动本次手动任务", body["message"])
	conflictData, ok := body["data"].(map[string]any)
	require.True(t, ok, "data missing: %s", rec.Body.String())
	assert.Equal(t, firstTaskID, conflictData["task_id"])
	assert.Equal(t, model.SystemTaskTypeChannelTest, conflictData["type"])

	// A controlled one-shot runner completes the task and records the summary.
	service.RunPendingSystemTasksOnce()
	var task model.SystemTask
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := model.DB.Where("task_id = ?", firstTaskID).First(&task).Error
		require.NoError(t, err)
		if task.Status == model.SystemTaskStatusSucceeded || task.Status == model.SystemTaskStatusFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s did not finish; status=%s", firstTaskID, task.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	assert.Equal(t, model.SystemTaskStatusSucceeded, task.Status, "task error: %s", task.Error)
	assert.Nil(t, task.ActiveKey, "active key must be cleared on completion")
	var summary map[string]any
	require.NoError(t, json.Unmarshal([]byte(task.Result), &summary))
	assert.Equal(t, float64(1), summary["tested"])
	assert.Equal(t, float64(1), summary["succeeded"])
	assert.Equal(t, float64(0), summary["failed"])

	// The sweep persisted the tested channel's response_time.
	var refreshed model.Channel
	require.NoError(t, model.DB.First(&refreshed, slowCh.Id).Error)
	assert.Greater(t, refreshed.ResponseTime, 0)
	assert.Greater(t, refreshed.TestTime, int64(0))

	// After completion a new task can be enqueued. Wait for it to finish too
	// so the background runner has no writes outstanding when this test's
	// temp database is cleaned up.
	rec = do(http.MethodGet, "/api/channel/test", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	secondData := body["data"].(map[string]any)
	secondTaskID, ok := secondData["task_id"].(string)
	require.True(t, ok)
	assert.NotEqual(t, firstTaskID, secondTaskID)
	service.RunPendingSystemTasksOnce()
	var secondTask model.SystemTask
	deadline = time.Now().Add(10 * time.Second)
	for {
		err := model.DB.Where("task_id = ?", secondTaskID).First(&secondTask).Error
		require.NoError(t, err)
		if secondTask.Status == model.SystemTaskStatusSucceeded || secondTask.Status == model.SystemTaskStatusFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("second task %s did not finish; status=%s", secondTaskID, secondTask.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	assert.Equal(t, model.SystemTaskStatusSucceeded, secondTask.Status, "task error: %s", secondTask.Error)
}
