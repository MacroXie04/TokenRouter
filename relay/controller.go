package relay

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/pkg/billingexpr"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

var relayHTTPClient = &http.Client{
	Timeout: time.Duration(common.GetEnvInt("RELAY_TIMEOUT", 0)) * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: common.GetEnvBool("TLS_INSECURE_SKIP_VERIFY", false)},
		MaxIdleConns:    100,
		DialContext:     common.SafeDialContext,
	},
}

type firstResponseWriter struct {
	gin.ResponseWriter
	once sync.Once
	at   time.Time
}

func (writer *firstResponseWriter) Write(data []byte) (int, error) {
	writer.mark()
	return writer.ResponseWriter.Write(data)
}

func (writer *firstResponseWriter) WriteString(data string) (int, error) {
	writer.mark()
	return writer.ResponseWriter.WriteString(data)
}

func (writer *firstResponseWriter) mark() {
	writer.once.Do(func() { writer.at = time.Now() })
}

func init() {
	if relayHTTPClient.Timeout == 0 {
		relayHTTPClient.Timeout = 0 // no timeout (0 = unlimited)
	}
}

// RelayInfo carries state through a single relay request lifecycle.
type RelayInfo struct {
	Mode          constant.RelayMode
	Format        constant.RelayFormat
	ModelName     string
	Request       *protocolkit.GeneralOpenAIRequest
	ClaudeRequest *protocolkit.ClaudeRequest // set for Claude-format /v1/messages relays
	GeminiRequest *protocolkit.GeminiChatRequest
	RawBody       []byte // verbatim request body for native passthrough relays
	PromptTokens  int
	Quota         int
	QuotaClamp    *common.QuotaClamp
	Channel       *model.Channel
	Usage         *protocolkit.Usage
	Group         string
	IsStream      bool
}

// Relay is the main relay handler for OpenAI-compatible paths.
func Relay(c *gin.Context) {
	mode := constant.PathToRelayMode(c.Request.URL.Path)
	if mode == constant.RelayModeUnknown {
		c.JSON(http.StatusNotFound, gin.H{"error": protocolkit.OpenAIError{Message: "不支持的接口路径", Type: "invalid_request_error", Code: "not_found"}})
		return
	}

	request, rawBody, err := parseRequest(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": protocolkit.OpenAIError{Message: err.Error(), Type: "invalid_request_error"}})
		return
	}

	info := &RelayInfo{
		Mode:      mode,
		Format:    relaycommon.GetRelayFormat(constant.ChannelTypeOpenAI, mode),
		Request:   request,
		RawBody:   rawBody,
		ModelName: request.Model,
		Group:     getRelayGroup(c),
	}

	if err := relayAndSettle(c, info); err != nil {
		// Errors are written to the client within relayAndSettle where a
		// partial response hasn't already been streamed.
		return
	}
}

func getRelayGroup(c *gin.Context) string {
	if g := middleware.GetTokenGroup(c); g != "" {
		return g
	}
	return service.GroupDefault
}

// parseRequest reads and parses the request body into the OpenAI DTO.
func parseRequest(c *gin.Context) (*protocolkit.GeneralOpenAIRequest, []byte, error) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 16*1024*1024))
	if err != nil {
		return nil, nil, errors.New("读取请求体失败")
	}
	req := &protocolkit.GeneralOpenAIRequest{}
	if err := protocolkit.UnmarshalJSON(body, req); err != nil {
		return nil, nil, errors.New("无效的 JSON 请求体")
	}
	// Preserve the raw body for provider-specific passthrough fields.
	var extra map[string]any
	_ = protocolkit.UnmarshalJSON(body, &extra)
	req.Extra = extra
	if req.Model == "" {
		return nil, nil, errors.New("缺少 model 字段")
	}
	return req, body, nil
}

// relayAndSettle runs the full lifecycle for a relay request.
func relayAndSettle(c *gin.Context, info *RelayInfo) error {
	return relayAndSettleWithDispatch(c, info, dispatchUpstream)
}

// relayAndSettleWithDispatch is the shared lifecycle; Claude-format relays
// pass their own dispatch (native Anthropic passthrough or OpenAI conversion).
func relayAndSettleWithDispatch(c *gin.Context, info *RelayInfo, dispatch func(*gin.Context, *RelayInfo) (*protocolkit.Usage, error)) error {
	start := time.Now()
	originalWriter := c.Writer
	timedWriter := &firstResponseWriter{ResponseWriter: originalWriter}
	c.Writer = timedWriter
	metricSuccess := false
	defer func() {
		now := time.Now()
		latencyMs := now.Sub(start).Milliseconds()
		ttftMs := int64(0)
		generationMs := latencyMs
		hasTtft := info.IsStream && timedWriter.at.After(start)
		if hasTtft {
			ttftMs = timedWriter.at.Sub(start).Milliseconds()
			generationMs = now.Sub(timedWriter.at).Milliseconds()
		}
		if generationMs <= 0 {
			generationMs = latencyMs
		}
		outputTokens := int64(0)
		if info.Usage != nil {
			outputTokens = int64(info.Usage.CompletionTokens)
		}
		service.RecordPerfMetricSample(service.PerfMetricSample{
			Model: info.ModelName, Group: info.Group, LatencyMs: latencyMs,
			TtftMs: ttftMs, HasTtft: hasTtft, Success: metricSuccess,
			OutputTokens: outputTokens, GenerationMs: generationMs,
		})
		c.Writer = originalWriter
	}()
	token := middleware.GetRelayToken(c)
	userId := common.GetUserId(c)

	info.IsStream = info.Request.Stream

	// Sensitive-word content moderation (gated on the same defaults as the
	// reference: CheckSensitiveEnabled + CheckSensitiveOnPromptEnabled).
	if service.ShouldCheckPromptSensitive() && requestContainsSensitive(info.Request) {
		abortRelay(c, http.StatusBadRequest, "请求包含敏感内容", "invalid_request_error")
		return nil
	}

	// Prompt token estimation (for pre-consume and stream fallback).
	info.PromptTokens = relaycommon.EstimatePromptTokens(info.Request)

	// Pre-consume reservation against the token (check) and the user (reserve).
	reserved := estimateReservation(info)
	if token != nil && !token.UnlimitedQuota {
		if token.RemainQuota < reserved {
			abortRelay(c, http.StatusBadRequest, "令牌额度不足", "insufficient_quota")
			return nil
		}
	}
	// Always reserve before the upstream call, regardless of token quota mode,
	// so concurrent requests cannot overspend. The funding session picks the
	// source (subscription or wallet) from the user's billing preference.
	session, err := service.NewFundingSession(userId, reserved)
	if err != nil {
		switch {
		case service.IsSubscriptionFundingErr(err):
			abortRelay(c, http.StatusBadRequest, "订阅额度不足或未配置订阅: "+err.Error(), "insufficient_quota")
		case errors.Is(err, service.ErrInsufficientQuota):
			abortRelay(c, http.StatusBadRequest, "用户额度不足", "insufficient_quota")
		default:
			abortRelay(c, http.StatusInternalServerError, "预扣费失败: "+err.Error(), "pre_consume_failed")
		}
		return nil
	}

	retryTimes := common.GetEnvInt("RETRY_TIMES", setting.GetOptionIntOrDefault(setting.RetryTimesOption, 0))
	preferredChannelID, affinityFound := service.GetPreferredChannelByAffinity(c, info.ModelName, info.Group, info.RawBody)
	ignore := make(map[int]struct{}, retryTimes+1)
	initialChannelID := 0
	var lastErr error
	for attempt := 0; attempt <= retryTimes; attempt++ {
		channel, usedAffinity, err := service.GetSatisfiedChannelWithPreferred(info.Group, info.ModelName, preferredChannelID, ignore, nil)
		if err != nil {
			lastErr = err
			break
		}
		if attempt == 0 && affinityFound && !usedAffinity && !service.ShouldKeepChannelAffinityOnChannelDisabled() {
			service.ClearCurrentChannelAffinityCache(c)
		}
		if initialChannelID == 0 {
			initialChannelID = channel.Id
		}
		if usedAffinity {
			service.MarkChannelAffinityUsed(c, info.Group, channel.Id)
		}
		info.Channel = channel

		usage, rerr := dispatch(c, info)
		if rerr != nil {
			lastErr = rerr
			ignore[channel.Id] = struct{}{}
			if service.ShouldSkipRetryAfterChannelAffinityFailure(c) || !relaycommon.IsRetryableUpstreamError(rerr) {
				break
			}
			continue
		}
		info.Usage = usage
		lastErr = nil
		metricSuccess = true
		service.RecordChannelAffinity(c, initialChannelID, info.Channel.Id)
		break
	}

	if info.Usage == nil && lastErr != nil {
		// Refund the reservation: no successful upstream response was produced.
		session.Refund()
		relaycommon.WriteUpstreamError(c, lastErr)
		return nil
	}
	if info.Usage == nil {
		info.Usage = &protocolkit.Usage{}
	}
	service.ObserveChannelAffinityUsage(c, info.Usage, info.Format)

	// Settle actual quota (tiered expression billing when configured).
	isClaude := info.Channel != nil && info.Channel.Type == int(constant.ChannelTypeAnthropic)
	reqInput := billingexpr.RequestInput{
		Header: headerToMap(c.Request.Header),
		Body:   info.Request.Extra,
	}
	if info.Usage.PromptTokens == 0 && info.Usage.CompletionTokens == 0 {
		// No usage reported (e.g. a stream without a usage chunk); fall back to
		// the prompt estimate with flat pricing.
		info.Quota, info.QuotaClamp = service.ComputeQuotaChecked(info.ModelName, info.Group, info.PromptTokens, 0)
	} else {
		info.Quota, info.QuotaClamp, _ = service.ComputeBillingQuota(info.ModelName, info.Group, isClaude, info.Usage, reqInput)
	}

	// Settle the funding source: adjust the reservation to the actual amount
	// (refund the over-reservation or deduct the shortfall), and record
	// used_quota + request count exactly once.
	_ = session.Settle(info.Quota)

	// Low-quota reminder (once per threshold crossing; see service).
	service.CheckAndSendQuotaReminder(userId)

	// Token accounting: deduct actual usage from the token's remaining quota.
	if token != nil && !token.UnlimitedQuota {
		_ = service.DecreaseTokenQuota(token.Id, info.Quota)
	}

	// Usage log.
	service.RecordConsumeLog(
		userId,
		common.GetUsername(c),
		common.GetString(c, common.ContextKeyTokenName),
		info.ModelName,
		max(info.Usage.PromptTokens, info.PromptTokens),
		info.Usage.CompletionTokens,
		info.Quota,
		int(time.Since(start).Milliseconds()),
		info.IsStream,
		info.Channel.Id,
		info.Group,
		c.ClientIP(),
		common.GetRequestId(c),
		"",
		common.GetInt(c, common.ContextKeyTokenId),
		buildLogOther(c, info, session),
	)
	return nil
}

// dispatchUpstream builds the provider request and performs the upstream call.
func dispatchUpstream(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
	// Alpha search is a Codex-standalone endpoint: only the same upstream
	// families the reference gates support it; other channel types error so
	// the retry loop can fall through to another channel.
	if info.Mode == constant.RelayModeAlphaSearch {
		switch constant.ChannelType(info.Channel.Type) {
		case constant.ChannelTypeSub2API, constant.ChannelTypeNewAPI,
			constant.ChannelTypeCodex, constant.ChannelTypeAdvancedCustom:
		default:
			return nil, errors.New("channel does not support /v1/alpha/search")
		}
	}
	adaptor := GetAdaptor(constant.ChannelType(info.Channel.Type))
	meta := &relaycommon.Meta{
		Channel:      info.Channel,
		Mode:         info.Mode,
		Format:       relaycommon.GetRelayFormat(constant.ChannelType(info.Channel.Type), info.Mode),
		ModelName:    relaycommon.GetMappedModel(info.Channel, info.ModelName),
		BaseURL:      info.Channel.BaseURL,
		APIKey:       service.GetChannelKey(info.Channel),
		Request:      info.Request,
		IsStream:     info.IsStream,
		PromptTokens: info.PromptTokens,
	}
	adaptor.Init(meta)

	url, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	body, err := adaptor.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(req, meta); err != nil {
		return nil, err
	}
	service.ApplyChannelAffinityRequestHeaders(c, req)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return adaptor.DoResponse(c, resp, meta)
}

func estimateReservation(info *RelayInfo) int {
	estimatedCompletion := 256
	if info.Request.MaxTokens != nil {
		estimatedCompletion = *info.Request.MaxTokens
	} else if info.Request.MaxCompletionTokens != nil {
		estimatedCompletion = *info.Request.MaxCompletionTokens
	}
	return service.ComputeQuota(info.ModelName, info.Group, info.PromptTokens, estimatedCompletion)
}

func abortRelay(c *gin.Context, status int, message, code string) {
	c.JSON(status, gin.H{"error": protocolkit.OpenAIError{Message: message, Type: "invalid_request_error", Code: code}})
}

func buildLogOther(c *gin.Context, info *RelayInfo, session *service.FundingSession) map[string]any {
	other := map[string]any{}
	if info.QuotaClamp != nil {
		other["quota_saturation"] = info.QuotaClamp
	}
	for k, v := range session.BillingLogFields() {
		other[k] = v
	}
	service.AppendChannelAffinityAdminInfo(c, other)
	return other
}

// requestContainsSensitive reports whether a request's text content contains
// any configured sensitive word.
func requestContainsSensitive(req *protocolkit.GeneralOpenAIRequest) bool {
	if req == nil {
		return false
	}
	var sb strings.Builder
	for _, m := range req.Messages {
		sb.WriteString(relaycommon.MessageToText(m))
	}
	if s, ok := req.Prompt.(string); ok {
		sb.WriteString(s)
	}
	return service.CheckSensitiveContent(sb.String())
}

// headerToMap flattens an http.Header to a single-value string map.
func headerToMap(h http.Header) map[string]string {
	if len(h) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}
