// Package baidu_v2 implements Baidu Qianfan's OpenAI-compatible v2 chat
// contract. Other v2 paths exposed by the provider are intentionally rejected:
// the pinned reference has no working conversion for them.
package baidu_v2

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/relay/channel/openai"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	ChannelName                   = "baidu_v2"
	defaultBaseURL                = "https://qianfan.baidubce.com"
	maxBaiduV2RequestBodyBytes    = 16 << 20
	maxBaiduV2CredentialBytes     = 16 << 10
	maxBaiduV2BaseURLBytes        = 8 << 10
	maxBaiduV2ModelBytes          = 1024
	maxBaiduV2StreamResponseBytes = 64 << 20
)

var supportedModels = [...]string{
	"ernie-4.0-8k-latest",
	"ernie-4.0-8k-preview",
	"ernie-4.0-8k",
	"ernie-4.0-turbo-8k-latest",
	"ernie-4.0-turbo-8k-preview",
	"ernie-4.0-turbo-8k",
	"ernie-4.0-turbo-128k",
	"ernie-3.5-8k-preview",
	"ernie-3.5-8k",
	"ernie-3.5-128k",
	"ernie-speed-8k",
	"ernie-speed-128k",
	"ernie-speed-pro-128k",
	"ernie-lite-8k",
	"ernie-lite-pro-128k",
	"ernie-tiny-8k",
	"ernie-char-8k",
	"ernie-char-fiction-8k",
	"ernie-novel-8k",
	"deepseek-v3",
	"deepseek-r1",
	"deepseek-r1-distill-qwen-32b",
	"deepseek-r1-distill-qwen-14b",
}

func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

type Adaptor struct {
	mode   constant.RelayMode
	format constant.RelayFormat
	openAI openai.Adaptor
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = constant.RelayModeUnknown
	a.format = constant.RelayFormatUnknown
	a.openAI = openai.Adaptor{}
	if meta != nil {
		a.mode = meta.Mode
		a.format = meta.Format
	}
	a.openAI.Init(meta)
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if err := a.validate(meta); err != nil {
		return "", err
	}
	base, err := validateBaseURL(meta.BaseURL)
	if err != nil {
		return "", err
	}
	return relaycommon.JoinURL(base, "/v2/chat/completions"), nil
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	if req == nil || meta == nil {
		return errors.New("Baidu v2 request metadata is nil")
	}
	if err := a.validate(meta); err != nil {
		return err
	}
	token, appID, err := parseCredential(meta.APIKey)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Del("x-api-key")
	req.Header.Del("api-key")
	req.Header.Del("appid")
	if appID != "" {
		req.Header.Set("appid", appID)
	}
	req.Header.Set("Content-Type", "application/json")
	if meta.IsStream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if len(meta.RawBody) > maxBaiduV2RequestBodyBytes {
		return nil, fmt.Errorf("Baidu v2 request exceeds %d bytes", maxBaiduV2RequestBodyBytes)
	}
	body, err := a.openAI.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := protocolkit.UnmarshalJSON(body, &payload); err != nil || payload == nil {
		return nil, errors.New("encode Baidu v2 request")
	}
	model := meta.ModelName
	if strings.HasSuffix(model, "-search") {
		model = strings.TrimSuffix(model, "-search")
		if _, configured := payload["web_search"]; !configured {
			payload["web_search"] = map[string]any{
				"enable": true, "enable_citation": true, "enable_trace": true, "enable_status": false,
			}
		}
	}
	payload["model"] = model
	body, err = protocolkit.MarshalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Baidu v2 request: %w", err)
	}
	if len(body) > maxBaiduV2RequestBodyBytes {
		return nil, fmt.Errorf("Baidu v2 converted request exceeds %d bytes", maxBaiduV2RequestBodyBytes)
	}
	return body, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || resp == nil || meta == nil {
		return nil, errors.New("Baidu v2 response metadata is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if !meta.IsStream {
		return a.openAI.DoResponse(c, resp, meta)
	}
	limited := &io.LimitedReader{R: resp.Body, N: maxBaiduV2StreamResponseBytes + 1}
	bounded := *resp
	bounded.Body = io.NopCloser(limited)
	usage, err := a.openAI.DoResponse(c, &bounded, meta)
	if err != nil {
		return usage, err
	}
	if limited.N == 0 {
		return usage, fmt.Errorf("%w: Baidu v2 stream maximum is %d bytes", relaycommon.ErrUpstreamResponseTooLarge, maxBaiduV2StreamResponseBytes)
	}
	return usage, nil
}

func (a *Adaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil || meta.Request == nil {
		return errors.New("Baidu v2 relay metadata is nil")
	}
	mode := meta.Mode
	if mode == constant.RelayModeUnknown {
		mode = a.mode
	}
	format := meta.Format
	if format == constant.RelayFormatUnknown {
		format = a.format
	}
	if mode != constant.RelayModeChatCompletions || format != constant.RelayFormatOpenAI {
		return fmt.Errorf("Baidu v2 channel does not support relay mode %d format %q", mode, format)
	}
	if meta.Request.Stream != meta.IsStream {
		return errors.New("Baidu v2 request stream flag does not match relay metadata")
	}
	return validateModel(meta.ModelName)
}

func parseCredential(raw string) (string, string, error) {
	if len(raw) == 0 || len(raw) > maxBaiduV2CredentialBytes || strings.ContainsAny(raw, "\r\n") {
		return "", "", errors.New("Baidu v2 API key is invalid")
	}
	parts := strings.Split(raw, "|")
	token := strings.TrimSpace(parts[0])
	if token == "" {
		return "", "", errors.New("Baidu v2 authorization token is required")
	}
	appID := ""
	if len(parts) > 1 {
		appID = strings.TrimSpace(parts[1])
	}
	if strings.ContainsAny(token+appID, "\r\n") {
		return "", "", errors.New("Baidu v2 API key is invalid")
	}
	return token, appID, nil
}

func validateModel(model string) error {
	if model == "" || strings.TrimSpace(model) != model || len(model) > maxBaiduV2ModelBytes {
		return errors.New("Baidu v2 upstream model is invalid")
	}
	for _, character := range model {
		if unicode.IsControl(character) {
			return errors.New("Baidu v2 upstream model is invalid")
		}
	}
	return nil
}

func validateBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = defaultBaseURL
	}
	if len(raw) > maxBaiduV2BaseURLBytes {
		return "", errors.New("Baidu v2 base URL length is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse Baidu v2 base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("Baidu v2 base URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("Baidu v2 base URL is missing a host")
	}
	if parsed.User != nil {
		return "", errors.New("Baidu v2 base URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Baidu v2 base URL must not contain a query or fragment")
	}
	return strings.TrimRight(raw, "/"), nil
}
