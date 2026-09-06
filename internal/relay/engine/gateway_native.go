package engine

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/newapi"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"strings"
)

// sendClaudeViaGateway keeps the Anthropic Messages document and wire format
// intact for gateway-to-gateway channels. Gateways accept both bearer and
// Anthropic authentication headers on the same canonical path.
func sendClaudeViaGateway(c *gin.Context, info *RelayInfo, base, rawCredential string) (*protocolkit.Usage, error) {
	if c == nil || info == nil || info.Channel == nil || info.ClaudeRequest == nil {
		return nil, errors.New("NewAPI Claude relay metadata is nil")
	}
	credential, err := newapi.ValidateCredential(rawCredential)
	if err != nil {
		return nil, err
	}
	requestURL, err := newapi.RequestURL(base, "/v1/messages")
	if err != nil {
		return nil, err
	}
	requestBody := *info.ClaudeRequest
	requestBody.Model = info.ClaudeRequest.Model
	body, err := protocolkit.MarshalJSON(&requestBody)
	if err != nil {
		return nil, fmt.Errorf("encode NewAPI Claude request: %w", err)
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	version := strings.TrimSpace(c.GetHeader("anthropic-version"))
	if version == "" {
		version = "2023-06-01"
	}
	if err := newapi.ValidateAnthropicVersion(version); err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("x-api-key", credential)
	request.Header.Set("anthropic-version", version)
	setting.ApplyClaudeModelHeaders(info.ModelName, request.Header)
	if info.IsStream {
		request.Header.Set("Accept", "text/event-stream")
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, request)

	response, err := relayHTTPClient.Do(request)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	var usage *protocolkit.Usage
	if info.IsStream {
		usage, err = claudeNativeStreamResponse(c, response, info)
	} else {
		usage, err = claudeNativeNonStreamResponseWithInfo(c, response, info)
	}
	return gatewayAcceptedResult(info, usage, err)
}

// geminiGatewayPassthrough preserves the caller's v1/v1beta native path,
// native request body, and native response while adding both gateway and
// Gemini authentication headers.
func geminiGatewayPassthrough(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
	if c == nil || info == nil || info.Channel == nil || info.GeminiRequest == nil {
		return nil, errors.New("NewAPI Gemini relay metadata is nil")
	}
	credential, err := newapi.ValidateCredential(channelssvc.GetChannelKey(info.Channel))
	if err != nil {
		return nil, err
	}
	requestURI, err := gatewayGeminiRequestURI(c, info)
	if err != nil {
		return nil, err
	}
	requestURL, err := newapi.RequestURL(info.Channel.BaseURL, requestURI)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, requestURL, bytes.NewReader(info.RawBody))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("x-goog-api-key", credential)
	if info.IsStream {
		request.Header.Set("Accept", "text/event-stream")
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, request)

	response, err := relayHTTPClient.Do(request)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	var usage *protocolkit.Usage
	if info.IsStream {
		usage, err = geminiNativeStreamResponseWithInfo(c, response, info)
	} else {
		usage, err = geminiNativeNonStreamResponseWithInfo(c, response, info)
	}
	return gatewayAcceptedResult(info, usage, err)
}

func gatewayGeminiRequestURI(c *gin.Context, info *RelayInfo) (string, error) {
	pathValue := c.Request.URL.Path
	prefix := ""
	switch {
	case strings.HasPrefix(pathValue, "/v1beta/models/"):
		prefix = "/v1beta/models/"
	case strings.HasPrefix(pathValue, "/v1/models/"):
		prefix = "/v1/models/"
	default:
		return "", errors.New("NewAPI Gemini request path is invalid")
	}
	remainder := strings.TrimPrefix(pathValue, prefix)
	action := ":generateContent"
	if info.IsStream && strings.HasSuffix(remainder, ":streamGenerateContent") {
		action = ":streamGenerateContent"
	}
	if !strings.HasSuffix(remainder, action) {
		return "", errors.New("NewAPI Gemini request action is invalid")
	}
	modelName := strings.TrimSuffix(remainder, action)
	if modelName == "" || modelName != info.ModelName || strings.ContainsAny(modelName, "/\\:?#\r\n\x00") {
		return "", errors.New("NewAPI Gemini request model is invalid")
	}
	// Gateway channels intentionally preserve the caller-visible model path.
	// The downstream gateway owns its alias mapping; local body policies have
	// already been applied without replacing that routing contract.
	requestURI := prefix + modelName + action
	if c.Request.URL.RawQuery != "" {
		requestURI += "?" + c.Request.URL.RawQuery
	}
	return requestURI, nil
}

// A 2xx gateway response means work may already have been accepted. If its
// body is malformed or the downstream write fails, return a conservative
// non-nil usage so the lifecycle settles once and never retries/refunds that
// potentially completed work.
func gatewayAcceptedResult(info *RelayInfo, usage *protocolkit.Usage, err error) (*protocolkit.Usage, error) {
	if err == nil || usage != nil {
		return usage, err
	}
	promptTokens := 1
	if info != nil && info.PromptTokens > promptTokens {
		promptTokens = info.PromptTokens
	}
	return &protocolkit.Usage{PromptTokens: promptTokens, TotalTokens: promptTokens}, err
}
