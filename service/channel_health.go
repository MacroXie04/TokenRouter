package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	awsprovider "github.com/tokenrouter/tokenrouter/relay/channel/aws"
	"github.com/tokenrouter/tokenrouter/relay/channel/baidu"
	"github.com/tokenrouter/tokenrouter/relay/channel/baidu_v2"
	"github.com/tokenrouter/tokenrouter/relay/channel/coze"
	"github.com/tokenrouter/tokenrouter/relay/channel/dify"
	"github.com/tokenrouter/tokenrouter/relay/channel/mokaai"
	"github.com/tokenrouter/tokenrouter/relay/channel/palm"
	"github.com/tokenrouter/tokenrouter/relay/channel/zhipu"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/setting"
	"gorm.io/gorm"
)

// healthHTTPClient dials upstreams through the SSRF guard.
var healthHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext:     common.SafeDialContext,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: common.GetEnvBool("TLS_INSECURE_SKIP_VERIFY", false)},
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// TestChannelHealth performs a minimal chat-completion against a channel and
// reports success and latency. It is used by the channel-test endpoint and the
// periodic auto-disable job.
func TestChannelHealth(channelId int) (bool, int) {
	success, latency, _ := TestChannelHealthDetailed(channelId, "")
	return success, latency
}

// TestChannelHealthDetailed is TestChannelHealth plus an optional test-model
// override and a human-readable error message for the dashboard channel-test
// contract.
func TestChannelHealthDetailed(channelId int, modelOverride string) (bool, int, string) {
	return testChannelHealthDetailedContext(context.Background(), channelId, modelOverride)
}

type channelHealthProbeResult struct {
	Success       bool
	Latency       int
	DisplayReason string
	Failure       *ChannelFailure
	LocalError    bool
}

func testChannelHealthDetailedContext(ctx context.Context, channelId int, modelOverride string) (bool, int, string) {
	result := probeChannelHealthContext(ctx, channelId, modelOverride)
	return result.Success, result.Latency, result.DisplayReason
}

func probeChannelHealthContext(ctx context.Context, channelId int, modelOverride string) channelHealthProbeResult {
	channel, err := GetChannelByID(channelId)
	if err != nil {
		return channelHealthProbeResult{DisplayReason: "channel not found", LocalError: true}
	}
	modelName := strings.TrimSpace(modelOverride)
	if modelName == "" {
		modelName = channel.TestModel
	}
	if modelName == "" {
		modelName = firstModel(channel.Models)
	}
	if modelName == "" {
		return channelHealthProbeResult{DisplayReason: "channel has no test model", LocalError: true}
	}
	req, err := buildChannelHealthRequest(ctx, channel, modelName)
	if err != nil {
		return channelHealthProbeResult{DisplayReason: common.RedactSensitiveText(err.Error()), LocalError: true}
	}

	start := time.Now()
	resp, err := healthHTTPClient.Do(req)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		display := relaycommon.SanitizeTransportError(err).Error()
		return channelHealthProbeResult{
			Latency: latency, DisplayReason: display,
			Failure: &ChannelFailure{ErrorPresent: true, ChannelError: true, Message: display},
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, readErr := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamErrorBodyBytes)
		failure := &ChannelFailure{
			ErrorPresent: true, StatusCode: resp.StatusCode,
			BadResponseBody: readErr != nil,
		}
		if readErr == nil {
			failure.Message = string(body)
		}
		return channelHealthProbeResult{
			Latency: latency, DisplayReason: "upstream returned status " + resp.Status,
			Failure: failure,
		}
	}
	return channelHealthProbeResult{Success: true, Latency: latency}
}

func buildChannelHealthRequest(ctx context.Context, channel *model.Channel, modelName string) (*http.Request, error) {
	if channel == nil {
		return nil, errors.New("channel not found")
	}
	channelType := constant.ChannelType(channel.Type)
	if !channelHealthSupported(channelType) {
		return nil, fmt.Errorf("%s channel test is not supported", constant.ChannelTypeName(channelType))
	}

	mappedModel := mapChannelModel(channel, modelName)
	if strings.TrimSpace(mappedModel) == "" {
		return nil, errors.New("channel test model is empty after mapping")
	}
	credential := GetChannelKey(channel)
	if channelType == constant.ChannelTypeAws {
		return buildAWSHealthRequest(ctx, channel, modelName, mappedModel, credential)
	}

	base := strings.TrimRight(strings.TrimSpace(channel.BaseURL), "/")
	if base == "" && channel.Type >= 0 && channel.Type < len(constant.ChannelBaseURLs) {
		base = strings.TrimRight(constant.ChannelBaseURLs[channel.Type], "/")
	}
	if base == "" {
		return nil, errors.New("channel has no upstream base URL")
	}

	var (
		requestURL string
		payload    any
	)
	switch channelType {
	case constant.ChannelTypeCodex:
		accessToken, accountID, err := parseCodexOperationalCredential(credential)
		if err != nil {
			return nil, err
		}
		requestURL = base + "/backend-api/codex/responses"
		payload = map[string]any{
			"model": mappedModel, "input": "ping", "instructions": "", "store": false,
		}
		req, err := newJSONHealthRequest(ctx, requestURL, payload)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("chatgpt-account-id", accountID)
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("originator", "codex_cli_rs")
		return req, nil
	case constant.ChannelTypeAnthropic:
		if credential == "" {
			return nil, errors.New("Anthropic channel API key is empty")
		}
		requestURL = base + "/v1/messages"
		payload = map[string]any{
			"model": mappedModel, "max_tokens": 1,
			"messages": []map[string]any{{"role": "user", "content": "ping"}},
		}
		req, err := newJSONHealthRequest(ctx, requestURL, payload)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-api-key", credential)
		req.Header.Set("anthropic-version", "2023-06-01")
		return req, nil
	case constant.ChannelTypeGemini:
		if credential == "" {
			return nil, errors.New("Gemini channel API key is empty")
		}
		geminiModel := strings.TrimPrefix(strings.TrimSpace(mappedModel), "models/")
		requestURL = base + "/v1beta/models/" + url.PathEscape(geminiModel) + ":generateContent"
		payload = map[string]any{
			"contents": []map[string]any{{
				"role": "user", "parts": []map[string]any{{"text": "ping"}},
			}},
			"generationConfig": map[string]any{"maxOutputTokens": 1},
		}
		req, err := newJSONHealthRequest(ctx, requestURL, payload)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-goog-api-key", credential)
		return req, nil
	case constant.ChannelTypeAzure:
		if credential == "" {
			return nil, errors.New("Azure OpenAI API key is empty")
		}
		version := strings.TrimSpace(channel.Other)
		if version == "" {
			version = common.GetEnv("AZURE_DEFAULT_API_VERSION", "2025-04-01-preview")
		}
		query := url.Values{"api-version": []string{version}}.Encode()
		requestURL = base + "/openai/deployments/" + url.PathEscape(mappedModel) + "/chat/completions?" + query
		payload = openAIHealthPayload(mappedModel)
		req, err := newJSONHealthRequest(ctx, requestURL, payload)
		if err != nil {
			return nil, err
		}
		req.Header.Set("api-key", credential)
		return req, nil
	case constant.ChannelTypePerplexity:
		requestURL = base + "/chat/completions"
		payload = openAIHealthPayload(mappedModel)
		req, err := newJSONHealthRequest(ctx, requestURL, payload)
		if err != nil {
			return nil, err
		}
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		return req, nil
	case constant.ChannelTypeAli:
		if credential == "" {
			return nil, errors.New("Ali channel API key is empty")
		}
		requestURL = base + "/compatible-mode/v1/chat/completions"
		payload = openAIHealthPayload(mappedModel)
		req, err := newJSONHealthRequest(ctx, requestURL, payload)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Accept", "application/json")
		return req, nil
	case constant.ChannelTypeBaidu:
		return buildBaiduHealthRequest(ctx, channel, base, mappedModel, credential, &baidu.Adaptor{})
	case constant.ChannelTypeBaiduV2:
		return buildBaiduHealthRequest(ctx, channel, base, mappedModel, credential, &baidu_v2.Adaptor{})
	case constant.ChannelTypeZhipu:
		requestURL, err := zhipu.LegacyRequestURL(base, mappedModel, false)
		if err != nil {
			return nil, err
		}
		authorization, err := zhipu.LegacyAuthorization(credential)
		if err != nil {
			return nil, err
		}
		payload = map[string]any{
			"prompt": []map[string]any{{"role": "user", "content": "ping"}},
		}
		req, err := newJSONHealthRequest(ctx, requestURL, payload)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", authorization)
		req.Header.Set("Accept", "application/json")
		return req, nil
	case constant.ChannelTypeZhipuV4:
		if strings.TrimSpace(credential) == "" {
			return nil, errors.New("Zhipu v4 channel API key is empty")
		}
		requestURL, err := zhipu.V4RequestURL(base, constant.RelayModeChatCompletions, constant.RelayFormatOpenAI)
		if err != nil {
			return nil, err
		}
		payload = openAIHealthPayload(mappedModel)
		req, err := newJSONHealthRequest(ctx, requestURL, payload)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Accept", "application/json")
		return req, nil
	case constant.ChannelTypeMiniMax:
		if strings.TrimSpace(credential) == "" {
			return nil, errors.New("MiniMax channel API key is empty")
		}
		requestURL = base + "/v1/text/chatcompletion_v2"
		payload = openAIHealthPayload(mappedModel)
		req, err := newJSONHealthRequest(ctx, requestURL, payload)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Accept", "application/json")
		return req, nil
	case constant.ChannelTypeCohere:
		if credential == "" {
			return nil, errors.New("Cohere channel API key is empty")
		}
		requestURL = base + "/v1/chat"
		payload = map[string]any{
			"model": mappedModel, "message": "ping", "chat_history": []any{},
			"stream": false, "max_tokens": 1,
		}
		req, err := newJSONHealthRequest(ctx, requestURL, payload)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Accept", "application/json")
		return req, nil
	case constant.ChannelTypeDify:
		difyURL, err := dify.RequestURL(base)
		if err != nil {
			return nil, err
		}
		requestURL = difyURL
		payload = map[string]any{
			"inputs": map[string]any{}, "query": "USER: \nping\n", "response_mode": "blocking",
			"user": common.BestEffortUUID(), "auto_generate_name": false, "files": []any{},
		}
		req, err := newJSONHealthRequest(ctx, requestURL, payload)
		if err != nil {
			return nil, err
		}
		meta := &relaycommon.Meta{
			Mode: constant.RelayModeChatCompletions, Format: constant.RelayFormatOpenAI,
			APIKey: credential, Request: &protocolkit.GeneralOpenAIRequest{},
		}
		adapter := &dify.Adaptor{}
		adapter.Init(meta)
		if err := adapter.SetupRequestHeader(req, meta); err != nil {
			return nil, err
		}
		return req, nil
	case constant.ChannelTypeCoze:
		maxTokens := 1
		requestBody := &protocolkit.GeneralOpenAIRequest{
			Model: mappedModel,
			Messages: []protocolkit.Message{{
				Role: "user", Content: "ping",
			}},
			MaxTokens: &maxTokens,
		}
		meta := &relaycommon.Meta{
			Context: ctx, Channel: channel,
			Mode: constant.RelayModeChatCompletions, Format: constant.RelayFormatOpenAI,
			OriginalModelName: modelName, ModelName: mappedModel,
			BaseURL: base, APIKey: credential, Request: requestBody,
		}
		adapter := &coze.Adaptor{}
		adapter.Init(meta)
		requestURL, err := adapter.GetRequestURL(meta)
		if err != nil {
			return nil, err
		}
		body, err := adapter.ConvertRequest(meta)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if err := adapter.SetupRequestHeader(req, meta); err != nil {
			return nil, err
		}
		return req, nil
	case constant.ChannelTypeMokaAI:
		meta := &relaycommon.Meta{
			Context: ctx, Channel: channel,
			Mode: constant.RelayModeEmbeddings, Format: constant.RelayFormatEmbedding,
			ModelName: mappedModel, BaseURL: base, APIKey: credential,
			Request: &protocolkit.GeneralOpenAIRequest{Extra: map[string]any{"input": "ping"}},
		}
		adapter := &mokaai.Adaptor{}
		adapter.Init(meta)
		requestURL, err := adapter.GetRequestURL(meta)
		if err != nil {
			return nil, err
		}
		body, err := adapter.ConvertRequest(meta)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if err := adapter.SetupRequestHeader(req, meta); err != nil {
			return nil, err
		}
		return req, nil
	case constant.ChannelTypePaLM:
		meta := &relaycommon.Meta{
			Context: ctx, Channel: channel,
			Mode: constant.RelayModeChatCompletions, Format: constant.RelayFormatOpenAI,
			ModelName: mappedModel, BaseURL: base, APIKey: credential,
			Request: &protocolkit.GeneralOpenAIRequest{
				Model: mappedModel, Messages: []protocolkit.Message{{Role: "user", Content: "ping"}}, Extra: map[string]any{},
			},
		}
		adapter := &palm.Adaptor{}
		adapter.Init(meta)
		requestURL, err := adapter.GetRequestURL(meta)
		if err != nil {
			return nil, err
		}
		body, err := adapter.ConvertRequest(meta)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if err := adapter.SetupRequestHeader(req, meta); err != nil {
			return nil, err
		}
		return req, nil
	default:
		requestURL = base + "/v1/chat/completions"
		payload = openAIHealthPayload(mappedModel)
		req, err := newJSONHealthRequest(ctx, requestURL, payload)
		if err != nil {
			return nil, err
		}
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		if channelType == constant.ChannelTypeOpenAI && strings.TrimSpace(channel.OpenAIOrganization) != "" {
			req.Header.Set("OpenAI-Organization", channel.OpenAIOrganization)
		}
		if channelType == constant.ChannelTypeOpenRouter {
			req.Header.Set("HTTP-Referer", "https://github.com/MacroXie04/TokenRouter")
			req.Header.Set("X-OpenRouter-Title", "TokenRouter")
		}
		return req, nil
	}
}

func channelHealthSupported(channelType constant.ChannelType) bool {
	return channelType == constant.ChannelTypeAnthropic ||
		channelType == constant.ChannelTypeAws ||
		channelType == constant.ChannelTypeGemini ||
		channelType == constant.ChannelTypeCodex ||
		channelType == constant.ChannelTypeAli ||
		channelType == constant.ChannelTypeZhipu ||
		channelType == constant.ChannelTypeZhipuV4 ||
		channelType == constant.ChannelTypeMiniMax ||
		channelType == constant.ChannelTypeCohere ||
		channelType == constant.ChannelTypeCoze ||
		channelType == constant.ChannelTypeDify ||
		channelType == constant.ChannelTypeBaidu ||
		channelType == constant.ChannelTypeBaiduV2 ||
		channelType == constant.ChannelTypeMokaAI ||
		channelType == constant.ChannelTypePaLM ||
		channelType == constant.ChannelTypeSubmodel ||
		constant.IsOpenAICompatibleChannelType(channelType)
}

func buildAWSHealthRequest(ctx context.Context, channel *model.Channel, originalModel, mappedModel, credential string) (*http.Request, error) {
	maxTokens := 1
	openAIRequest := &protocolkit.GeneralOpenAIRequest{
		Model: mappedModel, MaxTokens: &maxTokens,
		Messages: []protocolkit.Message{{Role: "user", Content: "ping"}},
		Extra:    map[string]any{"model": mappedModel},
	}
	meta := &relaycommon.Meta{
		Context: ctx, Channel: channel, Mode: constant.RelayModeChatCompletions,
		Format: constant.RelayFormatOpenAI, OriginalModelName: originalModel, ModelName: mappedModel,
		BaseURL: channel.BaseURL, APIKey: credential, Request: openAIRequest,
		RequestContentType: "application/json",
	}
	adaptor := &awsprovider.Adaptor{}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	body, err := adaptor.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("build AWS channel health request")
	}
	if err := adaptor.SetupRequestHeader(request, meta); err != nil {
		return nil, err
	}
	return request, nil
}

func buildBaiduHealthRequest(
	ctx context.Context,
	channel *model.Channel,
	baseURL string,
	modelName string,
	credential string,
	adaptor relaycommon.Adaptor,
) (*http.Request, error) {
	maxTokens := 1
	requestBody := &protocolkit.GeneralOpenAIRequest{
		Model: modelName,
		Messages: []protocolkit.Message{{
			Role: "user", Content: "ping",
		}},
		MaxTokens: &maxTokens,
	}
	meta := &relaycommon.Meta{
		Context: ctx, Channel: channel,
		Mode: constant.RelayModeChatCompletions, Format: constant.RelayFormatOpenAI,
		ModelName: modelName, BaseURL: baseURL, APIKey: credential, Request: requestBody,
	}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	body, err := adaptor.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(request, meta); err != nil {
		return nil, err
	}
	return request, nil
}

func openAIHealthPayload(modelName string) map[string]any {
	return map[string]any{
		"model": modelName, "max_tokens": 1,
		"messages": []map[string]any{{"role": "user", "content": "ping"}},
	}
}

func newJSONHealthRequest(ctx context.Context, requestURL string, payload any) (*http.Request, error) {
	body, err := common.Marshal(payload)
	if err != nil {
		return nil, errors.New("encode channel health request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func mapChannelModel(channel *model.Channel, modelName string) string {
	if channel == nil || strings.TrimSpace(channel.ModelMapping) == "" {
		return modelName
	}
	mapping := map[string]string{}
	if err := common.UnmarshalJsonStr(channel.ModelMapping, &mapping); err != nil {
		return modelName
	}
	if mapped := strings.TrimSpace(mapping[modelName]); mapped != "" {
		return mapped
	}
	if mapped := strings.TrimSpace(mapping["*"]); mapped != "" {
		return mapped
	}
	return modelName
}

func parseCodexOperationalCredential(raw string) (string, string, error) {
	var key struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	}
	if err := common.UnmarshalJsonStr(strings.TrimSpace(raw), &key); err != nil {
		return "", "", errors.New("Codex channel key must be a valid JSON OAuth object")
	}
	key.AccessToken = strings.TrimSpace(key.AccessToken)
	key.AccountID = strings.TrimSpace(key.AccountID)
	if key.AccessToken == "" {
		return "", "", errors.New("Codex channel access_token is required")
	}
	if key.AccountID == "" {
		return "", "", errors.New("Codex channel account_id is required")
	}
	return key.AccessToken, key.AccountID, nil
}

func firstModel(models string) string {
	for _, m := range strings.Split(models, ",") {
		if m = strings.TrimSpace(m); m != "" {
			return m
		}
	}
	return ""
}

// testAndRecordChannel runs the health test for one channel and persists the
// outcome: response_time/test_time are always updated, and auto-ban channels
// are disabled on failure / re-enabled on recovery.
func testAndRecordChannel(ch *model.Channel, now int64) (bool, int, error) {
	return testAndRecordChannelContext(context.Background(), ch, now)
}

func testAndRecordChannelContext(ctx context.Context, ch *model.Channel, now int64) (bool, int, error) {
	return testAndRecordChannelContextWithMode(ctx, ch, now, true)
}

func testAndRecordChannelContextWithMode(ctx context.Context, ch *model.Channel, now int64, allowDisable bool) (bool, int, error) {
	probe := probeChannelHealthContext(ctx, ch.Id, "")
	success, latency := probe.Success, probe.Latency
	if err := ctx.Err(); err != nil {
		return false, latency, err
	}
	updates := map[string]any{"response_time": latency, "test_time": now}
	policy := setting.GetChannelReliabilitySetting()
	transition := DecideChannelHealthTransition(policy, ChannelHealthPolicyInput{
		Status: ch.Status, AutoBan: channelAutoBanEnabled(*ch), AllowDisable: allowDisable,
		LocalError: probe.LocalError, LatencyMilliseconds: latency, Failure: probe.Failure,
	})
	newStatus := transition.NextStatus
	statusChanged := newStatus != ch.Status
	if statusChanged {
		updates["status"] = newStatus
	}
	err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.Channel{}).Where("id = ?", ch.Id).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var count int64
			if err := tx.Model(&model.Channel{}).Where("id = ?", ch.Id).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return gorm.ErrRecordNotFound
			}
		}
		if statusChanged {
			if err := tx.Model(&model.Ability{}).Where("channel_id = ?", ch.Id).
				Update("enabled", newStatus == constant.ChannelStatusEnabled).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return success, latency, fmt.Errorf("persist channel %d health result: %w", ch.Id, err)
	}
	if statusChanged {
		if err := SyncAbilityCache(); err != nil {
			return success, latency, fmt.Errorf("refresh ability cache after channel %d health result: %w", ch.Id, err)
		}
		if err := notifyRootChannelStatus(ch.Id, ch.Name, newStatus, transition.Reason); err != nil {
			common.SysError(fmt.Sprintf("notify root about channel %d health status failed", ch.Id))
		}
	}
	ch.ResponseTime = latency
	ch.TestTime = now
	ch.Status = newStatus
	return success, latency, nil
}

// RunChannelHealthTests tests every channel that has a test model, records its
// response time, and auto-disables failing channels (or re-enables recovered
// ones) when the channel opts into auto-ban.
func RunChannelHealthTests() error {
	return RunChannelHealthTestsMode(setting.ChannelTestModeScheduledAll)
}

// RunChannelHealthTestsMode executes one policy-selected sweep. Passive
// recovery can re-enable a healthy auto-disabled channel but can never disable
// an enabled one, even if the probe fails.
func RunChannelHealthTestsMode(mode string) error {
	var channels []model.Channel
	if err := model.DB.Order("id asc").Find(&channels).Error; err != nil {
		return err
	}
	selected, err := SelectChannelsForReliabilityTest(channels, mode)
	if err != nil {
		return err
	}
	now := common.NowTimestamp()
	persistErrors := make([]error, 0)
	allowDisable := mode != setting.ChannelTestModePassiveRecovery
	for i := range selected {
		ch := &selected[i]
		if _, _, err := testAndRecordChannelContextWithMode(context.Background(), ch, now, allowDisable); err != nil {
			persistErrors = append(persistErrors, err)
		}
	}
	return errors.Join(persistErrors...)
}
