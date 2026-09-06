package relay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/task/hailuo"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

type fakeHailuoProvider struct {
	submit func(context.Context, string, string, *hailuo.PreparedRequest) (*hailuo.Task, []byte, error)
	fetch  func(context.Context, string, string, string) (*hailuo.Task, []byte, error)
}

func (provider *fakeHailuoProvider) Submit(ctx context.Context, baseURL, key string,
	prepared *hailuo.PreparedRequest) (*hailuo.Task, []byte, error) {
	return provider.submit(ctx, baseURL, key, prepared)
}

func (provider *fakeHailuoProvider) Fetch(ctx context.Context, baseURL, key, id string) (*hailuo.Task, []byte, error) {
	return provider.fetch(ctx, baseURL, key, id)
}

type hailuoRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn hailuoRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

const hailuoTestModel = "MiniMax-Hailuo-2.3"

func configureHailuoLifecycleFixture(t *testing.T, fixedPrice bool) relayAccountingFixture {
	t.Helper()
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "hailuo-test=0123456789abcdef0123456789abcdef")
	t.Setenv("HAILUO_TASK_RECOVERY_DIR", t.TempDir())
	fixture := newRelayAccountingFixture(t, 2_000_000, 2_000_000)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Task{}, &model.TaskOperation{}, &model.JimengTaskOperation{},
		&model.AuditLogOutbox{}, &model.RelayQuotaReservationReviewEvent{},
	))
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", fixture.token.Id).
		Update("group", service.GroupDefault).Error)
	fixture.token.Group = service.GroupDefault
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Updates(map[string]any{
			"type": int(constant.ChannelTypeMiniMax), "key": "upstream-hailuo-key",
			"base_url": "https://hailuo.example", "models": strings.Join(hailuo.ModelList(), ","),
			"model_mapping": `{"MiniMax-Hailuo-2.3":"MiniMax-Hailuo-2.3-Fast"}`,
		}).Error)
	require.NoError(t, model.DB.Where("channel_id = ?", fixture.channel.Id).Delete(&model.Ability{}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: service.GroupDefault, Model: hailuoTestModel,
		ChannelId: fixture.channel.Id, Enabled: true, Weight: 1}).Error)
	require.NoError(t, service.InitAbilityCache())
	keys := []string{setting.ModelBillingModeOption, setting.PerCallModelPriceOption,
		setting.ModelRatioOption, setting.CompletionRatioOption, setting.GroupRatioOption,
		setting.GroupGroupRatioOption}
	previous := setting.GetOptions(keys...)
	t.Cleanup(func() { _ = setting.UpdateOptions(previous) })
	price, ratio := `{"MiniMax-Hailuo-2.3":0.004}`, `{}`
	if !fixedPrice {
		price, ratio = `{}`, `{"MiniMax-Hailuo-2.3":2}`
	}
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"MiniMax-Hailuo-2.3":"reference"}`,
		setting.PerCallModelPriceOption: price,
		setting.ModelRatioOption:        ratio,
		setting.CompletionRatioOption:   `{"MiniMax-Hailuo-2.3":99}`,
		setting.GroupRatioOption:        `{"default":1}`,
		setting.GroupGroupRatioOption:   `{}`,
	}))
	return fixture
}

func hailuoLifecycleContext(t *testing.T, fixture relayAccountingFixture, method, path, body, taskID string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if taskID != "" {
		c.Params = gin.Params{{Key: "task_id", Value: taskID}}
	}
	common.SetUserId(c, fixture.user.Id)
	common.SetUsername(c, fixture.user.Username)
	common.SetUserGroup(c, service.GroupDefault)
	middleware.SetupRelayTokenContext(c, &fixture.token)
	middleware.SetRelayGroupPolicy(c, service.RelayGroupPolicy{Groups: []string{service.GroupDefault}})
	return c, recorder
}

func installHailuoProvider(t *testing.T, provider hailuoProvider) {
	t.Helper()
	previous := newHailuoProvider
	newHailuoProvider = func() hailuoProvider { return provider }
	t.Cleanup(func() { newHailuoProvider = previous })
}

func decodeHailuoPublicID(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response hailuoVideoResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.NoError(t, validateHailuoTaskPublicID(response.ID))
	return response.ID
}

func forceHailuoRecoveryDue(t *testing.T, taskID string) {
	t.Helper()
	require.NoError(t, model.DB.Model(&model.TaskOperation{}).Where("task_id = ?", taskID).
		Updates(map[string]any{"next_attempt_at": 0, "lease_owner": "", "lease_expires_at": 0}).Error)
}

func TestHailuoGenericVideoSubmitFetchAndEncryptedReservationLifecycle(t *testing.T) {
	fixture := configureHailuoLifecycleFixture(t, true)
	var submitCalls, fetchCalls atomic.Int32
	installHailuoProvider(t, &fakeHailuoProvider{
		submit: func(_ context.Context, baseURL, key string, prepared *hailuo.PreparedRequest) (*hailuo.Task, []byte, error) {
			submitCalls.Add(1)
			assert.Equal(t, "https://hailuo.example", baseURL)
			assert.Equal(t, "upstream-hailuo-key", key)
			assert.Equal(t, "MiniMax-Hailuo-2.3-Fast", prepared.UpstreamModel)
			assert.Equal(t, hailuo.ActionGenerate, prepared.Action)
			assert.True(t, prepared.HasInputReference)
			var wire map[string]any
			require.NoError(t, json.Unmarshal(prepared.Body, &wire))
			assert.Equal(t, "MiniMax-Hailuo-2.3-Fast", wire["model"])
			assert.Equal(t, float64(10), wire["duration"])
			assert.Equal(t, "1080P", wire["resolution"])

			var task model.Task
			require.NoError(t, model.DB.Where("platform = ?", hailuoTaskPlatform).First(&task).Error)
			assert.Equal(t, model.TaskStatusNotStart, task.Status)
			var operation model.TaskOperation
			require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
			assert.Equal(t, model.TaskOperationDispatching, operation.State)
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reservation.Status,
				"quota must be reserved and dispatch-marked before provider I/O")
			return &hailuo.Task{ProviderTaskID: "provider-hailuo-1", Status: hailuo.StatusSubmitted}, nil, nil
		},
		fetch: func(context.Context, string, string, string) (*hailuo.Task, []byte, error) {
			fetchCalls.Add(1)
			return nil, nil, errors.New("local fetch contacted provider")
		},
	})
	c, recorder := hailuoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"MiniMax-Hailuo-2.3","prompt":"mapped request","duration":10,"size":"1920x1080","image":"https://assets.example/frame.png"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeHailuoPublicID(t, recorder)
	assert.NotContains(t, recorder.Body.String(), "provider-hailuo-1")
	assert.NotContains(t, recorder.Body.String(), "upstream-hailuo-key")

	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, hailuoTaskPlatform, task.Platform)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
	assert.Equal(t, 2_000, task.Quota)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, 2_000, reservation.ActualQuota)
	assert.NotContains(t, task.PrivateData, "upstream-hailuo-key")
	assert.NotContains(t, task.PrivateData, "provider-hailuo-1")
	assert.NotContains(t, operation.EncryptedProviderTaskID, "provider-hailuo-1")
	privateData, err := decodeHailuoTaskPrivateData(task.PrivateData)
	require.NoError(t, err)
	assert.Equal(t, operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID)
	decryptedID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		hailuoProviderTaskBinding(taskID, operation.ReservationID, task.UserId, task.ChannelId))
	require.NoError(t, err)
	assert.Equal(t, "provider-hailuo-1", decryptedID)

	for _, route := range []string{"/v1/videos/", "/v1/video/generations/"} {
		c, recorder = hailuoLifecycleContext(t, fixture, http.MethodGet, route+taskID, "", taskID)
		RelayVideoTaskFetch(c)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), taskID)
		assert.NotContains(t, recorder.Body.String(), "provider-hailuo-1")
		assert.NotContains(t, recorder.Body.String(), "upstream-hailuo-key")
	}
	assert.Equal(t, int32(1), submitCalls.Load())
	assert.Zero(t, fetchCalls.Load(), "user fetch is local and scheduler-owned polling is separate")

	c, recorder = hailuoLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID, "", taskID)
	common.SetUserId(c, fixture.user.Id+1)
	RelayVideoTaskFetch(c)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestHailuoRecoveryPollUsesSnapshotsAndContentIsOwnerScoped(t *testing.T) {
	fixture := configureHailuoLifecycleFixture(t, false)
	var fetchCalls, contentCalls atomic.Int32
	installHailuoProvider(t, &fakeHailuoProvider{
		submit: func(context.Context, string, string, *hailuo.PreparedRequest) (*hailuo.Task, []byte, error) {
			return &hailuo.Task{ProviderTaskID: "provider-content", Status: hailuo.StatusSubmitted}, nil, nil
		},
		fetch: func(_ context.Context, baseURL, key, id string) (*hailuo.Task, []byte, error) {
			fetchCalls.Add(1)
			assert.Equal(t, "https://hailuo.example", baseURL)
			assert.Equal(t, "upstream-hailuo-key", key)
			assert.Equal(t, "provider-content", id)
			return &hailuo.Task{ProviderTaskID: id, Status: hailuo.StatusSucceeded,
				ResultURL: "https://cdn.example/hailuo.mp4", VideoWidth: 1920, VideoHeight: 1080}, nil, nil
		},
	})
	previousContentClient := newHailuoContentHTTPClient
	newHailuoContentHTTPClient = func() *http.Client {
		return &http.Client{Transport: hailuoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			contentCalls.Add(1)
			assert.Equal(t, "https://cdn.example/hailuo.mp4", request.URL.String())
			assert.Empty(t, request.Header.Get("Authorization"), "provider credentials must not reach result hosts")
			return &http.Response{StatusCode: http.StatusOK,
				Header: http.Header{"Content-Type": []string{"video/mp4"}},
				Body:   io.NopCloser(strings.NewReader("hailuo-video")), Request: request}, nil
		})}
	}
	t.Cleanup(func() { newHailuoContentHTTPClient = previousContentClient })
	c, recorder := hailuoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"MiniMax-Hailuo-2.3","prompt":"content"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeHailuoPublicID(t, recorder)

	// Mutable channel and pricing settings cannot redirect or rebill recovery.
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Updates(map[string]any{"base_url": "https://changed.example", "key": "changed-key"}).Error)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelRatioOption: `{"MiniMax-Hailuo-2.3":99}`,
		setting.GroupRatioOption: `{"default":9}`,
	}))
	forceHailuoRecoveryDue(t, taskID)
	require.NoError(t, reconcileAsyncHailuoTasks(context.Background()))
	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.Equal(t, model.TaskOperationTerminal, operation.State)
	assert.Equal(t, common.QuotaPerUnit, task.Quota)
	assert.Equal(t, common.QuotaPerUnit, reservation.ActualQuota)
	assert.Contains(t, task.Data, "https://cdn.example/hailuo.mp4")
	require.NoError(t, reconcileAsyncHailuoTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())

	c, recorder = hailuoLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID+"/content", "", taskID)
	VideoProxy(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "hailuo-video", recorder.Body.String())
	assert.Equal(t, "video/mp4", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))

	c, recorder = hailuoLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID+"/content", "", taskID)
	common.SetUserId(c, fixture.user.Id+1)
	VideoProxy(c)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, int32(1), contentCalls.Load())
}

func TestHailuoDefinitiveRejectionRefundsAndAmbiguityNeverReplays(t *testing.T) {
	t.Run("definitive HTTP rejection", func(t *testing.T) {
		fixture := configureHailuoLifecycleFixture(t, true)
		var submitCalls atomic.Int32
		installHailuoProvider(t, &fakeHailuoProvider{
			submit: func(context.Context, string, string, *hailuo.PreparedRequest) (*hailuo.Task, []byte, error) {
				submitCalls.Add(1)
				return nil, nil, &hailuo.RequestError{Err: &relaycommon.UpstreamError{
					StatusCode: http.StatusBadRequest, Body: `{"message":"upstream-hailuo-key denied"}`,
				}, Dispatched: true}
			},
			fetch: func(context.Context, string, string, string) (*hailuo.Task, []byte, error) {
				return nil, nil, errors.New("unexpected fetch")
			},
		})
		c, recorder := hailuoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
			`{"model":"MiniMax-Hailuo-2.3","prompt":"reject"}`, "")
		RelayVideoTask(c)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.NotContains(t, recorder.Body.String(), "upstream-hailuo-key")
		assert.Equal(t, int32(1), submitCalls.Load())
		assertHailuoRefunded(t, fixture)
	})

	t.Run("authoritative business rejection", func(t *testing.T) {
		fixture := configureHailuoLifecycleFixture(t, true)
		installHailuoProvider(t, &fakeHailuoProvider{
			submit: func(context.Context, string, string, *hailuo.PreparedRequest) (*hailuo.Task, []byte, error) {
				return &hailuo.Task{Status: hailuo.StatusFailed, ErrorCode: "1004",
					ErrorMessage: "upstream-hailuo-key rejected"}, nil, nil
			},
			fetch: func(context.Context, string, string, string) (*hailuo.Task, []byte, error) {
				return nil, nil, errors.New("unexpected fetch")
			},
		})
		c, recorder := hailuoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
			`{"model":"MiniMax-Hailuo-2.3","prompt":"business reject"}`, "")
		RelayVideoTask(c)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.NotContains(t, recorder.Body.String(), "upstream-hailuo-key")
		assertHailuoRefunded(t, fixture)
	})

	t.Run("ambiguous dispatch retained for explicit review", func(t *testing.T) {
		fixture := configureHailuoLifecycleFixture(t, true)
		var submitCalls, fetchCalls atomic.Int32
		installHailuoProvider(t, &fakeHailuoProvider{
			submit: func(context.Context, string, string, *hailuo.PreparedRequest) (*hailuo.Task, []byte, error) {
				submitCalls.Add(1)
				return nil, nil, &hailuo.RequestError{Err: errors.New("connection reset after write"), Dispatched: true}
			},
			fetch: func(context.Context, string, string, string) (*hailuo.Task, []byte, error) {
				fetchCalls.Add(1)
				return nil, nil, errors.New("must not poll without provider id")
			},
		})
		c, recorder := hailuoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
			`{"model":"MiniMax-Hailuo-2.3","prompt":"ambiguous"}`, "")
		RelayVideoTask(c)
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
		require.NoError(t, reconcileAsyncHailuoTasks(context.Background()))
		assert.Equal(t, int32(1), submitCalls.Load(), "ambiguous work is never resubmitted")
		assert.Zero(t, fetchCalls.Load())

		review, err := service.GetManualReviewRelayQuotaReservation(reservation.ReservationID)
		require.NoError(t, err)
		assert.Equal(t, service.RelayQuotaReviewKindHailuoTask, review.ReviewKind)
		assert.Equal(t, hailuoTaskPlatform, review.TaskPlatform)
		assert.True(t, review.ResolutionRequired)
		assert.False(t, review.ProviderTaskIDPresent)
		resolved, err := ResolveHailuoRelayQuotaReservationReview(reservation.ReservationID, 9101,
			service.RelayQuotaReviewResolutionRefund)
		require.NoError(t, err)
		assert.True(t, resolved.Changed)
		assert.Equal(t, model.RelayQuotaReservationStatusRefunded, resolved.ReservationStatus)
		replay, err := ResolveHailuoRelayQuotaReservationReview(reservation.ReservationID, 9102,
			service.RelayQuotaReviewResolutionRefund)
		require.NoError(t, err)
		assert.False(t, replay.Changed)
		assert.Equal(t, resolved.AuditEventID, replay.AuditEventID)
	})
}

func assertHailuoRefunded(t *testing.T, fixture relayAccountingFixture) {
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

func TestHailuoTerminalFailureReversesSettledChargeExactlyOnce(t *testing.T) {
	fixture := configureHailuoLifecycleFixture(t, true)
	var fetchCalls atomic.Int32
	installHailuoProvider(t, &fakeHailuoProvider{
		submit: func(context.Context, string, string, *hailuo.PreparedRequest) (*hailuo.Task, []byte, error) {
			return &hailuo.Task{ProviderTaskID: "provider-failure", Status: hailuo.StatusSubmitted}, nil, nil
		},
		fetch: func(context.Context, string, string, string) (*hailuo.Task, []byte, error) {
			fetchCalls.Add(1)
			return &hailuo.Task{ProviderTaskID: "provider-failure", Status: hailuo.StatusFailed,
				ErrorCode: "1026", ErrorMessage: "content policy rejected"}, nil, nil
		},
	})
	c, recorder := hailuoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"MiniMax-Hailuo-2.3","prompt":"fail later"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeHailuoPublicID(t, recorder)
	forceHailuoRecoveryDue(t, taskID)
	require.NoError(t, reconcileAsyncHailuoTasks(context.Background()))
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
	assert.Equal(t, "Hailuo task failed", task.FailReason)
	assert.NotContains(t, task.Data, "content policy rejected")
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationReversed, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusReversed, reservation.Status)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
	var refundAudit int64
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", "hailuo-refund:"+reservation.ReservationID).Count(&refundAudit).Error)
	assert.EqualValues(t, 1, refundAudit)
	require.NoError(t, reconcileAsyncHailuoTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())
}

func createPreparedHailuoTaskForRecovery(t *testing.T, fixture relayAccountingFixture) (*model.Task, *service.RelayQuotaReservation) {
	t.Helper()
	pricing, enabled, err := service.ResolveReferenceAsyncTaskBillingPlan(hailuoTestModel, "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	quota, err := pricing.PreConsumeQuota()
	require.NoError(t, err)
	taskID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	properties, err := marshalHailuoTaskProperties(hailuoTaskProperties{
		Version: hailuoTaskMetadataVersion, Family: "hailuo", Input: "restart",
		OriginModelName: hailuoTestModel, UpstreamModelName: "MiniMax-Hailuo-2.3-Fast",
		Action: hailuo.ActionGenerate, Duration: hailuo.DefaultDuration,
		Resolution: hailuo.Resolution768P, Pricing: pricing,
	})
	require.NoError(t, err)
	now := common.NowTimestamp()
	task := &model.Task{CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: hailuoTaskPlatform,
		UserId: fixture.user.Id, Group: "default", ChannelId: fixture.channel.Id, Quota: quota,
		Action: string(hailuo.ActionGenerate), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: properties, Data: "null"}
	baseURL := "https://hailuo.example"
	encryptedKey, err := asyncTaskEncryptBound("upstream-hailuo-key",
		hailuoChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, baseURL))
	require.NoError(t, err)
	privateData := hailuoTaskPrivateData{Version: hailuoTaskMetadataVersion, ChannelBaseURL: baseURL,
		EncryptedChannelKey: encryptedKey, Pricing: pricing}
	reservation, err := createHailuoReservedTask(task, &fixture.token, &privateData)
	require.NoError(t, err)
	return task, reservation
}

func TestHailuoAcceptedJournalPromotesAcrossRestart(t *testing.T) {
	fixture := configureHailuoLifecycleFixture(t, true)
	task, reservation := createPreparedHailuoTaskForRecovery(t, fixture)
	require.NoError(t, markHailuoTaskDispatching(task, reservation))
	require.NoError(t, persistAcceptedHailuoRecoveryJournal(task, reservation.ReservationID(),
		"provider-restart", hailuo.ActionGenerate))
	path, err := hailuoRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)
	disk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(disk), "provider-restart")
	assert.NotContains(t, string(disk), "upstream-hailuo-key")
	require.NoError(t, PromoteHailuoTaskRecoveryJournalsContext(context.Background()))
	_, err = os.Stat(path)
	assert.True(t, errors.Is(err, os.ErrNotExist))
	var operation model.TaskOperation
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.True(t, operation.SettlementPending)
	assert.NotContains(t, operation.EncryptedProviderTaskID, "provider-restart")

	installHailuoProvider(t, &fakeHailuoProvider{
		submit: func(context.Context, string, string, *hailuo.PreparedRequest) (*hailuo.Task, []byte, error) {
			return nil, nil, errors.New("unexpected submit")
		},
		fetch: func(_ context.Context, _, _, id string) (*hailuo.Task, []byte, error) {
			return &hailuo.Task{ProviderTaskID: id, Status: hailuo.StatusSucceeded,
				ResultURL: "https://cdn.example/restart.mp4"}, nil, nil
		},
	})
	forceHailuoRecoveryDue(t, task.TaskID)
	require.NoError(t, reconcileAsyncHailuoTasks(context.Background()))
	forceHailuoRecoveryDue(t, task.TaskID)
	require.NoError(t, reconcileAsyncHailuoTasks(context.Background()))
	require.NoError(t, model.DB.Where("id = ?", task.ID).First(task).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
}

func TestRetryHailuoPollReviewIsAuditedAndDoesNotRebill(t *testing.T) {
	fixture := configureHailuoLifecycleFixture(t, true)
	installHailuoProvider(t, &fakeHailuoProvider{
		submit: func(context.Context, string, string, *hailuo.PreparedRequest) (*hailuo.Task, []byte, error) {
			return &hailuo.Task{ProviderTaskID: "provider-manual", Status: hailuo.StatusSubmitted}, nil, nil
		},
		fetch: func(context.Context, string, string, string) (*hailuo.Task, []byte, error) {
			return nil, nil, errors.New("poll must not run during review retry")
		},
	})
	c, recorder := hailuoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"MiniMax-Hailuo-2.3","prompt":"manual poll"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeHailuoPublicID(t, recorder)
	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	oldCreatedAt := now - int64(8*24*time.Hour/time.Second)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status": model.TaskStatusUnknown, "fail_reason": hailuoPollingManualReviewReason,
		"finish_time": now, "updated_at": now,
	}).Error)
	require.NoError(t, model.DB.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
		"state": model.TaskOperationManualReview, "settlement_pending": false,
		"attempts": hailuoOperationMaxAttempts, "next_attempt_at": 0,
		"lease_owner": "", "lease_expires_at": 0, "last_error": hailuoPollingManualReviewReason,
		"created_at": oldCreatedAt, "updated_at": now, "completed_at": now,
	}).Error)
	review, err := service.GetManualReviewRelayQuotaReservation(reservation.ReservationID)
	require.NoError(t, err)
	assert.Equal(t, service.RelayQuotaReviewKindHailuoTask, review.ReviewKind)
	assert.True(t, review.ProviderTaskIDPresent)
	assert.True(t, review.Retryable)

	var beforeUser model.User
	var beforeToken model.Token
	var beforeChannel model.Channel
	require.NoError(t, model.DB.First(&beforeUser, fixture.user.Id).Error)
	require.NoError(t, model.DB.Unscoped().First(&beforeToken, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&beforeChannel, fixture.channel.Id).Error)
	result, err := RetryHailuoTaskManualReview(reservation.ReservationID, 8401)
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Equal(t, taskID, result.TaskID)
	assert.Equal(t, model.TaskOperationSubmitted, result.OperationState)
	replay, err := RetryHailuoTaskManualReview(reservation.ReservationID, 8402)
	require.NoError(t, err)
	assert.False(t, replay.Changed)
	assert.Equal(t, result.AuditEventID, replay.AuditEventID)

	var afterUser model.User
	var afterToken model.Token
	var afterChannel model.Channel
	require.NoError(t, model.DB.First(&afterUser, fixture.user.Id).Error)
	require.NoError(t, model.DB.Unscoped().First(&afterToken, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&afterChannel, fixture.channel.Id).Error)
	assert.Equal(t, beforeUser.Quota, afterUser.Quota)
	assert.Equal(t, beforeUser.UsedQuota, afterUser.UsedQuota)
	assert.Equal(t, beforeToken.RemainQuota, afterToken.RemainQuota)
	assert.Equal(t, beforeToken.UsedQuota, afterToken.UsedQuota)
	assert.Equal(t, beforeChannel.UsedQuota, afterChannel.UsedQuota)
}

func TestHailuoRecoveryLeasePreventsConcurrentPolls(t *testing.T) {
	fixture := configureHailuoLifecycleFixture(t, true)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var fetchCalls atomic.Int32
	installHailuoProvider(t, &fakeHailuoProvider{
		submit: func(context.Context, string, string, *hailuo.PreparedRequest) (*hailuo.Task, []byte, error) {
			return &hailuo.Task{ProviderTaskID: "provider-concurrent", Status: hailuo.StatusSubmitted}, nil, nil
		},
		fetch: func(_ context.Context, _, _, id string) (*hailuo.Task, []byte, error) {
			fetchCalls.Add(1)
			started <- struct{}{}
			<-release
			return &hailuo.Task{ProviderTaskID: id, Status: hailuo.StatusProcessing}, nil, nil
		},
	})
	c, recorder := hailuoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"MiniMax-Hailuo-2.3","prompt":"concurrent"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeHailuoPublicID(t, recorder)
	forceHailuoRecoveryDue(t, taskID)
	firstDone := make(chan error, 1)
	go func() { firstDone <- reconcileAsyncHailuoTasks(context.Background()) }()
	<-started
	require.NoError(t, reconcileAsyncHailuoTasks(context.Background()))
	close(release)
	require.NoError(t, <-firstDone)
	assert.Equal(t, int32(1), fetchCalls.Load())
}

func TestHailuoPlatformIsDistinctFromSoraAndOtherTaskFamilies(t *testing.T) {
	assert.Equal(t, "35", model.TaskOperationPlatformHailuo)
	assert.True(t, model.IsHailuoTaskOperationPlatform(model.TaskOperationPlatformHailuo))
	assert.False(t, model.IsOpenAIVideoTaskOperationPlatform(model.TaskOperationPlatformHailuo))
	assert.NotContains(t, model.OpenAIVideoTaskOperationPlatforms(), model.TaskOperationPlatformHailuo)
	assert.False(t, model.IsViduTaskOperationPlatform(model.TaskOperationPlatformHailuo))
}
