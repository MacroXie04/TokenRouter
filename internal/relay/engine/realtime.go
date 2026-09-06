package engine

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	billingexpr "github.com/tokenrouter/tokenrouter/internal/billing/expression"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	openaichannel "github.com/tokenrouter/tokenrouter/internal/relay/providers/openai"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultRealtimePreConsumedTokens = 500
	defaultRealtimeMaxMessageBytes   = 4 << 20
	defaultRealtimeMaxMessages       = 10_000
	defaultRealtimeMaxResponses      = 256
	defaultRealtimeHandshakeTimeout  = 10 * time.Second
	defaultRealtimeIdleTimeout       = 2 * time.Minute
	defaultRealtimeWriteTimeout      = 10 * time.Second
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsDialer dials upstream WebSockets through the SSRF-guarded dialer.
var wsDialer = &websocket.Dialer{
	NetDialContext:   httpx.SafeDialContext,
	HandshakeTimeout: defaultRealtimeHandshakeTimeout,
	TLSClientConfig:  &tls.Config{InsecureSkipVerify: env.GetEnvBool("TLS_INSECURE_SKIP_VERIFY", false)},
}

type realtimeLimits struct {
	maxMessageBytes int64
	maxMessages     int
	maxResponses    int
	idleTimeout     time.Duration
	writeTimeout    time.Duration
}

type realtimeReservation interface {
	MarkDispatched() error
	SettleWithChannel(actual, channelId int) error
	Refund() error
	Quota() int
	BillingLogFields() map[string]any
}

type realtimeLogEntry struct {
	userId            int
	username          string
	tokenName         string
	modelName         string
	promptTokens      int
	completionTokens  int
	quota             int
	useTime           int
	channelId         int
	userGroup         string
	group             string
	ip                string
	requestId         string
	upstreamRequestId string
	tokenId           int
	other             map[string]any
}

// realtimePricingSnapshot freezes the billing policy selected before either
// WebSocket handshake. Reference-mode sessions reuse this exact plan for every
// response so a hot reload cannot mix hold, settlement, or audit ratios.
type realtimePricingSnapshot struct {
	reservationQuota int
	freeModel        bool
	allowZero        bool
	reference        bool
	referencePlan    billingsvc.ReferenceBillingPlan
}

type realtimeDependencies struct {
	selectChannel    func(group, modelName string) (*model.Channel, error)
	reserve          func(userId int, token *model.Token, quota int) (realtimeReservation, error)
	reserveFree      func(userId int, token *model.Token, quota int, freeModel bool) (realtimeReservation, error)
	dial             func(urlStr string, requestHeader http.Header) (*websocket.Conn, *http.Response, error)
	resolvePricing   func(userId int, modelName, userGroup, usingGroup string) (realtimePricingSnapshot, error)
	reservationQuota func(modelName, userGroup, usingGroup string) (int, error)
	usageQuota       func(modelName, userGroup, usingGroup string, usage normalizedRealtimeUsage) (int, error)
	recordLog        func(realtimeLogEntry) error
	limits           realtimeLimits
	now              func() time.Time
}

func productionRealtimeDependencies() realtimeDependencies {
	return realtimeDependencies{
		selectChannel: func(group, modelName string) (*model.Channel, error) {
			return channelssvc.GetRandomSatisfiedChannel(group, modelName, nil, nil)
		},
		reserve: func(userId int, token *model.Token, quota int) (realtimeReservation, error) {
			return billingsvc.NewRelayQuotaReservation(userId, token, quota)
		},
		reserveFree: func(userId int, token *model.Token, quota int, freeModel bool) (realtimeReservation, error) {
			return billingsvc.NewRelayQuotaReservationWithFreeModel(userId, token, quota, freeModel)
		},
		dial: wsDialer.Dial,
		resolvePricing: func(userId int, modelName, userGroup, usingGroup string) (realtimePricingSnapshot, error) {
			plan, enabled, err := billingsvc.ResolveOrdinaryReferenceBillingPlanForUser(
				userId, modelName, userGroup, usingGroup,
			)
			if err != nil {
				return realtimePricingSnapshot{}, err
			}
			if enabled {
				quota, quotaErr := plan.PreConsumeQuota(0, 0, false)
				return realtimePricingSnapshot{
					reservationQuota: quota,
					freeModel:        plan.FreeModel(),
					allowZero:        true,
					reference:        true,
					referencePlan:    plan,
				}, quotaErr
			}
			if billingsvc.ShouldSkipOrdinaryFreeModelPreConsume(modelName, userGroup, usingGroup) {
				return realtimePricingSnapshot{freeModel: true, allowZero: true}, nil
			}
			quota, clamp, quotaErr := billingsvc.ComputeBillingQuotaForUser(modelName, userGroup, usingGroup, false, &protocolkit.Usage{
				PromptTokens: defaultRealtimePreConsumedTokens,
				TotalTokens:  defaultRealtimePreConsumedTokens,
			}, billingexpr.RequestInput{})
			if quotaErr != nil {
				return realtimePricingSnapshot{}, quotaErr
			}
			if clamp != nil {
				return realtimePricingSnapshot{}, clamp
			}
			if quota < 1 {
				quota = 1
			}
			return realtimePricingSnapshot{reservationQuota: quota}, nil
		},
		reservationQuota: func(modelName, userGroup, usingGroup string) (int, error) {
			quota, clamp, err := billingsvc.ComputeBillingQuotaForUser(modelName, userGroup, usingGroup, false, &protocolkit.Usage{
				PromptTokens: defaultRealtimePreConsumedTokens,
				TotalTokens:  defaultRealtimePreConsumedTokens,
			}, billingexpr.RequestInput{})
			if err != nil {
				return 0, err
			}
			if clamp != nil {
				return 0, clamp
			}
			if quota < 1 {
				quota = 1
			}
			return quota, nil
		},
		usageQuota: func(modelName, userGroup, usingGroup string, usage normalizedRealtimeUsage) (int, error) {
			quota, clamp, err := billingsvc.ComputeBillingQuotaForUser(modelName, userGroup, usingGroup, false, &protocolkit.Usage{
				PromptTokens:         usage.inputTokens,
				CompletionTokens:     usage.outputTokens,
				TotalTokens:          usage.totalTokens,
				PromptCacheHitTokens: usage.inputCachedTokens,
				PromptTokensDetails: &protocolkit.InputTokenDetails{
					CachedTokens: usage.inputCachedTokens,
					TextTokens:   usage.inputTextTokens,
					AudioTokens:  usage.inputAudioTokens,
				},
				CompletionTokensDetails: &protocolkit.OutputTokenDetails{
					TextTokens:  usage.outputTextTokens,
					AudioTokens: usage.outputAudioTokens,
				},
			}, billingexpr.RequestInput{})
			if err != nil {
				return 0, err
			}
			if clamp != nil {
				return 0, clamp
			}
			if quota < 0 {
				return 0, errors.New("computed realtime quota is negative")
			}
			return quota, nil
		},
		recordLog: func(entry realtimeLogEntry) error {
			return billingsvc.RecordConsumeLogChecked(
				entry.userId, entry.username, entry.tokenName, entry.modelName,
				entry.promptTokens, entry.completionTokens, entry.quota, entry.useTime,
				true, entry.channelId, entry.group, entry.ip, entry.requestId,
				entry.upstreamRequestId, entry.tokenId, entry.other,
			)
		},
		limits: realtimeLimits{
			maxMessageBytes: defaultRealtimeMaxMessageBytes,
			maxMessages:     defaultRealtimeMaxMessages,
			maxResponses:    defaultRealtimeMaxResponses,
			idleTimeout:     defaultRealtimeIdleTimeout,
			writeTimeout:    defaultRealtimeWriteTimeout,
		},
		now: time.Now,
	}
}

// RelayWebSocket proxies an OpenAI Realtime WebSocket through a bounded,
// pre-funded accounting session.
func RelayWebSocket(c *gin.Context) {
	relayWebSocket(c, productionRealtimeDependencies())
}

func relayWebSocket(c *gin.Context, deps realtimeDependencies) {
	token := middleware.GetRelayToken(c)
	if token == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return
	}
	userId := requestctx.GetUserId(c)
	userGroup := requestctx.GetUserGroup(c)
	if userId <= 0 || token.UserId != userId {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token owner"})
		return
	}
	groups := middleware.GetTokenGroups(c)
	if len(groups) == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "no authorized group"})
		return
	}
	modelName := c.Query("model")
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing model"})
		return
	}

	var channel *model.Channel
	group := ""
	var err error
	for _, candidate := range groups {
		selected, selectErr := deps.selectChannel(candidate, modelName)
		if selectErr == nil && selected != nil {
			channel = selected
			group = candidate
			break
		}
		if selectErr != nil && !errors.Is(selectErr, channelssvc.ErrChannelNotFound) {
			err = selectErr
			break
		}
	}
	if err != nil || channel == nil || group == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no available channel"})
		return
	}
	c.Set(requestctx.ContextKeyGroup, group)
	pricing, err := resolveRealtimePricingSnapshot(deps, userId, modelName, userGroup, group)
	if err != nil || pricing.reservationQuota < 0 || pricing.reservationQuota == 0 && !pricing.allowZero {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid realtime reservation"})
		return
	}

	// Both the limited token and the user's selected funding source are
	// reserved before either WebSocket handshake can consume upstream work.
	reservation, err := reserveRealtimeQuota(deps, userId, token, pricing)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, billingsvc.ErrInsufficientTokenQuota) ||
			errors.Is(err, billingsvc.ErrInsufficientQuota) || billingsvc.IsSubscriptionFundingErr(err) {
			status = http.StatusTooManyRequests
		}
		c.JSON(status, gin.H{"error": "insufficient quota for realtime session"})
		return
	}

	accountant := newRealtimeAccountant(realtimeAccountantConfig{
		deps:             deps,
		current:          reservation,
		reservationQuota: pricing.reservationQuota,
		pricing:          pricing,
		token:            token,
		log: realtimeLogEntry{
			userId: userId, username: requestctx.GetUsername(c),
			tokenName: requestctx.GetString(c, requestctx.ContextKeyTokenName),
			modelName: modelName, channelId: channel.Id, userGroup: userGroup, group: group,
			ip: c.ClientIP(), requestId: requestctx.GetRequestId(c), tokenId: token.Id,
		},
		startedAt: deps.now(),
	})

	mappedModel := relaycommon.GetMappedModel(channel, modelName)
	wsURL, header, err := buildRealtimeUpstreamForRequest(
		channel, modelName, mappedModel, c.Query("api-version"),
		c.GetHeader("Sec-WebSocket-Protocol"), c.Request.Header,
	)
	if err != nil {
		refundRealtimeAccountant(accountant, "invalid upstream URL")
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid upstream websocket URL"})
		return
	}
	if err := reservation.MarkDispatched(); err != nil {
		refundErr := accountant.close()
		combined := errors.Join(
			fmt.Errorf("mark realtime quota reservation dispatched: %w", err),
			wrapRelayError("refund undispatched realtime quota reservation", refundErr),
		)
		logging.SysError("realtime dispatch accounting failed: " + combined.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist realtime dispatch"})
		return
	}
	upstreamConn, upstreamResp, err := deps.dial(wsURL, header)
	if upstreamResp != nil && upstreamResp.Body != nil {
		if closeErr := upstreamResp.Body.Close(); closeErr != nil {
			logging.SysError("realtime upstream handshake response close failed: " + closeErr.Error())
		}
	}
	if err != nil {
		refundRealtimeAccountant(accountant, "upstream handshake failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream websocket unavailable"})
		return
	}

	clientConn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		if closeErr := upstreamConn.Close(); closeErr != nil {
			logging.SysError("realtime upstream close after client handshake failure: " + closeErr.Error())
		}
		refundRealtimeAccountant(accountant, "client handshake failed")
		return
	}

	configureRealtimeConnection(clientConn, deps.limits)
	configureRealtimeConnection(upstreamConn, deps.limits)

	results := make(chan realtimePumpResult, 2)
	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() {
		defer pumps.Done()
		proxyRealtimeMessages(upstreamConn, clientConn, accountant, true, deps.limits, results)
	}()
	go func() {
		defer pumps.Done()
		proxyRealtimeMessages(clientConn, upstreamConn, accountant, false, deps.limits, results)
	}()

	first := <-results
	if err := clientConn.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logging.SysError("realtime client close failed: " + err.Error())
	}
	if err := upstreamConn.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logging.SysError("realtime upstream close failed: " + err.Error())
	}
	second := <-results
	// A pump reports before its goroutine has fully unwound. Join both pumps
	// before touching the accountant or returning from this hijacked handler so
	// no connection or accounting work can survive the request lifecycle.
	pumps.Wait()

	if first.err != nil && !isNormalWebSocketClose(first.err) {
		logging.SysError("realtime " + first.direction + " stopped: " + realtimeErrorForLog(first.err))
	}
	if second.err != nil && !isNormalWebSocketClose(second.err) {
		logging.SysError("realtime " + second.direction + " stopped: " + realtimeErrorForLog(second.err))
	}
	refundRealtimeAccountant(accountant, "connection cleanup")
}

type realtimePumpResult struct {
	direction string
	err       error
}

func proxyRealtimeMessages(src, dst *websocket.Conn, accountant *realtimeAccountant, fromUpstream bool, limits realtimeLimits, results chan<- realtimePumpResult) {
	direction := "client-to-upstream"
	if fromUpstream {
		direction = "upstream-to-client"
	}
	result := realtimePumpResult{direction: direction}
	defer func() { results <- result }()

	for messageCount := 0; messageCount < limits.maxMessages; messageCount++ {
		if err := src.SetReadDeadline(time.Now().Add(limits.idleTimeout)); err != nil {
			result.err = err
			return
		}
		messageType, data, err := src.ReadMessage()
		if err != nil {
			result.err = err
			return
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			result.err = fmt.Errorf("unsupported websocket message type %d", messageType)
			return
		}

		forward := true
		var afterWriteErr error
		if fromUpstream && messageType == websocket.TextMessage {
			forward, afterWriteErr = accountant.processUpstreamMessage(data)
		}
		if forward {
			if err := dst.SetWriteDeadline(time.Now().Add(limits.writeTimeout)); err != nil {
				result.err = err
				return
			}
			if err := dst.WriteMessage(messageType, data); err != nil {
				result.err = err
				return
			}
		}
		if afterWriteErr != nil {
			result.err = afterWriteErr
			return
		}
	}
	result.err = fmt.Errorf("websocket message limit exceeded (%d)", limits.maxMessages)
}

func configureRealtimeConnection(conn *websocket.Conn, limits realtimeLimits) {
	conn.SetReadLimit(limits.maxMessageBytes)
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(limits.idleTimeout))
	})
}

func isNormalWebSocketClose(err error) bool {
	return websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) ||
		strings.Contains(err.Error(), "use of closed network connection")
}

func realtimeErrorForLog(err error) string {
	if err == nil {
		return ""
	}
	var closeError *websocket.CloseError
	if errors.As(err, &closeError) {
		return fmt.Sprintf("websocket closed with code %d", closeError.Code)
	}
	return err.Error()
}

func refundRealtimeAccountant(accountant *realtimeAccountant, reason string) {
	if accountant == nil {
		return
	}
	if err := accountant.close(); err != nil {
		logging.SysError("realtime " + reason + " refund failed: " + err.Error())
	}
}

type realtimeAccountantConfig struct {
	deps             realtimeDependencies
	current          realtimeReservation
	reservationQuota int
	pricing          realtimePricingSnapshot
	token            *model.Token
	log              realtimeLogEntry
	startedAt        time.Time
}

type realtimeAccountant struct {
	deps             realtimeDependencies
	current          realtimeReservation
	currentConsumed  bool
	reservationQuota int
	pricing          realtimePricingSnapshot
	token            *model.Token
	log              realtimeLogEntry
	startedAt        time.Time
	seenResponses    map[string]struct{}
	responseCount    int
}

func newRealtimeAccountant(config realtimeAccountantConfig) *realtimeAccountant {
	return &realtimeAccountant{
		deps: config.deps, current: config.current,
		reservationQuota: config.reservationQuota, pricing: config.pricing, token: config.token,
		log: config.log, startedAt: config.startedAt,
		seenResponses: make(map[string]struct{}),
	}
}

type realtimeResponseDone struct {
	EventId  string `json:"event_id"`
	Type     string `json:"type"`
	Response *struct {
		Id    string         `json:"id"`
		Usage *realtimeUsage `json:"usage"`
	} `json:"response"`
}

type realtimeUsage struct {
	TotalTokens        *int                 `json:"total_tokens"`
	InputTokens        *int                 `json:"input_tokens"`
	OutputTokens       *int                 `json:"output_tokens"`
	InputTokenDetails  *realtimeTokenDetail `json:"input_token_details"`
	OutputTokenDetails *realtimeTokenDetail `json:"output_token_details"`
}

type realtimeTokenDetail struct {
	CachedTokens *int `json:"cached_tokens"`
	TextTokens   *int `json:"text_tokens"`
	AudioTokens  *int `json:"audio_tokens"`
}

type normalizedRealtimeUsage struct {
	totalTokens       int
	inputTokens       int
	outputTokens      int
	inputTextTokens   int
	inputAudioTokens  int
	inputCachedTokens int
	outputTextTokens  int
	outputAudioTokens int
	totalCorrected    bool
}

func (a *realtimeAccountant) processUpstreamMessage(data []byte) (bool, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := jsonutil.Unmarshal(data, &envelope); err != nil {
		if bytes.Contains(data, []byte("response.done")) {
			return false, a.settleMalformedResponse("", fmt.Errorf("decode response.done envelope: %w", err))
		}
		return true, nil
	}
	if envelope.Type != "response.done" {
		return true, nil
	}

	var event realtimeResponseDone
	if err := jsonutil.Unmarshal(data, &event); err != nil {
		return false, a.settleMalformedResponse("", fmt.Errorf("decode response.done: %w", err))
	}
	responseId := event.EventId
	if event.Response != nil && event.Response.Id != "" {
		responseId = event.Response.Id
	}
	responseId = requestctx.NormalizeProviderCorrelationID(responseId)
	if responseId != "" {
		if _, duplicate := a.seenResponses[responseId]; duplicate {
			return true, nil
		}
	}
	if responseId == "" {
		return false, a.settleMalformedResponse("", errors.New("response.done is missing a response identifier"))
	}
	if a.responseCount >= a.deps.limits.maxResponses {
		return false, a.settleMalformedResponse(responseId, fmt.Errorf("realtime response limit exceeded (%d)", a.deps.limits.maxResponses))
	}
	a.seenResponses[responseId] = struct{}{}
	a.responseCount++

	if event.Response == nil || event.Response.Usage == nil {
		return false, a.settleMalformedResponse(responseId, errors.New("response.done is missing usage"))
	}
	usage, err := normalizeRealtimeUsage(event.Response.Usage)
	if err != nil {
		return false, a.settleMalformedResponse(responseId, err)
	}
	quota, billingFields, err := a.settlementQuota(usage)
	if err != nil {
		return false, a.settleMalformedResponse(responseId, fmt.Errorf("compute response quota: %w", err))
	}

	a.currentConsumed = true
	settled := a.current
	if settled == nil {
		return false, errors.New("response.done has no active quota reservation")
	}
	if err := settled.SettleWithChannel(quota, a.log.channelId); err != nil {
		settleErr := fmt.Errorf("settle response %s: %w", responseId, err)
		if logErr := a.recordUsage(responseId, usage, quota, settled, billingFields, nil, settleErr); logErr != nil {
			return false, errors.Join(settleErr, fmt.Errorf("record failed settlement: %w", logErr))
		}
		return false, settleErr
	}
	a.current = nil
	a.currentConsumed = false
	if err := a.recordUsage(responseId, usage, quota, settled, billingFields, nil, nil); err != nil {
		return false, fmt.Errorf("record response %s usage: %w", responseId, err)
	}

	next, err := reserveRealtimeQuota(a.deps, a.log.userId, a.token, a.pricing)
	if err != nil {
		return true, fmt.Errorf("reserve next realtime response: %w", err)
	}
	if err := next.MarkDispatched(); err != nil {
		refundErr := next.Refund()
		return true, errors.Join(
			fmt.Errorf("mark next realtime reservation dispatched: %w", err),
			wrapRelayError("refund undispatched next realtime reservation", refundErr),
		)
	}
	a.current = next
	return true, nil
}

func (a *realtimeAccountant) settleMalformedResponse(responseId string, usageErr error) error {
	if a.current == nil {
		return fmt.Errorf("malformed realtime usage: %w", usageErr)
	}
	a.currentConsumed = true
	settled := a.current
	fallbackQuota := settled.Quota()
	if err := settled.SettleWithChannel(fallbackQuota, a.log.channelId); err != nil {
		settleErr := fmt.Errorf("settle fallback reservation: %w", err)
		emptyUsage := normalizedRealtimeUsage{}
		if logErr := a.recordUsage(responseId, emptyUsage, fallbackQuota, settled, nil, usageErr, settleErr); logErr != nil {
			return errors.Join(fmt.Errorf("malformed realtime usage: %w", usageErr), settleErr, fmt.Errorf("record failed fallback settlement: %w", logErr))
		}
		return errors.Join(fmt.Errorf("malformed realtime usage: %w", usageErr), settleErr)
	}
	a.current = nil
	a.currentConsumed = false
	emptyUsage := normalizedRealtimeUsage{}
	if err := a.recordUsage(responseId, emptyUsage, fallbackQuota, settled, nil, usageErr, nil); err != nil {
		return errors.Join(fmt.Errorf("malformed realtime usage: %w", usageErr), fmt.Errorf("record fallback usage: %w", err))
	}
	return fmt.Errorf("malformed realtime usage: %w", usageErr)
}

func (a *realtimeAccountant) settlementQuota(usage normalizedRealtimeUsage) (int, map[string]any, error) {
	if !a.pricing.reference {
		quota, err := a.deps.usageQuota(a.log.modelName, a.log.userGroup, a.log.group, usage)
		return quota, nil, err
	}
	settlement, err := a.pricing.referencePlan.SettlementUsageQuota(
		realtimeProtocolUsage(usage),
		billingsvc.ReferenceUsageContext{ForceAudio: true, Realtime: true},
	)
	return settlement.Quota, settlement.BillingLogFields(), err
}

func realtimeProtocolUsage(usage normalizedRealtimeUsage) *protocolkit.Usage {
	return &protocolkit.Usage{
		PromptTokens:         usage.inputTokens,
		CompletionTokens:     usage.outputTokens,
		TotalTokens:          usage.totalTokens,
		PromptCacheHitTokens: usage.inputCachedTokens,
		PromptTokensDetails: &protocolkit.InputTokenDetails{
			CachedTokens: usage.inputCachedTokens,
			TextTokens:   usage.inputTextTokens,
			AudioTokens:  usage.inputAudioTokens,
		},
		CompletionTokensDetails: &protocolkit.OutputTokenDetails{
			TextTokens:  usage.outputTextTokens,
			AudioTokens: usage.outputAudioTokens,
		},
	}
}

func (a *realtimeAccountant) recordUsage(responseId string, usage normalizedRealtimeUsage, quota int, settled realtimeReservation, billingFields map[string]any, fallbackErr, settlementErr error) error {
	responseId = requestctx.NormalizeProviderCorrelationID(responseId)
	entry := a.log
	entry.promptTokens = usage.inputTokens
	entry.completionTokens = usage.outputTokens
	entry.quota = quota
	entry.useTime = boundedDurationMillis(a.deps.now().Sub(a.startedAt))
	entry.upstreamRequestId = responseId
	entry.other = map[string]any{
		"realtime":                     true,
		"realtime_response_id":         responseId,
		"realtime_total_tokens":        usage.totalTokens,
		"realtime_input_text_tokens":   usage.inputTextTokens,
		"realtime_input_audio_tokens":  usage.inputAudioTokens,
		"realtime_cached_tokens":       usage.inputCachedTokens,
		"realtime_output_text_tokens":  usage.outputTextTokens,
		"realtime_output_audio_tokens": usage.outputAudioTokens,
	}
	if a.pricing.reference {
		for key, value := range a.pricing.referencePlan.BillingLogFields() {
			entry.other[key] = value
		}
	} else {
		ratio, special := billingsvc.EffectiveGroupRatio(entry.userGroup, entry.group)
		entry.other["group_ratio"] = ratio
		if special {
			entry.other["user_group_ratio"] = ratio
		}
	}
	for key, value := range billingFields {
		entry.other[key] = value
	}
	if usage.totalCorrected {
		entry.other["realtime_total_normalized"] = true
	}
	if fallbackErr != nil {
		entry.other["realtime_usage_fallback"] = "reserved_quota"
		entry.other["realtime_usage_error"] = fallbackErr.Error()
	}
	if settlementErr != nil {
		entry.other["realtime_settlement_status"] = "failed"
		entry.other["realtime_settlement_error"] = settlementErr.Error()
		entry.other["realtime_reservation_retained"] = true
		entry.other["realtime_actual_quota"] = quota
		// Only the pre-authorized amount is known to remain held when the
		// exact reconciliation transaction fails.
		entry.quota = settled.Quota()
	}
	for key, value := range settled.BillingLogFields() {
		entry.other[key] = value
	}
	return a.deps.recordLog(entry)
}

func resolveRealtimePricingSnapshot(deps realtimeDependencies, userId int, modelName, userGroup, usingGroup string) (realtimePricingSnapshot, error) {
	if deps.resolvePricing != nil {
		return deps.resolvePricing(userId, modelName, userGroup, usingGroup)
	}
	if deps.reservationQuota == nil {
		return realtimePricingSnapshot{}, errors.New("realtime reservation pricing is unavailable")
	}
	quota, err := deps.reservationQuota(modelName, userGroup, usingGroup)
	return realtimePricingSnapshot{reservationQuota: quota}, err
}

func reserveRealtimeQuota(deps realtimeDependencies, userId int, token *model.Token, pricing realtimePricingSnapshot) (realtimeReservation, error) {
	if deps.reserveFree != nil {
		return deps.reserveFree(userId, token, pricing.reservationQuota, pricing.freeModel)
	}
	if deps.reserve == nil {
		return nil, errors.New("realtime quota reservation is unavailable")
	}
	return deps.reserve(userId, token, pricing.reservationQuota)
}

func boundedDurationMillis(duration time.Duration) int {
	millis := duration.Milliseconds()
	if millis <= 0 {
		return 0
	}
	if millis > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(millis)
}

func (a *realtimeAccountant) close() error {
	if a == nil || a.current == nil {
		return nil
	}
	if a.currentConsumed {
		return errors.New("usage settlement failed; reservation retained")
	}
	err := a.current.Refund()
	a.current = nil
	return err
}

func normalizeRealtimeUsage(usage *realtimeUsage) (normalizedRealtimeUsage, error) {
	if usage == nil || usage.InputTokens == nil || usage.OutputTokens == nil {
		return normalizedRealtimeUsage{}, errors.New("usage must include input_tokens and output_tokens")
	}
	if *usage.InputTokens < 0 || *usage.OutputTokens < 0 {
		return normalizedRealtimeUsage{}, errors.New("usage token counts must not be negative")
	}
	if *usage.InputTokens > math.MaxInt32 || *usage.OutputTokens > math.MaxInt32 ||
		*usage.InputTokens > math.MaxInt32-*usage.OutputTokens {
		return normalizedRealtimeUsage{}, errors.New("usage token counts overflow")
	}
	normalized := normalizedRealtimeUsage{
		inputTokens: *usage.InputTokens, outputTokens: *usage.OutputTokens,
		totalTokens: *usage.InputTokens + *usage.OutputTokens,
	}
	if usage.TotalTokens != nil && *usage.TotalTokens < 0 {
		return normalizedRealtimeUsage{}, errors.New("total_tokens must not be negative")
	}
	if usage.TotalTokens == nil || *usage.TotalTokens != normalized.totalTokens {
		normalized.totalCorrected = true
	}

	var err error
	normalized.inputTextTokens, normalized.inputAudioTokens, normalized.inputCachedTokens, err = normalizeRealtimeDetails(usage.InputTokenDetails, normalized.inputTokens, true)
	if err != nil {
		return normalizedRealtimeUsage{}, fmt.Errorf("invalid input_token_details: %w", err)
	}
	normalized.outputTextTokens, normalized.outputAudioTokens, _, err = normalizeRealtimeDetails(usage.OutputTokenDetails, normalized.outputTokens, false)
	if err != nil {
		return normalizedRealtimeUsage{}, fmt.Errorf("invalid output_token_details: %w", err)
	}
	return normalized, nil
}

func normalizeRealtimeDetails(details *realtimeTokenDetail, parent int, allowCached bool) (textTokens, audioTokens, cachedTokens int, err error) {
	if details == nil {
		return parent, 0, 0, nil
	}
	values := []*int{details.TextTokens, details.AudioTokens}
	for _, value := range values {
		if value != nil && *value < 0 {
			return 0, 0, 0, errors.New("detail token counts must not be negative")
		}
	}
	if details.TextTokens != nil {
		textTokens = *details.TextTokens
	}
	if details.AudioTokens != nil {
		audioTokens = *details.AudioTokens
	}
	if textTokens > parent || audioTokens > parent-textTokens {
		return 0, 0, 0, errors.New("text and audio details exceed parent token count")
	}
	if details.TextTokens == nil {
		textTokens = parent - audioTokens
	} else if details.AudioTokens == nil {
		audioTokens = parent - textTokens
	}
	if details.CachedTokens != nil {
		if !allowCached {
			return 0, 0, 0, errors.New("cached tokens are not valid for output")
		}
		if *details.CachedTokens < 0 || *details.CachedTokens > parent {
			return 0, 0, 0, errors.New("cached token count is outside input range")
		}
		cachedTokens = *details.CachedTokens
	}
	return textTokens, audioTokens, cachedTokens, nil
}

func buildRealtimeUpstream(channel *model.Channel, modelName, apiVersion, clientSubprotocol string) (string, http.Header, error) {
	return buildRealtimeUpstreamForRequest(channel, modelName, modelName, apiVersion, clientSubprotocol, nil)
}

func buildRealtimeUpstreamForRequest(
	channel *model.Channel,
	originalModel string,
	mappedModel string,
	apiVersion string,
	clientSubprotocol string,
	clientHeaders http.Header,
) (string, http.Header, error) {
	if channel == nil {
		return "", nil, errors.New("realtime channel is nil")
	}
	originalModel = strings.TrimSpace(originalModel)
	mappedModel = strings.TrimSpace(mappedModel)
	if originalModel == "" || mappedModel == "" {
		return "", nil, errors.New("realtime upstream model is empty")
	}
	apiKey := strings.TrimSpace(channelssvc.GetChannelKey(channel))

	channelType := channelcatalog.ChannelType(channel.Type)
	if channelType == channelcatalog.ChannelTypeAdvancedCustom {
		meta := &relaycommon.Meta{
			Channel:           channel,
			Mode:              channelcatalog.RelayModeRealtime,
			Format:            channelcatalog.RelayFormatOpenAI,
			RequestPath:       "/v1/realtime",
			OriginalModelName: originalModel,
			ModelName:         mappedModel,
			BaseURL:           channel.BaseURL,
			APIKey:            apiKey,
			APIVersion:        apiVersion,
			ClientHeaders:     clientHeaders.Clone(),
		}
		adaptor := GetAdaptor(channelType)
		if adaptor == nil {
			return "", nil, errors.New("advanced custom realtime adapter is unavailable")
		}
		adaptor.Init(meta)
		requestURL, err := adaptor.GetRequestURL(meta)
		if err != nil {
			return "", nil, err
		}
		request, err := http.NewRequest(http.MethodGet, requestURL, nil)
		if err != nil {
			return "", nil, err
		}
		if err := adaptor.SetupRequestHeader(request, meta); err != nil {
			return "", nil, err
		}
		websocketURL, err := websocketURLFromHTTP(requestURL)
		if err != nil {
			return "", nil, err
		}
		return websocketURL, request.Header, nil
	}
	if apiKey == "" {
		return "", nil, errors.New("realtime upstream API key is empty")
	}
	if !isOpenAICompatibleChannelType(channelType) {
		return "", nil, fmt.Errorf("channel type %d has no implemented realtime adapter", channel.Type)
	}
	meta := &relaycommon.Meta{
		Channel: channel, Mode: channelcatalog.RelayModeRealtime,
		ModelName: mappedModel, BaseURL: channel.BaseURL,
		APIKey: apiKey, APIVersion: apiVersion,
	}
	adaptor := &openaichannel.Adaptor{}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return "", nil, err
	}
	request, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return "", nil, err
	}
	if err := adaptor.SetupRequestHeader(request, meta); err != nil {
		return "", nil, err
	}
	websocketURL, err := websocketURLFromHTTP(requestURL)
	if err != nil {
		return "", nil, err
	}
	header := request.Header
	if channelType == channelcatalog.ChannelTypeAzure {
		return websocketURL, header, nil
	}
	legacyBeta := strings.Contains(mappedModel, "-realtime-preview")
	if strings.TrimSpace(clientSubprotocol) != "" {
		header.Del("Authorization")
		protocols := []string{"realtime", "openai-insecure-api-key." + apiKey}
		if legacyBeta {
			protocols = append(protocols, "openai-beta.realtime-v1")
		}
		header.Set("Sec-WebSocket-Protocol", strings.Join(protocols, ","))
	} else if legacyBeta {
		header.Set("OpenAI-Beta", "realtime=v1")
	}
	return websocketURL, header, nil
}

func websocketURLFromHTTP(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported websocket scheme %q", parsed.Scheme)
	}
	return parsed.String(), nil
}
