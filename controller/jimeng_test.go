package controller_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func TestJimengOfficialTaskContract(t *testing.T) {
	t.Setenv("RETRY_TIMES", "2")
	var submitCalls atomic.Int32
	var fetchCalls atomic.Int32
	var seenMu sync.Mutex
	var submitPayload, fetchPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.Header.Get("Authorization"), "HMAC-SHA256 Credential=access/") ||
			request.Header.Get("X-Date") == "" || request.Header.Get("X-Content-Sha256") == "" {
			http.Error(writer, "missing Jimeng signature", http.StatusUnauthorized)
			return
		}
		if request.URL.Query().Get("Version") != "2022-08-31" {
			http.Error(writer, "wrong version", http.StatusBadRequest)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			http.Error(writer, "invalid JSON", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Query().Get("Action") {
		case "CVSync2AsyncSubmitTask":
			submitCalls.Add(1)
			seenMu.Lock()
			submitPayload = payload
			seenMu.Unlock()
			if payload["prompt"] == "fail" {
				http.Error(writer, "provider unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = writer.Write([]byte(`{"code":10000,"message":"success","request_id":"jimeng-submit-request","data":{"task_id":"upstream-task-123"}}`))
		case "CVSync2AsyncGetResult":
			fetchCalls.Add(1)
			seenMu.Lock()
			fetchPayload = payload
			seenMu.Unlock()
			_, _ = writer.Write([]byte(`{"code":10000,"message":"success","request_id":"jimeng-fetch-request","data":{"status":"done","video_url":"https://cdn.example.test/video.mp4"}}`))
		default:
			http.Error(writer, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer upstream.Close()

	handler, _, userID := setupChannelRead(t, constant.RoleCommonUser)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Task{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{},
	))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).
		Updates(map[string]any{"quota": 100000, "group": "default"}).Error)
	token := model.Token{
		UserId: userID, Key: "sk-jimeng-client", Name: "jimeng-client",
		Status: service.TokenStatusEnabled, RemainQuota: 100000, Group: "default",
	}
	require.NoError(t, model.DB.Create(&token).Error)
	priority := int64(10)
	channel := model.Channel{
		Type: int(constant.ChannelTypeJimeng), Key: "access|secret", Name: "jimeng-local",
		Status: constant.ChannelStatusEnabled, BaseURL: upstream.URL,
		Models: "jimeng_vgfm_t2v_l20", Group: "default", Priority: &priority,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "jimeng_vgfm_t2v_l20", ChannelId: channel.Id,
		Enabled: true, Priority: &priority, Weight: 1,
	}).Error)
	require.NoError(t, service.InitAbilityCache())
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{
		"jimeng_vgfm_t2v_l20": {Prompt: 0.01},
	})
	service.SetGroupRatios(map[string]float64{"default": 1})
	require.NoError(t, setting.UpdateOption(service.DataExportEnabledOption, "false"))
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
		service.ResetQuotaDataCache()
	})

	doBearer := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost,
			"/jimeng/?Action=CVSync2AsyncSubmitTask&Version=2022-08-31", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token.Key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	submit := doBearer(`{"req_key":"jimeng_vgfm_t2v_l20","prompt":"animate this","frames":121,"seed":42,"aspect_ratio":"16:9","ignored":"not-forwarded"}`)
	require.Equal(t, http.StatusOK, submit.Code, submit.Body.String())
	submitBody := decodeBody(t, submit)
	publicTaskID, ok := submitBody["id"].(string)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(publicTaskID, "task_"))
	assert.Len(t, publicTaskID, len("task_")+32)
	assert.Equal(t, publicTaskID, submitBody["task_id"])
	assert.Equal(t, "video", submitBody["object"])
	assert.Equal(t, "jimeng_vgfm_t2v_l20", submitBody["model"])
	assert.Equal(t, "queued", submitBody["status"])
	assert.Equal(t, float64(0), submitBody["progress"])
	assert.Equal(t, int32(1), submitCalls.Load())

	seenMu.Lock()
	assert.Equal(t, "jimeng_vgfm_t2v_l20", submitPayload["req_key"])
	assert.Equal(t, "animate this", submitPayload["prompt"])
	assert.Equal(t, float64(121), submitPayload["frames"])
	assert.Equal(t, float64(42), submitPayload["seed"])
	assert.Equal(t, "16:9", submitPayload["aspect_ratio"])
	assert.NotContains(t, submitPayload, "ignored")
	seenMu.Unlock()

	var task model.Task
	require.NoError(t, model.DB.Where("task_id = ?", publicTaskID).First(&task).Error)
	assert.Equal(t, "47", task.Platform)
	assert.Equal(t, userID, task.UserId)
	assert.Equal(t, channel.Id, task.ChannelId)
	assert.Equal(t, 5000, task.Quota)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
	assert.NotContains(t, task.PrivateData, token.Key)
	assert.Contains(t, task.PrivateData, "upstream-task-123")

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 95000, user.Quota)
	assert.Equal(t, 5000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 95000, token.RemainQuota)
	assert.Equal(t, 5000, token.UsedQuota)
	var consumption model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", service.LogTypeConsume).First(&consumption).Error)
	assert.Equal(t, "jimeng_vgfm_t2v_l20", consumption.ModelName)
	assert.Equal(t, channel.Id, consumption.ChannelId)
	assert.Equal(t, token.Id, consumption.TokenId)
	assert.Equal(t, 5000, consumption.Quota)
	assert.Equal(t, "jimeng-submit-request", consumption.UpstreamRequestId)
	assert.NotContains(t, consumption.Other, "access|secret")

	fetchRequest := httptest.NewRequest(http.MethodPost,
		"/jimeng/?Action=CVSync2AsyncGetResult&Version=2022-08-31",
		strings.NewReader(`{"req_key":"ignored-by-owner-lookup","task_id":"`+publicTaskID+`"}`))
	fetchRequest.Header.Set("Content-Type", "application/json")
	fetchRequest.Header.Set("Authorization", "Bearer "+token.Key)
	fetchResponse := httptest.NewRecorder()
	handler.ServeHTTP(fetchResponse, fetchRequest)
	require.Equal(t, http.StatusOK, fetchResponse.Code, fetchResponse.Body.String())
	fetchBody := decodeBody(t, fetchResponse)
	assert.Equal(t, publicTaskID, fetchBody["id"])
	assert.Equal(t, "completed", fetchBody["status"])
	assert.Equal(t, float64(100), fetchBody["progress"])
	assert.Equal(t, "https://cdn.example.test/video.mp4", fetchBody["metadata"].(map[string]any)["url"])
	assert.Equal(t, int32(1), fetchCalls.Load())
	seenMu.Lock()
	assert.Equal(t, "upstream-task-123", fetchPayload["task_id"])
	assert.Equal(t, "jimeng_vgfm_t2v_l20", fetchPayload["req_key"])
	seenMu.Unlock()
	require.NoError(t, model.DB.Where("task_id = ?", publicTaskID).First(&task).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.Equal(t, "100%", task.Progress)
	assert.Contains(t, task.PrivateData, "https://cdn.example.test/video.mp4")

	cachedFetchRequest := httptest.NewRequest(http.MethodPost,
		"/jimeng/?Action=CVSync2AsyncGetResult", strings.NewReader(`{"task_id":"`+publicTaskID+`"}`))
	cachedFetchRequest.Header.Set("Content-Type", "application/json")
	cachedFetchRequest.Header.Set("Authorization", "Bearer "+token.Key)
	cachedFetchResponse := httptest.NewRecorder()
	handler.ServeHTTP(cachedFetchResponse, cachedFetchRequest)
	require.Equal(t, http.StatusOK, cachedFetchResponse.Code)
	assert.Equal(t, int32(1), fetchCalls.Load(), "terminal tasks must be served from persisted state")
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 1, user.RequestCount, "fetches are not separately billed")

	backupPriority := int64(5)
	backupChannel := model.Channel{
		Type: int(constant.ChannelTypeJimeng), Key: "access|secret", Name: "jimeng-backup",
		Status: constant.ChannelStatusEnabled, BaseURL: upstream.URL,
		Models: "jimeng_vgfm_t2v_l20", Group: "default", Priority: &backupPriority,
	}
	require.NoError(t, model.DB.Create(&backupChannel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "jimeng_vgfm_t2v_l20", ChannelId: backupChannel.Id,
		Enabled: true, Priority: &backupPriority, Weight: 1,
	}).Error)
	require.NoError(t, service.InitAbilityCache())

	beforeFailureUserQuota := user.Quota
	beforeFailureTokenQuota := token.RemainQuota
	failed := doBearer(`{"req_key":"jimeng_vgfm_t2v_l20","prompt":"fail","frames":121}`)
	require.Equal(t, http.StatusServiceUnavailable, failed.Code, failed.Body.String())
	failedBody := decodeBody(t, failed)
	assert.Equal(t, "fail_to_fetch_task", failedBody["code"])
	assert.Contains(t, failedBody["message"], "provider unavailable")
	assert.Equal(t, int32(2), submitCalls.Load(), "a dispatched task submit must never retry")
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, beforeFailureUserQuota, user.Quota)
	assert.Equal(t, beforeFailureTokenQuota, token.RemainQuota)
	assert.Equal(t, 5000, token.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
}

func TestJimengMiddlewareAndOwnershipFailures(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	handler, _, userID := setupChannelRead(t, constant.RoleCommonUser)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Task{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{},
	))
	token := model.Token{UserId: userID, Key: "sk-jimeng-owner", Name: "owner", Status: service.TokenStatusEnabled,
		RemainQuota: 10000, Group: "default"}
	require.NoError(t, model.DB.Create(&token).Error)

	request := httptest.NewRequest(http.MethodPost, "/jimeng/", strings.NewReader(`{"prompt":"x"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	assert.Equal(t, "Action query parameter is required", decodeBody(t, response)["error"].(map[string]any)["message"])

	request = httptest.NewRequest(http.MethodPost, "/jimeng/?Action=Unknown", strings.NewReader(`{"prompt":"x"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	assert.Equal(t, "Unsupported Action query parameter", decodeBody(t, response)["error"].(map[string]any)["message"])

	request = httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncSubmitTask", strings.NewReader(`{"prompt":`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code)

	request = httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncSubmitTask", strings.NewReader(`{"prompt":`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.Key)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	assert.Equal(t, "Invalid request body", decodeBody(t, response)["error"].(map[string]any)["message"])

	request = httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncSubmitTask", strings.NewReader(`{"req_key":"model","prompt":"x"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusUnauthorized, response.Code)

	request = httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncGetResult", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.Key)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	assert.Equal(t, "task_id is required for CVSync2AsyncGetResult", decodeBody(t, response)["error"].(map[string]any)["message"])

	otherUser := model.User{Username: "jimeng-other", Password: "pw", Status: model.UserStatusEnabled,
		Role: constant.RoleCommonUser, Quota: 10000, Group: "default", AuthVersion: 1}
	require.NoError(t, model.DB.Create(&otherUser).Error)
	otherToken := model.Token{UserId: otherUser.Id, Key: "sk-jimeng-other", Name: "other",
		Status: service.TokenStatusEnabled, RemainQuota: 10000, Group: "default"}
	require.NoError(t, model.DB.Create(&otherToken).Error)
	task := model.Task{TaskID: model.GenerateTaskID(), Platform: "47", UserId: userID, Status: model.TaskStatusNotStart,
		CreatedAt: 1, UpdatedAt: 1, SubmitTime: 1, Progress: "0%"}
	require.NoError(t, model.DB.Create(&task).Error)
	request = httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncGetResult",
		strings.NewReader(`{"task_id":"`+task.TaskID+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+otherToken.Key)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	assert.Equal(t, "task_not_exist", decodeBody(t, response)["code"])
}

type bodyReadProbe struct {
	read atomic.Bool
}

func (p *bodyReadProbe) Read([]byte) (int, error) {
	p.read.Store(true)
	return 0, errors.New("body must not be read")
}

func TestJimengPreAuthBodyIsRateLimited(t *testing.T) {
	_, _, _ = setupChannelRead(t, constant.RoleCommonUser)
	t.Setenv("GLOBAL_API_RATE_LIMIT", "1")
	handler := router.SetUpRouter()

	firstBody := &bodyReadProbe{}
	request := httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncSubmitTask", firstBody)
	request.RemoteAddr = "198.51.100.201:12001"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code)
	assert.False(t, firstBody.read.Load(), "anonymous request body must not be parsed before authentication")

	secondBody := &bodyReadProbe{}
	request = httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncSubmitTask", secondBody)
	request.RemoteAddr = "198.51.100.201:12002"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.False(t, secondBody.read.Load(), "rate-limited request body must not be read")
}

func TestJimengAcceptedTaskCommitFailureRetainsReservations(t *testing.T) {
	t.Setenv("RETRY_TIMES", "2")
	var submitCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		submitCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":10000,"message":"success","request_id":"accepted-before-db-failure","data":{"task_id":"upstream-accepted"}}`))
	}))
	defer upstream.Close()

	handler, _, userID := setupChannelRead(t, constant.RoleCommonUser)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Task{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{},
	))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).
		Updates(map[string]any{"quota": 100000, "group": "default"}).Error)
	token := model.Token{
		UserId: userID, Key: "sk-jimeng-commit-failure", Name: "jimeng-commit-failure",
		Status: service.TokenStatusEnabled, RemainQuota: 100000, Group: "default",
	}
	require.NoError(t, model.DB.Create(&token).Error)
	priority := int64(10)
	channel := model.Channel{
		Type: int(constant.ChannelTypeJimeng), Key: "access|secret", Name: "jimeng-commit-failure",
		Status: constant.ChannelStatusEnabled, BaseURL: upstream.URL,
		Models: "jimeng-commit-model", Group: "default", Priority: &priority,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "jimeng-commit-model", ChannelId: channel.Id,
		Enabled: true, Priority: &priority, Weight: 1,
	}).Error)
	require.NoError(t, service.InitAbilityCache())
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{"jimeng-commit-model": {Prompt: 0.01}})
	service.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
	})
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER reject_jimeng_accept
		BEFORE UPDATE OF status ON tasks
		WHEN NEW.status = 'SUBMITTED'
		BEGIN
			SELECT RAISE(FAIL, 'forced accepted-task commit failure');
		END
	`).Error)

	request := httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncSubmitTask",
		strings.NewReader(`{"req_key":"jimeng-commit-model","prompt":"accepted"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.Key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
	body := decodeBody(t, response)
	assert.Equal(t, "task_commit_failed", body["code"])
	data := body["data"].(map[string]any)
	publicTaskID := data["task_id"].(string)
	assert.Equal(t, "accepted", data["status"])
	assert.Equal(t, int32(1), submitCalls.Load(), "accepted submits must never be retried")

	var task model.Task
	require.NoError(t, model.DB.Where("task_id = ?", publicTaskID).First(&task).Error)
	assert.Equal(t, model.TaskStatusNotStart, task.Status)
	assert.NotContains(t, task.PrivateData, "upstream-accepted")
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 95000, user.Quota, "accepted task reservation must not be refunded")
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, 95000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
}
