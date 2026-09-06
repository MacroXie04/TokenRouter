package coze

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultPollInterval    = time.Second
	defaultMaxPollAttempts = 600
	pollRequestTimeout     = 30 * time.Second
)

func newPollClient() *http.Client {
	return &http.Client{
		Timeout: pollRequestTimeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           httpx.SafeDialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: env.GetEnvBool("TLS_INSECURE_SKIP_VERIFY", false),
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (a *Adaptor) pollUntilComplete(meta *relaycommon.Meta, conversationID, chatID string) (cozeUsage, error) {
	if err := validateProviderID("conversation", conversationID); err != nil {
		return cozeUsage{}, err
	}
	if err := validateProviderID("chat", chatID); err != nil {
		return cozeUsage{}, err
	}
	attempts := a.maxPollAttempts
	if attempts <= 0 {
		attempts = defaultMaxPollAttempts
	}
	interval := a.pollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	for attempt := 0; attempt < attempts; attempt++ {
		response, err := a.getJSON(meta, "/v3/chat/retrieve", conversationID, chatID)
		if err != nil {
			return cozeUsage{}, err
		}
		var envelope chatEnvelope
		if err := strictJSON(response, &envelope); err != nil {
			return cozeUsage{}, errors.New("Coze returned an invalid chat status response")
		}
		if envelope.Code != 0 {
			return cozeUsage{}, cozeProviderError(envelope.Code, envelope.Msg, http.StatusBadGateway)
		}
		status := strings.ToLower(strings.TrimSpace(envelope.Data.Status))
		switch status {
		case "completed":
			if envelope.Data.ID != "" && envelope.Data.ID != chatID {
				return cozeUsage{}, errors.New("Coze chat status returned a mismatched chat ID")
			}
			if envelope.Data.ConversationID != "" && envelope.Data.ConversationID != conversationID {
				return cozeUsage{}, errors.New("Coze chat status returned a mismatched conversation ID")
			}
			return envelope.Data.Usage, nil
		case "failed", "canceled", "cancelled", "requires_action":
			message := envelope.Data.LastError.Message
			if strings.TrimSpace(message) == "" {
				message = "Coze chat ended with status " + status
			}
			return cozeUsage{}, cozeProviderError(envelope.Data.LastError.Code, message, http.StatusBadGateway)
		case "created", "pending", "queued", "queueing", "in_progress", "processing", "running":
			// Continue below after a context-aware delay.
		case "":
			return cozeUsage{}, errors.New("Coze chat status is empty")
		default:
			return cozeUsage{}, errors.New("Coze chat returned an unsupported status")
		}
		if attempt+1 == attempts {
			break
		}
		ctx := metaContext(meta)
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return cozeUsage{}, fmt.Errorf("wait for Coze chat completion: %w", ctx.Err())
		case <-timer.C:
		}
	}
	return cozeUsage{}, fmt.Errorf("Coze chat did not complete after %d status checks", attempts)
}

func (a *Adaptor) getMessageList(meta *relaycommon.Meta, conversationID, chatID string) (messageListEnvelope, error) {
	response, err := a.getJSON(meta, "/v3/chat/message/list", conversationID, chatID)
	if err != nil {
		return messageListEnvelope{}, err
	}
	var envelope messageListEnvelope
	if err := strictJSON(response, &envelope); err != nil {
		return messageListEnvelope{}, errors.New("Coze returned an invalid message-list response")
	}
	if envelope.Code != 0 {
		return messageListEnvelope{}, cozeProviderError(envelope.Code, envelope.Msg, http.StatusBadGateway)
	}
	if len(envelope.Data) > maxMessages {
		return messageListEnvelope{}, fmt.Errorf("Coze returned more than %d messages", maxMessages)
	}
	return envelope, nil
}

func (a *Adaptor) getJSON(meta *relaycommon.Meta, path, conversationID, chatID string) ([]byte, error) {
	if err := validateProviderID("conversation", conversationID); err != nil {
		return nil, err
	}
	if err := validateProviderID("chat", chatID); err != nil {
		return nil, err
	}
	base, err := validatedBaseURL(meta.BaseURL)
	if err != nil {
		return nil, err
	}
	requestURL := relaycommon.JoinURL(base, path)
	parsed, err := url.Parse(requestURL)
	if err != nil {
		return nil, errors.New("build Coze status URL")
	}
	query := parsed.Query()
	query.Set("conversation_id", conversationID)
	query.Set("chat_id", chatID)
	parsed.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(metaContext(meta), http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, errors.New("build Coze status request")
	}
	if err := a.SetupRequestHeader(request, meta); err != nil {
		return nil, err
	}
	client := a.pollClient
	if client == nil {
		client = newPollClient()
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request Coze chat status: %w", relaycommon.SanitizeTransportError(err))
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, maxProviderResponseBody)
	if err != nil {
		return nil, fmt.Errorf("read Coze status response: %w", err)
	}
	return body, nil
}

func validateProviderID(name, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed != value || len(value) > maxProviderIDBytes || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("Coze %s ID is invalid", name)
	}
	return nil
}

func metaContext(meta *relaycommon.Meta) context.Context {
	if meta != nil && meta.Context != nil {
		return meta.Context
	}
	return context.Background()
}

func cozeProviderError(code int, rawMessage string, status int) error {
	message := strings.TrimSpace(rawMessage)
	if message == "" || len(message) > maxProviderErrorBytes || !utf8.ValidString(message) {
		message = "Coze returned a provider error"
	}
	codeValue := "provider_error"
	if code != 0 {
		codeValue = fmt.Sprintf("coze_%d", code)
	}
	return relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
		Message: message,
		Type:    "provider_error",
		Code:    codeValue,
	}, status)
}
