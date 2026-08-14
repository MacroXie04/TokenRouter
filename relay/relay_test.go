package relay_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
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

func setupRelayIntegration(t *testing.T, mockURL string) (string, int) {
	t.Helper()
	t.Setenv("SSRF_DISABLE", "true") // the mock upstream listens on loopback
	common.InitSSRF()
	dsn := "file:" + filepath.Join(t.TempDir(), "relay.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{}, &model.PerfMetric{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}))
	model.DB = db
	model.LOG_DB = db

	user := model.User{Username: "relayuser", Password: "x", Role: constant.RoleCommonUser, Status: 1, Group: "default", Quota: 500000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)

	key := "sk-relaytestkey"
	token := model.Token{UserId: user.Id, Key: key, Status: service.TokenStatusEnabled, UnlimitedQuota: false, RemainQuota: 500000}
	require.NoError(t, model.DB.Create(&token).Error)

	w := uint(1)
	channel := model.Channel{Name: "mock", Type: int(constant.ChannelTypeOpenAI), Key: "sk-upstream", Status: constant.ChannelStatusEnabled, BaseURL: mockURL, Models: "gpt-4", Group: "default", Weight: &w}
	require.NoError(t, model.DB.Create(&channel).Error)

	ability := model.Ability{Group: "default", Model: "gpt-4", ChannelId: channel.Id, Enabled: true, Weight: 1}
	require.NoError(t, model.DB.Create(&ability).Error)
	embedAbility := model.Ability{Group: "default", Model: "text-embedding-3-small", ChannelId: channel.Id, Enabled: true, Weight: 1}
	require.NoError(t, model.DB.Create(&embedAbility).Error)
	perfAbility := model.Ability{Group: "default", Model: "gpt-perf-e2e", ChannelId: channel.Id, Enabled: true, Weight: 1}
	require.NoError(t, model.DB.Create(&perfAbility).Error)
	require.NoError(t, service.InitAbilityCache())

	return key, user.Id
}

func TestRelayChatCompletionsEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("SSRF_DISABLE", "true") // the mock upstream listens on loopback
	common.InitSSRF()
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

	metrics, err := service.QueryPerfMetrics("gpt-perf-e2e", "default", 24)
	require.NoError(t, err)
	require.Len(t, metrics.Groups, 1)
	assert.Equal(t, 100.0, metrics.Groups[0].SuccessRate)
	assert.Len(t, metrics.Groups[0].Series, 1)
}

func TestRelayRejectsInvalidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "relay.db")
	db, _ := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{})
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
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()

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
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()

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
	server := httptest.NewServer(r)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/realtime?model=gpt-4"
	header := http.Header{"Authorization": []string{"Bearer " + key}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	require.NoError(t, err)
	defer conn.Close()

	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("hello")))
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

func TestRelayRetriesToSecondChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("SSRF_DISABLE", "true")
	t.Setenv("RETRY_TIMES", "1")
	common.InitSSRF()

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
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}))
	model.DB = db
	model.LOG_DB = db

	user := model.User{Username: "r", Password: "x", Role: constant.RoleCommonUser, Status: 1, Group: "default", Quota: 500000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	key := "sk-retry"
	token := model.Token{UserId: user.Id, Key: key, Status: service.TokenStatusEnabled, UnlimitedQuota: false, RemainQuota: 500000}
	require.NoError(t, model.DB.Create(&token).Error)

	w := uint(1)
	chA := model.Channel{Name: "a", Type: int(constant.ChannelTypeOpenAI), Key: "k", Status: 1, BaseURL: failUp.URL, Weight: &w}
	chB := model.Channel{Name: "b", Type: int(constant.ChannelTypeOpenAI), Key: "k", Status: 1, BaseURL: okUp.URL, Weight: &w}
	require.NoError(t, model.DB.Create(&chA).Error)
	require.NoError(t, model.DB.Create(&chB).Error)

	hi := int64(100)
	lo := int64(0)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-4", ChannelId: chA.Id, Enabled: true, Weight: 1, Priority: &hi}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-4", ChannelId: chB.Id, Enabled: true, Weight: 1, Priority: &lo}).Error)
	require.NoError(t, service.InitAbilityCache())

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
	now := common.NowTimestamp()
	plan := model.SubscriptionPlan{Title: "Relay Plan", PriceAmount: "10", Enabled: true,
		TotalAmount: total, DurationUnit: "day", DurationValue: 1}
	require.NoError(t, model.DB.Create(&plan).Error)
	sub := model.UserSubscription{UserId: userId, PlanId: plan.Id, AmountTotal: total,
		StartTime: now - 3600, EndTime: now + 86400, Status: service.SubscriptionStatusActive,
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
	assert.Equal(t, service.SubscriptionPreConsumeStatusConsumed, record.Status)

	// The consume log carries the reference billing fields.
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userId, service.LogTypeConsume).
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
	require.NoError(t, service.InitAbilityCache())
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
	assert.Equal(t, service.SubscriptionPreConsumeStatusRefunded, record.Status)

	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	assert.Equal(t, 500000, user.Quota)
	assert.Equal(t, 0, user.UsedQuota, "no usage recorded for a failed request")
	assert.Equal(t, 0, user.RequestCount)

	var logs int64
	model.LOG_DB.Model(&model.Log{}).Where("user_id = ? AND type = ?", userId, service.LogTypeConsume).Count(&logs)
	assert.Zero(t, logs, "no consume log for a refunded request")

	metrics, err := service.QueryPerfMetrics("gpt-failure", "default", 24)
	require.NoError(t, err)
	require.Len(t, metrics.Groups, 1)
	assert.Equal(t, 0.0, metrics.Groups[0].SuccessRate)
}

func TestRelayAffinityOverridesPriorityAndSkipsRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("SSRF_DISABLE", "true")
	t.Setenv("RETRY_TIMES", "1")
	common.InitSSRF()

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
		&model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}))
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
		service.ClearChannelAffinityCacheAll()
	})

	user := model.User{Username: "affinity-user", Password: "x", Role: constant.RoleCommonUser, Status: 1, Group: "default", Quota: 500000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	key := "sk-affinity-relay"
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: key, Status: service.TokenStatusEnabled, RemainQuota: 500000}).Error)

	weight := uint(1)
	fallback := model.Channel{Name: "priority-fallback", Type: int(constant.ChannelTypeOpenAI), Key: "k", Status: 1, BaseURL: fallbackUpstream.URL, Weight: &weight}
	preferred := model.Channel{Name: "affinity-preferred", Type: int(constant.ChannelTypeOpenAI), Key: "k", Status: 1, BaseURL: preferredUpstream.URL, Weight: &weight}
	require.NoError(t, model.DB.Create(&fallback).Error)
	require.NoError(t, model.DB.Create(&preferred).Error)
	high, low := int64(100), int64(1)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "affinity-model", ChannelId: fallback.Id, Enabled: true, Priority: &high, Weight: 1}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "affinity-model", ChannelId: preferred.Id, Enabled: true, Priority: &low, Weight: 1}).Error)
	require.NoError(t, service.InitAbilityCache())

	rules := []setting.ChannelAffinityRule{{
		Name: "relay affinity", ModelRegex: []string{"^affinity-model$"}, PathRegex: []string{"^/v1/chat/completions$"},
		KeySources: []setting.ChannelAffinityKeySource{{Type: "gjson", Path: "prompt_cache_key"}},
		TTLSeconds: 60, SkipRetryOnFailure: true, IncludeRuleName: true, IncludeUsingGroup: true,
	}}
	encodedRules, err := common.Marshal(rules)
	require.NoError(t, err)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ChannelAffinityEnabledOption: "true", setting.ChannelAffinityRulesOption: string(encodedRules),
	}))
	service.ClearChannelAffinityCacheAll()

	body := []byte(`{"model":"affinity-model","prompt_cache_key":"stable-thread","messages":[{"role":"user","content":"hi"}]}`)
	seedRecorder := httptest.NewRecorder()
	seedContext, _ := gin.CreateTestContext(seedRecorder)
	seedContext.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	_, found := service.GetPreferredChannelByAffinity(seedContext, "affinity-model", "default", body)
	assert.False(t, found)
	service.RecordChannelAffinity(seedContext, preferred.Id, preferred.Id)

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
