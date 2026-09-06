package tasks

import (
	"context"
	"errors"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	geminiVeo "github.com/tokenrouter/tokenrouter/internal/relay/providers/task/gemini"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"strings"
	"unicode/utf8"
)

type geminiVeoRoutingSnapshot struct {
	BaseURL string `json:"base_url"`
}

func geminiVeoProviderDescriptor() VeoTaskProviderDescriptor {
	return VeoTaskProviderDescriptor{
		ChannelType: channelcatalog.ChannelTypeGemini, Platform: model.TaskOperationPlatformGeminiVeo,
		Family: "gemini_api", MaxCredentialBytes: 16 << 10,
		MaxProviderTaskIDBytes: geminiVeo.MaxProviderTaskIDBytes,
		CaptureCredential: func(channel *model.Channel) (string, error) {
			credential := strings.TrimSpace(channelssvc.GetChannelKey(channel))
			if credential == "" || len(credential) > 16<<10 || !utf8.ValidString(credential) ||
				strings.ContainsAny(credential, "\r\n\x00") {
				return "", errors.New("Gemini API Veo channel has no valid key")
			}
			return credential, nil
		},
		PrepareRoutingSnapshot: func(channel *model.Channel, _ string) (string, error) {
			if channel == nil || channelcatalog.ChannelType(channel.Type) != channelcatalog.ChannelTypeGemini {
				return "", errors.New("invalid Gemini API Veo channel")
			}
			baseURL, err := geminiVeo.EffectiveBaseURL(channel.BaseURL)
			if err != nil {
				return "", err
			}
			encoded, err := jsonutil.Marshal(geminiVeoRoutingSnapshot{BaseURL: baseURL})
			return string(encoded), err
		},
		ValidateRoutingSnapshot: func(raw, _ string) error {
			_, err := decodeGeminiVeoRoutingSnapshot(raw)
			return err
		},
		ValidateProviderTaskID: func(raw, mappedModel, providerID string) error {
			if _, err := decodeGeminiVeoRoutingSnapshot(raw); err != nil {
				return err
			}
			return geminiVeo.ValidateOperationName(providerID, mappedModel)
		},
		ValidatePrepared: func(prepared *geminiVeo.PreparedRequest) error {
			if prepared == nil || !geminiVeo.ValidModelSettings(prepared.UpstreamModel,
				prepared.Duration, prepared.Resolution, prepared.AspectRatio) {
				return errors.New("invalid Gemini API Veo request")
			}
			return nil
		},
		Submit: func(ctx context.Context, raw, credential string, prepared *geminiVeo.PreparedRequest) (*VeoProviderTask, []byte, error) {
			routing, err := decodeGeminiVeoRoutingSnapshot(raw)
			if err != nil {
				return nil, nil, err
			}
			task, response, err := newGeminiVeoProvider().Submit(ctx, routing.BaseURL, credential, prepared)
			return normalizeGeminiVeoProviderTask(task), response, err
		},
		Fetch: func(ctx context.Context, raw, credential, _ string, providerID string) (*VeoProviderTask, []byte, error) {
			routing, err := decodeGeminiVeoRoutingSnapshot(raw)
			if err != nil {
				return nil, nil, err
			}
			task, response, err := newGeminiVeoProvider().Fetch(ctx, routing.BaseURL, credential, providerID)
			return normalizeGeminiVeoProviderTask(task), response, err
		},
		Content: func(ctx context.Context, raw, credential string, task VeoProviderTask) (*http.Response, error) {
			routing, err := decodeGeminiVeoRoutingSnapshot(raw)
			if err != nil {
				return nil, err
			}
			return newGeminiVeoProvider().Content(ctx, routing.BaseURL, credential, task.ResultURL)
		},
		SubmitWasDispatched: geminiVeo.SubmitWasDispatched,
	}
}

func decodeGeminiVeoRoutingSnapshot(raw string) (geminiVeoRoutingSnapshot, error) {
	if raw == "" || len(raw) > geminiVeoTaskRoutingMaxBytes {
		return geminiVeoRoutingSnapshot{}, errors.New("invalid Gemini API Veo routing snapshot")
	}
	var snapshot geminiVeoRoutingSnapshot
	if strictGeminiVeoJSON([]byte(raw), &snapshot) != nil {
		return geminiVeoRoutingSnapshot{}, errors.New("invalid Gemini API Veo routing snapshot")
	}
	baseURL, err := geminiVeo.EffectiveBaseURL(snapshot.BaseURL)
	if err != nil || baseURL != snapshot.BaseURL {
		return geminiVeoRoutingSnapshot{}, errors.New("invalid Gemini API Veo base URL snapshot")
	}
	return snapshot, nil
}

func normalizeGeminiVeoProviderTask(task *geminiVeo.Task) *VeoProviderTask {
	if task == nil {
		return nil
	}
	return &VeoProviderTask{
		ProviderTaskID: task.ProviderTaskID, Status: task.Status,
		ErrorCode: task.ErrorCode, ErrorMessage: task.ErrorMessage, ResultURL: task.ResultURL,
	}
}
