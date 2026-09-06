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
	aliWan "github.com/tokenrouter/tokenrouter/relay/channel/task/ali"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

type fakeAliWanProvider struct {
	submit func(context.Context, string, string, *aliWan.PreparedRequest) (*aliWan.Task, []byte, error)
	fetch  func(context.Context, string, string, string) (*aliWan.Task, []byte, error)
}

func (provider *fakeAliWanProvider) Submit(ctx context.Context, baseURL, key string,
	prepared *aliWan.PreparedRequest) (*aliWan.Task, []byte, error) {
	return provider.submit(ctx, baseURL, key, prepared)
}

func (provider *fakeAliWanProvider) Fetch(ctx context.Context, baseURL, key, id string) (*aliWan.Task, []byte, error) {
	return provider.fetch(ctx, baseURL, key, id)
}

type aliWanRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn aliWanRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

const aliWanTestModel = "wan2.7-t2v"

func configureAliWanLifecycleFixture(t *testing.T, fixedPrice bool) relayAccountingFixture {
	t.Helper()
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "ali-wan-test=0123456789abcdef0123456789abcdef")
	t.Setenv("ALI_VIDEO_TASK_RECOVERY_DIR", t.TempDir())
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
			"type": int(constant.ChannelTypeAli), "key": "upstream-ali-wan-key",
			"base_url": "https://dashscope.example", "models": strings.Join(aliWan.ModelList(), ","),
			"model_mapping": `{}`,
		}).Error)
	require.NoError(t, model.DB.Where("channel_id = ?", fixture.channel.Id).Delete(&model.Ability{}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: service.GroupDefault, Model: aliWanTestModel,
		ChannelId: fixture.channel.Id, Enabled: true, Weight: 1}).Error)
	require.NoError(t, service.InitAbilityCache())
	keys := []string{setting.ModelBillingModeOption, setting.PerCallModelPriceOption,
		setting.ModelRatioOption, setting.CompletionRatioOption, setting.GroupRatioOption,
		setting.GroupGroupRatioOption}
	previous := setting.GetOptions(keys...)
	t.Cleanup(func() { _ = setting.UpdateOptions(previous) })
	price, ratio := `{"wan2.7-t2v":0.004}`, `{}`
	if !fixedPrice {
		price, ratio = `{}`, `{"wan2.7-t2v":0.2}`
	}
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"wan2.7-t2v":"reference"}`,
		setting.PerCallModelPriceOption: price,
		setting.ModelRatioOption:        ratio,
		setting.CompletionRatioOption:   `{"wan2.7-t2v":99}`,
		setting.GroupRatioOption:        `{"default":1}`,
		setting.GroupGroupRatioOption:   `{}`,
	}))
	return fixture
}

func aliWanLifecycleContext(t *testing.T, fixture relayAccountingFixture, method, path, body, taskID string) (*gin.Context, *httptest.ResponseRecorder) {
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

func installAliWanProvider(t *testing.T, provider aliWanProvider) {
	t.Helper()
	previous := newAliWanProvider
	newAliWanProvider = func() aliWanProvider { return provider }
	t.Cleanup(func() { newAliWanProvider = previous })
}

func decodeAliWanPublicID(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response aliWanVideoResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.NoError(t, validateAliWanTaskPublicID(response.ID))
	return response.ID
}

func forceAliWanRecoveryDue(t *testing.T, taskID string) {
	t.Helper()
	require.NoError(t, model.DB.Model(&model.TaskOperation{}).Where("task_id = ?", taskID).
		Updates(map[string]any{"next_attempt_at": 0, "lease_owner": "", "lease_expires_at": 0}).Error)
}

func TestAliWanPricingSnapshotFreezesDurationAndResolutionMultiplier(t *testing.T) {
	plan := service.ReferenceAsyncTaskBillingPlan{
		Version: 1, ModelName: "wan2.2-i2v-flash", GroupRatio: "1",
		UseFixedPrice: true, FixedPrice: "0.004",
	}
	pricing, err := newAliWanPricingSnapshot(plan, "wan2.2-i2v-flash", 5, "720P")
	require.NoError(t, err)
	assert.Equal(t, "2", pricing.ResolutionMultiplier)
	quota, err := pricing.quota("wan2.2-i2v-flash", "720P")
	require.NoError(t, err)
	assert.Equal(t, 20_000, quota)

	tampered := pricing
	tampered.Duration = 11
	assert.Error(t, tampered.validate("wan2.2-i2v-flash", "720P"))
	tampered = pricing
	tampered.ResolutionMultiplier = "3"
	assert.Error(t, tampered.validate("wan2.2-i2v-flash", "720P"))

	properties := aliWanTaskProperties{
		Version: aliWanTaskMetadataVersion, Family: "ali_wan",
		OriginModelName: "wan2.2-i2v-flash", UpstreamModelName: "wan2.2-i2v-flash",
		Action: aliWanActionGenerate, HasInputReference: true,
		Duration: 5, Resolution: "720P", Pricing: pricing,
	}
	_, err = marshalAliWanTaskProperties(properties)
	require.NoError(t, err, "image-to-video permits an omitted prompt")
	properties.Action = aliWanActionTextGenerate
	properties.HasInputReference = false
	_, err = marshalAliWanTaskProperties(properties)
	assert.Error(t, err, "text-to-video requires a prompt")
}

func TestAliWanGenericVideoSubmitFetchAndEncryptedReservationLifecycle(t *testing.T) {
	fixture := configureAliWanLifecycleFixture(t, true)
	var submitCalls, fetchCalls atomic.Int32
	installAliWanProvider(t, &fakeAliWanProvider{
		submit: func(_ context.Context, baseURL, key string, prepared *aliWan.PreparedRequest) (*aliWan.Task, []byte, error) {
			submitCalls.Add(1)
			assert.Equal(t, "https://dashscope.example", baseURL)
			assert.Equal(t, "upstream-ali-wan-key", key)
			assert.Equal(t, "wan2.7-t2v", prepared.UpstreamModel)
			assert.False(t, prepared.HasInputReference)
			var wire map[string]any
			require.NoError(t, json.Unmarshal(prepared.Body, &wire))
			assert.Equal(t, "wan2.7-t2v", wire["model"])
			parameters := wire["parameters"].(map[string]any)
			assert.Equal(t, float64(10), parameters["duration"])
			assert.Equal(t, "1920*1080", parameters["size"])

			var task model.Task
			require.NoError(t, model.DB.Where("platform = ?", aliWanTaskPlatform).First(&task).Error)
			assert.Equal(t, model.TaskStatusNotStart, task.Status)
			var operation model.TaskOperation
			require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
			assert.Equal(t, model.TaskOperationDispatching, operation.State)
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reservation.Status,
				"quota must be reserved and dispatch-marked before provider I/O")
			return &aliWan.Task{ProviderTaskID: "provider-ali-wan-1", Status: aliWan.StatusSubmitted}, nil, nil
		},
		fetch: func(context.Context, string, string, string) (*aliWan.Task, []byte, error) {
			fetchCalls.Add(1)
			return nil, nil, errors.New("local fetch contacted provider")
		},
	})
	c, recorder := aliWanLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"wan2.7-t2v","prompt":"mapped request","duration":10,"size":"1920*1080"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeAliWanPublicID(t, recorder)
	assert.NotContains(t, recorder.Body.String(), "provider-ali-wan-1")
	assert.NotContains(t, recorder.Body.String(), "upstream-ali-wan-key")

	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, aliWanTaskPlatform, task.Platform)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
	assert.Equal(t, 20_000, task.Quota)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, 20_000, reservation.ActualQuota)
	assert.NotContains(t, task.PrivateData, "upstream-ali-wan-key")
	assert.NotContains(t, task.PrivateData, "provider-ali-wan-1")
	assert.NotContains(t, operation.EncryptedProviderTaskID, "provider-ali-wan-1")
	privateData, err := decodeAliWanTaskPrivateData(task.PrivateData)
	require.NoError(t, err)
	assert.Equal(t, operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID)
	decryptedID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		aliWanProviderTaskBinding(taskID, operation.ReservationID, task.UserId, task.ChannelId))
	require.NoError(t, err)
	assert.Equal(t, "provider-ali-wan-1", decryptedID)

	for _, route := range []string{"/v1/videos/", "/v1/video/generations/"} {
		c, recorder = aliWanLifecycleContext(t, fixture, http.MethodGet, route+taskID, "", taskID)
		RelayVideoTaskFetch(c)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), taskID)
		assert.NotContains(t, recorder.Body.String(), "provider-ali-wan-1")
		assert.NotContains(t, recorder.Body.String(), "upstream-ali-wan-key")
	}
	assert.Equal(t, int32(1), submitCalls.Load())
	assert.Zero(t, fetchCalls.Load(), "user fetch is local and scheduler-owned polling is separate")

	c, recorder = aliWanLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID, "", taskID)
	common.SetUserId(c, fixture.user.Id+1)
	RelayVideoTaskFetch(c)
	assert.Equal(t, http.StatusNotFound, recorder.Code)

	c, recorder = aliWanLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID, "", taskID)
	middleware.SetRelayGroupPolicy(c, service.RelayGroupPolicy{Groups: []string{"other-group"}})
	RelayVideoTaskFetch(c)
	assert.Equal(t, http.StatusForbidden, recorder.Code)

	c, recorder = aliWanLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID, "", taskID)
	restricted := fixture.token
	restricted.ModelLimitsEnabled = true
	restricted.ModelLimits = "some-other-model"
	middleware.SetupRelayTokenContext(c, &restricted)
	RelayVideoTaskFetch(c)
	assert.Equal(t, http.StatusForbidden, recorder.Code)

	shadow := task
	shadow.ID = 0
	shadow.Platform = model.TaskOperationPlatformSora
	shadow.Properties = `{}`
	shadow.PrivateData = ""
	require.NoError(t, model.DB.Create(&shadow).Error)
	c, recorder = aliWanLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID, "", taskID)
	RelayVideoTaskFetch(c)
	assert.Equal(t, http.StatusOK, recorder.Code, "wrong-platform shadow rows must not affect Alibaba lookup")
}

func TestAliWanRecoveryPollUsesSnapshotsAndContentIsOwnerScoped(t *testing.T) {
	fixture := configureAliWanLifecycleFixture(t, false)
	var fetchCalls, contentCalls atomic.Int32
	installAliWanProvider(t, &fakeAliWanProvider{
		submit: func(context.Context, string, string, *aliWan.PreparedRequest) (*aliWan.Task, []byte, error) {
			return &aliWan.Task{ProviderTaskID: "provider-content", Status: aliWan.StatusSubmitted}, nil, nil
		},
		fetch: func(_ context.Context, baseURL, key, id string) (*aliWan.Task, []byte, error) {
			fetchCalls.Add(1)
			assert.Equal(t, "https://dashscope.example", baseURL)
			assert.Equal(t, "upstream-ali-wan-key", key)
			assert.Equal(t, "provider-content", id)
			return &aliWan.Task{ProviderTaskID: id, Status: aliWan.StatusSucceeded,
				ResultURL: "https://cdn.example/ali-wan.mp4"}, nil, nil
		},
	})
	previousContentClient := newAliWanContentHTTPClient
	newAliWanContentHTTPClient = func() *http.Client {
		return &http.Client{Transport: aliWanRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			contentCalls.Add(1)
			assert.Equal(t, "https://cdn.example/ali-wan.mp4", request.URL.String())
			assert.Empty(t, request.Header.Get("Authorization"), "provider credentials must not reach result hosts")
			return &http.Response{StatusCode: http.StatusOK,
				Header: http.Header{"Content-Type": []string{"video/mp4"}},
				Body:   io.NopCloser(strings.NewReader("ali-wan-video")), Request: request}, nil
		})}
	}
	t.Cleanup(func() { newAliWanContentHTTPClient = previousContentClient })
	c, recorder := aliWanLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"wan2.7-t2v","prompt":"content"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeAliWanPublicID(t, recorder)

	// Mutable channel and pricing settings cannot redirect or rebill recovery.
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Updates(map[string]any{"base_url": "https://changed.example", "key": "changed-key"}).Error)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelRatioOption: `{"wan2.7-t2v":99}`,
		setting.GroupRatioOption: `{"default":9}`,
	}))
	forceAliWanRecoveryDue(t, taskID)
	require.NoError(t, reconcileAsyncAliWanTasks(context.Background()))
	var task model.Task
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.Equal(t, model.TaskOperationTerminal, operation.State)
	assert.Equal(t, common.QuotaPerUnit/2, task.Quota)
	assert.Equal(t, common.QuotaPerUnit/2, reservation.ActualQuota)
	assert.Contains(t, task.Data, "https://cdn.example/ali-wan.mp4")
	require.NoError(t, reconcileAsyncAliWanTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())

	c, recorder = aliWanLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID+"/content", "", taskID)
	VideoProxy(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "ali-wan-video", recorder.Body.String())
	assert.Equal(t, "video/mp4", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))

	newAliWanContentHTTPClient = func() *http.Client {
		return &http.Client{Transport: aliWanRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			contentCalls.Add(1)
			return &http.Response{StatusCode: http.StatusOK, ContentLength: (512 << 20) + 1,
				Header: http.Header{"Content-Type": []string{"video/mp4"}},
				Body:   io.NopCloser(strings.NewReader("must-not-stream")), Request: request}, nil
		})}
	}
	c, recorder = aliWanLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID+"/content", "", taskID)
	VideoProxy(c)
	assert.Equal(t, http.StatusBadGateway, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "must-not-stream")

	newAliWanContentHTTPClient = func() *http.Client {
		return &http.Client{Transport: aliWanRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			contentCalls.Add(1)
			return &http.Response{StatusCode: http.StatusOK,
				Header: http.Header{"Content-Type": []string{"text/html"}},
				Body:   io.NopCloser(strings.NewReader("<script>unsafe</script>")), Request: request}, nil
		})}
	}
	c, recorder = aliWanLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID+"/content", "", taskID)
	VideoProxy(c)
	assert.Equal(t, http.StatusBadGateway, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "<script>")

	c, recorder = aliWanLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID+"/content", "", taskID)
	common.SetUserId(c, fixture.user.Id+1)
	VideoProxy(c)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, int32(3), contentCalls.Load())
}

func TestAliWanDefinitiveRejectionRefundsAndAmbiguityNeverReplays(t *testing.T) {
	t.Run("definitive HTTP rejection", func(t *testing.T) {
		fixture := configureAliWanLifecycleFixture(t, true)
		var submitCalls atomic.Int32
		installAliWanProvider(t, &fakeAliWanProvider{
			submit: func(context.Context, string, string, *aliWan.PreparedRequest) (*aliWan.Task, []byte, error) {
				submitCalls.Add(1)
				return nil, nil, &aliWan.RequestError{Err: &relaycommon.UpstreamError{
					StatusCode: http.StatusBadRequest, Body: `{"message":"upstream-ali-wan-key denied"}`,
				}, Dispatched: true}
			},
			fetch: func(context.Context, string, string, string) (*aliWan.Task, []byte, error) {
				return nil, nil, errors.New("unexpected fetch")
			},
		})
		c, recorder := aliWanLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
			`{"model":"wan2.7-t2v","prompt":"reject"}`, "")
		RelayVideoTask(c)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.NotContains(t, recorder.Body.String(), "upstream-ali-wan-key")
		assert.Equal(t, int32(1), submitCalls.Load())
		assertAliWanRefunded(t, fixture)
	})

	t.Run("authoritative business rejection", func(t *testing.T) {
		fixture := configureAliWanLifecycleFixture(t, true)
		installAliWanProvider(t, &fakeAliWanProvider{
			submit: func(context.Context, string, string, *aliWan.PreparedRequest) (*aliWan.Task, []byte, error) {
				return &aliWan.Task{Status: aliWan.StatusFailed, ErrorCode: "1004",
					ErrorMessage: "upstream-ali-wan-key rejected"}, nil, nil
			},
			fetch: func(context.Context, string, string, string) (*aliWan.Task, []byte, error) {
				return nil, nil, errors.New("unexpected fetch")
			},
		})
		c, recorder := aliWanLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
			`{"model":"wan2.7-t2v","prompt":"business reject"}`, "")
		RelayVideoTask(c)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.NotContains(t, recorder.Body.String(), "upstream-ali-wan-key")
		assertAliWanRefunded(t, fixture)
	})

	t.Run("ambiguous dispatch retained for explicit review", func(t *testing.T) {
		fixture := configureAliWanLifecycleFixture(t, true)
		var submitCalls, fetchCalls atomic.Int32
		installAliWanProvider(t, &fakeAliWanProvider{
			submit: func(context.Context, string, string, *aliWan.PreparedRequest) (*aliWan.Task, []byte, error) {
				submitCalls.Add(1)
				return nil, nil, &aliWan.RequestError{Err: errors.New("connection reset after write"), Dispatched: true}
			},
			fetch: func(context.Context, string, string, string) (*aliWan.Task, []byte, error) {
				fetchCalls.Add(1)
				return nil, nil, errors.New("must not poll without provider id")
			},
		})
		c, recorder := aliWanLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
			`{"model":"wan2.7-t2v","prompt":"ambiguous"}`, "")
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
		require.NoError(t, reconcileAsyncAliWanTasks(context.Background()))
		assert.Equal(t, int32(1), submitCalls.Load(), "ambiguous work is never resubmitted")
		assert.Zero(t, fetchCalls.Load())

		review, err := service.GetManualReviewRelayQuotaReservation(reservation.ReservationID)
		require.NoError(t, err)
		assert.Equal(t, service.RelayQuotaReviewKindAliWanTask, review.ReviewKind)
		assert.Equal(t, aliWanTaskPlatform, review.TaskPlatform)
		assert.True(t, review.ResolutionRequired)
		assert.False(t, review.ProviderTaskIDPresent)
		resolved, err := ResolveAliWanRelayQuotaReservationReview(reservation.ReservationID, 9101,
			service.RelayQuotaReviewResolutionRefund)
		require.NoError(t, err)
		assert.True(t, resolved.Changed)
		assert.Equal(t, model.RelayQuotaReservationStatusRefunded, resolved.ReservationStatus)
		replay, err := ResolveAliWanRelayQuotaReservationReview(reservation.ReservationID, 9102,
			service.RelayQuotaReviewResolutionRefund)
		require.NoError(t, err)
		assert.False(t, replay.Changed)
		assert.Equal(t, resolved.AuditEventID, replay.AuditEventID)
	})
}

func assertAliWanRefunded(t *testing.T, fixture relayAccountingFixture) {
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

func TestAliWanTerminalFailureReversesSettledChargeExactlyOnce(t *testing.T) {
	fixture := configureAliWanLifecycleFixture(t, true)
	var fetchCalls atomic.Int32
	installAliWanProvider(t, &fakeAliWanProvider{
		submit: func(context.Context, string, string, *aliWan.PreparedRequest) (*aliWan.Task, []byte, error) {
			return &aliWan.Task{ProviderTaskID: "provider-failure", Status: aliWan.StatusSubmitted}, nil, nil
		},
		fetch: func(context.Context, string, string, string) (*aliWan.Task, []byte, error) {
			fetchCalls.Add(1)
			return &aliWan.Task{ProviderTaskID: "provider-failure", Status: aliWan.StatusFailed,
				ErrorCode: "1026", ErrorMessage: "content policy rejected"}, nil, nil
		},
	})
	c, recorder := aliWanLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"wan2.7-t2v","prompt":"fail later"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeAliWanPublicID(t, recorder)
	forceAliWanRecoveryDue(t, taskID)
	require.NoError(t, reconcileAsyncAliWanTasks(context.Background()))
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
	assert.Equal(t, "Alibaba Wan provider reported task failure", task.FailReason)
	assert.NotContains(t, task.Data, "content policy rejected")
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationReversed, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusReversed, reservation.Status)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
	var refundAudit int64
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", "ali-wan-refund:"+reservation.ReservationID).Count(&refundAudit).Error)
	assert.EqualValues(t, 1, refundAudit)
	require.NoError(t, reconcileAsyncAliWanTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())
}

func createPreparedAliWanTaskForRecovery(t *testing.T, fixture relayAccountingFixture) (*model.Task, *service.RelayQuotaReservation) {
	t.Helper()
	pricing, enabled, err := service.ResolveReferenceAsyncTaskBillingPlan(aliWanTestModel, "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	pricingSnapshot, err := newAliWanPricingSnapshot(pricing, aliWanTestModel, aliWan.DefaultDuration, "1280*720")
	require.NoError(t, err)
	quota, err := pricingSnapshot.quota(aliWanTestModel, "1280*720")
	require.NoError(t, err)
	taskID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	properties, err := marshalAliWanTaskProperties(aliWanTaskProperties{
		Version: aliWanTaskMetadataVersion, Family: "ali_wan", Input: "restart",
		OriginModelName: aliWanTestModel, UpstreamModelName: aliWanTestModel,
		Action: aliWanActionTextGenerate, Duration: aliWan.DefaultDuration,
		Resolution: "1280*720", Pricing: pricingSnapshot,
	})
	require.NoError(t, err)
	now := common.NowTimestamp()
	task := &model.Task{CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: aliWanTaskPlatform,
		UserId: fixture.user.Id, Group: "default", ChannelId: fixture.channel.Id, Quota: quota,
		Action: string(aliWanActionTextGenerate), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: properties, Data: "null"}
	baseURL := "https://dashscope.example"
	encryptedKey, err := asyncTaskEncryptBound("upstream-ali-wan-key",
		aliWanChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, baseURL))
	require.NoError(t, err)
	privateData := aliWanTaskPrivateData{Version: aliWanTaskMetadataVersion, ChannelBaseURL: baseURL,
		EncryptedChannelKey: encryptedKey, Pricing: pricingSnapshot}
	reservation, err := createAliWanReservedTask(task, &fixture.token, &privateData)
	require.NoError(t, err)
	return task, reservation
}

func TestAliWanAcceptedJournalPromotesAcrossRestart(t *testing.T) {
	fixture := configureAliWanLifecycleFixture(t, true)
	task, reservation := createPreparedAliWanTaskForRecovery(t, fixture)
	require.NoError(t, markAliWanTaskDispatching(task, reservation))
	require.NoError(t, persistAcceptedAliWanRecoveryJournal(task, reservation.ReservationID(),
		"provider-restart", aliWanActionTextGenerate))
	path, err := aliWanRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)
	disk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(disk), "provider-restart")
	assert.NotContains(t, string(disk), "upstream-ali-wan-key")
	require.NoError(t, PromoteAliWanTaskRecoveryJournalsContext(context.Background()))
	_, err = os.Stat(path)
	assert.True(t, errors.Is(err, os.ErrNotExist))
	var operation model.TaskOperation
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.True(t, operation.SettlementPending)
	assert.NotContains(t, operation.EncryptedProviderTaskID, "provider-restart")

	installAliWanProvider(t, &fakeAliWanProvider{
		submit: func(context.Context, string, string, *aliWan.PreparedRequest) (*aliWan.Task, []byte, error) {
			return nil, nil, errors.New("unexpected submit")
		},
		fetch: func(_ context.Context, _, _, id string) (*aliWan.Task, []byte, error) {
			return &aliWan.Task{ProviderTaskID: id, Status: aliWan.StatusSucceeded,
				ResultURL: "https://cdn.example/restart.mp4"}, nil, nil
		},
	})
	forceAliWanRecoveryDue(t, task.TaskID)
	require.NoError(t, reconcileAsyncAliWanTasks(context.Background()))
	forceAliWanRecoveryDue(t, task.TaskID)
	require.NoError(t, reconcileAsyncAliWanTasks(context.Background()))
	require.NoError(t, model.DB.Where("id = ?", task.ID).First(task).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
}

func TestRetryAliWanPollReviewIsAuditedAndDoesNotRebill(t *testing.T) {
	fixture := configureAliWanLifecycleFixture(t, true)
	var pollEnabled atomic.Bool
	var submitCalls, fetchCalls atomic.Int32
	installAliWanProvider(t, &fakeAliWanProvider{
		submit: func(context.Context, string, string, *aliWan.PreparedRequest) (*aliWan.Task, []byte, error) {
			submitCalls.Add(1)
			return &aliWan.Task{ProviderTaskID: "provider-manual", Status: aliWan.StatusSubmitted}, nil, nil
		},
		fetch: func(_ context.Context, _, _, providerID string) (*aliWan.Task, []byte, error) {
			fetchCalls.Add(1)
			if !pollEnabled.Load() {
				return nil, nil, errors.New("poll ran before operator retry completed")
			}
			return &aliWan.Task{ProviderTaskID: providerID, Status: aliWan.StatusSucceeded,
				ResultURL: "https://cdn.example/operator-retry.mp4"}, nil, nil
		},
	})
	c, recorder := aliWanLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"wan2.7-t2v","prompt":"manual poll"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeAliWanPublicID(t, recorder)
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
		"status": model.TaskStatusUnknown, "fail_reason": aliWanPollingManualReviewReason,
		"finish_time": now, "updated_at": now,
	}).Error)
	require.NoError(t, model.DB.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
		"state": model.TaskOperationManualReview, "settlement_pending": false,
		"attempts": aliWanOperationMaxAttempts, "next_attempt_at": 0,
		"lease_owner": "", "lease_expires_at": 0, "last_error": aliWanPollingManualReviewReason,
		"created_at": oldCreatedAt, "updated_at": now, "completed_at": now,
	}).Error)
	review, err := service.GetManualReviewRelayQuotaReservation(reservation.ReservationID)
	require.NoError(t, err)
	assert.Equal(t, service.RelayQuotaReviewKindAliWanTask, review.ReviewKind)
	assert.True(t, review.ProviderTaskIDPresent)
	assert.True(t, review.Retryable)

	var beforeUser model.User
	var beforeToken model.Token
	var beforeChannel model.Channel
	require.NoError(t, model.DB.First(&beforeUser, fixture.user.Id).Error)
	require.NoError(t, model.DB.Unscoped().First(&beforeToken, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&beforeChannel, fixture.channel.Id).Error)
	result, err := RetryAliWanTaskManualReview(reservation.ReservationID, 8402)
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Equal(t, taskID, result.TaskID)
	assert.Equal(t, model.TaskOperationSubmitted, result.OperationState)
	replay, err := RetryAliWanTaskManualReview(reservation.ReservationID, 8403)
	require.NoError(t, err)
	assert.False(t, replay.Changed)
	assert.Equal(t, result.AuditEventID, replay.AuditEventID)
	pollEnabled.Store(true)
	require.NoError(t, reconcileAsyncAliWanTasks(context.Background()))
	assert.Equal(t, int32(1), submitCalls.Load(), "operator poll retry never resubmits provider work")
	assert.Equal(t, int32(1), fetchCalls.Load())
	require.NoError(t, model.DB.Where("id = ?", task.ID).First(&task).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)

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

func TestAliWanRecoveryLeasePreventsConcurrentPolls(t *testing.T) {
	fixture := configureAliWanLifecycleFixture(t, true)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var fetchCalls atomic.Int32
	installAliWanProvider(t, &fakeAliWanProvider{
		submit: func(context.Context, string, string, *aliWan.PreparedRequest) (*aliWan.Task, []byte, error) {
			return &aliWan.Task{ProviderTaskID: "provider-concurrent", Status: aliWan.StatusSubmitted}, nil, nil
		},
		fetch: func(_ context.Context, _, _, id string) (*aliWan.Task, []byte, error) {
			fetchCalls.Add(1)
			started <- struct{}{}
			<-release
			return &aliWan.Task{ProviderTaskID: id, Status: aliWan.StatusProcessing}, nil, nil
		},
	})
	c, recorder := aliWanLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"wan2.7-t2v","prompt":"concurrent"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeAliWanPublicID(t, recorder)
	forceAliWanRecoveryDue(t, taskID)
	firstDone := make(chan error, 1)
	go func() { firstDone <- reconcileAsyncAliWanTasks(context.Background()) }()
	<-started
	require.NoError(t, reconcileAsyncAliWanTasks(context.Background()))
	close(release)
	require.NoError(t, <-firstDone)
	assert.Equal(t, int32(1), fetchCalls.Load())
}

func TestAliWanPlatformIsDistinctFromSoraAndOtherTaskFamilies(t *testing.T) {
	assert.Equal(t, "17", model.TaskOperationPlatformAliWan)
	assert.True(t, model.IsAliWanTaskOperationPlatform(model.TaskOperationPlatformAliWan))
	assert.False(t, model.IsOpenAIVideoTaskOperationPlatform(model.TaskOperationPlatformAliWan))
	assert.NotContains(t, model.OpenAIVideoTaskOperationPlatforms(), model.TaskOperationPlatformAliWan)
	assert.False(t, model.IsViduTaskOperationPlatform(model.TaskOperationPlatformAliWan))
	assert.EqualError(t, sanitizedAliWanProviderPollError(errors.New("upstream-ali-wan-key leaked")),
		"Alibaba Wan provider polling failed")
	assert.ErrorIs(t, sanitizedAliWanProviderPollError(context.DeadlineExceeded), context.DeadlineExceeded)
}
