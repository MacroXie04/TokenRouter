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
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
)

func setupRelayIntegration(t *testing.T, mockURL string) (string, int) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "relay.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{}))
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
			"id": "chatcmpl-1", "object": "chat.completion", "created": 1, "model": "gpt-4",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "Hello from mock"}, "finish_reason": "stop"}},
			"usage":  map[string]any{"prompt_tokens": 7, "completion_tokens": 2, "total_tokens": 9},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	key, userId := setupRelayIntegration(t, mock.URL)
	r := router.SetUpRouter()

	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`
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
}

func TestRelayRejectsInvalidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "relay.db")
	db, _ := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{})
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
			"usage":  map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer failUp.Close()
	defer okUp.Close()

	dsn := "file:" + filepath.Join(t.TempDir(), "relay.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{}))
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
