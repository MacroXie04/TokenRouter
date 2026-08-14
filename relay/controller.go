package relay

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/pkg/billingexpr"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/service"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
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

func init() {
	if relayHTTPClient.Timeout == 0 {
		relayHTTPClient.Timeout = 0 // no timeout (0 = unlimited)
	}
}

// RelayInfo carries state through a single relay request lifecycle.
type RelayInfo struct {
	Mode         constant.RelayMode
	Format       constant.RelayFormat
	ModelName    string
	Request      *protocolkit.GeneralOpenAIRequest
	PromptTokens int
	Quota        int
	QuotaClamp   *common.QuotaClamp
	Channel      *model.Channel
	Usage        *protocolkit.Usage
	Group        string
	IsStream     bool
}

// Relay is the main relay handler for OpenAI-compatible paths.
func Relay(c *gin.Context) {
	mode := constant.PathToRelayMode(c.Request.URL.Path)
	if mode == constant.RelayModeUnknown {
		c.JSON(http.StatusNotFound, gin.H{"error": protocolkit.OpenAIError{Message: "不支持的接口路径", Type: "invalid_request_error", Code: "not_found"}})
		return
	}

	request, err := parseRequest(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": protocolkit.OpenAIError{Message: err.Error(), Type: "invalid_request_error"}})
		return
	}

	info := &RelayInfo{
		Mode:    mode,
		Request: request,
		ModelName: request.Model,
		Group:  getRelayGroup(c),
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
func parseRequest(c *gin.Context) (*protocolkit.GeneralOpenAIRequest, error) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 16*1024*1024))
	if err != nil {
		return nil, errors.New("读取请求体失败")
	}
	req := &protocolkit.GeneralOpenAIRequest{}
	if err := protocolkit.UnmarshalJSON(body, req); err != nil {
		return nil, errors.New("无效的 JSON 请求体")
	}
	// Preserve the raw body for provider-specific passthrough fields.
	var extra map[string]any
	_ = protocolkit.UnmarshalJSON(body, &extra)
	req.Extra = extra
	if req.Model == "" {
		return nil, errors.New("缺少 model 字段")
	}
	return req, nil
}

// relayAndSettle runs the full lifecycle for a relay request.
func relayAndSettle(c *gin.Context, info *RelayInfo) error {
	start := time.Now()
	token := middleware.GetRelayToken(c)
	userId := common.GetUserId(c)

	info.IsStream = info.Request.Stream

	// Sensitive-word content moderation.
	if requestContainsSensitive(info.Request) {
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
	// Always reserve user quota before the upstream call, regardless of token
	// quota mode, so concurrent requests cannot overspend the shared quota.
	if err := service.PreConsumeUserQuota(userId, reserved); err != nil {
		abortRelay(c, http.StatusBadRequest, "用户额度不足", "insufficient_quota")
		return nil
	}

	retryTimes := common.GetEnvInt("RETRY_TIMES", setting.GetOptionIntOrDefault(setting.RetryTimesOption, 0))
	var lastErr error
	for attempt := 0; attempt <= retryTimes; attempt++ {
		ignore := map[int]struct{}{}
		if info.Channel != nil {
			ignore[info.Channel.Id] = struct{}{}
		}
		channel, err := service.GetSatisfiedChannelWithAffinity(info.Group, info.ModelName, userId, ignore, nil)
		if err != nil {
			lastErr = err
			break
		}
		info.Channel = channel

		usage, rerr := dispatchUpstream(c, info)
		if rerr != nil {
			lastErr = rerr
			// Retryable upstream errors (5xx, transport) retry; client errors abort.
			if !relaycommon.IsRetryableUpstreamError(rerr) {
				break
			}
			continue
		}
		info.Usage = usage
		service.SetAffinityChannel(userId, info.ModelName, info.Channel.Id)
		break
	}

	if info.Usage == nil && lastErr != nil {
		// Refund the reservation: no successful upstream response was produced.
		_ = service.RefundUserQuota(userId, reserved)
		relaycommon.WriteUpstreamError(c, lastErr)
		return nil
	}
	if info.Usage == nil {
		info.Usage = &protocolkit.Usage{}
	}

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

	// Settle user accounting: adjust the reservation to the actual amount
	// (refund the over-reservation or deduct the shortfall), and record
	// used_quota + request count exactly once.
	_ = service.SettleUserQuota(userId, reserved, info.Quota)

	// Token accounting: deduct actual usage from the token's remaining quota.
	if token != nil && !token.UnlimitedQuota {
		_ = service.DecreaseTokenQuota(token.Id, info.Quota)
	}

	// Usage log.
	service.RecordConsumeLog(
		userId,
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
		buildLogOther(info),
	)
	return nil
}

// dispatchUpstream builds the provider request and performs the upstream call.
func dispatchUpstream(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
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

func buildLogOther(info *RelayInfo) map[string]any {
	other := map[string]any{}
	if info.QuotaClamp != nil {
		other["quota_saturation"] = info.QuotaClamp
	}
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
