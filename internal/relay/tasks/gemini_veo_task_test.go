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
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	geminiVeo "github.com/tokenrouter/tokenrouter/internal/relay/providers/task/gemini"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	geminiVeoTestOriginModel = "veo-3.1-fast-generate-preview"
	geminiVeoTestMappedModel = "veo-3.1-generate-preview"
	geminiVeoTestCredential  = "gemini-veo-test-key"
	geminiVeoTestBaseURL     = "https://gemini-veo.example"
	geminiVeoTestTinyPNG     = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
)

type fakeGeminiVeoProvider struct {
	submit  func(context.Context, string, string, *geminiVeo.PreparedRequest) (*geminiVeo.Task, []byte, error)
	fetch   func(context.Context, string, string, string) (*geminiVeo.Task, []byte, error)
	content func(context.Context, string, string, string) (*http.Response, error)
}

func (provider *fakeGeminiVeoProvider) Submit(ctx context.Context, baseURL, credential string,
	prepared *geminiVeo.PreparedRequest) (*geminiVeo.Task, []byte, error) {
	if provider == nil || provider.submit == nil {
		return nil, nil, errors.New("unexpected Gemini Veo submit")
	}
	return provider.submit(ctx, baseURL, credential, prepared)
}

func (provider *fakeGeminiVeoProvider) Fetch(ctx context.Context, baseURL, credential,
	providerID string) (*geminiVeo.Task, []byte, error) {
	if provider == nil || provider.fetch == nil {
		return nil, nil, errors.New("unexpected Gemini Veo fetch")
	}
	return provider.fetch(ctx, baseURL, credential, providerID)
}

func (provider *fakeGeminiVeoProvider) Content(ctx context.Context, baseURL, credential,
	resultURL string) (*http.Response, error) {
	if provider == nil || provider.content == nil {
		return nil, errors.New("unexpected Gemini Veo content")
	}
	return provider.content(ctx, baseURL, credential, resultURL)
}

func configureGeminiVeoLifecycleFixture(t *testing.T) relayAccountingFixture {
	t.Helper()
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "veo-test=0123456789abcdef0123456789abcdef")
	t.Setenv("VIDEO_TASK_RECOVERY_DIR", t.TempDir())
	fixture := newRelayAccountingFixture(t, 2_000_000, 2_000_000)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Task{}, &model.TaskOperation{}, &model.JimengTaskOperation{},
		&model.AuditLogOutbox{}, &model.RelayQuotaReservationReviewEvent{},
	))
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", fixture.token.Id).
		Update("group", userssvc.GroupDefault).Error)
	fixture.token.Group = userssvc.GroupDefault
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Updates(map[string]any{
			"type": int(channelcatalog.ChannelTypeGemini), "key": geminiVeoTestCredential,
			"base_url": geminiVeoTestBaseURL, "models": strings.Join(geminiVeo.ModelList(), ","),
			"model_mapping": `{"veo-3.1-fast-generate-preview":"veo-3.1-generate-preview"}`,
		}).Error)
	require.NoError(t, model.DB.Where("channel_id = ?", fixture.channel.Id).Delete(&model.Ability{}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: userssvc.GroupDefault,
		Model: geminiVeoTestOriginModel, ChannelId: fixture.channel.Id, Enabled: true, Weight: 1}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
	keys := []string{setting.ModelBillingModeOption, setting.PerCallModelPriceOption,
		setting.ModelRatioOption, setting.CompletionRatioOption, setting.GroupRatioOption,
		setting.GroupGroupRatioOption}
	previous := setting.GetOptions(keys...)
	t.Cleanup(func() { _ = setting.UpdateOptions(previous) })
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"veo-3.1-fast-generate-preview":"reference"}`,
		setting.PerCallModelPriceOption: `{"veo-3.1-fast-generate-preview":0.004}`,
		setting.ModelRatioOption:        `{}`,
		setting.CompletionRatioOption:   `{"veo-3.1-fast-generate-preview":99}`,
		setting.GroupRatioOption:        `{"default":1}`,
		setting.GroupGroupRatioOption:   `{}`,
	}))
	return fixture
}

func geminiVeoLifecycleContext(t *testing.T, fixture relayAccountingFixture, method, path,
	body, taskID string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
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

func installGeminiVeoProvider(t *testing.T, provider geminiVeoProvider) {
	t.Helper()
	previous := newGeminiVeoProvider
	newGeminiVeoProvider = func() geminiVeoProvider { return provider }
	t.Cleanup(func() { newGeminiVeoProvider = previous })
}

func decodeGeminiVeoPublicID(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response geminiVeoVideoResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.NoError(t, validateGeminiVeoTaskPublicID(response.ID))
	return response.ID
}

func forceGeminiVeoRecoveryDue(t *testing.T, taskID string) {
	t.Helper()
	require.NoError(t, model.DB.Model(&model.TaskOperation{}).Where("task_id = ?", taskID).
		Updates(map[string]any{"next_attempt_at": 0, "lease_owner": "", "lease_expires_at": 0}).Error)
}

func TestGeminiVeoVideoSubmitFetchEncryptedReservationAndMappedPricing(t *testing.T) {
	fixture := configureGeminiVeoLifecycleFixture(t)
	providerID := "models/" + geminiVeoTestMappedModel + "/operations/op-submit"
	var submitCalls, fetchCalls atomic.Int32
	installGeminiVeoProvider(t, &fakeGeminiVeoProvider{
		submit: func(_ context.Context, baseURL, credential string,
			prepared *geminiVeo.PreparedRequest) (*geminiVeo.Task, []byte, error) {
			submitCalls.Add(1)
			assert.Equal(t, geminiVeoTestBaseURL, baseURL)
			assert.Equal(t, geminiVeoTestCredential, credential)
			assert.Equal(t, geminiVeoTestOriginModel, prepared.OriginModel)
			assert.Equal(t, geminiVeoTestMappedModel, prepared.UpstreamModel)
			assert.Equal(t, geminiVeo.ActionGenerate, prepared.Action)
			assert.Equal(t, 6, prepared.Duration)
			assert.Equal(t, "4k", prepared.Resolution)
			assert.Equal(t, "16:9", prepared.AspectRatio)
			assert.True(t, prepared.HasImage)
			var wire map[string]any
			require.NoError(t, json.Unmarshal(prepared.Body, &wire))
			parameters, ok := wire["parameters"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, float64(1), parameters["sampleCount"])
			assert.Equal(t, float64(6), parameters["durationSeconds"])

			var task model.Task
			require.NoError(t, model.DB.Where("platform = ?", model.TaskOperationPlatformGeminiVeo).
				First(&task).Error)
			assert.Equal(t, model.TaskStatusNotStart, task.Status)
			var operation model.TaskOperation
			require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
			assert.Equal(t, model.TaskOperationDispatching, operation.State)
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).
				First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reservation.Status,
				"the immutable Veo charge must be held before provider I/O")
			assert.Equal(t, 18_000, reservation.RequestedQuota)
			return &geminiVeo.Task{ProviderTaskID: providerID, Status: geminiVeo.StatusProcessing}, nil, nil
		},
		fetch: func(context.Context, string, string, string) (*geminiVeo.Task, []byte, error) {
			fetchCalls.Add(1)
			return nil, nil, errors.New("public fetch contacted Gemini")
		},
	})
	body := `{"model":"` + geminiVeoTestOriginModel + `","prompt":"mapped image request",` +
		`"seconds":"6","size":"3840x2160","image":"data:image/png;base64,` + geminiVeoTestTinyPNG + `",` +
		`"metadata":{"sampleCount":7}}`
	c, recorder := geminiVeoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos", body, "")
	RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeGeminiVeoPublicID(t, recorder)
	assert.NotContains(t, recorder.Body.String(), providerID)
	assert.NotContains(t, recorder.Body.String(), geminiVeoTestCredential)

	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.TaskOperationPlatformGeminiVeo, task.Platform)
	assert.Equal(t, model.TaskOperationPlatformGeminiVeo, operation.Platform)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, 18_000, task.Quota)
	assert.Equal(t, 18_000, reservation.ActualQuota)
	assert.NotContains(t, task.PrivateData, geminiVeoTestCredential)
	assert.NotContains(t, task.PrivateData, providerID)
	assert.NotContains(t, operation.EncryptedProviderTaskID, providerID)
	privateData, err := decodeGeminiVeoTaskPrivateData(task.PrivateData)
	require.NoError(t, err)
	assert.Equal(t, operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID)
	assert.Equal(t, 6, privateData.Pricing.Duration)
	assert.Equal(t, "1.5", privateData.Pricing.ResolutionMultiplier)
	decryptedKey, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		geminiVeoChannelCredentialBinding(taskID, task.Platform, task.UserId, task.ChannelId,
			privateData.RoutingSnapshot))
	require.NoError(t, err)
	assert.Equal(t, geminiVeoTestCredential, decryptedKey)
	decryptedID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		geminiVeoProviderTaskBinding(taskID, operation.ReservationID, task.Platform, task.UserId, task.ChannelId))
	require.NoError(t, err)
	assert.Equal(t, providerID, decryptedID)

	c, recorder = geminiVeoLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID, "", taskID)
	RelayVideoTaskFetch(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), taskID)
	assert.NotContains(t, recorder.Body.String(), providerID)
	assert.NotContains(t, recorder.Body.String(), geminiVeoTestCredential)
	assert.Zero(t, fetchCalls.Load(), "user fetch is local and scheduler-owned polling remains separate")

	c, recorder = geminiVeoLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID, "", taskID)
	requestctx.SetUserId(c, fixture.user.Id+1)
	RelayVideoTaskFetch(c, middleware.CaptureRelayRequestState(c))
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, int32(1), submitCalls.Load())
}

func TestGeminiVeoRecoveryUsesFrozenRouteAndContentIsOwnerScoped(t *testing.T) {
	fixture := configureGeminiVeoLifecycleFixture(t)
	providerID := "models/" + geminiVeoTestMappedModel + "/operations/op-content"
	resultURL := geminiVeoTestBaseURL + "/v1beta/files/result:download"
	var fetchCalls, contentCalls atomic.Int32
	installGeminiVeoProvider(t, &fakeGeminiVeoProvider{
		submit: func(context.Context, string, string, *geminiVeo.PreparedRequest) (*geminiVeo.Task, []byte, error) {
			return &geminiVeo.Task{ProviderTaskID: providerID, Status: geminiVeo.StatusProcessing}, nil, nil
		},
		fetch: func(_ context.Context, baseURL, credential, id string) (*geminiVeo.Task, []byte, error) {
			fetchCalls.Add(1)
			assert.Equal(t, geminiVeoTestBaseURL, baseURL)
			assert.Equal(t, geminiVeoTestCredential, credential)
			assert.Equal(t, providerID, id)
			return &geminiVeo.Task{ProviderTaskID: id, Status: geminiVeo.StatusSucceeded,
				ResultURL: resultURL}, nil, nil
		},
		content: func(_ context.Context, baseURL, credential, url string) (*http.Response, error) {
			contentCalls.Add(1)
			assert.Equal(t, geminiVeoTestBaseURL, baseURL)
			assert.Equal(t, geminiVeoTestCredential, credential)
			assert.Equal(t, resultURL, url)
			return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len("veo-video")),
				Header: http.Header{"Content-Type": []string{"video/mp4"}},
				Body:   io.NopCloser(strings.NewReader("veo-video"))}, nil
		},
	})
	c, recorder := geminiVeoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"`+geminiVeoTestOriginModel+`","prompt":"durable route","duration":4}`, "")
	RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeGeminiVeoPublicID(t, recorder)

	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Updates(map[string]any{"base_url": "https://changed.example", "key": "changed-key"}).Error)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.PerCallModelPriceOption: `{"veo-3.1-fast-generate-preview":9}`,
		setting.GroupRatioOption:        `{"default":9}`,
	}))
	forceGeminiVeoRecoveryDue(t, taskID)
	require.NoError(t, reconcileAsyncGeminiVeoTasks(context.Background()))

	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.Equal(t, model.TaskOperationTerminal, operation.State)
	assert.Equal(t, 8_000, task.Quota)
	assert.Equal(t, 8_000, reservation.ActualQuota)
	assert.Contains(t, task.Data, resultURL)
	require.NoError(t, reconcileAsyncGeminiVeoTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load(), "terminal tasks must never regress or repoll")

	c, recorder = geminiVeoLifecycleContext(t, fixture, http.MethodGet,
		"/v1/videos/"+taskID+"/content", "", taskID)
	VideoProxy(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "veo-video", recorder.Body.String())
	assert.Equal(t, "video/mp4", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))

	c, recorder = geminiVeoLifecycleContext(t, fixture, http.MethodGet,
		"/v1/videos/"+taskID+"/content", "", taskID)
	requestctx.SetUserId(c, fixture.user.Id+1)
	VideoProxy(c, middleware.CaptureRelayRequestState(c))
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, int32(1), contentCalls.Load())
}

func TestGeminiVeoDefinitiveRejectionRefundsAndAmbiguityNeverReplays(t *testing.T) {
	t.Run("definitive rejection", func(t *testing.T) {
		fixture := configureGeminiVeoLifecycleFixture(t)
		installGeminiVeoProvider(t, &fakeGeminiVeoProvider{
			submit: func(context.Context, string, string, *geminiVeo.PreparedRequest) (*geminiVeo.Task, []byte, error) {
				return nil, nil, &geminiVeo.RequestError{Err: &relaycommon.UpstreamError{
					StatusCode: http.StatusBadRequest, Body: `{"message":"` + geminiVeoTestCredential + ` denied"}`,
				}, Dispatched: true}
			},
		})
		c, recorder := geminiVeoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
			`{"model":"`+geminiVeoTestOriginModel+`","prompt":"reject"}`, "")
		RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.NotContains(t, recorder.Body.String(), geminiVeoTestCredential)
		assertGeminiVeoRefunded(t, fixture)
	})

	t.Run("ambiguous dispatch", func(t *testing.T) {
		fixture := configureGeminiVeoLifecycleFixture(t)
		var submitCalls, fetchCalls atomic.Int32
		installGeminiVeoProvider(t, &fakeGeminiVeoProvider{
			submit: func(context.Context, string, string, *geminiVeo.PreparedRequest) (*geminiVeo.Task, []byte, error) {
				submitCalls.Add(1)
				return nil, nil, &geminiVeo.RequestError{Err: errors.New("connection reset after write"), Dispatched: true}
			},
			fetch: func(context.Context, string, string, string) (*geminiVeo.Task, []byte, error) {
				fetchCalls.Add(1)
				return nil, nil, errors.New("must not poll without provider operation")
			},
		})
		c, recorder := geminiVeoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
			`{"model":"`+geminiVeoTestOriginModel+`","prompt":"ambiguous"}`, "")
		RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
		require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
		var task model.Task
		var operation model.TaskOperation
		var reservation model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.First(&task).Error)
		require.NoError(t, model.DB.First(&operation).Error)
		require.NoError(t, model.DB.First(&reservation).Error)
		assert.Equal(t, model.TaskStatusUnknown, task.Status)
		assert.Equal(t, model.TaskOperationManualReview, operation.State)
		assert.True(t, operation.SettlementPending)
		assert.Empty(t, operation.EncryptedProviderTaskID)
		assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reservation.Status)
		require.NoError(t, reconcileAsyncGeminiVeoTasks(context.Background()))
		assert.Equal(t, int32(1), submitCalls.Load(), "ambiguous submissions are never replayed")
		assert.Zero(t, fetchCalls.Load())

		review, err := billingsvc.GetManualReviewRelayQuotaReservation(reservation.ReservationID)
		require.NoError(t, err)
		assert.Equal(t, billingsvc.RelayQuotaReviewKindGeminiVeoTask, review.ReviewKind)
		assert.Equal(t, model.TaskOperationPlatformGeminiVeo, review.TaskPlatform)
		assert.True(t, review.ResolutionRequired)
		assert.False(t, review.ProviderTaskIDPresent)
		resolved, err := ResolveGeminiVeoRelayQuotaReservationReview(reservation.ReservationID, 9201,
			billingsvc.RelayQuotaReviewResolutionRefund)
		require.NoError(t, err)
		assert.True(t, resolved.Changed)
		assert.Equal(t, model.RelayQuotaReservationStatusRefunded, resolved.ReservationStatus)
		replay, err := ResolveGeminiVeoRelayQuotaReservationReview(reservation.ReservationID, 9202,
			billingsvc.RelayQuotaReviewResolutionRefund)
		require.NoError(t, err)
		assert.False(t, replay.Changed)
		assert.Equal(t, resolved.AuditEventID, replay.AuditEventID)
	})
}

func assertGeminiVeoRefunded(t *testing.T, fixture relayAccountingFixture) {
	t.Helper()
	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	var user model.User
	var token model.Token
	require.NoError(t, model.DB.First(&task).Error)
	require.NoError(t, model.DB.First(&operation).Error)
	require.NoError(t, model.DB.First(&reservation).Error)
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.Unscoped().First(&token, fixture.token.Id).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationRefunded, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
}

func TestGeminiVeoTerminalFailureReversesSettledChargeExactlyOnce(t *testing.T) {
	fixture := configureGeminiVeoLifecycleFixture(t)
	providerID := "models/" + geminiVeoTestMappedModel + "/operations/op-failure"
	var fetchCalls atomic.Int32
	installGeminiVeoProvider(t, &fakeGeminiVeoProvider{
		submit: func(context.Context, string, string, *geminiVeo.PreparedRequest) (*geminiVeo.Task, []byte, error) {
			return &geminiVeo.Task{ProviderTaskID: providerID, Status: geminiVeo.StatusProcessing}, nil, nil
		},
		fetch: func(context.Context, string, string, string) (*geminiVeo.Task, []byte, error) {
			fetchCalls.Add(1)
			return &geminiVeo.Task{ProviderTaskID: providerID, Status: geminiVeo.StatusFailed,
				ErrorCode: "POLICY_REJECTED", ErrorMessage: geminiVeoTestCredential + " is secret"}, nil, nil
		},
	})
	c, recorder := geminiVeoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"`+geminiVeoTestOriginModel+`","prompt":"fail later"}`, "")
	RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeGeminiVeoPublicID(t, recorder)
	forceGeminiVeoRecoveryDue(t, taskID)
	require.NoError(t, reconcileAsyncGeminiVeoTasks(context.Background()))

	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	var user model.User
	var token model.Token
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.Unscoped().First(&token, fixture.token.Id).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Equal(t, model.TaskOperationReversed, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusReversed, reservation.Status)
	assert.Zero(t, task.Quota)
	assert.NotContains(t, task.Data, geminiVeoTestCredential)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
	var refundAudit int64
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", "geminiVeo-refund:"+reservation.ReservationID).Count(&refundAudit).Error)
	assert.EqualValues(t, 1, refundAudit)
	require.NoError(t, reconcileAsyncGeminiVeoTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())
}

func createPreparedGeminiVeoTaskForRecovery(t *testing.T,
	fixture relayAccountingFixture) (*model.Task, *billingsvc.RelayQuotaReservation) {
	t.Helper()
	plan, enabled, err := billingsvc.ResolveReferenceAsyncTaskBillingPlan(
		geminiVeoTestOriginModel, userssvc.GroupDefault, userssvc.GroupDefault)
	require.NoError(t, err)
	require.True(t, enabled)
	pricing, err := newGeminiVeoPricingSnapshot(plan, geminiVeoTestMappedModel, 8, "720p")
	require.NoError(t, err)
	quota, err := pricing.quota(geminiVeoTestMappedModel, "720p")
	require.NoError(t, err)
	taskID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	properties, err := marshalGeminiVeoTaskProperties(geminiVeoTaskProperties{
		Version: geminiVeoTaskMetadataVersion, Family: "gemini_api", Input: "restart",
		OriginModelName: geminiVeoTestOriginModel, UpstreamModelName: geminiVeoTestMappedModel,
		Action: geminiVeo.ActionTextGenerate, Duration: 8, Resolution: "720p",
		AspectRatio: "16:9", Pricing: pricing,
	})
	require.NoError(t, err)
	routing, err := geminiVeoProviderDescriptor().PrepareRoutingSnapshot(&model.Channel{
		Id: fixture.channel.Id, Type: int(channelcatalog.ChannelTypeGemini), BaseURL: geminiVeoTestBaseURL,
	}, geminiVeoTestMappedModel)
	require.NoError(t, err)
	now := wallclock.NowTimestamp()
	task := &model.Task{CreatedAt: now, UpdatedAt: now, TaskID: taskID,
		Platform: model.TaskOperationPlatformGeminiVeo, UserId: fixture.user.Id,
		Group: userssvc.GroupDefault, ChannelId: fixture.channel.Id, Quota: quota,
		Action: string(geminiVeo.ActionTextGenerate), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: properties, Data: "null"}
	encryptedKey, err := asyncTaskEncryptBound(geminiVeoTestCredential,
		geminiVeoChannelCredentialBinding(task.TaskID, task.Platform, task.UserId, task.ChannelId, routing))
	require.NoError(t, err)
	privateData := geminiVeoTaskPrivateData{Version: geminiVeoTaskMetadataVersion,
		RoutingSnapshot: routing, EncryptedChannelKey: encryptedKey, Pricing: pricing}
	reservation, err := createGeminiVeoReservedTask(task, &fixture.token, &privateData)
	require.NoError(t, err)
	return task, reservation
}

func TestGeminiVeoAcceptedJournalPromotesAcrossRestart(t *testing.T) {
	fixture := configureGeminiVeoLifecycleFixture(t)
	task, reservation := createPreparedGeminiVeoTaskForRecovery(t, fixture)
	providerID := "models/" + geminiVeoTestMappedModel + "/operations/op-restart"
	require.NoError(t, markGeminiVeoTaskDispatching(task, reservation))
	require.NoError(t, persistAcceptedGeminiVeoRecoveryJournal(task, reservation.ReservationID(),
		providerID, geminiVeo.ActionTextGenerate))
	path, err := geminiVeoRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)
	disk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(disk), providerID)
	assert.NotContains(t, string(disk), geminiVeoTestCredential)
	require.NoError(t, PromoteGeminiVeoTaskRecoveryJournalsContext(context.Background()))
	_, err = os.Stat(path)
	assert.True(t, errors.Is(err, os.ErrNotExist))
	var operation model.TaskOperation
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.True(t, operation.SettlementPending)
	assert.NotContains(t, operation.EncryptedProviderTaskID, providerID)

	var fetchCalls atomic.Int32
	installGeminiVeoProvider(t, &fakeGeminiVeoProvider{
		fetch: func(_ context.Context, baseURL, credential, id string) (*geminiVeo.Task, []byte, error) {
			fetchCalls.Add(1)
			assert.Equal(t, geminiVeoTestBaseURL, baseURL)
			assert.Equal(t, geminiVeoTestCredential, credential)
			return &geminiVeo.Task{ProviderTaskID: id, Status: geminiVeo.StatusSucceeded,
				ResultURL: "https://gemini-veo.example/restart.mp4"}, nil, nil
		},
	})
	forceGeminiVeoRecoveryDue(t, task.TaskID)
	require.NoError(t, reconcileAsyncGeminiVeoTasks(context.Background()))
	forceGeminiVeoRecoveryDue(t, task.TaskID)
	require.NoError(t, reconcileAsyncGeminiVeoTasks(context.Background()))
	require.NoError(t, model.DB.Where("id = ?", task.ID).First(task).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.Equal(t, int32(1), fetchCalls.Load())
}

func TestGeminiVeoRecoveryLeasePreventsConcurrentPolls(t *testing.T) {
	fixture := configureGeminiVeoLifecycleFixture(t)
	providerID := "models/" + geminiVeoTestMappedModel + "/operations/op-concurrent"
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var fetchCalls atomic.Int32
	installGeminiVeoProvider(t, &fakeGeminiVeoProvider{
		submit: func(context.Context, string, string, *geminiVeo.PreparedRequest) (*geminiVeo.Task, []byte, error) {
			return &geminiVeo.Task{ProviderTaskID: providerID, Status: geminiVeo.StatusProcessing}, nil, nil
		},
		fetch: func(_ context.Context, _, _, id string) (*geminiVeo.Task, []byte, error) {
			fetchCalls.Add(1)
			started <- struct{}{}
			<-release
			return &geminiVeo.Task{ProviderTaskID: id, Status: geminiVeo.StatusProcessing}, nil, nil
		},
	})
	c, recorder := geminiVeoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"`+geminiVeoTestOriginModel+`","prompt":"concurrent"}`, "")
	RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeGeminiVeoPublicID(t, recorder)
	forceGeminiVeoRecoveryDue(t, taskID)
	firstDone := make(chan error, 1)
	go func() { firstDone <- reconcileAsyncGeminiVeoTasks(context.Background()) }()
	<-started
	require.NoError(t, reconcileAsyncGeminiVeoTasks(context.Background()))
	close(release)
	require.NoError(t, <-firstDone)
	assert.Equal(t, int32(1), fetchCalls.Load())
}

func TestVeoInlineArtifactIsEncryptedIdentityBoundAndStrictlyVerified(t *testing.T) {
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "veo-artifact=abcdef0123456789abcdef0123456789")
	t.Setenv("VIDEO_TASK_RECOVERY_DIR", t.TempDir())
	taskID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	task := &model.Task{TaskID: taskID, Platform: model.TaskOperationPlatformVertexVeo,
		UserId: 101, ChannelId: 202}
	plain := append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2',
		0, 0, 0, 0, 'm', 'p', '4', '2', 'i', 's', 'o', 'm'}, []byte("private-video-payload")...)
	encoded := base64.StdEncoding.EncodeToString(plain)
	artifactID, err := persistGeminiVeoInlineArtifact(task, "video/mp4", encoded, int64(len(plain)))
	require.NoError(t, err)
	path, err := geminiVeoInlineArtifactPathForID(taskID, artifactID)
	require.NoError(t, err)
	disk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(disk), "private-video-payload")
	assert.NotContains(t, string(disk), encoded)

	data := geminiVeoStoredTaskData{State: geminiVeo.StatusSucceeded, HasInlineVideo: true,
		InlineMIMEType: "video/mp4", InlineBytes: int64(len(plain)), InlineArtifactID: artifactID}
	response, err := openGeminiVeoInlineArtifact(task, data)
	require.NoError(t, err)
	got, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, plain, got)

	wrongOwner := *task
	wrongOwner.UserId++
	_, err = openGeminiVeoInlineArtifact(&wrongOwner, data)
	assert.Error(t, err, "the encrypted artifact must be bound to the public task owner")

	disk[len(disk)-8] ^= 0x40
	require.NoError(t, os.WriteFile(path, disk, 0o600))
	response, err = openGeminiVeoInlineArtifact(task, data)
	require.NoError(t, err, "the authenticated header remains readable before chunk streaming")
	_, err = io.ReadAll(response.Body)
	assert.Error(t, err, "ciphertext corruption must fail authentication")
	assert.NoError(t, response.Body.Close())
}

func TestVeoInlineArtifactsAreVersionedAgainstStaleWriterReplacement(t *testing.T) {
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "veo-artifact=abcdef0123456789abcdef0123456789")
	t.Setenv("VIDEO_TASK_RECOVERY_DIR", t.TempDir())
	taskID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	task := &model.Task{TaskID: taskID, Platform: model.TaskOperationPlatformVertexVeo,
		UserId: 303, ChannelId: 404}
	prefix := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2',
		0, 0, 0, 0, 'm', 'p', '4', '2', 'i', 's', 'o', 'm'}
	first := append(append([]byte(nil), prefix...), []byte("first-version")...)
	second := append(append([]byte(nil), prefix...), []byte("stale-version")...)
	firstEncoded := base64.StdEncoding.EncodeToString(first)
	secondEncoded := base64.StdEncoding.EncodeToString(second)

	firstID, err := persistGeminiVeoInlineArtifact(task, "video/mp4", firstEncoded, int64(len(first)))
	require.NoError(t, err)
	secondID, err := persistGeminiVeoInlineArtifact(task, "video/mp4", secondEncoded, int64(len(second)))
	require.NoError(t, err)
	assert.NotEqual(t, firstID, secondID)
	firstPath, err := geminiVeoInlineArtifactPathForID(taskID, firstID)
	require.NoError(t, err)
	secondPath, err := geminiVeoInlineArtifactPathForID(taskID, secondID)
	require.NoError(t, err)
	assert.NotEqual(t, firstPath, secondPath)

	for _, fixture := range []struct {
		id      string
		content []byte
	}{
		{id: firstID, content: first},
		{id: secondID, content: second},
	} {
		data := geminiVeoStoredTaskData{State: geminiVeo.StatusSucceeded, HasInlineVideo: true,
			InlineMIMEType: "video/mp4", InlineBytes: int64(len(fixture.content)), InlineArtifactID: fixture.id}
		response, err := openGeminiVeoInlineArtifact(task, data)
		require.NoError(t, err)
		got, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		assert.Equal(t, fixture.content, got)
	}
}

func TestGeminiAndVertexVeoPlatformsShareOnlyTheStateMachine(t *testing.T) {
	assert.Equal(t, "24", model.TaskOperationPlatformGeminiVeo)
	assert.Equal(t, "41", model.TaskOperationPlatformVertexVeo)
	assert.True(t, model.IsVeoTaskOperationPlatform(model.TaskOperationPlatformGeminiVeo))
	assert.True(t, model.IsVeoTaskOperationPlatform(model.TaskOperationPlatformVertexVeo))
	assert.NotEqual(t, geminiVeoProviderDescriptor().Family, vertexVeoProviderDescriptor().Family)
	assert.NotEqual(t, geminiVeoProviderDescriptor().ChannelType, vertexVeoProviderDescriptor().ChannelType)
	assert.ElementsMatch(t, []string{"24", "41"}, model.VeoTaskOperationPlatforms())
}

func TestGeminiVeoDurableJSONRejectsDuplicateKeysTrailingDataAndExcessDepth(t *testing.T) {
	_, err := decodeGeminiVeoRoutingSnapshot(
		`{"base_url":"https://first.example","base_url":"https://second.example"}`)
	assert.Error(t, err)
	_, err = decodeGeminiVeoStoredTaskData(
		`{"state":"processing","state":"succeeded","result_url":"https://result.example/video.mp4"}`)
	assert.Error(t, err)
	_, err = decodeGeminiVeoStoredTaskData(`{"state":"processing"} {"state":"failed"}`)
	assert.Error(t, err)
	deep := strings.Repeat(`{"nested":`, geminiVeoTaskJSONMaxDepth+2) + `null` +
		strings.Repeat(`}`, geminiVeoTaskJSONMaxDepth+2)
	var target map[string]any
	assert.Error(t, strictGeminiVeoJSON([]byte(deep), &target))
}

func TestGeminiVeoRemixIsRejectedBeforeChannelOrProviderDispatch(t *testing.T) {
	fixture := configureGeminiVeoLifecycleFixture(t)
	var submitCalls atomic.Int32
	installGeminiVeoProvider(t, &fakeGeminiVeoProvider{submit: func(context.Context, string, string,
		*geminiVeo.PreparedRequest) (*geminiVeo.Task, []byte, error) {
		submitCalls.Add(1)
		return nil, nil, errors.New("unexpected submit")
	}})
	c, recorder := geminiVeoLifecycleContext(t, fixture, http.MethodPost,
		"/v1/videos/task_0123456789abcdef0123456789abcdef/remix",
		`{"model":"`+geminiVeoTestOriginModel+`","prompt":"unsupported remix"}`, "")
	RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "not supported")
	assert.Zero(t, submitCalls.Load())
	var taskCount int64
	require.NoError(t, model.DB.Model(&model.Task{}).Count(&taskCount).Error)
	assert.Zero(t, taskCount)
}

func TestGeminiVeoPricingSnapshotRejectsTamperingAndArithmeticOverflow(t *testing.T) {
	plan := billingsvc.ReferenceAsyncTaskBillingPlan{Version: 1, ModelName: geminiVeoTestOriginModel,
		GroupRatio: "1", UseFixedPrice: true, FixedPrice: "0.004"}
	pricing, err := newGeminiVeoPricingSnapshot(plan, geminiVeoTestMappedModel, 8, "4k")
	require.NoError(t, err)
	quota, err := pricing.quota(geminiVeoTestMappedModel, "4k")
	require.NoError(t, err)
	assert.Equal(t, 24_000, quota)

	tampered := pricing
	tampered.ResolutionMultiplier = "2.333333"
	_, err = tampered.quota(geminiVeoTestMappedModel, "4k")
	assert.Error(t, err)
	tampered = pricing
	tampered.Duration = 7
	_, err = tampered.quota(geminiVeoTestMappedModel, "4k")
	assert.Error(t, err)

	overflow := geminiVeoPricingSnapshot{Plan: billingsvc.ReferenceAsyncTaskBillingPlan{
		Version: 1, ModelName: geminiVeoTestOriginModel, GroupRatio: "1",
		UseFixedPrice: true, FixedPrice: "1000000",
	}, Duration: 8, ResolutionMultiplier: "1.5"}
	_, err = overflow.quota(geminiVeoTestMappedModel, "4k")
	assert.Error(t, err, "oversized Veo charges must fail instead of saturating or wrapping")
}
