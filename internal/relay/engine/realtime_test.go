package engine

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRealtimeReservation struct {
	mu          sync.Mutex
	quota       int
	settledWith []int
	channels    []int
	refunds     int
	dispatches  int
	dispatchErr error
	settleErr   error
	refundErr   error
}

func (r *fakeRealtimeReservation) MarkDispatched() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatches++
	return r.dispatchErr
}

func (r *fakeRealtimeReservation) SettleWithChannel(actual, channelId int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settledWith = append(r.settledWith, actual)
	r.channels = append(r.channels, channelId)
	return r.settleErr
}

func (r *fakeRealtimeReservation) Refund() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refunds++
	return r.refundErr
}

func (r *fakeRealtimeReservation) Quota() int { return r.quota }

func (r *fakeRealtimeReservation) BillingLogFields() map[string]any {
	return map[string]any{"billing_source": "test"}
}

func (r *fakeRealtimeReservation) snapshot() ([]int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.settledWith...), r.refunds
}

func (r *fakeRealtimeReservation) channelSnapshot() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.channels...)
}

func (r *fakeRealtimeReservation) dispatchedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dispatches
}

func TestRealtimeErrorForLogOmitsPeerCloseReason(t *testing.T) {
	const secretReason = "sk-live-peer-close-secret"
	message := realtimeErrorForLog(&websocket.CloseError{Code: 4001, Text: secretReason})
	assert.Equal(t, "websocket closed with code 4001", message)
	assert.NotContains(t, message, secretReason)
}

func TestBuildRealtimeUpstreamUsesProviderURLAndAuthentication(t *testing.T) {
	t.Run("OpenAI GA bearer", func(t *testing.T) {
		channel := &model.Channel{Type: int(channelcatalog.ChannelTypeOpenAI), BaseURL: "https://api.example.test", Key: "openai-key"}
		requestURL, header, err := buildRealtimeUpstream(channel, "gpt-realtime", "", "")
		require.NoError(t, err)
		assert.Equal(t, "wss://api.example.test/v1/realtime?model=gpt-realtime", requestURL)
		assert.Equal(t, "Bearer openai-key", header.Get("Authorization"))
		assert.Empty(t, header.Get("OpenAI-Beta"))
	})

	t.Run("OpenAI legacy browser subprotocol", func(t *testing.T) {
		channel := &model.Channel{Type: int(channelcatalog.ChannelTypeOpenAI), BaseURL: "https://api.example.test", Key: "preview-key"}
		requestURL, header, err := buildRealtimeUpstream(channel, "gpt-4o-realtime-preview", "", "realtime")
		require.NoError(t, err)
		assert.Contains(t, requestURL, "model=gpt-4o-realtime-preview")
		assert.Empty(t, header.Get("Authorization"))
		assert.Equal(t, "realtime,openai-insecure-api-key.preview-key,openai-beta.realtime-v1", header.Get("Sec-WebSocket-Protocol"))
	})

	t.Run("Advanced Custom configured route and headers", func(t *testing.T) {
		channel := &model.Channel{
			Type:    int(channelcatalog.ChannelTypeAdvancedCustom),
			BaseURL: "https://gateway.example/root",
			Key:     "advanced-key",
			OtherSettings: `{"advanced_custom":{"advanced_routes":[{
				"incoming_path":"/v1/realtime","upstream_path":"/socket/{model}?fixed=1",
				"models":["client-realtime"],"auth":{"type":"query","name":"token","value":"prefix-{api_key}"}
			}]}}`,
			HeaderOverride: `{"X-Trace-Upstream":"{client_header:X-Trace}","Sec-WebSocket-Protocol":"{client_header:Sec-WebSocket-Protocol}"}`,
		}
		clientHeaders := make(http.Header)
		clientHeaders.Set("X-Trace", "trace-7")
		clientHeaders.Set("Sec-WebSocket-Protocol", "realtime")
		requestURL, header, err := buildRealtimeUpstreamForRequest(
			channel, "client-realtime", "provider-realtime", "", "realtime",
			clientHeaders,
		)
		require.NoError(t, err)
		assert.Equal(t, "wss://gateway.example/root/socket/provider-realtime?fixed=1&token=prefix-advanced-key", requestURL)
		assert.Empty(t, header.Get("Authorization"))
		assert.Equal(t, "trace-7", header.Get("X-Trace-Upstream"))
		assert.Equal(t, "realtime", header.Get("Sec-WebSocket-Protocol"))
	})

	t.Run("Azure deployment and API key", func(t *testing.T) {
		channel := &model.Channel{
			Type: int(channelcatalog.ChannelTypeAzure), BaseURL: "https://resource.openai.azure.com",
			Key: "azure-key", CreatedTime: 1_800_000_000,
		}
		requestURL, header, err := buildRealtimeUpstream(channel, "deployment.name", "2024-10-01-preview", "")
		require.NoError(t, err)
		assert.Equal(t, "wss://resource.openai.azure.com/openai/realtime?api-version=2024-10-01-preview&deployment=deployment.name", requestURL)
		assert.Equal(t, "azure-key", header.Get("api-key"))
		assert.Empty(t, header.Get("Authorization"))
	})

	t.Run("invalid Azure version", func(t *testing.T) {
		channel := &model.Channel{Type: int(channelcatalog.ChannelTypeAzure), BaseURL: "https://resource.openai.azure.com", Key: "azure-key"}
		_, _, err := buildRealtimeUpstream(channel, "deployment", "bad&injected=true", "")
		assert.Error(t, err)
	})

	t.Run("unsupported provider fails closed", func(t *testing.T) {
		channel := &model.Channel{Type: int(channelcatalog.ChannelTypeAws), BaseURL: "https://api.openai.com", Key: "aws-secret"}
		_, _, err := buildRealtimeUpstream(channel, "model", "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no implemented realtime adapter")
	})

	t.Run("compatible provider without a base cannot inherit OpenAI", func(t *testing.T) {
		channel := &model.Channel{Type: int(channelcatalog.ChannelTypeXinference), Key: "private-provider-secret"}
		_, _, err := buildRealtimeUpstream(channel, "model", "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "explicit upstream base URL")
	})
}

type fakeRealtimeState struct {
	mu            sync.Mutex
	reservations  []*fakeRealtimeReservation
	logs          []realtimeLogEntry
	settleFirst   error
	dispatchFirst error
	recordErr     error
}

func (s *fakeRealtimeState) reserve(_ int, _ *model.Token, quota int) (realtimeReservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &fakeRealtimeReservation{quota: quota}
	if len(s.reservations) == 0 {
		r.settleErr = s.settleFirst
		r.dispatchErr = s.dispatchFirst
	}
	s.reservations = append(s.reservations, r)
	return r, nil
}

func (s *fakeRealtimeState) record(entry realtimeLogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, entry)
	return s.recordErr
}

func (s *fakeRealtimeState) snapshot() ([]*fakeRealtimeReservation, []realtimeLogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*fakeRealtimeReservation(nil), s.reservations...), append([]realtimeLogEntry(nil), s.logs...)
}

func realtimeTestDependencies(upstreamURL string, state *fakeRealtimeState) realtimeDependencies {
	now := time.Unix(1_700_000_000, 0)
	return realtimeDependencies{
		selectChannel: func(_, _ string) (*model.Channel, error) {
			return &model.Channel{Id: 9, Type: int(channelcatalog.ChannelTypeOpenAI), BaseURL: upstreamURL, Key: "upstream-key"}, nil
		},
		reserve: state.reserve,
		dial:    websocket.DefaultDialer.Dial,
		reservationQuota: func(_, _, _ string) (int, error) {
			return 10, nil
		},
		usageQuota: func(_, _, _ string, usage normalizedRealtimeUsage) (int, error) {
			return usage.inputTokens + 2*usage.outputTokens, nil
		},
		recordLog: state.record,
		limits: realtimeLimits{
			maxMessageBytes: 1024,
			maxMessages:     100,
			maxResponses:    8,
			idleTimeout:     2 * time.Second,
			writeTimeout:    time.Second,
		},
		now: func() time.Time {
			now = now.Add(time.Millisecond)
			return now
		},
	}
}

func startRealtimeGateway(t *testing.T, deps realtimeDependencies) (*httptest.Server, *model.Token, <-chan struct{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	token := &model.Token{Id: 3, UserId: 2, Name: "test-token", Key: "sk-test", Status: billingsvc.TokenStatusEnabled}
	done := make(chan struct{})
	r := gin.New()
	r.GET("/v1/realtime", func(c *gin.Context) {
		requestctx.SetUserId(c, token.UserId)
		requestctx.SetUsername(c, "alice")
		requestctx.SetUserGroup(c, "default")
		requestctx.SetRequestId(c, "request-1")
		middleware.SetupRelayTokenContext(c, token)
		c.Set(requestctx.ContextKeyGroup, userssvc.GroupDefault)
		relayWebSocket(c, middleware.CaptureRelayRequestState(c), deps)
		close(done)
	})
	return httptest.NewServer(r), token, done
}

func dialRealtimeGateway(t *testing.T, gatewayURL string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(gatewayURL, "http") + "/v1/realtime?model=gpt-test"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	return conn
}

func waitRealtimeHandler(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("realtime handler did not terminate")
	}
}

func TestRealtimeRejectsQuotaBeforeEitherHandshake(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var dialed bool
	deps := realtimeDependencies{
		selectChannel: func(_, _ string) (*model.Channel, error) {
			return &model.Channel{Id: 1, Type: int(channelcatalog.ChannelTypeOpenAI), BaseURL: "http://unused.test", Key: "key"}, nil
		},
		reservationQuota: func(_, _, _ string) (int, error) { return 10, nil },
		reserve: func(_ int, _ *model.Token, _ int) (realtimeReservation, error) {
			return nil, billingsvc.ErrInsufficientTokenQuota
		},
		dial: func(string, http.Header) (*websocket.Conn, *http.Response, error) {
			dialed = true
			return nil, nil, errors.New("must not dial")
		},
	}
	token := &model.Token{Id: 1, UserId: 7}
	r := gin.New()
	r.GET("/v1/realtime", func(c *gin.Context) {
		requestctx.SetUserId(c, token.UserId)
		middleware.SetupRelayTokenContext(c, token)
		middleware.SetRelayGroupPolicy(c, billingsvc.RelayGroupPolicy{Groups: []string{userssvc.GroupDefault}})
		relayWebSocket(c, middleware.CaptureRelayRequestState(c), deps)
	})
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/realtime?model=gpt-test", nil))

	assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
	assert.False(t, dialed)
}

func TestRealtimeSelectsAuthorizedAutoGroupAndUsesItForBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var selectedGroups []string
	var billedUserGroup string
	var billedGroup string
	deps := realtimeDependencies{
		selectChannel: func(group, _ string) (*model.Channel, error) {
			selectedGroups = append(selectedGroups, group)
			if group == "staff" {
				return nil, channelssvc.ErrChannelNotFound
			}
			return &model.Channel{Id: 1, Type: int(channelcatalog.ChannelTypeOpenAI), BaseURL: "http://unused.test", Key: "key"}, nil
		},
		reservationQuota: func(_, userGroup, group string) (int, error) {
			billedUserGroup = userGroup
			billedGroup = group
			return 10, nil
		},
		reserve: func(_ int, _ *model.Token, _ int) (realtimeReservation, error) {
			return nil, billingsvc.ErrInsufficientTokenQuota
		},
	}
	token := &model.Token{Id: 1, UserId: 7, Group: userssvc.GroupAuto}
	router := gin.New()
	router.GET("/v1/realtime", func(c *gin.Context) {
		requestctx.SetUserId(c, token.UserId)
		requestctx.SetUserGroup(c, "member")
		middleware.SetupRelayTokenContext(c, token)
		middleware.SetRelayGroupPolicy(c, billingsvc.RelayGroupPolicy{Groups: []string{"staff", "vip"}, Auto: true})
		relayWebSocket(c, middleware.CaptureRelayRequestState(c), deps)
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/realtime?model=gpt-test", nil))

	assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
	assert.Equal(t, []string{"staff", "vip"}, selectedGroups)
	assert.Equal(t, "member", billedUserGroup)
	assert.Equal(t, "vip", billedGroup)
}

func TestProductionRealtimePricingUsesUserGroupSpecialRatio(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousGroups := billingsvc.ExportedGroupRatios()
	previousSpecial := billingsvc.ExportedGroupGroupRatios()
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousGroups)
		billingsvc.SetGroupGroupRatios(previousSpecial)
	})
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"special-realtime-model": {Prompt: 2, Completion: 4},
	})
	billingsvc.SetGroupRatios(map[string]float64{"vip": 2})
	billingsvc.SetGroupGroupRatios(map[string]map[string]float64{"member": {"vip": 0.5}})

	deps := productionRealtimeDependencies()
	reserved, err := deps.reservationQuota("special-realtime-model", "member", "vip")
	require.NoError(t, err)
	assert.Equal(t, 250, reserved)
	actual, err := deps.usageQuota("special-realtime-model", "member", "vip", normalizedRealtimeUsage{
		inputTokens: 1_000, outputTokens: 100, totalTokens: 1_100,
	})
	require.NoError(t, err)
	assert.Equal(t, 600, actual)
}

func TestRealtimeUpstreamHandshakeFailureRefundsReservation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := &fakeRealtimeState{}
	deps := realtimeTestDependencies("http://unused.test", state)
	deps.dial = func(string, http.Header) (*websocket.Conn, *http.Response, error) {
		return nil, nil, errors.New("injected upstream handshake failure")
	}
	token := &model.Token{Id: 1, UserId: 7}
	r := gin.New()
	r.GET("/v1/realtime", func(c *gin.Context) {
		requestctx.SetUserId(c, token.UserId)
		middleware.SetupRelayTokenContext(c, token)
		middleware.SetRelayGroupPolicy(c, billingsvc.RelayGroupPolicy{Groups: []string{userssvc.GroupDefault}})
		relayWebSocket(c, middleware.CaptureRelayRequestState(c), deps)
	})
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/realtime?model=gpt-test", nil))

	assert.Equal(t, http.StatusBadGateway, recorder.Code)
	reservations, logs := state.snapshot()
	require.Len(t, reservations, 1)
	settled, refunded := reservations[0].snapshot()
	assert.Empty(t, settled)
	assert.Equal(t, 1, refunded)
	assert.Equal(t, 1, reservations[0].dispatchedCount(), "the at-risk marker must commit before dialing upstream")
	assert.Empty(t, logs)
}

func TestRealtimeDispatchMarkerFailurePreventsUpstreamDialAndRefunds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := &fakeRealtimeState{dispatchFirst: errors.New("injected dispatch marker failure")}
	deps := realtimeTestDependencies("http://unused.test", state)
	dialed := false
	deps.dial = func(string, http.Header) (*websocket.Conn, *http.Response, error) {
		dialed = true
		return nil, nil, errors.New("must not dial")
	}
	token := &model.Token{Id: 1, UserId: 7}
	r := gin.New()
	r.GET("/v1/realtime", func(c *gin.Context) {
		requestctx.SetUserId(c, token.UserId)
		middleware.SetupRelayTokenContext(c, token)
		middleware.SetRelayGroupPolicy(c, billingsvc.RelayGroupPolicy{Groups: []string{userssvc.GroupDefault}})
		relayWebSocket(c, middleware.CaptureRelayRequestState(c), deps)
	})
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/realtime?model=gpt-test", nil))

	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.False(t, dialed)
	reservations, logs := state.snapshot()
	require.Len(t, reservations, 1)
	settled, refunded := reservations[0].snapshot()
	assert.Empty(t, settled)
	assert.Equal(t, 1, refunded)
	assert.Equal(t, 1, reservations[0].dispatchedCount())
	assert.Empty(t, logs)
}

func TestRealtimeAccountsEachUniqueResponseAndRefundsDisconnectReservation(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "gpt-test", r.URL.Query().Get("model"))
		assert.Equal(t, "Bearer upstream-key", r.Header.Get("Authorization"))
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		first := `{"type":"response.done","event_id":"evt-1","response":{"id":"resp-1","usage":{"total_tokens":99,"input_tokens":2,"output_tokens":1,"input_token_details":{"text_tokens":2},"output_token_details":{"audio_tokens":1}}}}`
		second := `{"type":"response.done","event_id":"evt-2","response":{"id":"sk-test-super-secret-provider-response-id","usage":{"total_tokens":5,"input_tokens":3,"output_tokens":2}}}`
		_ = conn.WriteMessage(websocket.TextMessage, []byte(first))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(first))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(second))
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()

	state := &fakeRealtimeState{}
	gateway, _, done := startRealtimeGateway(t, realtimeTestDependencies(upstream.URL, state))
	defer gateway.Close()
	client := dialRealtimeGateway(t, gateway.URL)
	require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)))
	for i := 0; i < 3; i++ {
		_, _, err := client.ReadMessage()
		require.NoError(t, err)
	}
	require.NoError(t, client.Close())
	waitRealtimeHandler(t, done)

	reservations, logs := state.snapshot()
	require.Len(t, reservations, 3, "duplicate response.done must not allocate or charge")
	for _, reservation := range reservations {
		assert.Equal(t, 1, reservation.dispatchedCount(), "every response hold must be marked at risk before use")
	}
	settled, refunded := reservations[0].snapshot()
	assert.Equal(t, []int{4}, settled)
	assert.Equal(t, []int{9}, reservations[0].channelSnapshot())
	assert.Zero(t, refunded)
	settled, refunded = reservations[1].snapshot()
	assert.Equal(t, []int{7}, settled)
	assert.Equal(t, []int{9}, reservations[1].channelSnapshot())
	assert.Zero(t, refunded)
	settled, refunded = reservations[2].snapshot()
	assert.Empty(t, settled)
	assert.Equal(t, 1, refunded, "unused next-response reservation must be refunded on disconnect")
	require.Len(t, logs, 2)
	assert.Equal(t, cryptoutil.NormalizeProviderCorrelationID("resp-1"), logs[0].upstreamRequestId)
	assert.Equal(t, 2, logs[0].promptTokens)
	assert.Equal(t, 1, logs[0].completionTokens)
	assert.Equal(t, 4, logs[0].quota)
	assert.Equal(t, true, logs[0].other["realtime_total_normalized"])
	assert.Equal(t, cryptoutil.NormalizeProviderCorrelationID("sk-test-super-secret-provider-response-id"), logs[1].upstreamRequestId)
	assert.NotContains(t, logs[1].upstreamRequestId, "super-secret")
	assert.Equal(t, logs[1].upstreamRequestId, logs[1].other["realtime_response_id"])
}

func TestRealtimeDisconnectBeforeUsageRefundsReservation(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()

	state := &fakeRealtimeState{}
	gateway, _, done := startRealtimeGateway(t, realtimeTestDependencies(upstream.URL, state))
	defer gateway.Close()
	client := dialRealtimeGateway(t, gateway.URL)
	require.NoError(t, client.Close())
	waitRealtimeHandler(t, done)

	reservations, logs := state.snapshot()
	require.Len(t, reservations, 1)
	settled, refunded := reservations[0].snapshot()
	assert.Empty(t, settled)
	assert.Equal(t, 1, refunded)
	assert.Empty(t, logs)
}

func TestRealtimeMalformedUsageChargesFallbackAndTerminates(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.done","response":{"id":"resp-bad","usage":{"input_tokens":-1,"output_tokens":2}}}`))
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()

	state := &fakeRealtimeState{}
	gateway, _, done := startRealtimeGateway(t, realtimeTestDependencies(upstream.URL, state))
	defer gateway.Close()
	client := dialRealtimeGateway(t, gateway.URL)
	require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)))
	_, _, readErr := client.ReadMessage()
	assert.Error(t, readErr)
	_ = client.Close()
	waitRealtimeHandler(t, done)

	reservations, logs := state.snapshot()
	require.Len(t, reservations, 1)
	settled, refunded := reservations[0].snapshot()
	assert.Equal(t, []int{10}, settled)
	assert.Zero(t, refunded)
	require.Len(t, logs, 1)
	assert.Equal(t, "reserved_quota", logs[0].other["realtime_usage_fallback"])
}

func TestRealtimeOversizedUpstreamMessageRefundsAndTerminates(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 256)))
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()

	state := &fakeRealtimeState{}
	deps := realtimeTestDependencies(upstream.URL, state)
	deps.limits.maxMessageBytes = 64
	gateway, _, done := startRealtimeGateway(t, deps)
	defer gateway.Close()
	client := dialRealtimeGateway(t, gateway.URL)
	require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte("go")))
	_, _, readErr := client.ReadMessage()
	assert.Error(t, readErr)
	_ = client.Close()
	waitRealtimeHandler(t, done)

	reservations, logs := state.snapshot()
	require.Len(t, reservations, 1)
	settled, refunded := reservations[0].snapshot()
	assert.Empty(t, settled)
	assert.Equal(t, 1, refunded)
	assert.Empty(t, logs)
}

func TestRealtimeSettlementFailureRetainsReservation(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.done","response":{"id":"resp-fail","usage":{"total_tokens":3,"input_tokens":2,"output_tokens":1}}}`))
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()

	settleErr := errors.New("injected settlement failure")
	state := &fakeRealtimeState{settleFirst: settleErr}
	gateway, _, done := startRealtimeGateway(t, realtimeTestDependencies(upstream.URL, state))
	defer gateway.Close()
	client := dialRealtimeGateway(t, gateway.URL)
	require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte("go")))
	_, _, readErr := client.ReadMessage()
	assert.Error(t, readErr)
	_ = client.Close()
	waitRealtimeHandler(t, done)

	reservations, logs := state.snapshot()
	require.Len(t, reservations, 1)
	settled, refunded := reservations[0].snapshot()
	assert.Equal(t, []int{4}, settled)
	assert.Zero(t, refunded, "consumed work must not be refunded after settlement failure")
	require.Len(t, logs, 1)
	assert.Equal(t, 10, logs[0].quota, "failure audit records the amount known to remain held")
	assert.Equal(t, 4, logs[0].other["realtime_actual_quota"])
	assert.Equal(t, "failed", logs[0].other["realtime_settlement_status"])
	assert.Equal(t, true, logs[0].other["realtime_reservation_retained"])
}

func TestRealtimeLogFailureIsSurfacedAfterChargeWithoutDoubleRefund(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.done","response":{"id":"resp-log-fail","usage":{"total_tokens":3,"input_tokens":2,"output_tokens":1}}}`))
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()

	state := &fakeRealtimeState{recordErr: errors.New("injected durable log failure")}
	gateway, _, done := startRealtimeGateway(t, realtimeTestDependencies(upstream.URL, state))
	defer gateway.Close()
	client := dialRealtimeGateway(t, gateway.URL)
	require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte("go")))
	_, _, readErr := client.ReadMessage()
	assert.Error(t, readErr)
	_ = client.Close()
	waitRealtimeHandler(t, done)

	reservations, logs := state.snapshot()
	require.Len(t, reservations, 1)
	settled, refunded := reservations[0].snapshot()
	assert.Equal(t, []int{4}, settled)
	assert.Zero(t, refunded, "a committed charge must not be rolled back because its audit insert failed")
	require.Len(t, logs, 1, "the checked logger must be called exactly once")
}

func TestNormalizeRealtimeUsage(t *testing.T) {
	input, output, total := 4, 3, 999
	inputText, inputAudio, cached := 2, 2, 1
	outputAudio := 1
	usage, err := normalizeRealtimeUsage(&realtimeUsage{
		InputTokens: &input, OutputTokens: &output, TotalTokens: &total,
		InputTokenDetails: &realtimeTokenDetail{
			TextTokens: &inputText, AudioTokens: &inputAudio, CachedTokens: &cached,
		},
		OutputTokenDetails: &realtimeTokenDetail{AudioTokens: &outputAudio},
	})
	require.NoError(t, err)
	assert.Equal(t, 7, usage.totalTokens)
	assert.Equal(t, 2, usage.outputTextTokens)
	assert.True(t, usage.totalCorrected)

	negative := -1
	_, err = normalizeRealtimeUsage(&realtimeUsage{InputTokens: &input, OutputTokens: &output, TotalTokens: &negative})
	assert.ErrorContains(t, err, "total_tokens")
}
