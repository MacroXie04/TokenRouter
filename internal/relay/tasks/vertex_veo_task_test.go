package tasks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	geminiVeo "github.com/tokenrouter/tokenrouter/internal/relay/providers/task/gemini"
	vertexVeo "github.com/tokenrouter/tokenrouter/internal/relay/providers/task/vertex"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	vertexVeoOriginModel   = "veo-3.1-generate-preview"
	vertexVeoUpstreamModel = "veo-3.1-fast-generate-preview"
	vertexVeoOperationID   = "projects/project-123/locations/europe-west4/publishers/google/models/" +
		vertexVeoUpstreamModel + "/operations/operation-123"
)

type vertexVeoRoundTripFunc func(*http.Request) (*http.Response, error)

func (function vertexVeoRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func configureVertexVeoLifecycleFixture(t *testing.T) relayAccountingFixture {
	t.Helper()
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "vertex-veo-test=0123456789abcdef0123456789abcdef")
	t.Setenv("GEMINI_VEO_TASK_RECOVERY_DIR", t.TempDir())
	fixture := newRelayAccountingFixture(t, 2_000_000, 2_000_000)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Task{}, &model.TaskOperation{}, &model.JimengTaskOperation{},
		&model.AuditLogOutbox{}, &model.RelayQuotaReservationReviewEvent{},
	))
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", fixture.token.Id).
		Update("group", userssvc.GroupDefault).Error)
	fixture.token.Group = userssvc.GroupDefault
	credential := "{\n" +
		"  \"type\": \"service_account\",\n" +
		"  \"project_id\": \"project-123\",\n" +
		"  \"private_key\": \"durable-private-key-marker\",\n" +
		"  \"client_email\": \"vertex@project-123.iam.gserviceaccount.com\"\n" +
		"}"
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Updates(map[string]any{
			"type": int(channelcatalog.ChannelTypeVertexAi), "key": credential, "base_url": "",
			"other":         `{"veo-3.1-fast-generate-preview":"europe-west4","default":"global"}`,
			"settings":      `{"vertex_key_type":"json"}`,
			"models":        strings.Join(vertexVeo.ModelList(), ","),
			"model_mapping": `{"veo-3.1-generate-preview":"veo-3.1-fast-generate-preview"}`,
		}).Error)
	require.NoError(t, model.DB.First(&fixture.channel, fixture.channel.Id).Error)
	require.NoError(t, model.DB.Where("channel_id = ?", fixture.channel.Id).Delete(&model.Ability{}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: userssvc.GroupDefault, Model: vertexVeoOriginModel,
		ChannelId: fixture.channel.Id, Enabled: true, Weight: 1,
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
	keys := []string{setting.ModelBillingModeOption, setting.PerCallModelPriceOption,
		setting.ModelRatioOption, setting.CompletionRatioOption, setting.GroupRatioOption,
		setting.GroupGroupRatioOption}
	previous := setting.GetOptions(keys...)
	t.Cleanup(func() { _ = setting.UpdateOptions(previous) })
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"veo-3.1-generate-preview":"reference"}`,
		setting.PerCallModelPriceOption: `{"veo-3.1-generate-preview":0.001}`,
		setting.ModelRatioOption:        `{}`,
		setting.CompletionRatioOption:   `{}`,
		setting.GroupRatioOption:        `{"default":1}`,
		setting.GroupGroupRatioOption:   `{}`,
	}))
	return fixture
}

func vertexVeoLifecycleContext(t *testing.T, fixture relayAccountingFixture,
	method, path, body, taskID string,
) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	if taskID != "" {
		c.Params = gin.Params{{Key: "task_id", Value: taskID}}
	}
	requestctx.SetUserId(c, fixture.user.Id)
	requestctx.SetUsername(c, fixture.user.Username)
	requestctx.SetUserGroup(c, userssvc.GroupDefault)
	middleware.SetupRelayTokenContext(c, &fixture.token)
	middleware.SetRelayGroupPolicy(c, billingsvc.RelayGroupPolicy{Groups: []string{userssvc.GroupDefault}})
	return c, recorder
}

func installVertexVeoProvider(t *testing.T, provider vertexVeoProvider) {
	t.Helper()
	previous := newVertexVeoProvider
	newVertexVeoProvider = func() vertexVeoProvider { return provider }
	t.Cleanup(func() { newVertexVeoProvider = previous })
}

func decodeVertexVeoPublicID(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response geminiVeoVideoResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.NoError(t, validateGeminiVeoTaskPublicID(response.ID))
	return response.ID
}

func forceVertexVeoRecoveryDue(t *testing.T, taskID string) {
	t.Helper()
	result := model.DB.Model(&model.TaskOperation{}).Where("task_id = ? AND platform = ?", taskID,
		model.TaskOperationPlatformVertexVeo).
		Updates(map[string]any{"next_attempt_at": 0, "lease_owner": "", "lease_expires_at": 0})
	require.NoError(t, result.Error)
	require.Equal(t, int64(1), result.RowsAffected)
}

func TestVertexVeoDurableSubmitRecoveryAndInlineContent(t *testing.T) {
	fixture := configureVertexVeoLifecycleFixture(t)
	credential := fixture.channel.Key
	video := vertexVeoTestMP4()
	var submitCalls, fetchCalls atomic.Int32
	installVertexVeoProvider(t, vertexVeoProviderStub{
		submit: func(_ context.Context, config vertexVeo.Config,
			prepared *geminiVeo.PreparedRequest,
		) (*vertexVeo.Task, []byte, error) {
			submitCalls.Add(1)
			assert.Equal(t, credential, config.Credential)
			assert.Equal(t, "europe-west4", config.Snapshot.Region)
			assert.False(t, config.Snapshot.CustomBase)
			assert.Equal(t, "https://europe-west4-aiplatform.googleapis.com/v1", config.Snapshot.BaseURL)
			assert.Equal(t, vertexVeoUpstreamModel, prepared.UpstreamModel)
			assert.Equal(t, geminiVeo.ActionTextGenerate, prepared.Action)

			var task model.Task
			require.NoError(t, model.DB.Where("platform = ?", model.TaskOperationPlatformVertexVeo).First(&task).Error)
			assert.Equal(t, model.TaskStatusNotStart, task.Status)
			var operation model.TaskOperation
			require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
			assert.Equal(t, model.TaskOperationDispatching, operation.State)
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reservation.Status,
				"quota must be held and dispatch-marked before OAuth or provider I/O")
			return &vertexVeo.Task{ProviderTaskID: vertexVeoOperationID, Status: vertexVeo.StatusProcessing}, nil, nil
		},
		fetch: func(_ context.Context, config vertexVeo.Config, modelName, providerID string) (*vertexVeo.Task, []byte, error) {
			fetchCalls.Add(1)
			assert.Equal(t, credential, config.Credential, "recovery must use its encrypted credential snapshot")
			assert.Equal(t, "europe-west4", config.Snapshot.Region)
			assert.Equal(t, vertexVeoUpstreamModel, modelName)
			assert.Equal(t, vertexVeoOperationID, providerID)
			return &vertexVeo.Task{ProviderTaskID: providerID, Status: vertexVeo.StatusSucceeded,
				InlineVideo: &vertexVeo.InlineVideo{MIMEType: "video/mp4",
					Base64: base64.StdEncoding.EncodeToString(video), DecodedBytes: int64(len(video))}}, nil, nil
		},
	})

	c, recorder := vertexVeoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"veo-3.1-generate-preview","prompt":"a durable Vertex video","duration":8,"size":"1280x720"}`, "")
	RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeVertexVeoPublicID(t, recorder)
	assert.NotContains(t, recorder.Body.String(), vertexVeoOperationID)
	assert.NotContains(t, recorder.Body.String(), "durable-private-key-marker")
	assert.Equal(t, int32(1), submitCalls.Load())
	assert.Zero(t, fetchCalls.Load())

	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.TaskOperationPlatformVertexVeo, task.Platform)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
	assert.Positive(t, task.Quota)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, task.Quota, reservation.ActualQuota)
	assert.NotContains(t, task.PrivateData, vertexVeoOperationID)
	assert.NotContains(t, task.PrivateData, "durable-private-key-marker")
	assert.NotContains(t, operation.EncryptedProviderTaskID, vertexVeoOperationID)
	privateData, err := decodeGeminiVeoTaskPrivateData(task.PrivateData)
	require.NoError(t, err)
	require.NoError(t, vertexVeoProviderDescriptor().ValidateRoutingSnapshot(
		privateData.RoutingSnapshot, vertexVeoUpstreamModel))
	decryptedCredential, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		geminiVeoChannelCredentialBinding(taskID, task.Platform, task.UserId, task.ChannelId,
			privateData.RoutingSnapshot))
	require.NoError(t, err)
	assert.Equal(t, credential, decryptedCredential)
	decryptedOperation, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		geminiVeoProviderTaskBinding(taskID, operation.ReservationID, operation.Platform,
			operation.UserID, operation.ChannelID))
	require.NoError(t, err)
	assert.Equal(t, vertexVeoOperationID, decryptedOperation)

	c, recorder = vertexVeoLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID, "", taskID)
	RelayVideoTaskFetch(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"status":"queued"`)
	assert.NotContains(t, recorder.Body.String(), vertexVeoOperationID)
	assert.Zero(t, fetchCalls.Load(), "user fetch must remain local")

	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Update("key", "rotated-and-invalid").Error)
	forceVertexVeoRecoveryDue(t, taskID)
	require.NoError(t, reconcileAsyncGeminiVeoTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.Equal(t, model.TaskOperationTerminal, operation.State)
	data, err := decodeGeminiVeoStoredTaskData(task.Data)
	require.NoError(t, err)
	assert.True(t, data.HasInlineVideo)
	assert.Equal(t, int64(len(video)), data.InlineBytes)
	assert.True(t, validGeminiVeoInlineArtifactID(data.InlineArtifactID))
	artifactPath, err := geminiVeoInlineArtifactPathForID(taskID, data.InlineArtifactID)
	require.NoError(t, err)
	artifact, err := os.ReadFile(artifactPath)
	require.NoError(t, err)
	assert.NotContains(t, string(artifact), string(video))
	assert.NotContains(t, string(artifact), base64.StdEncoding.EncodeToString(video))
	legacyPath, err := geminiVeoInlineArtifactPath(taskID)
	require.NoError(t, err)
	_, err = os.Stat(legacyPath)
	assert.ErrorIs(t, err, os.ErrNotExist, "versioned artifacts must not reuse the stale legacy path")

	// Inline content is gateway-owned and remains available without consulting
	// or decrypting the provider credential snapshot.
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).
		Update("private_data", "intentionally-unavailable-after-success").Error)
	c, recorder = vertexVeoLifecycleContext(t, fixture, http.MethodGet,
		"/v1/videos/"+taskID+"/content", "", taskID)
	VideoProxy(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "video/mp4", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "private, max-age=86400", recorder.Header().Get("Cache-Control"))
	assert.Equal(t, video, recorder.Body.Bytes())

	c, recorder = vertexVeoLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID, "", taskID)
	requestctx.SetUserId(c, fixture.user.Id+1)
	RelayVideoTaskFetch(c, middleware.CaptureRelayRequestState(c))
	assert.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestVertexVeoMalformedCredentialRefundsWithoutProviderNetwork(t *testing.T) {
	fixture := configureVertexVeoLifecycleFixture(t)
	malformed := `{"type":"service_account","project_id":"project-123","private_key":"malformed-secret",` +
		`"client_email":"private@example.test"}`
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Update("key", malformed).Error)
	require.NoError(t, model.DB.First(&fixture.channel, fixture.channel.Id).Error)
	var providerCalls atomic.Int32
	installVertexVeoProvider(t, &vertexVeo.Client{HTTPClient: &http.Client{Transport: vertexVeoRoundTripFunc(
		func(*http.Request) (*http.Response, error) {
			providerCalls.Add(1)
			return nil, errors.New("provider network must not be reached")
		},
	)}})

	c, recorder := vertexVeoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"veo-3.1-generate-preview","prompt":"reject malformed credentials"}`, "")
	RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
	assert.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "malformed-secret")
	assert.NotContains(t, recorder.Body.String(), "private@example.test")
	assert.Zero(t, providerCalls.Load())

	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("platform = ?", model.TaskOperationPlatformVertexVeo).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationRefunded, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	var user model.User
	var token model.Token
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
}

func TestVertexVeoDefinitiveProviderRejectionRefundsExactly(t *testing.T) {
	fixture := configureVertexVeoLifecycleFixture(t)
	var submitCalls atomic.Int32
	installVertexVeoProvider(t, vertexVeoProviderStub{
		submit: func(context.Context, vertexVeo.Config, *geminiVeo.PreparedRequest) (*vertexVeo.Task, []byte, error) {
			submitCalls.Add(1)
			return nil, nil, &vertexVeo.RequestError{Err: &relaycommon.UpstreamError{
				StatusCode: http.StatusBadRequest, Body: `{"error":"sensitive-provider-detail"}`,
			}, Dispatched: true}
		},
		fetch: func(context.Context, vertexVeo.Config, string, string) (*vertexVeo.Task, []byte, error) {
			return nil, nil, errors.New("rejected submission must not be polled")
		},
	})

	c, recorder := vertexVeoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"veo-3.1-generate-preview","prompt":"definitive rejection"}`, "")
	RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "sensitive-provider-detail")
	assert.Equal(t, int32(1), submitCalls.Load())

	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("platform = ?", model.TaskOperationPlatformVertexVeo).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationRefunded, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	assert.NotContains(t, task.FailReason, "sensitive-provider-detail")
	var user model.User
	var token model.Token
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
}

func TestVertexVeoAmbiguousDispatchHoldsForManualReviewWithoutReplay(t *testing.T) {
	fixture := configureVertexVeoLifecycleFixture(t)
	var submitCalls, fetchCalls atomic.Int32
	installVertexVeoProvider(t, vertexVeoProviderStub{
		submit: func(context.Context, vertexVeo.Config, *geminiVeo.PreparedRequest) (*vertexVeo.Task, []byte, error) {
			submitCalls.Add(1)
			return nil, nil, &vertexVeo.RequestError{Err: errors.New("connection reset after provider write"), Dispatched: true}
		},
		fetch: func(context.Context, vertexVeo.Config, string, string) (*vertexVeo.Task, []byte, error) {
			fetchCalls.Add(1)
			return nil, nil, errors.New("ambiguous submission must not be polled without an operation id")
		},
	})
	c, recorder := vertexVeoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"veo-3.1-generate-preview","prompt":"ambiguous dispatch"}`, "")
	RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "connection reset")

	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("platform = ?", model.TaskOperationPlatformVertexVeo).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.TaskStatusUnknown, task.Status)
	assert.Equal(t, model.TaskOperationManualReview, operation.State)
	assert.True(t, operation.SettlementPending)
	assert.Empty(t, operation.EncryptedProviderTaskID)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reservation.Status)
	assert.Equal(t, int32(1), submitCalls.Load())
	require.NoError(t, reconcileAsyncGeminiVeoTasks(context.Background()))
	assert.Equal(t, int32(1), submitCalls.Load(), "ambiguous submission must never replay")
	assert.Zero(t, fetchCalls.Load())
}

func TestVertexVeoTerminalProviderFailureReversesExactSettlement(t *testing.T) {
	fixture := configureVertexVeoLifecycleFixture(t)
	installVertexVeoProvider(t, vertexVeoProviderStub{
		submit: func(context.Context, vertexVeo.Config, *geminiVeo.PreparedRequest) (*vertexVeo.Task, []byte, error) {
			return &vertexVeo.Task{ProviderTaskID: vertexVeoOperationID, Status: vertexVeo.StatusProcessing}, nil, nil
		},
		fetch: func(context.Context, vertexVeo.Config, string, string) (*vertexVeo.Task, []byte, error) {
			return &vertexVeo.Task{ProviderTaskID: vertexVeoOperationID, Status: vertexVeo.StatusFailed,
				ErrorCode: "content_filtered", ErrorMessage: "sensitive provider detail"}, nil, nil
		},
	})
	c, recorder := vertexVeoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"veo-3.1-generate-preview","prompt":"terminal reversal"}`, "")
	RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeVertexVeoPublicID(t, recorder)
	forceVertexVeoRecoveryDue(t, taskID)
	require.NoError(t, reconcileAsyncGeminiVeoTasks(context.Background()))

	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationReversed, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusReversed, reservation.Status)
	assert.NotContains(t, task.Data, "sensitive provider detail")
	var user model.User
	var token model.Token
	var channel model.Channel
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
	assert.Equal(t, int64(reservation.ActualQuota), channel.UsedQuota,
		"provider accounting remains an append-only gross-usage metric after reversal")

	c, recorder = vertexVeoLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID, "", taskID)
	RelayVideoTaskFetch(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"status":"failed"`)
	assert.NotContains(t, recorder.Body.String(), "sensitive provider detail")
}
