// Package codex implements the ChatGPT Codex backend contract. Codex channel
// keys are JSON OAuth snapshots rather than ordinary API keys; keeping this
// adapter separate prevents the complete JSON credential from being emitted
// as a bearer token by a generic OpenAI-compatible fallback.
package codex

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/openai"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"strings"
)

const defaultBaseURL = "https://chatgpt.com"

type oauthKey struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
}

type Adaptor struct {
	mode   channelcatalog.RelayMode
	openAI openai.Adaptor
}

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	if meta != nil {
		a.mode = meta.Mode
	}
	a.openAI.Init(meta)
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if meta == nil {
		return "", errors.New("Codex relay metadata is nil")
	}
	base := strings.TrimSpace(meta.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	var path string
	switch a.mode {
	case channelcatalog.RelayModeResponses:
		path = "/backend-api/codex/responses"
	case channelcatalog.RelayModeResponsesCompact:
		path = "/backend-api/codex/responses/compact"
	case channelcatalog.RelayModeAlphaSearch:
		path = "/backend-api/codex/alpha/search"
	default:
		return "", errors.New("Codex channel supports only Responses, Responses compaction, and alpha search")
	}
	return relaycommon.JoinURL(base, path), nil
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	if req == nil || meta == nil {
		return errors.New("Codex request metadata is nil")
	}
	var key oauthKey
	if err := protocolkit.UnmarshalJSON([]byte(strings.TrimSpace(meta.APIKey)), &key); err != nil {
		return errors.New("Codex channel key must be a valid JSON OAuth object")
	}
	key.AccessToken = strings.TrimSpace(key.AccessToken)
	key.AccountID = strings.TrimSpace(key.AccountID)
	if key.AccessToken == "" {
		return errors.New("Codex channel access_token is required")
	}
	if key.AccountID == "" {
		return errors.New("Codex channel account_id is required")
	}
	req.Header.Set("Authorization", "Bearer "+key.AccessToken)
	req.Header.Set("chatgpt-account-id", key.AccountID)
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("Content-Type", "application/json")
	if meta.IsStream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if meta == nil || meta.Request == nil {
		return nil, errors.New("Codex request is nil")
	}
	if a.mode != channelcatalog.RelayModeResponses &&
		a.mode != channelcatalog.RelayModeResponsesCompact &&
		a.mode != channelcatalog.RelayModeAlphaSearch {
		return nil, errors.New("Codex channel does not support this request mode")
	}
	body := make(map[string]any)
	if len(meta.RawBody) > 0 {
		if err := protocolkit.UnmarshalJSON(meta.RawBody, &body); err != nil {
			return nil, fmt.Errorf("decode Codex request: %w", err)
		}
	} else if meta.Request.Extra != nil {
		encoded, err := protocolkit.MarshalJSON(meta.Request.Extra)
		if err != nil {
			return nil, fmt.Errorf("clone Codex request: %w", err)
		}
		if err := protocolkit.UnmarshalJSON(encoded, &body); err != nil {
			return nil, fmt.Errorf("decode Codex request clone: %w", err)
		}
	}
	if strings.TrimSpace(meta.ModelName) == "" {
		return nil, errors.New("Codex upstream model is empty")
	}
	body["model"] = meta.ModelName
	// Dashboard group selection is local routing state, never provider input.
	delete(body, "group")
	if a.mode == channelcatalog.RelayModeResponses || a.mode == channelcatalog.RelayModeResponsesCompact {
		if instructions, exists := body["instructions"]; !exists || instructions == nil {
			body["instructions"] = ""
		}
	}
	if a.mode == channelcatalog.RelayModeResponses {
		body["store"] = false
		delete(body, "max_output_tokens")
		delete(body, "temperature")
		delete(body, "frequency_penalty")
		delete(body, "presence_penalty")
	}
	return protocolkit.MarshalJSON(body)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	return a.openAI.DoResponse(c, resp, meta)
}
