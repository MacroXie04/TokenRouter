package controller_test

import (
	"encoding/json"
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
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func TestPlaygroundChatCompletionsContract(t *testing.T) {
	var calls atomic.Int32
	var seenMu sync.Mutex
	var seenPath, seenAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		seenMu.Lock()
		seenPath = r.URL.Path
		seenAuthorization = r.Header.Get("Authorization")
		seenMu.Unlock()
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid test request", http.StatusBadRequest)
			return
		}
		if _, forwarded := request["group"]; forwarded {
			http.Error(w, "playground group leaked upstream", http.StatusBadRequest)
			return
		}
		if stream, _ := request["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			chunks := []string{
				`{"id":"chatcmpl-pg","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":null}]}`,
				`{"id":"chatcmpl-pg","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
			}
			for _, chunk := range chunks {
				_, _ = w.Write([]byte("data: " + chunk + "\n\n"))
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-pg","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	handler, do, userID := setupChannelRead(t, constant.RoleCommonUser)
	require.NoError(t, model.DB.AutoMigrate(&model.UserSubscription{}, &model.RelayQuotaReservationRecord{}))
	accessToken := "playground-dashboard-access-token"
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Updates(map[string]any{
		"group": "vip", "quota": 100000, "access_token": accessToken,
	}).Error)
	priority := int64(10)
	channel := model.Channel{
		Type: int(constant.ChannelTypeOpenAI), Key: "upstream-secret", Name: "playground-upstream",
		Status: constant.ChannelStatusEnabled, BaseURL: upstream.URL, Models: "playground-model", Group: "vip",
		Priority: &priority,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&[]model.Ability{
		{Group: "vip", Model: "playground-model", ChannelId: channel.Id, Enabled: true, Priority: &priority, Weight: 1},
		{Group: "staff", Model: "playground-model", ChannelId: channel.Id, Enabled: true, Priority: &priority, Weight: 1},
	}).Error)
	require.NoError(t, service.InitAbilityCache())
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{
		"playground-model": {Prompt: 1, Completion: 2},
	})
	service.SetGroupRatios(map[string]float64{"vip": 1.5, "staff": 2})
	require.NoError(t, setting.UpdateOption(setting.UserUsableGroupsOption, `{"vip":"VIP","staff":"Staff"}`))
	require.NoError(t, setting.UpdateOption(service.DataExportEnabledOption, "false"))
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
		service.ResetQuotaDataCache()
	})

	body := `{"model":"playground-model","group":"staff","messages":[{"role":"user","content":"ping"}]}`
	recorder := do(http.MethodPost, "/pg/chat/completions", body)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	response := decodeBody(t, recorder)
	assert.Equal(t, "chatcmpl-pg", response["id"])
	choices := response["choices"].([]any)
	assert.Equal(t, "pong", choices[0].(map[string]any)["message"].(map[string]any)["content"])
	seenMu.Lock()
	assert.Equal(t, "/v1/chat/completions", seenPath)
	assert.Equal(t, "Bearer upstream-secret", seenAuthorization)
	seenMu.Unlock()

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Less(t, user.Quota, 100000)
	assert.Greater(t, user.UsedQuota, 0)
	assert.Equal(t, 1, user.RequestCount)
	var consumption model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", service.LogTypeConsume).First(&consumption).Error)
	assert.Equal(t, "playground-staff", consumption.TokenName)
	assert.Equal(t, 0, consumption.TokenId)
	assert.Equal(t, "staff", consumption.Group)
	assert.Equal(t, channel.Id, consumption.ChannelId)
	var temporaryTokens int64
	require.NoError(t, model.DB.Model(&model.Token{}).Where("name = ?", "playground-staff").Count(&temporaryTokens).Error)
	assert.Zero(t, temporaryTokens, "playground token must remain request-local")

	streamBody := `{"model":"playground-model","stream":true,"messages":[{"role":"user","content":"ping"}]}`
	recorder = do(http.MethodPost, "/pg/chat/completions", streamBody)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, recorder.Body.String(), `"content":"pong"`)
	assert.Contains(t, recorder.Body.String(), "data: [DONE]")
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 2, user.RequestCount)
	var consumeCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", service.LogTypeConsume).Count(&consumeCount).Error)
	assert.Equal(t, int64(2), consumeCount)
	assert.Equal(t, int32(2), calls.Load())
	var latestConsumption model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", service.LogTypeConsume).Order("id DESC").First(&latestConsumption).Error)
	assert.Equal(t, "playground-vip", latestConsumption.TokenName)
	assert.Equal(t, "vip", latestConsumption.Group)

	forbidden := do(http.MethodPost, "/pg/chat/completions", `{"model":"playground-model","group":"blocked","messages":[]}`)
	require.Equal(t, http.StatusForbidden, forbidden.Code)
	forbiddenError := decodeBody(t, forbidden)["error"].(map[string]any)
	assert.Equal(t, "无权访问该分组", forbiddenError["message"])
	assert.Equal(t, "new_api_error", forbiddenError["type"])
	assert.Equal(t, "", forbiddenError["code"])
	assert.Equal(t, int32(2), calls.Load())

	invalidGroup := do(http.MethodPost, "/pg/chat/completions", `{"model":"playground-model","group":123,"messages":[]}`)
	require.Equal(t, http.StatusBadRequest, invalidGroup.Code)
	invalidGroupError := decodeBody(t, invalidGroup)["error"].(map[string]any)
	assert.Contains(t, invalidGroupError["message"], "无效的 Playground 请求")
	assert.Equal(t, "new_api_error", invalidGroupError["type"])
	assert.Equal(t, int32(2), calls.Load())

	invalid := do(http.MethodPost, "/pg/chat/completions", `{"messages":[]}`)
	require.Equal(t, http.StatusBadRequest, invalid.Code)
	assert.Equal(t, "缺少 model 字段", decodeBody(t, invalid)["error"].(map[string]any)["message"])
	assert.Equal(t, int32(2), calls.Load())

	patRequest := httptest.NewRequest(http.MethodPost, "/pg/chat/completions", strings.NewReader(body))
	patRequest.Header.Set("Content-Type", "application/json")
	patRequest.Header.Set("Authorization", "Bearer "+accessToken)
	patResponse := httptest.NewRecorder()
	handler.ServeHTTP(patResponse, patRequest)
	require.Equal(t, http.StatusInternalServerError, patResponse.Code, patResponse.Body.String())
	patError := decodeBody(t, patResponse)["error"].(map[string]any)
	assert.Equal(t, "暂不支持使用 access token", patError["message"])
	assert.Equal(t, "new_api_error", patError["type"])
	assert.Equal(t, "", patError["param"])
	assert.Equal(t, "access_denied", patError["code"])
	assert.Equal(t, int32(2), calls.Load())

	anonymousRequest := httptest.NewRequest(http.MethodPost, "/pg/chat/completions", strings.NewReader(body))
	anonymousRequest.Header.Set("Content-Type", "application/json")
	anonymousResponse := httptest.NewRecorder()
	handler.ServeHTTP(anonymousResponse, anonymousRequest)
	assert.Equal(t, http.StatusUnauthorized, anonymousResponse.Code)
	assert.Equal(t, false, decodeBody(t, anonymousResponse)["success"])
}
