package controller_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	relaypkg "github.com/tokenrouter/tokenrouter/relay"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func TestJimengOfficialTaskContract(t *testing.T) {
	t.Setenv("RETRY_TIMES", "2")
	t.Setenv("JIMENG_RECOVERY_DIR", t.TempDir())
	oldEncryptionKey := strings.Repeat("old-jimeng-contract-key-", 2)
	newEncryptionKey := strings.Repeat("new-jimeng-contract-key-", 2)
	t.Setenv("JIMENG_ENCRYPTION_KEYS", "old="+oldEncryptionKey)
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
			if payload["prompt"] == "ambiguous" {
				http.Error(writer, "provider unavailable", http.StatusServiceUnavailable)
				return
			}
			if payload["prompt"] == "fail" {
				_, _ = writer.Write([]byte(`{"code":50400,"message":"invalid request","request_id":"jimeng-rejected"}`))
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
		&model.Task{}, &model.JimengTaskOperation{}, &model.RelayQuotaReservationRecord{},
		&model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}, &model.AuditLogOutbox{},
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
	previousSpecialRatios := service.ExportedGroupGroupRatios()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{
		"jimeng_vgfm_t2v_l20": {Prompt: 0.01},
	})
	// The user's explicit special ratio must override the selected group's base
	// ratio on the asynchronous per-call path: 0.01 * 500000 * 1 = 5000.
	service.SetGroupRatios(map[string]float64{"default": 2})
	service.SetGroupGroupRatios(map[string]map[string]float64{"default": {"default": 1}})
	require.NoError(t, setting.UpdateOption(service.DataExportEnabledOption, "false"))
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
		service.SetGroupGroupRatios(previousSpecialRatios)
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

	submit := doBearer(`{"req_key":"jimeng_vgfm_t2v_l20","prompt":"animate this","frames":121,"seed":42,"aspect_ratio":"16:9","recovery_token":"attacker-controlled","ignored":"not-forwarded"}`)
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
	assert.NotContains(t, submitPayload, "recovery_token")
	seenMu.Unlock()

	var task model.Task
	require.NoError(t, model.DB.Where("task_id = ?", publicTaskID).First(&task).Error)
	assert.Equal(t, strconv.Itoa(int(constant.ChannelTypeJimeng)), task.Platform)
	assert.Equal(t, userID, task.UserId)
	assert.Equal(t, channel.Id, task.ChannelId)
	assert.Equal(t, 5000, task.Quota)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
	assert.NotContains(t, task.PrivateData, token.Key)
	assert.NotContains(t, task.PrivateData, "upstream-task-123")
	assert.Contains(t, task.PrivateData, "encrypted_upstream_task_id")

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 95000, user.Quota)
	assert.Equal(t, 5000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 95000, token.RemainQuota)
	assert.Equal(t, 5000, token.UsedQuota)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	assert.Equal(t, int64(5000), channel.UsedQuota)
	var consumption model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", service.LogTypeConsume).First(&consumption).Error)
	assert.Equal(t, "jimeng_vgfm_t2v_l20", consumption.ModelName)
	assert.Equal(t, channel.Id, consumption.ChannelId)
	assert.Equal(t, token.Id, consumption.TokenId)
	assert.Equal(t, 5000, consumption.Quota)
	assert.Equal(t, common.NormalizeProviderCorrelationID("jimeng-submit-request"), consumption.UpstreamRequestId)
	assert.NotContains(t, consumption.Other, "access|secret")
	var consumeOther map[string]any
	require.NoError(t, json.Unmarshal([]byte(consumption.Other), &consumeOther))
	assert.Equal(t, float64(1), consumeOther["group_ratio"])
	assert.Equal(t, float64(1), consumeOther["user_group_ratio"])

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

	// Terminal metadata is self-contained and contains no recovery ciphertext,
	// so historical reads survive retiring the key used during submission.
	t.Setenv("JIMENG_ENCRYPTION_KEYS", "new="+newEncryptionKey)
	rotatedFetchRequest := httptest.NewRequest(http.MethodPost,
		"/jimeng/?Action=CVSync2AsyncGetResult", strings.NewReader(`{"task_id":"`+publicTaskID+`"}`))
	rotatedFetchRequest.Header.Set("Content-Type", "application/json")
	rotatedFetchRequest.Header.Set("Authorization", "Bearer "+token.Key)
	rotatedFetchResponse := httptest.NewRecorder()
	handler.ServeHTTP(rotatedFetchResponse, rotatedFetchRequest)
	require.Equal(t, http.StatusOK, rotatedFetchResponse.Code, rotatedFetchResponse.Body.String())
	rotatedFetchBody := decodeBody(t, rotatedFetchResponse)
	assert.Equal(t, "completed", rotatedFetchBody["status"])
	assert.Equal(t, "https://cdn.example.test/video.mp4",
		rotatedFetchBody["metadata"].(map[string]any)["url"])
	assert.Equal(t, int32(1), fetchCalls.Load())

	// Pre-durability Jimeng rows could persist provider-controlled messages in
	// FailReason. A terminal official fetch must not reflect those details.
	legacySecret := "provider-echoed-legacy-secret"
	legacyFailed := model.Task{
		TaskID: "task_legacy_jimeng_failure", Platform: strconv.Itoa(int(constant.ChannelTypeJimeng)),
		UserId: userID, Group: "default", ChannelId: channel.Id, Status: model.TaskStatusFailure,
		Progress: "100%", FailReason: legacySecret,
		Properties: `{"origin_model_name":"jimeng_vgfm_t2v_l20"}`, PrivateData: `{}`,
	}
	require.NoError(t, model.DB.Create(&legacyFailed).Error)
	legacyFetchRequest := httptest.NewRequest(http.MethodPost,
		"/jimeng/?Action=CVSync2AsyncGetResult", strings.NewReader(`{"task_id":"`+legacyFailed.TaskID+`"}`))
	legacyFetchRequest.Header.Set("Content-Type", "application/json")
	legacyFetchRequest.Header.Set("Authorization", "Bearer "+token.Key)
	legacyFetchResponse := httptest.NewRecorder()
	handler.ServeHTTP(legacyFetchResponse, legacyFetchRequest)
	require.Equal(t, http.StatusOK, legacyFetchResponse.Code, legacyFetchResponse.Body.String())
	assert.NotContains(t, legacyFetchResponse.Body.String(), legacySecret)
	legacyFetchBody := decodeBody(t, legacyFetchResponse)
	assert.Equal(t, "Jimeng task failed", legacyFetchBody["error"].(map[string]any)["message"])
	assert.Equal(t, int32(1), fetchCalls.Load(), "legacy terminal tasks must not call the provider")

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
	require.NoError(t, model.DB.Model(&channel).Update("base_url", "not-a-valid-url").Error)
	preDispatchFallback := doBearer(`{"req_key":"jimeng_vgfm_t2v_l20","prompt":"safe fallback","frames":121}`)
	require.Equal(t, http.StatusOK, preDispatchFallback.Code, preDispatchFallback.Body.String())
	assert.Equal(t, int32(2), submitCalls.Load(), "pre-dispatch configuration failure may use a backup channel")
	require.NoError(t, model.DB.Model(&channel).Update("base_url", upstream.URL).Error)
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)

	beforeFailureUserQuota := user.Quota
	beforeFailureUserUsed := user.UsedQuota
	beforeFailureRequests := user.RequestCount
	beforeFailureTokenQuota := token.RemainQuota
	beforeFailureTokenUsed := token.UsedQuota
	failed := doBearer(`{"req_key":"jimeng_vgfm_t2v_l20","prompt":"fail","frames":121}`)
	require.Equal(t, http.StatusInternalServerError, failed.Code, failed.Body.String())
	failedBody := decodeBody(t, failed)
	assert.Equal(t, "50400", failedBody["code"])
	assert.Equal(t, "Jimeng provider rejected the request", failedBody["message"])
	assert.Equal(t, int32(3), submitCalls.Load(), "definitively rejected submits must never retry")
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, beforeFailureUserQuota, user.Quota)
	assert.Equal(t, beforeFailureUserUsed, user.UsedQuota)
	assert.Equal(t, beforeFailureRequests, user.RequestCount)
	assert.Equal(t, beforeFailureTokenQuota, token.RemainQuota)
	assert.Equal(t, beforeFailureTokenUsed, token.UsedQuota)

	ambiguous := doBearer(`{"req_key":"jimeng_vgfm_t2v_l20","prompt":"ambiguous","frames":121}`)
	require.Equal(t, http.StatusAccepted, ambiguous.Code, ambiguous.Body.String())
	ambiguousBody := decodeBody(t, ambiguous)
	assert.Equal(t, "submit_outcome_unknown", ambiguousBody["code"])
	ambiguousData := ambiguousBody["data"].(map[string]any)
	assert.Equal(t, "unknown", ambiguousData["status"])
	ambiguousTaskID := ambiguousData["task_id"].(string)
	assert.Equal(t, int32(4), submitCalls.Load(), "ambiguous submits must never retry")
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, beforeFailureUserQuota-5000, user.Quota)
	assert.Equal(t, beforeFailureUserUsed+5000, user.UsedQuota)
	assert.Equal(t, beforeFailureRequests+1, user.RequestCount)
	assert.Equal(t, beforeFailureTokenQuota-5000, token.RemainQuota)
	assert.Equal(t, beforeFailureTokenUsed+5000, token.UsedQuota)
	var ambiguousTask model.Task
	require.NoError(t, model.DB.Where("task_id = ?", ambiguousTaskID).First(&ambiguousTask).Error)
	assert.NotContains(t, ambiguousTask.PrivateData, "encrypted_channel_key")
	assert.NotContains(t, ambiguousTask.PrivateData, "channel_base_url")
	assert.Contains(t, ambiguousTask.PrivateData, "relay_reservation_id")

	unknownFetchRequest := httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncGetResult",
		strings.NewReader(`{"task_id":"`+ambiguousTaskID+`"}`))
	unknownFetchRequest.Header.Set("Content-Type", "application/json")
	unknownFetchRequest.Header.Set("Authorization", "Bearer "+token.Key)
	unknownFetchResponse := httptest.NewRecorder()
	handler.ServeHTTP(unknownFetchResponse, unknownFetchRequest)
	require.Equal(t, http.StatusOK, unknownFetchResponse.Code, unknownFetchResponse.Body.String())
	assert.Equal(t, "unknown", decodeBody(t, unknownFetchResponse)["status"])
	assert.Equal(t, int32(1), fetchCalls.Load(), "unknown outcome without an upstream ID must not poll")
}

func TestJimengMiddlewareAndOwnershipFailures(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	handler, _, userID := setupChannelRead(t, constant.RoleCommonUser)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Task{}, &model.JimengTaskOperation{}, &model.RelayQuotaReservationRecord{},
		&model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}, &model.AuditLogOutbox{},
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
	task := model.Task{TaskID: model.GenerateTaskID(), Platform: strconv.Itoa(int(constant.ChannelTypeJimeng)), UserId: userID, Status: model.TaskStatusNotStart,
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

var jimengRateLimitIPSequence atomic.Uint32

func (p *bodyReadProbe) Read([]byte) (int, error) {
	p.read.Store(true)
	return 0, errors.New("body must not be read")
}

func TestJimengPreAuthBodyIsRateLimited(t *testing.T) {
	_, _, _ = setupChannelRead(t, constant.RoleCommonUser)
	t.Setenv("GLOBAL_API_RATE_LIMIT", "1")
	handler := router.SetUpRouter()
	sequence := jimengRateLimitIPSequence.Add(1)
	clientIP := fmt.Sprintf("198.18.%d.%d", (sequence/254)%256, sequence%254+1)

	firstBody := &bodyReadProbe{}
	request := httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncSubmitTask", firstBody)
	request.RemoteAddr = clientIP + ":12001"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code)
	assert.False(t, firstBody.read.Load(), "anonymous request body must not be parsed before authentication")

	secondBody := &bodyReadProbe{}
	request = httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncSubmitTask", secondBody)
	request.RemoteAddr = clientIP + ":12002"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.False(t, secondBody.read.Load(), "rate-limited request body must not be read")
}

func TestJimengAcceptedTaskCommitFailureRecoversOnFetch(t *testing.T) {
	t.Setenv("RETRY_TIMES", "2")
	t.Setenv("JIMENG_RECOVERY_DIR", t.TempDir())
	var submitCalls atomic.Int32
	var fetchCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Query().Get("Action") {
		case "CVSync2AsyncSubmitTask":
			submitCalls.Add(1)
			_, _ = writer.Write([]byte(`{"code":10000,"message":"success","request_id":"accepted-before-db-failure","data":{"task_id":"upstream-accepted"}}`))
		case "CVSync2AsyncGetResult":
			fetchCalls.Add(1)
			_, _ = writer.Write([]byte(`{"code":10000,"message":"success","request_id":"recovered-fetch","data":{"status":"done","video_url":"https://cdn.example.test/recovered.mp4"}}`))
		default:
			http.Error(writer, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer upstream.Close()

	handler, _, userID := setupChannelRead(t, constant.RoleCommonUser)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Task{}, &model.JimengTaskOperation{}, &model.RelayQuotaReservationRecord{},
		&model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}, &model.AuditLogOutbox{},
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
		CREATE TRIGGER reject_jimeng_accounting
		BEFORE UPDATE OF used_quota ON users
		WHEN NEW.used_quota > OLD.used_quota
		BEGIN
			SELECT RAISE(FAIL, 'forced accepted-task accounting failure');
		END
	`).Error)

	request := httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncSubmitTask",
		strings.NewReader(`{"req_key":"jimeng-commit-model","prompt":"accepted"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.Key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	body := decodeBody(t, response)
	assert.Equal(t, "task_commit_pending", body["code"])
	data := body["data"].(map[string]any)
	publicTaskID := data["task_id"].(string)
	assert.Equal(t, "accepted", data["status"])
	assert.Equal(t, true, data["settlement_pending"])
	assert.Equal(t, true, data["recovery_durable"])
	assert.Equal(t, int32(1), submitCalls.Load(), "accepted submits must never be retried")

	var task model.Task
	require.NoError(t, model.DB.Where("task_id = ?", publicTaskID).First(&task).Error)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
	assert.NotContains(t, task.PrivateData, "upstream-accepted")
	assert.Contains(t, task.PrivateData, "encrypted_upstream_task_id")
	assert.Contains(t, task.PrivateData, `"settlement_pending":true`)
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 95000, user.Quota, "accepted task reservation must not be refunded")
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, 95000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)

	require.NoError(t, model.DB.Exec("DROP TRIGGER reject_jimeng_accounting").Error)
	pollToken := model.Token{
		UserId: userID, Key: "sk-jimeng-recovery-poller", Name: "jimeng-recovery-poller",
		Status: service.TokenStatusEnabled, UnlimitedQuota: true, Group: "default",
	}
	require.NoError(t, model.DB.Create(&pollToken).Error)
	require.NoError(t, model.DB.Delete(&token).Error)
	require.NoError(t, model.DB.Delete(&channel).Error)
	fetch := func() *httptest.ResponseRecorder {
		fetchRequest := httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncGetResult",
			strings.NewReader(`{"task_id":"`+publicTaskID+`"}`))
		fetchRequest.Header.Set("Content-Type", "application/json")
		fetchRequest.Header.Set("Authorization", "Bearer "+pollToken.Key)
		fetchResponse := httptest.NewRecorder()
		handler.ServeHTTP(fetchResponse, fetchRequest)
		return fetchResponse
	}
	fetchResponse := fetch()
	require.Equal(t, http.StatusOK, fetchResponse.Code, fetchResponse.Body.String())
	assert.Equal(t, "completed", decodeBody(t, fetchResponse)["status"])
	assert.Equal(t, int32(1), fetchCalls.Load())

	require.NoError(t, model.DB.Where("task_id = ?", publicTaskID).First(&task).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.NotContains(t, task.PrivateData, "settlement_pending")
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.Unscoped().First(&token, token.Id).Error)
	assert.True(t, token.DeletedAt.Valid)
	assert.Equal(t, 95000, user.Quota)
	assert.Equal(t, 5000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 95000, token.RemainQuota)
	assert.Equal(t, 5000, token.UsedQuota)

	cached := fetch()
	require.Equal(t, http.StatusOK, cached.Code, cached.Body.String())
	assert.Equal(t, int32(1), fetchCalls.Load(), "settled terminal task must be cached")
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.Unscoped().First(&token, token.Id).Error)
	assert.Equal(t, 5000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 5000, token.UsedQuota)
}

func TestJimengFallbackWriteFailureRecoversFromEncryptedDatabaseOperation(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	t.Setenv("SSRF_DISABLE", "true")
	t.Cleanup(common.InitSSRF)
	common.InitSSRF()
	recoveryDirectory := t.TempDir()
	t.Setenv("JIMENG_RECOVERY_DIR", recoveryDirectory)
	const providerTaskID = "opaquecredentialvalue0123456789"
	var fetchCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("Action") == "CVSync2AsyncGetResult" {
			fetchCalls.Add(1)
			_, _ = writer.Write([]byte(`{"code":10000,"message":"success","data":{"status":"done","video_url":"https://cdn.example.test/recovery-token.mp4"}}`))
			return
		}
		_, _ = writer.Write([]byte(`{"code":10000,"message":"success","request_id":"recovery-token-submit","data":{"task_id":"` + providerTaskID + `"}}`))
	}))
	defer upstream.Close()

	handler, _, userID := setupChannelRead(t, constant.RoleCommonUser)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Task{}, &model.JimengTaskOperation{}, &model.RelayQuotaReservationRecord{},
		&model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}, &model.AuditLogOutbox{},
	))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).
		Updates(map[string]any{"quota": 100000, "group": "default"}).Error)
	token := model.Token{UserId: userID, Key: "sk-jimeng-recovery-token", Name: "recovery-token",
		Status: service.TokenStatusEnabled, RemainQuota: 100000, Group: "default"}
	require.NoError(t, model.DB.Create(&token).Error)
	priority := int64(10)
	channel := model.Channel{Type: int(constant.ChannelTypeJimeng), Key: "access|secret", Name: "recovery-token",
		Status: constant.ChannelStatusEnabled, BaseURL: upstream.URL, Models: "jimeng-recovery-model",
		Group: "default", Priority: &priority}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "jimeng-recovery-model",
		ChannelId: channel.Id, Enabled: true, Priority: &priority, Weight: 1}).Error)
	require.NoError(t, service.InitAbilityCache())
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{"jimeng-recovery-model": {Prompt: 0.01}})
	service.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
	})
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER reject_jimeng_task_acceptance
		BEFORE UPDATE OF status ON tasks
		WHEN NEW.status = 'SUBMITTED'
		BEGIN
			SELECT RAISE(FAIL, 'forced task acceptance write failure');
		END
	`).Error)
	var logSink bytes.Buffer
	previousLogger := common.Logger
	common.SetLogger(slog.New(slog.NewJSONHandler(&logSink, nil)))
	t.Cleanup(func() { common.SetLogger(previousLogger) })

	submitRequest := httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncSubmitTask",
		strings.NewReader(`{"req_key":"jimeng-recovery-model","prompt":"accepted"}`))
	submitRequest.Header.Set("Content-Type", "application/json")
	submitRequest.Header.Set("Authorization", "Bearer "+token.Key)
	submitResponse := httptest.NewRecorder()
	handler.ServeHTTP(submitResponse, submitRequest)
	require.Equal(t, http.StatusAccepted, submitResponse.Code, submitResponse.Body.String())
	submitBody := decodeBody(t, submitResponse)
	assert.Equal(t, "task_commit_pending", submitBody["code"])
	data := submitBody["data"].(map[string]any)
	publicTaskID := data["task_id"].(string)
	assert.Equal(t, "accepted", data["status"])
	assert.Equal(t, true, data["settlement_pending"])
	assert.Equal(t, true, data["recovery_durable"])
	assert.NotContains(t, data, "recovery_degraded")
	assert.NotContains(t, data, "recovery_token")
	journalPath := filepath.Join(recoveryDirectory, publicTaskID+".json")
	_, err := os.Stat(journalPath)
	assert.ErrorIs(t, err, os.ErrNotExist, "primary-database fallback must not require a node-local journal")
	assert.NotContains(t, logSink.String(), providerTaskID,
		"provider task IDs must never enter process logs on recovery-write failure")
	var preSettlementLogCount, preSettlementOutboxCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("type = ?", service.LogTypeConsume).Count(&preSettlementLogCount).Error)
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).Count(&preSettlementOutboxCount).Error)
	assert.Zero(t, preSettlementLogCount, "an uncommitted charge must not emit a consume audit")
	assert.Zero(t, preSettlementOutboxCount, "audit creation must share the later settlement transaction")

	var task model.Task
	require.NoError(t, model.DB.Where("task_id = ?", publicTaskID).First(&task).Error)
	assert.Equal(t, model.TaskStatusNotStart, task.Status)
	assert.NotContains(t, task.PrivateData, providerTaskID)
	assert.NotContains(t, task.PrivateData, "access|secret")
	assert.Contains(t, task.PrivateData, "encrypted_channel_key")
	var operation model.JimengTaskOperation
	require.NoError(t, model.DB.Where("task_id = ?", publicTaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationSubmitted, operation.State)
	assert.True(t, operation.SettlementPending)
	assert.NotEmpty(t, operation.EncryptedProviderTaskID)
	assert.NotContains(t, operation.EncryptedProviderTaskID, providerTaskID)
	assert.Contains(t, operation.EncryptedProviderTaskID, "jimeng-key-v1:")

	require.NoError(t, model.DB.Exec("DROP TRIGGER reject_jimeng_task_acceptance").Error)
	// Exercise the critical ordering where a client fetch arrives before the
	// autonomous reconciler. The encrypted operation id and its pending bit must
	// first settle accounting/audit under a row lease, then cache the terminal
	// provider result.
	clientFirstFetch := httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncGetResult",
		strings.NewReader(`{"task_id":"`+publicTaskID+`"}`))
	clientFirstFetch.Header.Set("Content-Type", "application/json")
	clientFirstFetch.Header.Set("Authorization", "Bearer "+token.Key)
	clientFirstResponse := httptest.NewRecorder()
	handler.ServeHTTP(clientFirstResponse, clientFirstFetch)
	require.Equal(t, http.StatusOK, clientFirstResponse.Code, clientFirstResponse.Body.String())
	assert.Equal(t, "completed", decodeBody(t, clientFirstResponse)["status"])
	assert.Equal(t, int32(1), fetchCalls.Load())

	var databaseLocation struct {
		File string `gorm:"column:file"`
	}
	require.NoError(t, model.DB.Raw("PRAGMA database_list").Scan(&databaseLocation).Error)
	require.NotEmpty(t, databaseLocation.File)
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	restartedDB, err := gorm.Open(sqlite.Open("file:"+databaseLocation.File+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = restartedDB
	model.LOG_DB = restartedDB
	require.NoError(t, service.InitAbilityCache())
	handler = router.SetUpRouter()
	require.NoError(t, relaypkg.ReconcileJimengTaskOperations(), "another process must safely replay completed recovery")
	assert.Equal(t, int32(1), fetchCalls.Load())

	fetchRequest := httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncGetResult",
		strings.NewReader(`{"task_id":"`+publicTaskID+`"}`))
	fetchRequest.Header.Set("Content-Type", "application/json")
	fetchRequest.Header.Set("Authorization", "Bearer "+token.Key)
	fetchResponse := httptest.NewRecorder()
	handler.ServeHTTP(fetchResponse, fetchRequest)
	require.Equal(t, http.StatusOK, fetchResponse.Code, fetchResponse.Body.String())
	assert.Equal(t, "completed", decodeBody(t, fetchResponse)["status"])
	assert.Equal(t, int32(1), fetchCalls.Load(), "terminal result must be cached after autonomous recovery")
	_, err = os.Stat(journalPath)
	assert.ErrorIs(t, err, os.ErrNotExist)

	require.NoError(t, model.DB.Where("task_id = ?", publicTaskID).First(&task).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.NotContains(t, task.PrivateData, providerTaskID)
	assert.NotContains(t, task.PrivateData, "encrypted_upstream_task_id")
	assert.NotContains(t, task.PrivateData, "encrypted_channel_key")
	assert.NotContains(t, task.PrivateData, "settlement_pending")
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 95000, user.Quota)
	assert.Equal(t, 5000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 95000, token.RemainQuota)
	assert.Equal(t, 5000, token.UsedQuota)
	var recoveredAudit model.AuditLogOutbox
	require.NoError(t, model.DB.Where("event_id = ?", "jimeng:"+operation.ReservationID).
		First(&recoveredAudit).Error)
	assert.Equal(t, model.AuditLogOutboxStatusPending, recoveredAudit.Status)
	require.NoError(t, service.DeliverAuditLogOutbox())
	var logCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", service.LogTypeConsume).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)
}

func TestJimengAcceptedTaskRecoveryFailureDoesNotLogProviderTaskID(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	recoveryPath := filepath.Join(t.TempDir(), "recovery")
	t.Setenv("JIMENG_RECOVERY_DIR", recoveryPath)
	const providerTaskID = "opaqueprovidercredential0123456789"
	var failRecoveryWrites atomic.Bool

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		// The pre-dispatch recovery marker has already been written. Make its
		// directory unusable only after dispatch so the accepted-marker update
		// and both database recovery writes exercise the rare log sink below.
		if err := os.RemoveAll(recoveryPath); err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(recoveryPath, []byte("blocked"), 0o600); err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		failRecoveryWrites.Store(true)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":10000,"message":"success","request_id":"req_safe","data":{"task_id":"` + providerTaskID + `"}}`))
	}))
	defer upstream.Close()
	handler, _, token, _ := setupJimengDurabilityFixture(t, upstream.URL, "jimeng-log-safety")
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER reject_jimeng_recovery_write
		BEFORE UPDATE OF status ON tasks
		WHEN NEW.status = 'SUBMITTED'
		BEGIN
			SELECT RAISE(FAIL, 'forced task recovery write failure');
		END
	`).Error)
	const callbackName = "test:jimeng_recovery_operation_write_failure"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if failRecoveryWrites.Load() && tx.Statement.Table == (model.JimengTaskOperation{}).TableName() {
			tx.AddError(errors.New("forced operation recovery write failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	var sink bytes.Buffer
	previousLogger := common.Logger
	common.SetLogger(slog.New(slog.NewJSONHandler(&sink, nil)))
	t.Cleanup(func() { common.SetLogger(previousLogger) })
	request := httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncSubmitTask",
		strings.NewReader(`{"req_key":"jimeng-log-safety","prompt":"accepted"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.Key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	body := decodeBody(t, response)
	assert.Equal(t, "task_commit_pending", body["code"])
	data := body["data"].(map[string]any)
	assert.Equal(t, false, data["recovery_durable"],
		"a node-local recovery write must never be represented as cluster durable")
	assert.Equal(t, true, data["recovery_degraded"])
	assert.NotContains(t, data, "node_local_recovery",
		"an unavailable emergency directory cannot claim even same-node recovery")

	logged := sink.String()
	assert.Contains(t, logged, "database recovery write failed")
	assert.NotContains(t, logged, providerTaskID)
}

func TestJimengAcceptedMetadataEncryptionFailureReturnsHonestAcceptedRecoveryState(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	t.Setenv("JIMENG_RECOVERY_DIR", t.TempDir())
	t.Setenv("JIMENG_ENCRYPTION_KEYS", "active="+strings.Repeat("k", 32))
	const providerTaskID = "accepted-id-without-a-usable-recovery-key"
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		// The pre-dispatch snapshot and capacity checks used the valid key above.
		// Model a keyring/configuration failure immediately after the provider
		// accepted the request but before its task id can be encrypted.
		if err := os.Setenv("JIMENG_ENCRYPTION_KEYS", "invalid-entry"); err != nil {
			http.Error(writer, "test keyring mutation failed", http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":10000,"data":{"task_id":"` + providerTaskID + `"}}`))
	}))
	defer upstream.Close()

	const modelName = "jimeng-accepted-key-failure"
	handler, userID, token, _ := setupJimengDurabilityFixture(t, upstream.URL, modelName)
	response := submitJimengDurabilityRequest(handler, token, modelName)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	body := decodeBody(t, response)
	assert.Equal(t, "task_commit_pending", body["code"])
	data := body["data"].(map[string]any)
	assert.Equal(t, "unknown", data["status"])
	assert.Equal(t, true, data["settlement_pending"])
	assert.Equal(t, false, data["recovery_durable"])
	assert.Equal(t, true, data["recovery_degraded"])
	assert.Equal(t, false, data["provider_poll_recovery"])
	assert.NotContains(t, data, "node_local_recovery")
	assert.NotContains(t, response.Body.String(), providerTaskID)

	var operation model.JimengTaskOperation
	require.NoError(t, model.DB.Where("task_id = ? AND user_id = ?", data["task_id"], userID).
		First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationDispatching, operation.State)
	assert.Empty(t, operation.EncryptedProviderTaskID)
}

func setupJimengDurabilityFixture(t *testing.T, upstreamURL, modelName string) (http.Handler, int, model.Token, model.Channel) {
	t.Helper()
	handler, _, userID := setupChannelRead(t, constant.RoleCommonUser)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Task{}, &model.JimengTaskOperation{}, &model.RelayQuotaReservationRecord{},
		&model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}, &model.AuditLogOutbox{},
	))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).
		Updates(map[string]any{"quota": 100000, "group": "default"}).Error)
	token := model.Token{
		UserId: userID, Key: "sk-" + modelName, Name: modelName,
		Status: service.TokenStatusEnabled, RemainQuota: 100000, Group: "default",
	}
	require.NoError(t, model.DB.Create(&token).Error)
	priority := int64(10)
	channel := model.Channel{
		Type: int(constant.ChannelTypeJimeng), Key: "access|secret", Name: modelName,
		Status: constant.ChannelStatusEnabled, BaseURL: upstreamURL,
		Models: modelName, Group: "default", Priority: &priority,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: modelName, ChannelId: channel.Id,
		Enabled: true, Priority: &priority, Weight: 1,
	}).Error)
	require.NoError(t, service.InitAbilityCache())
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{modelName: {Prompt: 0.01}})
	service.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
	})
	return handler, userID, token, channel
}

func submitJimengDurabilityRequest(handler http.Handler, token model.Token, modelName string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncSubmitTask",
		strings.NewReader(`{"req_key":"`+modelName+`","prompt":"accepted"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.Key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func fetchJimengDurabilityTask(handler http.Handler, token model.Token, taskID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/jimeng/?Action=CVSync2AsyncGetResult",
		strings.NewReader(`{"task_id":"`+taskID+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.Key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestJimengPrimaryRecoveryAllowsDispatchWhenSecondaryJournalIsUnavailable(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	blockedDirectory := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(blockedDirectory, []byte("blocked"), 0o600))
	t.Setenv("JIMENG_RECOVERY_DIR", blockedDirectory)
	var submits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		submits.Add(1)
		_, _ = writer.Write([]byte(`{"code":10000,"data":{"task_id":"must-not-dispatch"}}`))
	}))
	defer upstream.Close()

	const modelName = "jimeng-recovery-preflight"
	handler, userID, token, _ := setupJimengDurabilityFixture(t, upstream.URL, modelName)
	response := submitJimengDurabilityRequest(handler, token, modelName)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, int32(1), submits.Load(), "the primary database recovery marker authorizes dispatch")

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 95000, user.Quota)
	assert.Equal(t, 5000, user.UsedQuota)
	assert.Equal(t, 95000, token.RemainQuota)
	assert.Equal(t, 5000, token.UsedQuota)
	var operation model.JimengTaskOperation
	require.NoError(t, model.DB.Where("task_id = ?", decodeBody(t, response)["task_id"]).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationSubmitted, operation.State)
}

func TestJimengFetchCannotLeaseBareInFlightDispatch(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	t.Setenv("JIMENG_RECOVERY_DIR", t.TempDir())
	submitEntered := make(chan struct{})
	releaseSubmit := make(chan struct{})
	var submitCalls, fetchCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("Action") == "CVSync2AsyncSubmitTask" {
			submitCalls.Add(1)
			close(submitEntered)
			<-releaseSubmit
			_, _ = writer.Write([]byte(`{"code":10000,"data":{"task_id":"provider-in-flight"}}`))
			return
		}
		fetchCalls.Add(1)
		_, _ = writer.Write([]byte(`{"code":10000,"data":{"status":"done"}}`))
	}))
	defer upstream.Close()
	var releaseSubmitOnce sync.Once
	releaseBlockedSubmit := func() { releaseSubmitOnce.Do(func() { close(releaseSubmit) }) }
	defer releaseBlockedSubmit()

	const modelName = "jimeng-in-flight-fetch"
	handler, userID, token, _ := setupJimengDurabilityFixture(t, upstream.URL, modelName)
	submitResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		submitResult <- submitJimengDurabilityRequest(handler, token, modelName)
	}()
	select {
	case <-submitEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("Jimeng submit did not reach provider")
	}

	var task model.Task
	require.NoError(t, model.DB.Where("user_id = ?", userID).
		Order("id desc").First(&task).Error)
	var operation model.JimengTaskOperation
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.Equal(t, model.JimengTaskOperationDispatching, operation.State)
	require.True(t, operation.SettlementPending)
	require.Empty(t, operation.LeaseOwner)

	concurrentFetch := fetchJimengDurabilityTask(handler, token, task.TaskID)
	require.Equal(t, http.StatusInternalServerError, concurrentFetch.Code, concurrentFetch.Body.String())
	assert.Equal(t, "task_data_invalid", decodeBody(t, concurrentFetch)["code"])
	assert.Zero(t, fetchCalls.Load(), "a bare pre-response dispatch has no provider id to poll")
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationDispatching, operation.State)
	assert.Empty(t, operation.LeaseOwner,
		"a client fetch must not fence the foreground acceptance transition")

	releaseBlockedSubmit()
	var submitted *httptest.ResponseRecorder
	select {
	case submitted = <-submitResult:
	case <-time.After(5 * time.Second):
		t.Fatal("Jimeng submit did not finish")
	}
	require.Equal(t, http.StatusOK, submitted.Code, submitted.Body.String())
	assert.Equal(t, int32(1), submitCalls.Load())
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationSubmitted, operation.State)
	assert.False(t, operation.SettlementPending)
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 5000, user.UsedQuota)
	assert.Equal(t, 5000, token.UsedQuota)
}

func TestJimengReservationStorageFailuresAreInternalAndCompensated(t *testing.T) {
	for _, test := range []struct {
		name        string
		failedTable string
	}{
		{name: "token reservation", failedTable: "tokens"},
		{name: "funding reservation", failedTable: "users"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("RETRY_TIMES", "0")
			t.Setenv("JIMENG_RECOVERY_DIR", t.TempDir())
			var submits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				submits.Add(1)
				_, _ = writer.Write([]byte(`{"code":10000,"data":{"task_id":"must-not-dispatch"}}`))
			}))
			defer upstream.Close()

			modelName := "jimeng-reserve-" + strings.ReplaceAll(test.name, " ", "-")
			handler, userID, token, _ := setupJimengDurabilityFixture(t, upstream.URL, modelName)
			callbackName := "test:jimeng_reserve_" + test.failedTable
			require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Table == test.failedTable {
					tx.AddError(errors.New("injected Jimeng reservation storage failure"))
				}
			}))
			t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

			response := submitJimengDurabilityRequest(handler, token, modelName)
			require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
			assert.Equal(t, "pre_consume_failed", decodeBody(t, response)["code"])
			assert.Zero(t, submits.Load())

			var user model.User
			require.NoError(t, model.DB.First(&user, userID).Error)
			require.NoError(t, model.DB.First(&token, token.Id).Error)
			assert.Equal(t, 100000, user.Quota)
			assert.Equal(t, 100000, token.RemainQuota,
				"a failed funding reservation must compensate the earlier token reservation")
		})
	}
}

func TestJimengRefundFailureRemainsDurableForAnotherWorker(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	t.Setenv("JIMENG_RECOVERY_DIR", t.TempDir())
	var failRefunds atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		failRefunds.Store(true)
		_, _ = writer.Write([]byte(`{"code":50400,"message":"definitive rejection"}`))
	}))
	defer upstream.Close()

	const modelName = "jimeng-refund-failure"
	handler, userID, token, _ := setupJimengDurabilityFixture(t, upstream.URL, modelName)
	var userRefunds, tokenRefunds atomic.Int32
	callbackName := "test:jimeng_refund_failures"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if !failRefunds.Load() {
			return
		}
		switch tx.Statement.Table {
		case "users":
			userRefunds.Add(1)
			tx.AddError(errors.New("injected Jimeng funding refund failure"))
		case "tokens":
			tokenRefunds.Add(1)
			tx.AddError(errors.New("injected Jimeng token refund failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	response := submitJimengDurabilityRequest(handler, token, modelName)
	require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
	body := decodeBody(t, response)
	assert.Equal(t, "task_cleanup_failed", body["code"])
	data := body["data"].(map[string]any)
	assert.Equal(t, "pending", data["reservation_state"])
	assert.GreaterOrEqual(t, userRefunds.Load(), int32(1),
		"the request path may retry the idempotent atomic refund")
	assert.Zero(t, tokenRefunds.Load(), "the transaction stops at the injected wallet failure")
	var operation model.JimengTaskOperation
	require.NoError(t, model.DB.Where("task_id = ?", data["task_id"]).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationPrepared, operation.State,
		"known provider rejection must remain queued for refund, never UNKNOWN settlement")

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 95000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 95000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)

	failRefunds.Store(false)
	require.NoError(t, model.DB.Model(&model.JimengTaskOperation{}).Where("state = ?", model.JimengTaskOperationPrepared).
		Update("next_attempt_at", 0).Error)
	require.NoError(t, relaypkg.ReconcileJimengTaskOperations())
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 100000, user.Quota)
	assert.Equal(t, 100000, token.RemainQuota)
}

func TestJimengRejectedTaskStatusFailureKeepsAtomicRefundPending(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	t.Setenv("JIMENG_RECOVERY_DIR", t.TempDir())
	var rejectStatusWrite atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		rejectStatusWrite.Store(true)
		_, _ = writer.Write([]byte(`{"code":50400,"message":"definitive rejection"}`))
	}))
	defer upstream.Close()

	const modelName = "jimeng-status-failure"
	handler, userID, token, _ := setupJimengDurabilityFixture(t, upstream.URL, modelName)
	callbackName := "test:jimeng_rejected_status_failure"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if rejectStatusWrite.Load() && tx.Statement.Table == "tasks" {
			tx.AddError(errors.New("injected Jimeng rejected-task status failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	response := submitJimengDurabilityRequest(handler, token, modelName)
	require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
	body := decodeBody(t, response)
	assert.Equal(t, "task_cleanup_failed", body["code"])
	data := body["data"].(map[string]any)
	assert.Equal(t, "pending", data["reservation_state"])

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 95000, user.Quota)
	assert.Equal(t, 95000, token.RemainQuota)

	rejectStatusWrite.Store(false)
	require.NoError(t, model.DB.Model(&model.JimengTaskOperation{}).Where("state = ?", model.JimengTaskOperationPrepared).
		Update("next_attempt_at", 0).Error)
	require.NoError(t, relaypkg.ReconcileJimengTaskOperations())
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 100000, user.Quota)
	assert.Equal(t, 100000, token.RemainQuota)
}

func TestJimengConsumeLogFailureKeepsAcceptedResponseAndCharge(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	t.Setenv("JIMENG_RECOVERY_DIR", t.TempDir())
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("Action") == "CVSync2AsyncGetResult" {
			_, _ = writer.Write([]byte(`{"code":10000,"data":{"status":"done","video_url":"https://cdn.example.test/log-failure.mp4"}}`))
			return
		}
		_, _ = writer.Write([]byte(`{"code":10000,"request_id":"accepted-log-failure","data":{"task_id":"upstream-log-failure"}}`))
	}))
	defer upstream.Close()

	const modelName = "jimeng-log-failure"
	handler, userID, token, _ := setupJimengDurabilityFixture(t, upstream.URL, modelName)
	logDB := model.LOG_DB
	model.LOG_DB = nil
	response := submitJimengDurabilityRequest(handler, token, modelName)
	model.LOG_DB = logDB
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	body := decodeBody(t, response)
	_, hasMetadata := body["metadata"]
	assert.False(t, hasMetadata, "the primary audit outbox makes the accepted response fully durable")
	taskID := body["task_id"].(string)
	var outbox model.AuditLogOutbox
	require.NoError(t, model.DB.Where("status = ?", model.AuditLogOutboxStatusPending).First(&outbox).Error)
	require.NotEmpty(t, outbox.EventID)

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 95000, user.Quota)
	assert.Equal(t, 5000, user.UsedQuota)
	assert.Equal(t, 95000, token.RemainQuota)
	assert.Equal(t, 5000, token.UsedQuota)

	fetch := fetchJimengDurabilityTask(handler, token, taskID)
	require.Equal(t, http.StatusOK, fetch.Code, fetch.Body.String())
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 5000, user.UsedQuota)
	assert.Equal(t, 5000, token.UsedQuota)
}

func TestJimengSecondaryRecoveryCleanupFailureCannotReplaySettlement(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	recoveryDirectory := t.TempDir()
	t.Setenv("JIMENG_RECOVERY_DIR", recoveryDirectory)
	var fetches atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("Action") == "CVSync2AsyncGetResult" {
			fetches.Add(1)
			_, _ = writer.Write([]byte(`{"code":10000,"data":{"status":"done"}}`))
			return
		}
		_, _ = writer.Write([]byte(`{"code":10000,"request_id":"cleanup-failure","data":{"task_id":"upstream-cleanup-failure"}}`))
	}))
	defer upstream.Close()

	const modelName = "jimeng-recovery-cleanup"
	handler, userID, token, _ := setupJimengDurabilityFixture(t, upstream.URL, modelName)
	response := submitJimengDurabilityRequest(handler, token, modelName)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	body := decodeBody(t, response)
	taskID := body["task_id"].(string)
	blockedRecoveryPath := filepath.Join(recoveryDirectory, taskID+".json")
	require.NoError(t, os.Mkdir(blockedRecoveryPath, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(blockedRecoveryPath, "block"), []byte("x"), 0o600))

	blockedFetch := fetchJimengDurabilityTask(handler, token, taskID)
	require.Equal(t, http.StatusOK, blockedFetch.Code, blockedFetch.Body.String())
	assert.Equal(t, int32(1), fetches.Load(), "secondary local cleanup must not gate database-backed polling")

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 5000, user.UsedQuota)
	assert.Equal(t, 5000, token.UsedQuota)
	fetch := fetchJimengDurabilityTask(handler, token, taskID)
	require.Equal(t, http.StatusOK, fetch.Code, fetch.Body.String())
	assert.Equal(t, int32(1), fetches.Load())
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 5000, user.UsedQuota)
	assert.Equal(t, 5000, token.UsedQuota)
}
