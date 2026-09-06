package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	geminiVeo "github.com/tokenrouter/tokenrouter/relay/channel/task/gemini"
	vertexVeo "github.com/tokenrouter/tokenrouter/relay/channel/task/vertex"
	"github.com/tokenrouter/tokenrouter/service"
)

type vertexVeoProvider interface {
	Submit(context.Context, vertexVeo.Config, *geminiVeo.PreparedRequest) (*vertexVeo.Task, []byte, error)
	Fetch(context.Context, vertexVeo.Config, string, string) (*vertexVeo.Task, []byte, error)
}

var newVertexVeoProvider = func() vertexVeoProvider { return &vertexVeo.Client{} }

func init() {
	RegisterVeoTaskProvider(vertexVeoProviderDescriptor())
}

func vertexVeoProviderDescriptor() VeoTaskProviderDescriptor {
	return VeoTaskProviderDescriptor{
		ChannelType:            constant.ChannelTypeVertexAi,
		Platform:               model.TaskOperationPlatformVertexVeo,
		Family:                 "vertex_ai",
		MaxCredentialBytes:     vertexVeo.MaxCredentialBytes,
		MaxProviderTaskIDBytes: vertexVeo.MaxOperationNameBytes,
		CaptureCredential: func(channel *model.Channel) (string, error) {
			if channel == nil || constant.ChannelType(channel.Type) != constant.ChannelTypeVertexAi {
				return "", errors.New("invalid Vertex AI Veo channel")
			}
			credential := service.GetChannelKey(channel)
			if strings.TrimSpace(credential) == "" || len(credential) > vertexVeo.MaxCredentialBytes ||
				!utf8.ValidString(credential) || strings.ContainsRune(credential, '\x00') {
				return "", errors.New("Vertex AI Veo channel has no bounded service-account credential")
			}
			return credential, nil
		},
		PrepareRoutingSnapshot: func(channel *model.Channel, mappedModel string) (string, error) {
			if channel == nil || constant.ChannelType(channel.Type) != constant.ChannelTypeVertexAi {
				return "", errors.New("invalid Vertex AI Veo channel")
			}
			snapshot, err := vertexVeo.PrepareSnapshot(
				channel.BaseURL, channel.Other, channel.OtherSettings, mappedModel,
			)
			if err != nil {
				return "", err
			}
			encoded, err := common.Marshal(snapshot)
			if err != nil {
				return "", errors.New("encode Vertex AI Veo routing snapshot")
			}
			return string(encoded), nil
		},
		ValidateRoutingSnapshot: func(raw, mappedModel string) error {
			_, err := decodeVertexVeoRoutingSnapshot(raw, mappedModel)
			return err
		},
		ValidateProviderTaskID: func(raw, mappedModel, providerID string) error {
			snapshot, err := decodeVertexVeoRoutingSnapshot(raw, mappedModel)
			if err != nil {
				return err
			}
			return vertexVeo.ValidateOperationForSnapshot(providerID, snapshot, mappedModel)
		},
		ValidatePrepared: vertexVeo.ValidatePrepared,
		Submit: func(ctx context.Context, raw, credential string, prepared *geminiVeo.PreparedRequest) (*VeoProviderTask, []byte, error) {
			snapshot, err := decodeVertexVeoRoutingSnapshot(raw, preparedModel(prepared))
			if err != nil {
				return nil, nil, err
			}
			task, response, err := newVertexVeoProvider().Submit(ctx, vertexVeo.Config{
				Snapshot: snapshot, Credential: credential,
			}, prepared)
			if err != nil {
				return nil, response, err
			}
			normalized, err := normalizeVertexVeoProviderTask(task)
			return normalized, response, err
		},
		Fetch: func(ctx context.Context, raw, credential, mappedModel, providerID string) (*VeoProviderTask, []byte, error) {
			snapshot, err := decodeVertexVeoRoutingSnapshot(raw, mappedModel)
			if err != nil {
				return nil, nil, err
			}
			task, response, err := newVertexVeoProvider().Fetch(ctx, vertexVeo.Config{
				Snapshot: snapshot, Credential: credential,
			}, mappedModel, providerID)
			if err != nil {
				return nil, response, err
			}
			normalized, err := normalizeVertexVeoProviderTask(task)
			return normalized, response, err
		},
		Content: func(_ context.Context, raw, _ string, task VeoProviderTask) (*http.Response, error) {
			if _, err := decodeVertexVeoRoutingSnapshot(raw, ""); err != nil {
				return nil, err
			}
			if task.Status != geminiVeo.StatusSucceeded || task.ProviderTaskID == "" ||
				task.ResultURL != "" || task.InlineMIMEType == "" || task.InlineBase64 == "" ||
				task.InlineBytes <= 0 {
				return nil, errors.New("invalid Vertex AI Veo content result")
			}
			inline := &vertexVeo.InlineVideo{
				MIMEType: task.InlineMIMEType, Base64: task.InlineBase64, DecodedBytes: task.InlineBytes,
			}
			reader, err := inline.Reader()
			if err != nil {
				return nil, err
			}
			headers := make(http.Header)
			headers.Set("Content-Type", inline.MIMEType)
			headers.Set("Content-Length", strconv.FormatInt(inline.DecodedBytes, 10))
			return &http.Response{
				StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(reader),
				ContentLength: inline.DecodedBytes,
			}, nil
		},
		SubmitWasDispatched: vertexVeo.SubmitWasDispatched,
	}
}

func preparedModel(prepared *geminiVeo.PreparedRequest) string {
	if prepared == nil {
		return ""
	}
	return prepared.UpstreamModel
}

func decodeVertexVeoRoutingSnapshot(raw, mappedModel string) (vertexVeo.Snapshot, error) {
	if raw == "" || len(raw) > 8<<10 {
		return vertexVeo.Snapshot{}, errors.New("invalid Vertex AI Veo routing snapshot")
	}
	var snapshot vertexVeo.Snapshot
	if strictGeminiVeoJSON([]byte(raw), &snapshot) != nil {
		return vertexVeo.Snapshot{}, errors.New("invalid Vertex AI Veo routing snapshot")
	}
	modelName := mappedModel
	if modelName == "" {
		models := vertexVeo.ModelList()
		if len(models) == 0 {
			return vertexVeo.Snapshot{}, errors.New("Vertex AI Veo model catalog is unavailable")
		}
		modelName = models[0]
	}
	if err := vertexVeo.ValidateSnapshot(snapshot, modelName); err != nil {
		return vertexVeo.Snapshot{}, errors.New("invalid Vertex AI Veo routing snapshot")
	}
	return snapshot, nil
}

func normalizeVertexVeoProviderTask(task *vertexVeo.Task) (*VeoProviderTask, error) {
	if task == nil {
		return nil, nil
	}
	normalized := &VeoProviderTask{
		ProviderTaskID: task.ProviderTaskID, Status: task.Status,
		ErrorCode: task.ErrorCode, ErrorMessage: task.ErrorMessage,
	}
	if task.InlineVideo != nil {
		if _, err := task.InlineVideo.Reader(); err != nil {
			return nil, errors.New("invalid Vertex AI Veo inline result")
		}
		normalized.InlineMIMEType = task.InlineVideo.MIMEType
		normalized.InlineBase64 = task.InlineVideo.Base64
		normalized.InlineBytes = task.InlineVideo.DecodedBytes
	}
	return normalized, nil
}
