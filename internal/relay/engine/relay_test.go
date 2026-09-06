package engine_test

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	operationssvc "github.com/tokenrouter/tokenrouter/internal/operations"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func setupRelayIntegration(t *testing.T, mockURL string) (string, int) {
	t.Helper()
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true") // the mock upstream listens on loopback
	httpx.InitSSRF()
	dsn := "file:" + filepath.Join(t.TempDir(), "relay.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{}, &model.PerfMetric{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{},
		&model.RelayQuotaReservationRecord{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init())

	user := model.User{Username: "relayuser", Password: "x", Role: roles.RoleCommonUser, Status: 1, Group: "default", Quota: 500000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)

	key := "sk-relaytestkey"
	token := model.Token{UserId: user.Id, Key: key, Status: billingsvc.TokenStatusEnabled, UnlimitedQuota: false, RemainQuota: 500000}
	require.NoError(t, model.DB.Create(&token).Error)

	w := uint(1)
	channel := model.Channel{Name: "mock", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "sk-upstream", Status: channelcatalog.ChannelStatusEnabled, BaseURL: mockURL, Models: "gpt-4", Group: "default", Weight: &w}
	require.NoError(t, model.DB.Create(&channel).Error)

	ability := model.Ability{Group: "default", Model: "gpt-4", ChannelId: channel.Id, Enabled: true, Weight: 1}
	require.NoError(t, model.DB.Create(&ability).Error)
	embedAbility := model.Ability{Group: "default", Model: "text-embedding-3-small", ChannelId: channel.Id, Enabled: true, Weight: 1}
	require.NoError(t, model.DB.Create(&embedAbility).Error)
	perfAbility := model.Ability{Group: "default", Model: "gpt-perf-e2e", ChannelId: channel.Id, Enabled: true, Weight: 1}
	require.NoError(t, model.DB.Create(&perfAbility).Error)
	providerContractAbility := model.Ability{Group: "default", Model: "gpt-provider-contract", ChannelId: channel.Id, Enabled: true, Weight: 1}
	require.NoError(t, model.DB.Create(&providerContractAbility).Error)
	require.NoError(t, channelssvc.InitAbilityCache())

	return key, user.Id
}

func TestRelayChatCompletionsEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true") // the mock upstream listens on loopback
	httpx.InitSSRF()
	// A mock OpenAI-compatible upstream.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		resp := map[string]any{
			"id": "chatcmpl-1", "object": "chat.completion", "created": 1, "model": "gpt-perf-e2e",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "Hello from mock"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 7, "completion_tokens": 2, "total_tokens": 9},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	key, userId := setupRelayIntegration(t, mock.URL)
	r := router.SetUpRouter()

	body := `{"model":"gpt-perf-e2e","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Hello from mock")

	// Quota was settled (7 prompt + 2 completion at $1/$3 per 1M -> 6 quota).
	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	assert.Less(t, user.Quota, 500000, "user quota must have been deducted")

	// A usage log was written.
	var count int64
	model.LOG_DB.Model(&model.Log{}).Where("user_id = ?", userId).Count(&count)
	assert.Equal(t, int64(1), count)

	metrics, err := operationssvc.QueryPerfMetrics("gpt-perf-e2e", "default", 24)
	require.NoError(t, err)
	require.Len(t, metrics.Groups, 1)
	assert.Equal(t, 100.0, metrics.Groups[0].SuccessRate)
	assert.Len(t, metrics.Groups[0].Series, 1)
}

func TestOpenAIImagesUsageAliasesTieredMultimodalSettlement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/images/generations", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"created":1710000000,
			"data":[{"url":"https://example.invalid/image.png"}],
			"usage":{
				"input_tokens":31,
				"output_tokens":29,
				"total_tokens":60,
				"input_tokens_details":{"text_tokens":23,"image_tokens":3,"audio_tokens":5},
				"output_tokens_details":{"text_tokens":11,"image_tokens":7,"audio_tokens":11}
			}
		}`))
	}))
	defer mock.Close()

	key, userID := setupRelayIntegration(t, mock.URL)
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"gpt-4": {Prompt: 2, Completion: 2}})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})
	require.NoError(t, setting.UpdateOptions(map[string]string{
		"ModelBillingMode": `{"gpt-4":"tiered_expr"}`,
		"ModelBillingExpr": `{"gpt-4":"p * 2 + c * 4 + img * 10 + ai * 14 + img_o * 18 + ao * 22"}`,
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(map[string]string{"ModelBillingMode": `{}`, "ModelBillingExpr": `{}`})
	})

	handler := router.SetUpRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"gpt-4","prompt":"","max_tokens":512,"n":1}`))
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
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	require.NoError(t, model.DB.First(&channel).Error)
	require.NoError(t, model.DB.First(&reservation).Error)
	require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 500_000-actualQuota, user.Quota)
	assert.Equal(t, actualQuota, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 500_000-actualQuota, token.RemainQuota,
		"the unused 233-quota hold is refunded after alias normalization")
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
	assert.Equal(t, billingsvc.BillingSourceWallet, other["billing_source"])
	assert.Equal(t, reservation.ReservationID, other["relay_reservation_id"])
}

func TestRelayRejectsInvalidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "relay.db")
	db, _ := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}, &model.RelayQuotaReservationRecord{})
	model.DB = db
	model.LOG_DB = db

	r := router.SetUpRouter()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4"}`))
	req.Header.Set("Authorization", "Bearer sk-nonexistent")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRelayEmbeddingsPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()

	var received map[string]any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/embeddings", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&received)
		resp := map[string]any{
			"object": "list",
			"data":   []map[string]any{{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2}}},
			"model":  "text-embedding-3-small",
			"usage":  map[string]any{"prompt_tokens": 4, "total_tokens": 4},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	key, _ := setupRelayIntegration(t, mock.URL)
	r := router.SetUpRouter()

	body := `{"model":"text-embedding-3-small","input":"hello world"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "embedding")
	// The mode-specific `input` field must survive passthrough.
	assert.Equal(t, "hello world", received["input"])
}

func TestRelayWebSocketProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	// Mock upstream WebSocket that echoes every message.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	key, _ := setupRelayIntegration(t, upstream.URL)
	r := router.SetUpRouter()
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(handlerDone)
		r.ServeHTTP(w, req)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/realtime?model=gpt-4"
	header := http.Header{"Authorization": []string{"Bearer " + key}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	require.NoError(t, err)
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Errorf("close realtime client: %v", closeErr)
		}
		select {
		case <-handlerDone:
		case <-time.After(3 * time.Second):
			t.Error("realtime handler did not terminate")
		}
	}()

	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("hello")))
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

func TestRelayRetriesToSecondChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	t.Setenv("RETRY_TIMES", "1")
	httpx.InitSSRF()

	// Channel A fails; channel B succeeds.
	failUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	okUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id": "c1", "object": "chat.completion", "created": 1, "model": "gpt-4",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer failUp.Close()
	defer okUp.Close()

	dsn := "file:" + filepath.Join(t.TempDir(), "relay.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{}, &model.PerfMetric{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}, &model.RelayQuotaReservationRecord{},
		&model.Option{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init())

	user := model.User{Username: "r", Password: "x", Role: roles.RoleCommonUser, Status: 1, Group: "default", Quota: 500000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	key := "sk-retry"
	token := model.Token{UserId: user.Id, Key: key, Status: billingsvc.TokenStatusEnabled, UnlimitedQuota: false, RemainQuota: 500000}
	require.NoError(t, model.DB.Create(&token).Error)

	w := uint(1)
	chA := model.Channel{Name: "a", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "k", Status: 1, BaseURL: failUp.URL, Weight: &w}
	chB := model.Channel{Name: "b", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "k", Status: 1, BaseURL: okUp.URL, Weight: &w}
	require.NoError(t, model.DB.Create(&chA).Error)
	require.NoError(t, model.DB.Create(&chB).Error)

	hi := int64(100)
	lo := int64(0)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-4", ChannelId: chA.Id, Enabled: true, Weight: 1, Priority: &hi}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-4", ChannelId: chB.Id, Enabled: true, Weight: 1, Priority: &lo}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())

	r := router.SetUpRouter()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	// The higher-priority channel fails; the retry must fall through to channel B.
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "ok")
}

// seedRelaySubscription gives the relay user an active subscription so the
// default subscription_first preference funds requests from it.
func seedRelaySubscription(t *testing.T, userId int, total int64) *model.UserSubscription {
	t.Helper()
	now := wallclock.NowTimestamp()
	plan := model.SubscriptionPlan{Title: "Relay Plan", PriceAmount: "10", Enabled: true,
		TotalAmount: total, DurationUnit: "day", DurationValue: 1}
	require.NoError(t, model.DB.Create(&plan).Error)
	sub := model.UserSubscription{UserId: userId, PlanId: plan.Id, AmountTotal: total,
		StartTime: now - 3600, EndTime: now + 86400, Status: billingsvc.SubscriptionStatusActive,
		AllowWalletOverflow: true, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, model.DB.Create(&sub).Error)
	return &sub
}

// TestRelaySubscriptionBillingEndToEnd: with an active subscription, the relay
// settles actual usage against the subscription — the wallet is untouched,
// lifetime usage counters still advance, and the consume log carries the
// subscription billing fields.
func TestRelaySubscriptionBillingEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id": "chatcmpl-sub", "object": "chat.completion", "created": 1, "model": "gpt-4",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "funded by subscription"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 7, "completion_tokens": 2, "total_tokens": 9},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	key, userId := setupRelayIntegration(t, mock.URL)
	sub := seedRelaySubscription(t, userId, 1000)
	r := router.SetUpRouter()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	// Wallet untouched; lifetime counters advanced (7 prompt + 2 completion at
	// $1/$3 per 1M -> 6 quota).
	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	assert.Equal(t, 500000, user.Quota, "wallet must not fund a subscription request")
	assert.Equal(t, 6, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)

	var gotSub model.UserSubscription
	require.NoError(t, model.DB.First(&gotSub, sub.Id).Error)
	assert.Equal(t, int64(6), gotSub.AmountUsed, "subscription settled to actual usage")

	var record model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("user_subscription_id = ?", sub.Id).First(&record).Error)
	assert.Equal(t, billingsvc.SubscriptionPreConsumeStatusSettled, record.Status)

	// The consume log carries the reference billing fields.
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userId, billingsvc.LogTypeConsume).
		First(&log).Error)
	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
	assert.Equal(t, "subscription", other["billing_source"])
	assert.Equal(t, float64(sub.Id), other["subscription_id"])
	assert.Equal(t, float64(1000), other["subscription_total"])
	assert.Equal(t, float64(6), other["subscription_used"])
	assert.Equal(t, float64(994), other["subscription_remain"])
	assert.Equal(t, float64(6), other["subscription_consumed"])
	assert.Equal(t, float64(0), other["wallet_quota_deducted"])
	assert.Greater(t, other["subscription_pre_consumed"], float64(0))
	_, hasPref := other["billing_preference"]
	assert.False(t, hasPref, "unset preference is omitted, not defaulted, in logs")
}

// TestRelaySubscriptionRefundOnUpstreamFailure: when every upstream attempt
// fails, the subscription reservation is refunded — usage returns to zero, the
// ledger row flips to refunded, and no consume log or counters are recorded.
func TestRelaySubscriptionRefundOnUpstreamFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("RETRY_TIMES", "0")
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mock.Close()

	key, userId := setupRelayIntegration(t, mock.URL)
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "gpt-failure", ChannelId: channel.Id, Enabled: true, Weight: 1,
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
	sub := seedRelaySubscription(t, userId, 1000)
	r := router.SetUpRouter()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-failure","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.GreaterOrEqual(t, w.Code, http.StatusInternalServerError, "body: %s", w.Body.String())

	var gotSub model.UserSubscription
	require.NoError(t, model.DB.First(&gotSub, sub.Id).Error)
	assert.Equal(t, int64(0), gotSub.AmountUsed, "reservation returned on failure")

	var record model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("user_subscription_id = ?", sub.Id).First(&record).Error)
	assert.Equal(t, billingsvc.SubscriptionPreConsumeStatusRefunded, record.Status)

	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	assert.Equal(t, 500000, user.Quota)
	assert.Equal(t, 0, user.UsedQuota, "no usage recorded for a failed request")
	assert.Equal(t, 0, user.RequestCount)

	var logs int64
	model.LOG_DB.Model(&model.Log{}).Where("user_id = ? AND type = ?", userId, billingsvc.LogTypeConsume).Count(&logs)
	assert.Zero(t, logs, "no consume log for a refunded request")

	metrics, err := operationssvc.QueryPerfMetrics("gpt-failure", "default", 24)
	require.NoError(t, err)
	require.Len(t, metrics.Groups, 1)
	assert.Equal(t, 0.0, metrics.Groups[0].SuccessRate)
}

func TestRelayAffinityOverridesPriorityAndSkipsRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	t.Setenv("RETRY_TIMES", "1")
	httpx.InitSSRF()

	var preferredCalls atomic.Int32
	var fallbackCalls atomic.Int32
	preferredUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		preferredCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	fallbackUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "fallback", "object": "chat.completion", "created": 1, "model": "affinity-model",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "fallback"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer preferredUpstream.Close()
	defer fallbackUpstream.Close()

	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "affinity-relay.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{},
		&model.Log{}, &model.PerfMetric{}, &model.Option{}, &model.SubscriptionPlan{},
		&model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}, &model.RelayQuotaReservationRecord{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init())
	previousAffinityOptions := map[string]string{
		setting.ChannelAffinityEnabledOption:           setting.GetOption(setting.ChannelAffinityEnabledOption),
		setting.ChannelAffinitySwitchOnSuccessOption:   setting.GetOption(setting.ChannelAffinitySwitchOnSuccessOption),
		setting.ChannelAffinityKeepOnDisabledOption:    setting.GetOption(setting.ChannelAffinityKeepOnDisabledOption),
		setting.ChannelAffinityMaxEntriesOption:        setting.GetOption(setting.ChannelAffinityMaxEntriesOption),
		setting.ChannelAffinityDefaultTTLSecondsOption: setting.GetOption(setting.ChannelAffinityDefaultTTLSecondsOption),
		setting.ChannelAffinityRulesOption:             setting.GetOption(setting.ChannelAffinityRulesOption),
	}
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateOptions(previousAffinityOptions))
		channelssvc.ClearChannelAffinityCacheAll()
	})

	user := model.User{Username: "affinity-user", Password: "x", Role: roles.RoleCommonUser, Status: 1, Group: "default", Quota: 500000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	key := "sk-affinity-relay"
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: key, Status: billingsvc.TokenStatusEnabled, RemainQuota: 500000}).Error)

	weight := uint(1)
	fallback := model.Channel{Name: "priority-fallback", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "k", Status: 1, BaseURL: fallbackUpstream.URL, Weight: &weight}
	preferred := model.Channel{Name: "affinity-preferred", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "k", Status: 1, BaseURL: preferredUpstream.URL, Weight: &weight}
	require.NoError(t, model.DB.Create(&fallback).Error)
	require.NoError(t, model.DB.Create(&preferred).Error)
	high, low := int64(100), int64(1)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "affinity-model", ChannelId: fallback.Id, Enabled: true, Priority: &high, Weight: 1}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "affinity-model", ChannelId: preferred.Id, Enabled: true, Priority: &low, Weight: 1}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())

	rules := []setting.ChannelAffinityRule{{
		Name: "relay affinity", ModelRegex: []string{"^affinity-model$"}, PathRegex: []string{"^/v1/chat/completions$"},
		KeySources: []setting.ChannelAffinityKeySource{{Type: "gjson", Path: "prompt_cache_key"}},
		TTLSeconds: 60, SkipRetryOnFailure: true, IncludeRuleName: true, IncludeUsingGroup: true,
	}}
	encodedRules, err := jsonutil.Marshal(rules)
	require.NoError(t, err)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ChannelAffinityEnabledOption: "true", setting.ChannelAffinityRulesOption: string(encodedRules),
	}))
	channelssvc.ClearChannelAffinityCacheAll()

	body := []byte(`{"model":"affinity-model","prompt_cache_key":"stable-thread","messages":[{"role":"user","content":"hi"}]}`)
	seedRecorder := httptest.NewRecorder()
	seedContext, _ := gin.CreateTestContext(seedRecorder)
	seedContext.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	_, found := channelssvc.GetPreferredChannelByAffinity(seedContext, "affinity-model", "default", body)
	assert.False(t, found)
	channelssvc.RecordChannelAffinity(seedContext, preferred.Id, preferred.Id)

	handler := router.SetUpRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
	assert.EqualValues(t, 1, preferredCalls.Load(), "the lower-priority affinity channel must be selected")
	assert.Zero(t, fallbackCalls.Load(), "skip_retry_on_failure must suppress fallback after an affinity failure")
}
