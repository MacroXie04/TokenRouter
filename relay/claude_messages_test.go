package relay_test

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
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func setupClaudeRelay(t *testing.T, mockURL string, channelType int, modelName string) (string, int) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Cleanup(common.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()
	dsn := "file:" + filepath.Join(t.TempDir(), "claude.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{}, &model.PerfMetric{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}, &model.RelayQuotaReservationRecord{},
		&model.Option{}))
	model.DB = db
	model.LOG_DB = db

	user := model.User{Username: "claudeuser", Password: "x", Role: constant.RoleCommonUser, Status: 1, Group: "default", Quota: 500000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	key := "sk-claudetest"
	token := model.Token{UserId: user.Id, Key: key, Status: service.TokenStatusEnabled, UnlimitedQuota: false, RemainQuota: 500000}
	require.NoError(t, model.DB.Create(&token).Error)

	w := uint(1)
	channel := model.Channel{Name: "mock", Type: channelType, Key: "sk-upstream", Status: constant.ChannelStatusEnabled, BaseURL: mockURL, Models: modelName, Group: "default", Weight: &w}
	require.NoError(t, model.DB.Create(&channel).Error)
	ability := model.Ability{Group: "default", Model: modelName, ChannelId: channel.Id, Enabled: true, Weight: 1}
	require.NoError(t, model.DB.Create(&ability).Error)
	require.NoError(t, service.InitAbilityCache())
	return key, user.Id
}

func claudeRelayRequest(t *testing.T, r http.Handler, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestClaudeMessagesViaOpenAIChannelNonStream(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		resp := map[string]any{
			"id": "chatcmpl-2", "object": "chat.completion", "created": 1, "model": "claude-3-5-sonnet-20241022",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "Bonjour"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	key, userId := setupClaudeRelay(t, mock.URL, int(constant.ChannelTypeOpenAI), "claude-3-5-sonnet-20241022")
	r := router.SetUpRouter()

	body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`
	rec := claudeRelayRequest(t, r, key, body)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var out struct {
		Type       string `json:"type"`
		Role       string `json:"role"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "message", out.Type)
	assert.Equal(t, "assistant", out.Role)
	assert.Equal(t, "end_turn", out.StopReason)
	require.Len(t, out.Content, 1)
	assert.Equal(t, "Bonjour", out.Content[0].Text)
	require.NotNil(t, out.Usage)
	assert.Equal(t, 5, out.Usage.InputTokens)
	assert.Equal(t, 1, out.Usage.OutputTokens)

	// Settlement happened (quota deducted).
	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	assert.Less(t, user.Quota, 500000)
}

func TestClaudeMessagesViaOpenAIChannelStream(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		chunks := []string{
			`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`,
		}
		time.Sleep(20 * time.Millisecond)
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
			fl.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		fl.Flush()
	}))
	defer mock.Close()

	key, _ := setupClaudeRelay(t, mock.URL, int(constant.ChannelTypeOpenAI), "claude-3-5-sonnet-20241022")
	r := router.SetUpRouter()

	body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"hello"}]}`
	rec := claudeRelayRequest(t, r, key, body)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")

	sse := rec.Body.String()
	assert.Contains(t, sse, `event: message_start`)
	assert.Contains(t, sse, `"type":"message_start"`)
	assert.Contains(t, sse, `"type":"content_block_delta"`)
	assert.Contains(t, sse, `"text":"Hel"`)
	assert.Contains(t, sse, `"text":"lo"`)
	assert.Contains(t, sse, `"type":"content_block_stop"`)
	assert.Contains(t, sse, `"stop_reason":"end_turn"`)
	assert.Contains(t, sse, `"type":"message_stop"`)
	assert.NotContains(t, sse, "chat.completion.chunk", "Claude clients must see only Claude events")

	metrics, err := service.QueryPerfMetrics("claude-3-5-sonnet-20241022", "default", 24)
	require.NoError(t, err)
	require.Len(t, metrics.Groups, 1)
	assert.GreaterOrEqual(t, metrics.Groups[0].AvgTtftMs, int64(15), "stream first-token latency is recorded")
}

func TestClaudeMessagesNativeAnthropicPassthrough(t *testing.T) {
	var gotPath, gotVersion, gotModel string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotVersion = r.Header.Get("anthropic-version")
		var request protocolkit.ClaudeRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		gotModel = request.Model
		resp := map[string]any{
			"id": "msg_01", "type": "message", "role": "assistant", "model": "claude-3-5-sonnet-20241022",
			"content":     []map[string]any{{"type": "text", "text": "native reply"}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 4, "output_tokens": 2},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	key, _ := setupClaudeRelay(t, mock.URL, int(constant.ChannelTypeAnthropic), "claude-3-5-sonnet-20241022")
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "mock").
		Update("model_mapping", `{"claude-3-5-sonnet-20241022":"claude-upstream-deployment"}`).Error)
	r := router.SetUpRouter()

	body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`
	rec := claudeRelayRequest(t, r, key, body)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "/v1/messages", gotPath, "native path must hit the Anthropic Messages endpoint")
	assert.Equal(t, "2023-06-01", gotVersion)
	assert.Equal(t, "claude-upstream-deployment", gotModel)
	assert.Contains(t, rec.Body.String(), "native reply")
}

func TestClaudeMessagesNativeAppliesHeadersDefaultsAndThinkingPolicy(t *testing.T) {
	const (
		clientModel   = "claude-policy-thinking"
		upstreamModel = "claude-policy"
	)
	var gotHeader, gotPolicyHeader string
	var gotRequest protocolkit.ClaudeRequest
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		gotHeader = request.Header.Get("anthropic-beta")
		gotPolicyHeader = request.Header.Get("x-policy")
		require.NoError(t, json.NewDecoder(request.Body).Decode(&gotRequest))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_policy", "type": "message", "role": "assistant", "model": upstreamModel,
			"content": []map[string]any{{"type": "text", "text": "configured"}}, "stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": 4, "output_tokens": 2},
		})
	}))
	defer mock.Close()

	key, _ := setupClaudeRelay(t, mock.URL, int(constant.ChannelTypeAnthropic), clientModel)
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ClaudeModelHeadersSettingsOption: `{"claude-policy-thinking":{"Anthropic-Beta":["token-efficient-tools"],"X-Policy":["enabled"]}}`,
		setting.ClaudeDefaultMaxTokensOption:     `{"default":8192,"claude-policy":4096}`,
	}))
	recorder := claudeRelayRequest(t, router.SetUpRouter(), key,
		`{"model":"claude-policy-thinking","messages":[{"role":"user","content":"hello"}]}`)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "token-efficient-tools", gotHeader)
	assert.Equal(t, "enabled", gotPolicyHeader)
	assert.Equal(t, upstreamModel, gotRequest.Model)
	assert.Equal(t, 4096, gotRequest.MaxTokens)
	require.NotNil(t, gotRequest.Thinking)
	assert.Equal(t, "enabled", gotRequest.Thinking.Type)
	assert.Equal(t, 3276, gotRequest.Thinking.BudgetTokens)
	require.NotNil(t, gotRequest.Temperature)
	assert.Equal(t, 1.0, *gotRequest.Temperature)
}

func TestClaudeMessagesNativePreservesConfiguredZeroDefaultMaxTokens(t *testing.T) {
	const modelName = "claude-cache"
	var gotRequest protocolkit.ClaudeRequest
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.NoError(t, json.NewDecoder(request.Body).Decode(&gotRequest))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_cache", "type": "message", "role": "assistant", "model": modelName,
			"content": []map[string]any{{"type": "text", "text": "warmed"}}, "stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 0},
		})
	}))
	defer mock.Close()

	key, _ := setupClaudeRelay(t, mock.URL, int(constant.ChannelTypeAnthropic), modelName)
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOption(setting.ClaudeDefaultMaxTokensOption,
		`{"default":8192,"claude-cache":0}`))
	recorder := claudeRelayRequest(t, router.SetUpRouter(), key,
		`{"model":"claude-cache","messages":[{"role":"user","content":"pre-warm"}]}`)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, modelName, gotRequest.Model)
	assert.Zero(t, gotRequest.MaxTokens)
}

func TestClaudeMessagesNativeTieredCacheTTLSettlement(t *testing.T) {
	const modelName = "claude-tiered-native"
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_tiered", "type": "message", "role": "assistant", "model": modelName,
			"content": []map[string]any{{"type": "text", "text": "priced"}}, "stop_reason": "end_turn",
			"usage": map[string]any{
				"input_tokens": 100, "cache_read_input_tokens": 20, "cache_creation_input_tokens": 30,
				"cache_creation": map[string]any{
					"ephemeral_5m_input_tokens": 10, "ephemeral_1h_input_tokens": 20,
				},
				"output_tokens": 5,
			},
		})
	}))
	defer mock.Close()

	key, userID := setupClaudeRelay(t, mock.URL, int(constant.ChannelTypeAnthropic), modelName)
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOptions(map[string]string{
		"ModelBillingMode": `{"` + modelName + `":"tiered_expr"}`,
		"ModelBillingExpr": `{"` + modelName + `":"p * 2 + c * 8 + cr * 0.2 + cc * 2.5 + cc1h * 4"}`,
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(map[string]string{"ModelBillingMode": `{}`, "ModelBillingExpr": `{}`})
	})

	recorder := claudeRelayRequest(t, router.SetUpRouter(), key,
		`{"model":"`+modelName+`","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var user model.User
	var token model.Token
	var channel model.Channel
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&token).Error)
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	assert.Equal(t, 500000-175, user.Quota)
	assert.Equal(t, 175, user.UsedQuota)
	assert.Equal(t, 175, token.UsedQuota)
	assert.Equal(t, int64(175), channel.UsedQuota)
}

func TestClaudeMessagesNativeStreamRetainsStartUsageForTieredSettlement(t *testing.T) {
	const modelName = "claude-tiered-stream"
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		events := []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20},"output_tokens":0}}}`,
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"priced"}}`,
			`{"type":"message_delta","usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		}
		for _, event := range events {
			_, _ = w.Write([]byte("data: " + event + "\n\n"))
			flusher.Flush()
		}
	}))
	defer mock.Close()

	key, userID := setupClaudeRelay(t, mock.URL, int(constant.ChannelTypeAnthropic), modelName)
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOptions(map[string]string{
		"ModelBillingMode": `{"` + modelName + `":"tiered_expr"}`,
		"ModelBillingExpr": `{"` + modelName + `":"p * 2 + c * 8 + cr * 0.2 + cc * 2.5 + cc1h * 4"}`,
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(map[string]string{"ModelBillingMode": `{}`, "ModelBillingExpr": `{}`})
	})

	recorder := claudeRelayRequest(t, router.SetUpRouter(), key,
		`{"model":"`+modelName+`","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"type":"message_start"`)

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 500000-175, user.Quota)
	assert.Equal(t, 175, user.UsedQuota)
}

func TestClaudeMessagesValidation(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mock.Close()
	key, _ := setupClaudeRelay(t, mock.URL, int(constant.ChannelTypeOpenAI), "claude-3-5-sonnet-20241022")
	r := router.SetUpRouter()

	// Missing model → Claude-shaped 400.
	rec := claudeRelayRequest(t, r, key, `{"max_tokens":10,"messages":[{"role":"user","content":"x"}]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var e struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e))
	assert.Equal(t, "error", e.Type)
	assert.Equal(t, "invalid_request_error", e.Error.Type)
}
