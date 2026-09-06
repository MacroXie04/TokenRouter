package relay

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	geminiVeo "github.com/tokenrouter/tokenrouter/relay/channel/task/gemini"
	vertexVeo "github.com/tokenrouter/tokenrouter/relay/channel/task/vertex"
)

type vertexVeoProviderStub struct {
	submit func(context.Context, vertexVeo.Config, *geminiVeo.PreparedRequest) (*vertexVeo.Task, []byte, error)
	fetch  func(context.Context, vertexVeo.Config, string, string) (*vertexVeo.Task, []byte, error)
}

func (stub vertexVeoProviderStub) Submit(ctx context.Context, config vertexVeo.Config,
	prepared *geminiVeo.PreparedRequest,
) (*vertexVeo.Task, []byte, error) {
	return stub.submit(ctx, config, prepared)
}

func (stub vertexVeoProviderStub) Fetch(ctx context.Context, config vertexVeo.Config,
	modelName, operationName string,
) (*vertexVeo.Task, []byte, error) {
	return stub.fetch(ctx, config, modelName, operationName)
}

func vertexVeoTestMP4() []byte {
	return []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0, 'i', 's', 'o', 'm', 'm', 'p', '4', '2'}
}

func TestVertexVeoDescriptorCapturesRoutingAndCredential(t *testing.T) {
	descriptor := vertexVeoProviderDescriptor()
	assert.Equal(t, constant.ChannelTypeVertexAi, descriptor.ChannelType)
	assert.Equal(t, model.TaskOperationPlatformVertexVeo, descriptor.Platform)
	assert.Equal(t, vertexVeo.MaxCredentialBytes, descriptor.MaxCredentialBytes)

	credential := "{\n  \"type\": \"service_account\",\n  \"project_id\": \"project-123\"\n}"
	channel := &model.Channel{
		Id: 41, Type: int(constant.ChannelTypeVertexAi), Key: credential,
		Other:         `{"veo-3.1-fast-generate-preview":"europe-west4","default":"global"}`,
		OtherSettings: `{"vertex_key_type":"json"}`,
	}
	captured, err := descriptor.CaptureCredential(channel)
	require.NoError(t, err)
	assert.Equal(t, credential, captured)

	routing, err := descriptor.PrepareRoutingSnapshot(channel, "veo-3.1-fast-generate-preview")
	require.NoError(t, err)
	require.NoError(t, descriptor.ValidateRoutingSnapshot(routing, "veo-3.1-fast-generate-preview"))
	assert.Contains(t, routing, "europe-west4-aiplatform.googleapis.com/v1")
	operation := "projects/project-123/locations/europe-west4/publishers/google/models/" +
		"veo-3.1-fast-generate-preview/operations/op-123"
	require.NoError(t, descriptor.ValidateProviderTaskID(routing, "veo-3.1-fast-generate-preview", operation))
	require.Error(t, descriptor.ValidateProviderTaskID(routing, "veo-3.1-generate-preview", operation))
	require.Error(t, descriptor.ValidateProviderTaskID(strings.Replace(routing, "europe-west4", "us-central1", 1),
		"veo-3.1-fast-generate-preview", operation))
}

func TestVertexVeoDescriptorAdaptsClientAndInlineContent(t *testing.T) {
	descriptor := vertexVeoProviderDescriptor()
	channel := &model.Channel{Id: 41, Type: int(constant.ChannelTypeVertexAi), Other: "us-central1"}
	routing, err := descriptor.PrepareRoutingSnapshot(channel, "veo-3.1-generate-preview")
	require.NoError(t, err)
	operation := "projects/project-123/locations/us-central1/publishers/google/models/" +
		"veo-3.1-generate-preview/operations/op-123"

	originalFactory := newVertexVeoProvider
	t.Cleanup(func() { newVertexVeoProvider = originalFactory })
	newVertexVeoProvider = func() vertexVeoProvider {
		return vertexVeoProviderStub{
			submit: func(_ context.Context, config vertexVeo.Config, prepared *geminiVeo.PreparedRequest) (*vertexVeo.Task, []byte, error) {
				assert.Equal(t, "us-central1", config.Snapshot.Region)
				assert.Equal(t, "service-account-json", config.Credential)
				assert.Equal(t, "veo-3.1-generate-preview", prepared.UpstreamModel)
				return &vertexVeo.Task{ProviderTaskID: operation, Status: vertexVeo.StatusProcessing}, []byte(`{"name":"ok"}`), nil
			},
			fetch: func(_ context.Context, config vertexVeo.Config, modelName, providerID string) (*vertexVeo.Task, []byte, error) {
				assert.Equal(t, "us-central1", config.Snapshot.Region)
				assert.Equal(t, "veo-3.1-generate-preview", modelName)
				assert.Equal(t, operation, providerID)
				video := vertexVeoTestMP4()
				return &vertexVeo.Task{ProviderTaskID: operation, Status: vertexVeo.StatusSucceeded,
					InlineVideo: &vertexVeo.InlineVideo{MIMEType: "video/mp4",
						Base64: base64.StdEncoding.EncodeToString(video), DecodedBytes: int64(len(video))}}, nil, nil
			},
		}
	}
	prepared := &geminiVeo.PreparedRequest{UpstreamModel: "veo-3.1-generate-preview", Body: []byte(`{}`)}
	submitted, _, err := descriptor.Submit(context.Background(), routing, "service-account-json", prepared)
	require.NoError(t, err)
	assert.Equal(t, operation, submitted.ProviderTaskID)

	fetched, _, err := descriptor.Fetch(context.Background(), routing, "service-account-json",
		"veo-3.1-generate-preview", operation)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, geminiVeo.StatusSucceeded, fetched.Status)
	assert.Equal(t, int64(len(vertexVeoTestMP4())), fetched.InlineBytes)

	response, err := descriptor.Content(context.Background(), routing, "", *fetched)
	require.NoError(t, err)
	require.NotNil(t, response)
	defer response.Body.Close()
	assert.Equal(t, "video/mp4", response.Header.Get("Content-Type"))
	content, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, vertexVeoTestMP4(), content)
}
