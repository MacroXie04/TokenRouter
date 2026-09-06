package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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
	awsprovider "github.com/tokenrouter/tokenrouter/relay/channel/aws"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

var relayHTTPClient = newRelayHTTPClient(defaultRelayHTTPTransportConfig())

const (
	maxRelayRequestBodyBytes   int64 = 16 << 20
	defaultRelayRequestTimeout       = 10 * time.Minute
	maxRelayRequestTimeout           = 24 * time.Hour
)

type firstResponseWriter struct {
	gin.ResponseWriter
	once         sync.Once
	writeErrOnce sync.Once
	at           time.Time
	writeErr     error
}

func (writer *firstResponseWriter) Write(data []byte) (int, error) {
	writer.mark()
	n, err := writer.ResponseWriter.Write(data)
	writer.recordWriteError(err)
	return n, err
}

func (writer *firstResponseWriter) WriteString(data string) (int, error) {
	writer.mark()
	n, err := writer.ResponseWriter.WriteString(data)
	writer.recordWriteError(err)
	return n, err
}

func (writer *firstResponseWriter) mark() {
	writer.once.Do(func() { writer.at = time.Now() })
}

func (writer *firstResponseWriter) recordWriteError(err error) {
	if err != nil {
		writer.writeErrOnce.Do(func() { writer.writeErr = err })
	}
}

func relayRequestTimeout() time.Duration {
	seconds := common.GetEnvInt("RELAY_TIMEOUT", int(defaultRelayRequestTimeout/time.Second))
	if seconds <= 0 {
		return defaultRelayRequestTimeout
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout <= 0 || timeout > maxRelayRequestTimeout {
		return maxRelayRequestTimeout
	}
	return timeout
}

func applyRelayRequestDeadline(c *gin.Context) context.CancelFunc {
	ctx, cancel := context.WithTimeout(c.Request.Context(), relayRequestTimeout())
	c.Request = c.Request.WithContext(ctx)
	return cancel
}

// RelayInfo carries state through a single relay request lifecycle.
type RelayInfo struct {
	Mode                    constant.RelayMode
	Format                  constant.RelayFormat
	ModelName               string
	Request                 *protocolkit.GeneralOpenAIRequest
	ClaudeRequest           *protocolkit.ClaudeRequest // set for Claude-format /v1/messages relays
	ClaudeMaxTokensProvided bool
	claudePolicyPrepared    bool
	GeminiRequest           *protocolkit.GeminiChatRequest
	GeminiUpstreamModel     string
	RawBody                 []byte // verbatim request body for native passthrough relays
	RequestContentType      string
	APIVersion              string
	PromptTokens            int
	Quota                   int
	QuotaClamp              *common.QuotaClamp
	ToolQuotaClamp          *common.QuotaClamp
	ReferencePricing        map[string]service.ReferenceBillingPlan
	FreeModelPricing        map[string]bool
	UsageBillingFields      map[string]any
	ToolBillingFields       map[string]any
	ToolPriceSnapshot       setting.ToolPriceSnapshot
	ToolDeclaredNames       []string
	ToolGroupRatios         map[string]float64
	ToolPotential           map[string]bool
	ToolUsageCounter        *service.ToolUsageCounter
	ToolUsageHooks          *relaycommon.ToolUsageHooks
	UserID                  int
	Channel                 *model.Channel
	Usage                   *protocolkit.Usage
	UserGroup               string
	Group                   string
	AuthorizedGroups        []string
	CrossGroupRetry         bool
	IsStream                bool
}

// Relay is the main relay handler for OpenAI-compatible paths.
func Relay(c *gin.Context) {
	cancel := applyRelayRequestDeadline(c)
	defer cancel()
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
	if err := validateAndNormalizeRelayRequest(mode, request, c.GetHeader("Content-Type")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": protocolkit.OpenAIError{Message: err.Error(), Type: "invalid_request_error"}})
		return
	}

	info := &RelayInfo{
		Mode:               mode,
		Format:             relaycommon.GetRelayFormat(constant.ChannelTypeOpenAI, mode),
		Request:            request,
		RawBody:            rawBody,
		RequestContentType: c.GetHeader("Content-Type"),
		APIVersion:         c.Query("api-version"),
		ModelName:          request.Model,
		UserGroup:          common.GetUserGroup(c),
		Group:              getRelayGroup(c),
	}

	if err := relayAndSettle(c, info); err != nil {
		// The lifecycle writes a client-visible error whenever the response is
		// still retractable. Once a stream has started, surface the failure to
		// operators without appending a second protocol envelope to the stream.
		common.SysError("ordinary relay lifecycle failed: " + err.Error())
		return
	}
}

func getRelayGroup(c *gin.Context) string {
	groups := middleware.GetTokenGroups(c)
	if len(groups) > 0 {
		return groups[0]
	}
	return ""
}

// parseRequest reads and parses the request body into the OpenAI DTO.
func parseRequest(c *gin.Context) (*protocolkit.GeneralOpenAIRequest, []byte, error) {
	body, err := common.ReadAllLimited(c.Request.Body, maxRelayRequestBodyBytes)
	if err != nil {
		if errors.Is(err, common.ErrBodyTooLarge) {
			return nil, nil, fmt.Errorf("请求体超过 %d 字节限制: %w", maxRelayRequestBodyBytes, err)
		}
		return nil, nil, errors.New("读取请求体失败")
	}
	mediaType, params, mediaErr := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if mediaErr == nil && mediaType == "multipart/form-data" {
		req, parseErr := parseMultipartRelayRequest(body, params["boundary"])
		if parseErr != nil {
			return nil, nil, parseErr
		}
		return req, body, nil
	}
	req := &protocolkit.GeneralOpenAIRequest{}
	if err := protocolkit.UnmarshalJSON(body, req); err != nil {
		return nil, nil, errors.New("无效的 JSON 请求体")
	}
	// Preserve the raw body for provider-specific passthrough fields.
	var extra map[string]any
	_ = protocolkit.UnmarshalJSON(body, &extra)
	if extra == nil {
		extra = make(map[string]any)
	}
	req.Extra = extra
	if req.Model == "" {
		switch constant.PathToRelayMode(c.Request.URL.Path) {
		case constant.RelayModeModerations:
			req.Model = "omni-moderation-latest"
			req.Extra["model"] = req.Model
		case constant.RelayModeEmbeddings:
			if strings.Contains(c.Request.URL.Path, "/engines/") {
				req.Model = strings.TrimSpace(c.Param("model"))
				if req.Model == "" {
					parts := strings.Split(strings.Trim(c.Request.URL.Path, "/"), "/")
					if len(parts) >= 3 && parts[len(parts)-1] == "embeddings" {
						req.Model = parts[len(parts)-2]
					}
				}
				if req.Model != "" {
					req.Extra["model"] = req.Model
				}
			}
		}
	}
	if req.Model == "" {
		return nil, nil, errors.New("缺少 model 字段")
	}
	return req, body, nil
}

func parseMultipartRelayRequest(body []byte, boundary string) (*protocolkit.GeneralOpenAIRequest, error) {
	if strings.TrimSpace(boundary) == "" {
		return nil, errors.New("multipart 请求缺少 boundary")
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	fields := make(map[string]any)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("无效的 multipart 请求体")
		}
		name := part.FormName()
		if name == "" || part.FileName() != "" {
			continue
		}
		value, err := io.ReadAll(part)
		if err != nil {
			return nil, errors.New("读取 multipart 字段失败")
		}
		text := string(value)
		if previous, exists := fields[name]; exists {
			switch values := previous.(type) {
			case []any:
				fields[name] = append(values, text)
			default:
				fields[name] = []any{previous, text}
			}
		} else {
			fields[name] = text
		}
	}
	modelName, _ := fields["model"].(string)
	if strings.TrimSpace(modelName) == "" {
		return nil, errors.New("缺少 model 字段")
	}
	req := &protocolkit.GeneralOpenAIRequest{Model: modelName, Extra: fields}
	if prompt, ok := fields["prompt"].(string); ok {
		req.Prompt = prompt
	}
	if stream, ok := fields["stream"].(string); ok {
		parsed, err := strconv.ParseBool(stream)
		if err != nil {
			return nil, errors.New("stream 必须是布尔值")
		}
		req.Stream = parsed
	}
	if rawN, ok := fields["n"].(string); ok && strings.TrimSpace(rawN) != "" {
		n, err := strconv.Atoi(rawN)
		if err != nil {
			return nil, errors.New("n 必须是整数")
		}
		req.N = &n
	}
	return req, nil
}

const maxImageCount = 128

func validateAndNormalizeRelayRequest(mode constant.RelayMode, req *protocolkit.GeneralOpenAIRequest, contentType string) error {
	if req == nil {
		return errors.New("请求体不能为空")
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "multipart/form-data" {
		switch mode {
		case constant.RelayModeImagesEdits, constant.RelayModeAudioTranscription, constant.RelayModeAudioTranslation:
		default:
			return errors.New("此接口不支持 multipart/form-data")
		}
	}
	switch mode {
	case constant.RelayModeImagesGenerations, constant.RelayModeImagesEdits:
		if req.N != nil && (*req.N < 0 || *req.N > maxImageCount) {
			return fmt.Errorf("n 必须是 1 到 %d 之间的整数", maxImageCount)
		}
		if req.N == nil || *req.N == 0 {
			one := 1
			req.N = &one
			if mediaType != "multipart/form-data" {
				req.Extra["n"] = one
			}
		}
	case constant.RelayModeEmbeddings, constant.RelayModeModerations:
		if req.Extra["input"] == nil {
			return errors.New("input 不能为空")
		}
	case constant.RelayModeResponses:
		if req.Extra["input"] == nil {
			return errors.New("input 不能为空")
		}
	case constant.RelayModeRerank:
		query, _ := req.Extra["query"].(string)
		documents, documentsOK := req.Extra["documents"].([]any)
		if strings.TrimSpace(query) == "" {
			return errors.New("query 不能为空")
		}
		if !documentsOK || len(documents) == 0 {
			return errors.New("documents 不能为空")
		}
	}
	return nil
}

// relayAndSettle runs the full lifecycle for a relay request.
func relayAndSettle(c *gin.Context, info *RelayInfo) error {
	return relayAndSettleWithDispatch(c, info, dispatchUpstream)
}

// relayAndSettleWithDispatch is the shared lifecycle; Claude-format relays
// pass their own dispatch (native Anthropic passthrough or OpenAI conversion).
func relayAndSettleWithDispatch(c *gin.Context, info *RelayInfo, dispatch func(*gin.Context, *RelayInfo) (*protocolkit.Usage, error)) error {
	if info == nil || info.Request == nil {
		abortRelay(c, http.StatusInternalServerError, "中继请求上下文无效", "relay_context_invalid")
		return errors.New("relay request context is nil")
	}
	if !middleware.RelayModelAllowed(c, info.ModelName) {
		message := "令牌无权访问模型 " + info.ModelName
		if info.Format == constant.RelayFormatClaude {
			abortClaude(c, http.StatusForbidden, "permission_error", message)
		} else {
			abortRelay(c, http.StatusForbidden, message, "model_not_allowed")
		}
		return nil
	}
	policy := middleware.GetRelayGroupPolicy(c)
	if len(policy.Groups) == 0 && info.Group != "" && info.Group != service.GroupAuto {
		// Internal tests and trusted callers may construct RelayInfo directly.
		policy.Groups = []string{info.Group}
	}
	if len(policy.Groups) == 0 {
		writeRelayAccountingFailure(c, info, http.StatusForbidden, "令牌分组不可用", "group_not_allowed")
		return nil
	}
	info.AuthorizedGroups = append([]string(nil), policy.Groups...)
	info.CrossGroupRetry = policy.CrossGroupRetry
	info.Group = info.AuthorizedGroups[0]

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
	info.UserID = userId

	info.IsStream = info.Request.Stream
	info.ToolPriceSnapshot = setting.CaptureToolPriceSnapshot().Frozen()
	info.ToolDeclaredNames = declaredToolPricingNames(info)

	// Sensitive-word content moderation (gated on the same defaults as the
	// reference: CheckSensitiveEnabled + CheckSensitiveOnPromptEnabled).
	if service.ShouldCheckPromptSensitive() && requestContainsSensitive(info.Request) {
		abortRelay(c, http.StatusBadRequest, "请求包含敏感内容", "invalid_request_error")
		return nil
	}

	// Prompt token estimation (for pre-consume and stream fallback).
	info.PromptTokens = relaycommon.EstimatePromptTokens(info.Request)

	// Atomically reserve both the limited token and the selected user funding
	// source before contacting an upstream. A stale in-memory token balance is
	// never used as the concurrency guard.
	reserved, err := estimateReservationForGroupsChecked(info, info.AuthorizedGroups)
	if err != nil {
		writeRelayAccountingFailure(c, info, http.StatusBadRequest, "预扣额度无效", "invalid_quota")
		return nil
	}
	reservation, err := service.NewOrdinaryRelayQuotaReservationWithFreeModel(
		userId, token, reserved, allRelayGroupsUseFreeModelPricing(info, info.AuthorizedGroups),
	)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "refund token reservation"):
			common.SysError("ordinary relay reservation rollback failed: " + err.Error())
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "预扣费回滚失败", "pre_consume_failed")
		case errors.Is(err, service.ErrInsufficientTokenQuota):
			abortRelay(c, http.StatusBadRequest, "令牌额度不足", "insufficient_quota")
		case service.IsSubscriptionFundingErr(err):
			abortRelay(c, http.StatusBadRequest, "订阅额度不足或未配置订阅: "+err.Error(), "insufficient_quota")
		case errors.Is(err, service.ErrInsufficientQuota):
			abortRelay(c, http.StatusBadRequest, "用户额度不足", "insufficient_quota")
		default:
			common.SysError("ordinary relay reservation failed: " + err.Error())
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "预扣费失败", "pre_consume_failed")
		}
		return nil
	}

	// Non-stream responses are held briefly so a failed accounting transaction
	// can replace an upstream success with a coherent error. The writer spills
	// responses above a fixed bound, preserving large/binary endpoint behavior.
	var deferredWriter *deferredResponseWriter
	if !info.IsStream {
		deferredWriter = newDeferredResponseWriter(originalWriter)
		timedWriter.ResponseWriter = deferredWriter
	}
	discardDeferred := func() {
		if deferredWriter != nil && deferredWriter.Discard() {
			timedWriter.ResponseWriter = originalWriter
		}
	}

	reliabilityPolicy := setting.GetChannelReliabilitySetting()
	retryTimes := reliabilityPolicy.RetryTimes
	ignore := make(map[int]struct{}, retryTimes+1)
	initialChannelID := 0
	initialChannelGroup := ""
	pinnedGroup := ""
	var lastErr error
	upstreamUsageObserved := false
	reservationDispatched := false
	for attempt := 0; attempt <= retryTimes; attempt++ {
		candidateGroups := info.AuthorizedGroups
		if pinnedGroup != "" && !info.CrossGroupRetry {
			candidateGroups = []string{pinnedGroup}
		}
		channel, selectedGroup, usedAffinity, affinityFound, err := selectAuthorizedRelayChannel(c, info, candidateGroups, ignore)
		if err != nil {
			lastErr = err
			break
		}
		if pinnedGroup == "" {
			pinnedGroup = selectedGroup
		}
		info.Group = selectedGroup
		c.Set(common.ContextKeyGroup, selectedGroup)
		if attempt == 0 && affinityFound && !usedAffinity && !service.ShouldKeepChannelAffinityOnChannelDisabled() {
			service.ClearCurrentChannelAffinityCache(c)
		}
		if initialChannelID == 0 {
			initialChannelID = channel.Id
			initialChannelGroup = selectedGroup
		}
		if usedAffinity {
			service.MarkChannelAffinityUsed(c, info.Group, channel.Id)
		}
		info.Channel = channel
		if err := prepareToolPricingAttempt(info); err != nil {
			lastErr = fmt.Errorf("prepare tool pricing: %w", err)
			break
		}
		if !reservationDispatched {
			// Persist the at-risk transition immediately before the first network
			// dispatch. A crash after this point must never make the recovery sweep
			// treat possibly accepted upstream work as an unused hold.
			if dispatchErr := reservation.MarkDispatched(); dispatchErr != nil {
				refundErr := reservation.Refund()
				discardDeferred()
				writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "无法持久化上游派发状态", "pre_consume_failed")
				return errors.Join(
					fmt.Errorf("mark relay quota reservation dispatched: %w", dispatchErr),
					wrapRelayError("refund undispatched relay quota reservation", refundErr),
				)
			}
			reservationDispatched = true
		}

		usage, rerr := dispatch(c, info)
		if rerr == nil && timedWriter.writeErr != nil {
			rerr = fmt.Errorf("write relay response: %w", timedWriter.writeErr)
		}
		if rerr != nil {
			if channel.Type == int(constant.ChannelTypeXai) {
				rerr = relaycommon.NormalizeGrokViolationError(rerr)
			}
			if usage != nil {
				info.Usage = usage
				upstreamUsageObserved = true
			}
			lastErr = rerr
			ignore[channel.Id] = struct{}{}
			// Retrying after any bytes or headers reached the client would splice
			// multiple upstream responses together. A partial response is settled
			// below; a fully deferred non-stream failure can still be retried.
			if originalWriter.Written() || upstreamUsageObserved {
				break
			}
			if deferredWriter != nil {
				deferredWriter.Reset()
			}
			affinityFailure := service.ShouldSkipRetryAfterChannelAffinityFailure(c)
			if !service.ShouldRetryChannelFailure(reliabilityPolicy, service.RetryPolicyInput{
				Failure: relayChannelFailure(rerr), AffinityFailure: affinityFailure,
				RemainingRetries: retryTimes - attempt,
			}) {
				break
			}
			continue
		}
		info.Usage = usage
		if info.Mode == constant.RelayModeAlphaSearch && info.ToolUsageHooks != nil &&
			info.ToolUsageHooks.MarkAlphaSearchComplete != nil {
			if observeErr := info.ToolUsageHooks.MarkAlphaSearchComplete(); observeErr != nil {
				lastErr = fmt.Errorf("observe alpha search completion: %w", observeErr)
				break
			}
		}
		lastErr = nil
		metricSuccess = true
		affinityInitialChannelID := initialChannelID
		if initialChannelGroup != info.Group {
			// The final candidate has a group-scoped affinity key. Never write a
			// failed channel from an earlier group into that group's cache.
			affinityInitialChannelID = info.Channel.Id
		}
		service.RecordChannelAffinity(c, affinityInitialChannelID, info.Channel.Id)
		break
	}

	responseCommitted := originalWriter.Written()
	if lastErr != nil && !responseCommitted && !upstreamUsageObserved {
		// Refund the reservation: no successful upstream response was produced.
		if refundErr := reservation.Refund(); refundErr != nil {
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "预扣额度退款失败", "refund_failed")
			return fmt.Errorf("refund relay quota after upstream failure: %w", errors.Join(lastErr, refundErr))
		}
		if relaycommon.IsGrokViolationError(lastErr) {
			ratio, _ := service.EffectiveGroupRatio(info.UserGroup, info.Group)
			statusCode := http.StatusBadRequest
			var upstream *relaycommon.UpstreamError
			if errors.As(lastErr, &upstream) && upstream != nil && upstream.StatusCode > 0 {
				statusCode = upstream.StatusCode
			}
			useTime := int(time.Since(start) / time.Second)
			if int64(useTime) > common.MaxQuota {
				useTime = int(common.MaxQuota)
			}
			if _, feeErr := service.ChargeGrokViolationFee(service.GrokViolationFeeInput{
				ReservationID: reservation.ReservationID(),
				ChannelID:     info.Channel.Id,
				ModelName:     info.ModelName,
				Group:         info.Group,
				RequestID:     common.GetRequestId(c),
				StatusCode:    statusCode,
				UseTime:       useTime,
				IsStream:      info.IsStream,
				GroupRatio:    ratio,
			}); feeErr != nil {
				discardDeferred()
				writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "违规费用记账失败", "violation_fee_accounting_failed")
				return fmt.Errorf("charge post-refund violation fee: %w", errors.Join(lastErr, feeErr))
			}
		}
		discardDeferred()
		relaycommon.WriteUpstreamError(c, lastErr)
		return nil
	}
	usageReported := info.Usage != nil
	if info.Usage == nil {
		info.Usage = &protocolkit.Usage{}
	}
	protocolkit.NormalizeOpenAIUsageAliases(info.Usage)
	if err := validateUsageForBilling(info.Usage); err != nil {
		discardDeferred()
		writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "上游用量无效，无法结算", "settlement_failed")
		return fmt.Errorf("invalid upstream usage: %w", err)
	}
	service.ObserveChannelAffinityUsage(c, info.Usage, info.Format)

	// Settle actual quota (tiered expression billing when configured).
	isClaude := info.Usage.BillingSemantic == "anthropic" || info.Channel != nil &&
		(info.Channel.Type == int(constant.ChannelTypeAnthropic) ||
			(info.Channel.Type == int(constant.ChannelTypeAws) &&
				!awsprovider.IsNovaModel(relaycommon.GetMappedModel(info.Channel, info.ModelName))))
	reqInput := billingexpr.RequestInput{
		Header: headerToMap(c.Request.Header),
		Body:   info.Request.Extra,
	}
	referencePlan, useReferencePricing := info.ReferencePricing[info.Group]
	if useReferencePricing {
		settlementUsage := info.Usage
		if !usageReported {
			// The reference distinguishes an explicit zero-usage report (free)
			// from an omitted usage report, which falls back to the prompt estimate.
			settlementUsage = &protocolkit.Usage{
				PromptTokens: info.PromptTokens,
				TotalTokens:  info.PromptTokens,
			}
		}
		settlement, billingErr := referencePlan.SettlementUsageQuota(
			settlementUsage,
			service.ReferenceUsageContext{
				IsClaude:   isClaude,
				ForceAudio: isExplicitAudioRelayMode(info.Mode),
			},
		)
		if billingErr != nil {
			info.QuotaClamp = settlement.Clamp
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "计费计算失败", "settlement_failed")
			return fmt.Errorf("compute reference relay quota: %w", billingErr)
		}
		info.Quota = settlement.Quota
		info.QuotaClamp = settlement.Clamp
		info.UsageBillingFields = settlement.BillingLogFields()
	} else if info.Usage.PromptTokens == 0 && info.Usage.CompletionTokens == 0 {
		// No usage reported (e.g. a stream without a usage chunk); fall back to
		// the prompt estimate with flat pricing.
		info.Quota, info.QuotaClamp = service.ComputeQuotaForUserChecked(
			info.ModelName, info.UserGroup, info.Group, info.PromptTokens, 0,
		)
	} else {
		var billingErr error
		info.Quota, info.QuotaClamp, billingErr = service.ComputeBillingQuotaForUser(
			info.ModelName, info.UserGroup, info.Group, isClaude, info.Usage, reqInput,
		)
		if billingErr != nil {
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "计费计算失败", "settlement_failed")
			return fmt.Errorf("compute relay quota: %w", billingErr)
		}
	}
	if info.QuotaClamp != nil {
		discardDeferred()
		writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "计费额度超出安全范围", "settlement_failed")
		return fmt.Errorf("model quota conversion was not exact: %w", info.QuotaClamp)
	}
	if info.Quota < 0 {
		discardDeferred()
		writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "计费额度无效", "settlement_failed")
		return fmt.Errorf("computed negative relay quota: %d", info.Quota)
	}
	if info.ToolUsageCounter != nil {
		groupRatio, present := info.ToolGroupRatios[info.Group]
		if !present {
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "工具计费快照缺失", "settlement_failed")
			return errors.New("tool pricing group ratio snapshot is missing")
		}
		toolSettlement, toolErr := info.ToolUsageCounter.Settle(groupRatio)
		info.ToolQuotaClamp = toolSettlement.Clamp
		if toolErr != nil {
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "工具计费计算失败", "settlement_failed")
			return fmt.Errorf("compute tool surcharge: %w", toolErr)
		}
		if info.ToolQuotaClamp != nil {
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "工具计费额度超出安全范围", "settlement_failed")
			return fmt.Errorf("tool quota conversion was not exact: %w", info.ToolQuotaClamp)
		}
		combined, ok := common.AddQuotaWithinBounds(info.Quota, toolSettlement.Quota)
		if !ok {
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "工具计费额度溢出", "settlement_failed")
			return errors.New("combined model and tool quota exceeds accounting bounds")
		}
		info.Quota = combined
		info.ToolBillingFields = toolSettlement.AuditFields()
	}

	// Reconcile user funding, token remaining quota, token used quota, and user
	// lifetime counters in one transaction. On failure the original reservation
	// stays held for reconciliation; it is never refunded after accepted work.
	if err := reservation.SettleWithChannel(info.Quota, info.Channel.Id); err != nil {
		discardDeferred()
		writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "请求已完成但额度结算失败", "settlement_failed")
		return fmt.Errorf("settle relay quota: %w", err)
	}

	// The finance transaction cannot span a separately configured log database.
	// A log failure is returned and reported, but the already-committed success
	// response is still delivered so clients are not encouraged to retry a
	// request that was charged successfully.
	logErr := service.RecordConsumeLogChecked(
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
		buildLogOther(c, info, reservation),
	)
	if lastErr != nil && !responseCommitted && !timedWriter.Written() {
		discardDeferred()
		relaycommon.WriteUpstreamError(c, lastErr)
		return errors.Join(
			fmt.Errorf("upstream failed after reporting billable usage: %w", lastErr),
			wrapRelayError("record relay consume log", logErr),
		)
	}
	var responseErr error
	if deferredWriter != nil {
		responseErr = deferredWriter.Commit()
	}
	// Logging is attempted before any user-configured notification endpoint can
	// delay this handler. The settled reservation selects wallet vs subscription.
	service.CheckAndSendQuotaReminderForReservation(userId, reservation)
	if lastErr != nil {
		return errors.Join(
			fmt.Errorf("upstream failed after relay response started: %w", lastErr),
			wrapRelayError("record relay consume log", logErr),
			wrapRelayError("commit relay response", responseErr),
		)
	}
	return errors.Join(
		wrapRelayError("record relay consume log", logErr),
		wrapRelayError("commit relay response", responseErr),
	)
}

func relayChannelFailure(err error) service.ChannelFailure {
	failure := service.ChannelFailure{ErrorPresent: err != nil}
	if err == nil {
		return failure
	}
	var upstream *relaycommon.UpstreamError
	if errors.As(err, &upstream) && upstream != nil {
		failure.StatusCode = upstream.StatusCode
		failure.SkipRetry = upstream.SkipRetry
		failure.BadResponseBody = errors.Is(upstream.Cause, relaycommon.ErrUpstreamResponseTooLarge)
		failure.Message = upstream.Body
		return failure
	}
	failure.ChannelError = errors.Is(err, relaycommon.ErrUpstreamTransportFailed)
	return failure
}

func prepareToolPricingAttempt(info *RelayInfo) error {
	if info == nil || info.Channel == nil {
		return errors.New("tool pricing channel is unavailable")
	}
	pricingContext := toolPricingContext(info, toolBillingProvider(info.Channel))
	counter, err := service.NewToolUsageCounter(
		info.ToolPriceSnapshot,
		pricingContext,
	)
	if err != nil {
		return err
	}
	info.ToolUsageCounter = counter
	hooks := &relaycommon.ToolUsageHooks{}
	switch pricingContext.Mode {
	case service.ToolBillingModeResponses:
		hooks.ObserveResponsesOutput = func(observation relaycommon.ToolResponsesObservation) error {
			_, err := counter.ObserveResponsesOutput(service.ResponsesToolOutput{
				Type: observation.Type, ID: observation.ID, CallID: observation.CallID,
				OutputIndex: observation.OutputIndex, Name: observation.Name,
				Status: observation.Status, Result: observation.Result,
			})
			return err
		}
		hooks.FinishResponses = counter.FinishResponses
	case service.ToolBillingModeChatCompletions:
		hooks.ObserveChatToolCall = func(observation relaycommon.ToolChatObservation) error {
			_, err := counter.ObserveChatToolCall(service.ChatToolCallObservation{
				ChoiceIndex: observation.ChoiceIndex, ToolIndex: observation.ToolIndex,
				ArrayIndex: observation.ArrayIndex, ID: observation.ID, Name: observation.Name,
			})
			return err
		}
	case service.ToolBillingModeClaudeMessages:
		hooks.ObserveClaudeToolUse = func(observation relaycommon.ToolClaudeObservation) error {
			_, err := counter.ObserveClaudeToolUse(service.ClaudeToolUseObservation{
				BlockIndex: observation.BlockIndex, ID: observation.ID, Name: observation.Name,
			})
			return err
		}
		hooks.SetClaudeWebSearchCount = counter.SetClaudeWebSearchRequests
	case service.ToolBillingModeGeminiNative:
		hooks.MarkGeminiGoogleSearch = counter.MarkGeminiGoogleSearch
	case service.ToolBillingModeAlphaSearch:
		hooks.MarkAlphaSearchComplete = counter.MarkAlphaSearchCompleted
	}
	info.ToolUsageHooks = hooks
	return nil
}

func toolPricingContext(info *RelayInfo, provider service.ToolBillingProvider) service.ToolPricingContext {
	context := service.ToolPricingContext{
		Mode: service.ToolBillingModeChatCompletions, Provider: provider,
	}
	if info == nil {
		return context
	}
	context.ModelName = info.ModelName
	context.DeclaredBuiltInTools = append([]string(nil), info.ToolDeclaredNames...)
	switch {
	case info.Mode == constant.RelayModeAlphaSearch:
		context.Mode = service.ToolBillingModeAlphaSearch
	case info.Mode == constant.RelayModeResponses || info.Format == constant.RelayFormatOpenAIResponses:
		context.Mode = service.ToolBillingModeResponses
	case info.Format == constant.RelayFormatClaude:
		context.Mode = service.ToolBillingModeClaudeMessages
	case info.Format == constant.RelayFormatGemini:
		context.Mode = service.ToolBillingModeGeminiNative
	case info.Channel != nil && toolResponseModeForChannel(info.Channel, info.ModelName) == service.ToolBillingModeClaudeMessages:
		context.Mode = service.ToolBillingModeClaudeMessages
	case info.Channel != nil && toolResponseModeForChannel(info.Channel, info.ModelName) == service.ToolBillingModeGeminiNative:
		context.Mode = service.ToolBillingModeGeminiNative
	}
	return context
}

func toolResponseModeForChannel(channel *model.Channel, modelName string) service.ToolBillingMode {
	if channel == nil {
		return service.ToolBillingModeChatCompletions
	}
	switch constant.ChannelType(channel.Type) {
	case constant.ChannelTypeAnthropic:
		return service.ToolBillingModeClaudeMessages
	case constant.ChannelTypeAws:
		if !awsprovider.IsNovaModel(relaycommon.GetMappedModel(channel, modelName)) {
			return service.ToolBillingModeClaudeMessages
		}
	case constant.ChannelTypeGemini, constant.ChannelTypeVertexAi:
		return service.ToolBillingModeGeminiNative
	}
	return service.ToolBillingModeChatCompletions
}

func toolBillingProvider(channel *model.Channel) service.ToolBillingProvider {
	if channel == nil {
		return service.ToolBillingProviderOther
	}
	switch constant.ChannelType(channel.Type) {
	case constant.ChannelTypeOpenAI, constant.ChannelTypeAzure, constant.ChannelTypeCodex:
		return service.ToolBillingProviderOpenAI
	case constant.ChannelTypeAnthropic, constant.ChannelTypeAws:
		return service.ToolBillingProviderAnthropic
	case constant.ChannelTypeGemini, constant.ChannelTypeVertexAi:
		return service.ToolBillingProviderGemini
	default:
		return service.ToolBillingProviderOther
	}
}

func declaredToolPricingNames(info *RelayInfo) []string {
	if info == nil {
		return nil
	}
	seen := make(map[string]struct{})
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	addTool := func(toolType, functionName string) {
		switch strings.ToLower(strings.TrimSpace(toolType)) {
		case setting.ToolWebSearch, "web_search_20250305":
			add(setting.ToolWebSearch)
		case setting.ToolWebSearchPreview:
			add(setting.ToolWebSearchPreview)
		case setting.ToolFileSearch:
			add(setting.ToolFileSearch)
		case setting.ToolImageGeneration, "image_generation_call":
			add(setting.ToolImageGeneration)
		case "function", "custom":
			add(functionName)
		default:
			add(functionName)
		}
	}
	if info.Request != nil {
		for _, tool := range info.Request.Tools {
			name := ""
			if tool.Function != nil {
				name = tool.Function.Name
			}
			addTool(tool.Type, name)
		}
		if info.Request.WebSearchOptions != nil {
			add(setting.ToolWebSearchPreview)
		}
	}
	if info.ClaudeRequest != nil {
		for _, tool := range info.ClaudeRequest.Tools {
			if strings.HasPrefix(strings.ToLower(tool.Name), "web_search") {
				add(setting.ToolWebSearch)
			} else {
				add(tool.Name)
			}
		}
	}
	if info.GeminiRequest != nil {
		for _, tool := range info.GeminiRequest.Tools {
			if tool.GoogleSearch != nil || tool.GoogleSearchRetrieval != nil {
				add(setting.ToolGoogleSearch)
			}
			for _, function := range tool.FunctionDeclarations {
				add(function.Name)
			}
		}
	}
	if info.Mode == constant.RelayModeAlphaSearch {
		add(setting.ToolWebSearchPreview)
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func selectAuthorizedRelayChannel(c *gin.Context, info *RelayInfo, groups []string, ignore map[int]struct{}) (*model.Channel, string, bool, bool, error) {
	for _, group := range groups {
		preferredChannelID, affinityFound := service.GetPreferredChannelByAffinity(c, info.ModelName, group, info.RawBody)
		channel, usedAffinity, err := service.GetSatisfiedChannelWithPreferred(group, info.ModelName, preferredChannelID, ignore, nil)
		if err == nil {
			return channel, group, usedAffinity, affinityFound, nil
		}
		if !errors.Is(err, service.ErrChannelNotFound) {
			return nil, "", false, affinityFound, err
		}
	}
	return nil, "", false, false, service.ErrChannelNotFound
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
	if adaptor == nil {
		return nil, fmt.Errorf("channel type %d has no implemented relay adapter", info.Channel.Type)
	}
	meta := &relaycommon.Meta{
		Context:            c.Request.Context(),
		Channel:            info.Channel,
		Mode:               info.Mode,
		Format:             relaycommon.GetRelayFormat(constant.ChannelType(info.Channel.Type), info.Mode),
		RequestPath:        c.Request.URL.Path,
		OriginalModelName:  info.ModelName,
		ModelName:          relaycommon.GetMappedModel(info.Channel, info.ModelName),
		BaseURL:            info.Channel.BaseURL,
		APIKey:             service.GetChannelKey(info.Channel),
		ClientHeaders:      c.Request.Header.Clone(),
		Request:            info.Request,
		RawBody:            info.RawBody,
		RequestContentType: info.RequestContentType,
		APIVersion:         info.APIVersion,
		IsStream:           info.IsStream,
		PromptTokens:       info.PromptTokens,
		ToolUsage:          info.ToolUsageHooks,
	}
	adaptor.Init(meta)

	url, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, relaycommon.SanitizeUpstreamError(err, meta.APIKey)
	}
	body, err := adaptor.ConvertRequest(meta)
	if err != nil {
		// Some provider conversions perform bounded, credentialed auxiliary
		// operations (for example uploading an inline file). Sanitize those
		// upstream errors just like the primary response path.
		return nil, relaycommon.SanitizeUpstreamError(err, meta.APIKey)
	}
	if direct, ok := adaptor.(relaycommon.DirectAdaptor); ok && isDirectRelayURL(url) {
		usage, directErr := direct.DoDirectRequest(c, url, body, meta)
		return usage, relaycommon.SanitizeUpstreamError(directErr, meta.APIKey)
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(req, meta); err != nil {
		return nil, relaycommon.SanitizeUpstreamError(err, meta.APIKey)
	}
	service.ApplyChannelAffinityRequestHeaders(c, req)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer resp.Body.Close()

	usage, responseErr := adaptor.DoResponse(c, resp, meta)
	return usage, relaycommon.SanitizeUpstreamError(responseErr, meta.APIKey)
}

func isDirectRelayURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Scheme == "ws" || parsed.Scheme == "wss"
}

const maxRelayOutputTokens = int(common.MaxQuota / 2)

func estimateReservationChecked(info *RelayInfo) (int, error) {
	return estimateReservationForGroupChecked(info, info.Group)
}

func estimateReservationForGroupsChecked(info *RelayInfo, groups []string) (int, error) {
	if len(groups) == 0 {
		return 0, errors.New("relay has no authorized groups")
	}
	maximum := 0
	for _, group := range groups {
		quota, err := estimateReservationForGroupChecked(info, group)
		if err != nil {
			return 0, err
		}
		if quota > maximum {
			maximum = quota
		}
	}
	return maximum, nil
}

func estimateReservationForGroupChecked(info *RelayInfo, group string) (int, error) {
	if info == nil || info.Request == nil {
		return 0, errors.New("relay request is nil")
	}
	for name, value := range map[string]*int{
		"max_tokens":            info.Request.MaxTokens,
		"max_completion_tokens": info.Request.MaxCompletionTokens,
	} {
		if value != nil && (*value < 0 || *value > maxRelayOutputTokens) {
			return 0, fmt.Errorf("%s is outside the supported range", name)
		}
	}
	if info.PromptTokens < 0 {
		return 0, errors.New("estimated prompt tokens must not be negative")
	}
	estimatedCompletion := 0
	completionLimitProvided := false
	if info.Request.MaxTokens != nil {
		estimatedCompletion = *info.Request.MaxTokens
		completionLimitProvided = true
	} else if info.Request.MaxCompletionTokens != nil {
		estimatedCompletion = *info.Request.MaxCompletionTokens
		completionLimitProvided = true
	}
	referencePlan, useReferencePricing, err := service.ResolveOrdinaryReferenceBillingPlanForUser(
		info.UserID, info.ModelName, info.UserGroup, group,
	)
	if err != nil {
		return 0, err
	}
	var baseQuota int
	if useReferencePricing {
		if info.ReferencePricing == nil {
			info.ReferencePricing = make(map[string]service.ReferenceBillingPlan)
		}
		info.ReferencePricing[group] = referencePlan
		if info.FreeModelPricing == nil {
			info.FreeModelPricing = make(map[string]bool)
		}
		info.FreeModelPricing[group] = referencePlan.FreeModel()
		quota, err := referencePlan.PreConsumeQuota(
			info.PromptTokens, estimatedCompletion, completionLimitProvided,
		)
		if err != nil {
			return 0, err
		}
		baseQuota = quota
	} else {
		delete(info.ReferencePricing, group)
		if info.FreeModelPricing == nil {
			info.FreeModelPricing = make(map[string]bool)
		}
		info.FreeModelPricing[group] = service.ShouldSkipOrdinaryFreeModelPreConsume(
			info.ModelName, info.UserGroup, group,
		)
		if !completionLimitProvided {
			estimatedCompletion = 256
		}
		quota, clamp := service.ComputeQuotaForUserChecked(
			info.ModelName, info.UserGroup, group, info.PromptTokens, estimatedCompletion,
		)
		if clamp != nil {
			return 0, clamp
		}
		if quota < 0 {
			return 0, fmt.Errorf("reservation quota must not be negative: %d", quota)
		}
		baseQuota = quota
	}
	ratio, _ := service.EffectiveGroupRatio(info.UserGroup, group)
	if info.ToolGroupRatios == nil {
		info.ToolGroupRatios = make(map[string]float64)
	}
	info.ToolGroupRatios[group] = ratio
	toolFloor, toolPotential, err := service.MinimumPotentialToolSurcharge(
		info.ToolPriceSnapshot, toolPricingContext(info, service.ToolBillingProviderOther), ratio,
	)
	if err != nil {
		return 0, err
	}
	if info.ToolPotential == nil {
		info.ToolPotential = make(map[string]bool)
	}
	info.ToolPotential[group] = toolPotential
	combined, ok := common.AddQuotaWithinBounds(baseQuota, toolFloor)
	if !ok {
		return 0, errors.New("combined model and minimum tool reservation exceeds accounting bounds")
	}
	return combined, nil
}

func allRelayGroupsUseFreeModelPricing(info *RelayInfo, groups []string) bool {
	if info == nil || len(groups) == 0 || len(info.FreeModelPricing) == 0 {
		return false
	}
	for _, group := range groups {
		if !info.FreeModelPricing[group] || info.ToolPotential[group] {
			return false
		}
	}
	return true
}

func isExplicitAudioRelayMode(mode constant.RelayMode) bool {
	switch mode {
	case constant.RelayModeAudioSpeech,
		constant.RelayModeAudioTranscription,
		constant.RelayModeAudioTranslation:
		return true
	default:
		return false
	}
}

func abortRelay(c *gin.Context, status int, message, code string) {
	c.JSON(status, gin.H{"error": protocolkit.OpenAIError{Message: message, Type: "invalid_request_error", Code: code}})
}

func writeRelayAccountingFailure(c *gin.Context, info *RelayInfo, status int, message, code string) {
	if c.Writer.Written() {
		return
	}
	if info != nil && info.Format == constant.RelayFormatClaude {
		abortClaude(c, status, "api_error", message)
		return
	}
	abortRelay(c, status, message, code)
}

func validateUsageForBilling(usage *protocolkit.Usage) error {
	if usage == nil {
		return nil
	}
	values := map[string]int{
		"prompt_tokens":                   usage.PromptTokens,
		"completion_tokens":               usage.CompletionTokens,
		"total_tokens":                    usage.TotalTokens,
		"input_tokens":                    usage.InputTokens,
		"output_tokens":                   usage.OutputTokens,
		"prompt_cache_hit_tokens":         usage.PromptCacheHitTokens,
		"prompt_cache_miss_tokens":        usage.PromptCacheMissTokens,
		"prompt_cache_write_tokens":       usage.PromptCacheWriteTokens,
		"prompt_cache_creation_tokens":    usage.PromptCacheCreationTokens,
		"prompt_cache_creation_5m_tokens": usage.PromptCacheCreation5mTokens,
		"prompt_cache_creation_1h_tokens": usage.PromptCacheCreation1hTokens,
		"audio_tokens":                    usage.AudioTokens,
		"reasoning_tokens":                usage.ReasoningTokens,
	}
	if usage.PromptTokensDetails != nil {
		values["prompt_details.cached_tokens"] = usage.PromptTokensDetails.CachedTokens
		values["prompt_details.cached_creation_tokens"] = usage.PromptTokensDetails.CachedCreationTokens
		values["prompt_details.cache_write_tokens"] = usage.PromptTokensDetails.CacheWriteTokens
		values["prompt_details.cache_creation_5m_tokens"] = usage.PromptTokensDetails.CacheCreation5mTokens
		values["prompt_details.cache_creation_1h_tokens"] = usage.PromptTokensDetails.CacheCreation1hTokens
		values["prompt_details.text_tokens"] = usage.PromptTokensDetails.TextTokens
		values["prompt_details.audio_tokens"] = usage.PromptTokensDetails.AudioTokens
		values["prompt_details.image_tokens"] = usage.PromptTokensDetails.ImageTokens
		values["prompt_details.reasoning_tokens"] = usage.PromptTokensDetails.ReasoningTokens
	}
	if usage.CompletionTokensDetails != nil {
		values["completion_details.text_tokens"] = usage.CompletionTokensDetails.TextTokens
		values["completion_details.audio_tokens"] = usage.CompletionTokensDetails.AudioTokens
		values["completion_details.image_tokens"] = usage.CompletionTokensDetails.ImageTokens
		values["completion_details.reasoning_tokens"] = usage.CompletionTokensDetails.ReasoningTokens
	}
	if usage.InputTokensDetails != nil {
		values["input_details.cached_tokens"] = usage.InputTokensDetails.CachedTokens
		values["input_details.cached_creation_tokens"] = usage.InputTokensDetails.CachedCreationTokens
		values["input_details.cache_write_tokens"] = usage.InputTokensDetails.CacheWriteTokens
		values["input_details.cache_creation_5m_tokens"] = usage.InputTokensDetails.CacheCreation5mTokens
		values["input_details.cache_creation_1h_tokens"] = usage.InputTokensDetails.CacheCreation1hTokens
		values["input_details.text_tokens"] = usage.InputTokensDetails.TextTokens
		values["input_details.audio_tokens"] = usage.InputTokensDetails.AudioTokens
		values["input_details.image_tokens"] = usage.InputTokensDetails.ImageTokens
		values["input_details.reasoning_tokens"] = usage.InputTokensDetails.ReasoningTokens
	}
	if usage.OutputTokensDetails != nil {
		values["output_details.text_tokens"] = usage.OutputTokensDetails.TextTokens
		values["output_details.audio_tokens"] = usage.OutputTokensDetails.AudioTokens
		values["output_details.image_tokens"] = usage.OutputTokensDetails.ImageTokens
		values["output_details.reasoning_tokens"] = usage.OutputTokensDetails.ReasoningTokens
	}
	for name, value := range values {
		if value < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
		if int64(value) > common.MaxQuota {
			return fmt.Errorf("%s is outside the supported range", name)
		}
	}
	return nil
}

func wrapRelayError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func buildLogOther(c *gin.Context, info *RelayInfo, reservation *service.RelayQuotaReservation) map[string]any {
	other := map[string]any{}
	if info != nil {
		if plan, referencePriced := info.ReferencePricing[info.Group]; referencePriced {
			for key, value := range plan.BillingLogFields() {
				other[key] = value
			}
		} else {
			ratio, special := service.EffectiveGroupRatio(info.UserGroup, info.Group)
			other["group_ratio"] = ratio
			if special {
				other["user_group_ratio"] = ratio
			}
		}
		for key, value := range info.UsageBillingFields {
			other[key] = value
		}
		for key, value := range info.ToolBillingFields {
			other[key] = value
		}
	}
	if info.QuotaClamp != nil {
		other["quota_saturation"] = info.QuotaClamp
	}
	if info.ToolQuotaClamp != nil {
		other["tool_quota_saturation"] = info.ToolQuotaClamp
	}
	for k, v := range reservation.BillingLogFields() {
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
