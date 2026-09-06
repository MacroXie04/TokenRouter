package relay_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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

// setupChannelIntegration creates a fresh relay DB with one enabled channel of
// the given type serving one model.
func setupChannelIntegration(t *testing.T, mockURL string, channelType constant.ChannelType, modelName string) string {
	t.Helper()
	t.Cleanup(common.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()
	dsn := "file:" + filepath.Join(t.TempDir(), "relay.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{},
		&model.RelayQuotaReservationRecord{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init())

	user := model.User{Username: "guser", Password: "x", Role: constant.RoleCommonUser, Status: 1, Group: "default", Quota: 500000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)

	key := "sk-geminitest"
	token := model.Token{UserId: user.Id, Key: key, Status: service.TokenStatusEnabled, UnlimitedQuota: false, RemainQuota: 500000}
	require.NoError(t, model.DB.Create(&token).Error)

	w := uint(1)
	channel := model.Channel{Name: "mock", Type: int(channelType), Key: "sk-upstream", Status: constant.ChannelStatusEnabled, BaseURL: mockURL, Models: modelName, Group: "default", Weight: &w}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: modelName, ChannelId: channel.Id, Enabled: true, Weight: 1}).Error)
	require.NoError(t, service.InitAbilityCache())
	return key
}

func TestGeminiNativeTieredMultimodalUsageSettlesExactAccounting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const modelName = "gemini-multimodal-billing"
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1beta/models/"+modelName+":generateContent", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP","index":0}],
			"usageMetadata":{
				"promptTokenCount":31,
				"candidatesTokenCount":29,
				"totalTokenCount":60,
				"promptTokensDetails":[{"modality":"TEXT","tokenCount":23},{"modality":"IMAGE","tokenCount":3},{"modality":"AUDIO","tokenCount":5}],
				"candidatesTokensDetails":[{"modality":"TEXT","tokenCount":11},{"modality":"IMAGE","tokenCount":7},{"modality":"AUDIO","tokenCount":11}]
			}
		}`))
	}))
	defer mock.Close()

	key := setupChannelIntegration(t, mock.URL, constant.ChannelTypeGemini, modelName)
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{modelName: {Prompt: 2, Completion: 2}})
	service.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
	})
	require.NoError(t, setting.UpdateOptions(map[string]string{
		"ModelBillingMode": `{"` + modelName + `":"tiered_expr"}`,
		"ModelBillingExpr": `{"` + modelName + `":"p * 2 + c * 4 + img * 10 + ai * 14 + img_o * 18 + ao * 22"}`,
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(map[string]string{"ModelBillingMode": `{}`, "ModelBillingExpr": `{}`})
	})

	handler := router.SetUpRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+modelName+":generateContent",
		strings.NewReader(`{"contents":[],"generationConfig":{"maxOutputTokens":512}}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	const (
		reservedQuota = 512
		actualQuota   = 279
	)
	var user model.User
	var token model.Token
	var channel model.Channel
	var reservation model.RelayQuotaReservationRecord
	var log model.Log
	require.NoError(t, model.DB.Where("username = ?", "guser").First(&user).Error)
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	require.NoError(t, model.DB.First(&channel).Error)
	require.NoError(t, model.DB.First(&reservation).Error)
	require.NoError(t, model.LOG_DB.Where("type = ?", service.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 500_000-actualQuota, user.Quota)
	assert.Equal(t, actualQuota, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 500_000-actualQuota, token.RemainQuota)
	assert.Equal(t, actualQuota, token.UsedQuota)
	assert.Equal(t, int64(actualQuota), channel.UsedQuota)
	assert.Equal(t, reservedQuota, reservation.RequestedQuota)
	assert.Equal(t, reservedQuota, reservation.ReservedQuota)
	assert.Equal(t, reservedQuota, reservation.TokenReserved)
	assert.Equal(t, actualQuota, reservation.ActualQuota)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, 31, log.PromptTokens)
	assert.Equal(t, 29, log.CompletionTokens)
	assert.Equal(t, actualQuota, log.Quota)
	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
	assert.Equal(t, service.BillingSourceWallet, other["billing_source"])
	assert.Equal(t, reservation.ReservationID, other["relay_reservation_id"])
}

func TestGeminiNativePassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const modelName = "gemini-2.0-flash"
	var gotPath, gotKey, gotBody string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		resp := map[string]any{
			"candidates": []map[string]any{{
				"content":      map[string]any{"role": "model", "parts": []map[string]any{{"text": "Hello from gemini"}}},
				"finishReason": "STOP",
				"index":        0,
			}},
			"usageMetadata": map[string]any{
				"promptTokenCount": 7, "candidatesTokenCount": 3, "totalTokenCount": 10,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	key := setupChannelIntegration(t, mock.URL, constant.ChannelTypeGemini, modelName)
	r := router.SetUpRouter()

	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"temperature":0.5}}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+modelName+":generateContent", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "/v1beta/models/"+modelName+":generateContent", gotPath)
	assert.Equal(t, "sk-upstream", gotKey)
	assert.JSONEq(t, body, gotBody)

	// Native gemini response passes back verbatim (not converted to OpenAI).
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Contains(t, out, "candidates")
	assert.NotContains(t, out, "choices")

	// Usage was settled from the native usageMetadata.
	var user model.User
	require.NoError(t, model.DB.Where("username = ?", "guser").First(&user).Error)
	assert.Less(t, user.Quota, 500000, "user quota must have been deducted")

	var count int64
	model.LOG_DB.Model(&model.Log{}).Where("user_id = ?", user.Id).Count(&count)
	assert.Equal(t, int64(1), count)
}

func TestGeminiNativeAppliesVersionThinkingAndFunctionResponsePolicies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		clientModel   = "gemini-policy-thinking"
		upstreamModel = "gemini-policy"
	)
	var gotPath string
	var gotBody map[string]any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		require.NoError(t, json.NewDecoder(request.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"policy reply"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}`))
	}))
	defer mock.Close()

	key := setupChannelIntegration(t, mock.URL, constant.ChannelTypeGemini, clientModel)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.GeminiVersionSettingsOption:          `{"default":"v1beta","gemini-policy":"v1"}`,
		setting.GeminiThinkingAdapterEnabledOption:   "true",
		setting.GeminiThinkingBudgetPercentageOption: "0.5",
	}))
	body := `{
		"model":"models/gemini-policy-thinking",
		"contents":[{"role":"user","parts":[{"text":"hi"},{"functionResponse":{"id":"call-1","name":"lookup","response":{"ok":true}},"futurePart":7}]}],
		"generationConfig":{"maxOutputTokens":2000,"futureGeneration":"kept"},
		"futureTopLevel":{"enabled":true}
	}`
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+clientModel+":generateContent", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "/v1/models/"+upstreamModel+":generateContent", gotPath)
	assert.Equal(t, "models/"+upstreamModel, gotBody["model"])
	assert.Equal(t, map[string]any{"enabled": true}, gotBody["futureTopLevel"])
	generation := gotBody["generationConfig"].(map[string]any)
	assert.Equal(t, "kept", generation["futureGeneration"])
	thinking := generation["thinkingConfig"].(map[string]any)
	assert.Equal(t, float64(1000), thinking["thinkingBudget"])
	assert.Equal(t, true, thinking["includeThoughts"])
	part := gotBody["contents"].([]any)[0].(map[string]any)["parts"].([]any)[1].(map[string]any)
	assert.Equal(t, float64(7), part["futurePart"])
	assert.NotContains(t, part["functionResponse"].(map[string]any), "id")
}

func TestGeminiNativeV1RoutePassthroughAndAccounting(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const modelName = "gemini-v1-route"
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{modelName: {Prompt: 1, Completion: 3}})
	service.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
	})

	var gotPath, gotKey string
	var gotBody map[string]any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotKey = request.Header.Get("x-goog-api-key")
		require.NoError(t, json.NewDecoder(request.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"v1 route reply"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":2,"totalTokenCount":11}}`))
	}))
	defer mock.Close()

	key := setupChannelIntegration(t, mock.URL, constant.ChannelTypeGemini, modelName)
	handler := router.SetUpRouter()
	body := `{"contents":[{"role":"user","parts":[{"text":"exercise the v1 alias"}]}],"generationConfig":{"temperature":0.25}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/models/"+modelName+":generateContent", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "/v1beta/models/"+modelName+":generateContent", gotPath)
	assert.Equal(t, "sk-upstream", gotKey)
	gotBodyJSON, err := json.Marshal(gotBody)
	require.NoError(t, err)
	assert.JSONEq(t, body, string(gotBodyJSON))
	assert.Contains(t, recorder.Body.String(), "v1 route reply")
	assert.Contains(t, recorder.Body.String(), `"candidates"`)
	assert.NotContains(t, recorder.Body.String(), `"choices"`)

	var user model.User
	require.NoError(t, model.DB.Where("username = ?", "guser").First(&user).Error)
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", user.Id, service.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 9, log.PromptTokens)
	assert.Equal(t, 2, log.CompletionTokens)
	assert.Greater(t, log.Quota, 0)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", user.Id).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, log.Quota, reservation.ActualQuota)
}

func TestGeminiNativeStreamPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const modelName = "gemini-2.0-flash"
	var gotPath, gotQuery string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hello \"}]},\"index\":0}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"stream\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2,\"totalTokenCount\":7}}\n\n"))
	}))
	defer mock.Close()

	key := setupChannelIntegration(t, mock.URL, constant.ChannelTypeGemini, modelName)
	r := router.SetUpRouter()

	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+modelName+":streamGenerateContent?alt=sse", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/v1beta/models/"+modelName+":streamGenerateContent", gotPath)
	assert.Equal(t, "alt=sse", gotQuery)
	assert.Contains(t, rec.Body.String(), "Hello ")
	assert.Contains(t, rec.Body.String(), "stream")
	// The stream is passed through verbatim with native gemini chunks.
	assert.Contains(t, rec.Body.String(), `"candidates"`)
	assert.NotContains(t, rec.Body.String(), `"choices"`)
}

func TestGeminiNativeViaOpenAIChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const modelName = "gemini-2.0-flash"
	var gotPath, gotBody string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		resp := map[string]any{
			"id": "chatcmpl-1", "object": "chat.completion", "created": 1, "model": modelName,
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "converted reply"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	key := setupChannelIntegration(t, mock.URL, constant.ChannelTypeOpenAI, modelName)
	r := router.SetUpRouter()

	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+modelName+":generateContent", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	// The OpenAI channel receives an OpenAI-format request.
	assert.Equal(t, "/v1/chat/completions", gotPath)
	var sent map[string]any
	require.NoError(t, json.Unmarshal([]byte(gotBody), &sent))
	assert.Contains(t, sent, "messages")

	// The client receives a native gemini response.
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Contains(t, out, "candidates")
	assert.NotContains(t, out, "choices")
	cand := out["candidates"].([]any)[0].(map[string]any)
	content := cand["content"].(map[string]any)
	parts := content["parts"].([]any)[0].(map[string]any)
	assert.Equal(t, "converted reply", parts["text"])
}

func TestGeminiNativeRejectsMismatchedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const modelName = "gemini-2.0-flash"

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mock.Close()
	key := setupChannelIntegration(t, mock.URL, constant.ChannelTypeGemini, modelName)
	r := router.SetUpRouter()

	body := `{"model":"models/some-other-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+modelName+":generateContent", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGeminiModelListShapes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mock.Close()
	key, _ := setupRelayIntegration(t, mock.URL)
	r := router.SetUpRouter()

	// OpenAI shape by default.
	rec := surfaceRequest(t, r, http.MethodGet, "/v1/models", key)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"object":"list"`)
	assert.Contains(t, rec.Body.String(), `"object":"model"`)

	// Gemini clients get the gemini shape (x-goog-api-key header).
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("x-goog-api-key", "AIza-test")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req)
	require.Equal(t, http.StatusOK, rec2.Code)
	assert.Contains(t, rec2.Body.String(), `"models":`)
	assert.Contains(t, rec2.Body.String(), `"nextPageToken"`)

	// /v1beta/models is always the gemini shape.
	rec3 := surfaceRequest(t, r, http.MethodGet, "/v1beta/models", key)
	require.Equal(t, http.StatusOK, rec3.Code)
	assert.Contains(t, rec3.Body.String(), `"models":`)
	assert.Contains(t, rec3.Body.String(), `"displayName"`)

	// /v1beta/openai/models is always the OpenAI shape.
	rec4 := surfaceRequest(t, r, http.MethodGet, "/v1beta/openai/models", key)
	require.Equal(t, http.StatusOK, rec4.Code)
	assert.Contains(t, rec4.Body.String(), `"object":"list"`)
}

func TestRelayResponsesCompactEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotPath string
	var gotBody map[string]any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		resp := map[string]any{
			"id": "cmpt-1", "object": "responses.compaction", "created_at": 1,
			"output": []map[string]any{},
			"usage":  map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	key, userId := setupRelayIntegration(t, mock.URL)
	r := router.SetUpRouter()

	body := `{"model":"gpt-4","previous_response_id":"resp-1","input":"compact this"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "/v1/responses/compact", gotPath)
	assert.Equal(t, "resp-1", gotBody["previous_response_id"])
	// The compaction envelope passes through unchanged.
	assert.Contains(t, rec.Body.String(), `"responses.compaction"`)

	// Usage from input_tokens/output_tokens was settled.
	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	assert.Less(t, user.Quota, 500000, "user quota must have been deducted")
}

func TestRelayResponsesCompactErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "compaction failed", "type": "invalid_request_error", "code": "bad_previous_response_id"},
		})
	}))
	defer mock.Close()

	key, _ := setupRelayIntegration(t, mock.URL)
	r := router.SetUpRouter()

	body := `{"model":"gpt-4","previous_response_id":"resp-bad","input":"compact this"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "compaction failed")
}

func TestRelayAlphaSearchPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotPath, gotCT string
	var gotBody map[string]any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"title": "hit"}}})
	}))
	defer mock.Close()

	key := setupChannelIntegration(t, mock.URL, constant.ChannelTypeSub2API, "gpt-4")
	r := router.SetUpRouter()

	body := `{"model":"gpt-4","query":"latest news","web_search_options":{"user_location":{"approximate":{"city":"sf"}}}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "/v1/alpha/search", gotPath)
	assert.Equal(t, "application/json", gotCT)
	// Unknown fields (web_search_options) survive passthrough.
	assert.Contains(t, gotBody, "web_search_options")
	assert.Contains(t, rec.Body.String(), "hit")
}

func TestRelayAlphaSearchChannelGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	defer mock.Close()

	// A plain OpenAI channel does not support the standalone alpha search
	// endpoint; the relay must not forward the request to it.
	key := setupChannelIntegration(t, mock.URL, constant.ChannelTypeOpenAI, "gpt-4")
	r := router.SetUpRouter()

	body := `{"model":"gpt-4","query":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "does not support /v1/alpha/search")
}

func TestRelayEnginesEmbeddingsViaGemini(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const modelName = "text-embedding-004"
	var gotPath string
	var gotBody map[string]any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embeddings": []map[string]any{{"values": []float64{0.1, 0.2}}},
		})
	}))
	defer mock.Close()

	key := setupChannelIntegration(t, mock.URL, constant.ChannelTypeGemini, modelName)
	r := router.SetUpRouter()

	body := `{"model":"text-embedding-004","input":"hello world"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/engines/"+modelName+"/embeddings", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "/v1beta/models/"+modelName+":batchEmbedContents", gotPath)
	// The OpenAI embedding request became a batch gemini payload.
	reqs, ok := gotBody["requests"].([]any)
	require.True(t, ok, "payload should carry a requests array, got: %v", gotBody)
	first := reqs[0].(map[string]any)
	assert.Equal(t, "models/"+modelName, first["model"])
	content := first["content"].(map[string]any)
	parts := content["parts"].([]any)[0].(map[string]any)
	assert.Equal(t, "hello world", parts["text"])

	// The client receives the OpenAI embeddings list shape.
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "list", out["object"])
	data := out["data"].([]any)
	assert.Len(t, data, 1)
	assert.Equal(t, []any{0.1, 0.2}, data[0].(map[string]any)["embedding"])
}

func TestRelayNotFoundSurface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mock.Close()
	key, _ := setupRelayIntegration(t, mock.URL)
	r := router.SetUpRouter()

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/definitely-not-a-route"},
		{http.MethodGet, "/api/definitely-not-a-route"},
		{http.MethodGet, "/assets/missing.png"},
	} {
		rec := surfaceRequest(t, r, tc.method, tc.path, key)
		require.Equal(t, http.StatusNotFound, rec.Code, "%s %s", tc.method, tc.path)
		var body struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "%s %s: %s", tc.method, tc.path, rec.Body.String())
		assert.Contains(t, body.Error.Message, "Invalid URL")
		assert.Equal(t, "invalid_request_error", body.Error.Type)
	}
}
