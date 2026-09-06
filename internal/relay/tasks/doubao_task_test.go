package tasks

import (
	"context"
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
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/task/doubao"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	doubaoLifecycleBaseModel  = "doubao-seedance-1-0-pro-250528"
	doubaoLifecycleRatioModel = "doubao-seedance-2-0-260128"
)

type doubaoRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn doubaoRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func doubaoHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func configureDoubaoLifecycleFixture(t *testing.T, fixed bool, channelType channelcatalog.ChannelType) relayAccountingFixture {
	t.Helper()
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "doubao-test=0123456789abcdef0123456789abcdef")
	t.Setenv("DOUBAO_TASK_RECOVERY_DIR", t.TempDir())
	fixture := newRelayAccountingFixture(t, 2_000_000, 2_000_000)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Task{}, &model.TaskOperation{}, &model.JimengTaskOperation{},
		&model.AuditLogOutbox{}, &model.RelayQuotaReservationReviewEvent{},
	))
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", fixture.token.Id).
		Update("group", userssvc.GroupDefault).Error)
	fixture.token.Group = userssvc.GroupDefault
	models := strings.Join(doubao.ModelList(), ",")
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Updates(map[string]any{
			"type": int(channelType), "key": "upstream-doubao-key",
			"base_url": "https://ark.example", "models": models,
		}).Error)
	require.NoError(t, model.DB.Where("channel_id = ?", fixture.channel.Id).Delete(&model.Ability{}).Error)
	for _, modelName := range doubao.ModelList() {
		require.NoError(t, model.DB.Create(&model.Ability{
			Group: userssvc.GroupDefault, Model: modelName, ChannelId: fixture.channel.Id,
			Enabled: true, Weight: 1,
		}).Error)
	}
	require.NoError(t, channelssvc.InitAbilityCache())
	keys := []string{
		setting.ModelBillingModeOption, setting.PerCallModelPriceOption, setting.ModelRatioOption,
		setting.CompletionRatioOption, setting.GroupRatioOption, setting.GroupGroupRatioOption,
	}
	previous := setting.GetOptions(keys...)
	t.Cleanup(func() { _ = setting.UpdateOptions(previous) })
	prices, ratios := `{"doubao-seedance-1-0-pro-250528":0.004,"doubao-seedance-2-0-260128":0.004}`, `{}`
	if !fixed {
		prices, ratios = `{}`, `{"doubao-seedance-1-0-pro-250528":2,"doubao-seedance-2-0-260128":2}`
	}
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"doubao-seedance-1-0-pro-250528":"reference","doubao-seedance-2-0-260128":"reference"}`,
		setting.PerCallModelPriceOption: prices,
		setting.ModelRatioOption:        ratios,
		setting.CompletionRatioOption:   `{"doubao-seedance-1-0-pro-250528":99,"doubao-seedance-2-0-260128":99}`,
		setting.GroupRatioOption:        `{"default":1}`,
		setting.GroupGroupRatioOption:   `{}`,
	}))
	return fixture
}

func doubaoLifecycleContext(
	t *testing.T,
	fixture relayAccountingFixture,
	method, path, body, taskID string,
) (*gin.Context, *httptest.ResponseRecorder) {
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

func installDoubaoClient(t *testing.T, client *doubao.Client) {
	t.Helper()
	previous := newDoubaoTaskClient
	newDoubaoTaskClient = func() *doubao.Client { return client }
	t.Cleanup(func() { newDoubaoTaskClient = previous })
}

func forceDoubaoRecoveryDue(t *testing.T, taskID string) {
	t.Helper()
	require.NoError(t, model.DB.Model(&model.TaskOperation{}).Where("task_id = ?", taskID).
		Updates(map[string]any{"next_attempt_at": 0, "lease_owner": "", "lease_expires_at": 0}).Error)
}

func decodeDoubaoPublicTaskID(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response doubaoSubmitResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.NoError(t, validateDoubaoTaskPublicID(response.ID))
	return response.ID
}

func TestDoubaoGenericRoutesPreserveSelectedPlatformAndFetchLocally(t *testing.T) {
	tests := []struct {
		name       string
		channel    channelcatalog.ChannelType
		platform   string
		submitPath string
		fetchPath  string
	}{
		{"VolcEngine legacy route", channelcatalog.ChannelTypeVolcEngine, model.TaskOperationPlatformVolcEngine,
			"/v1/video/generations", "/v1/video/generations/"},
		{"VolcEngine OpenAI route", channelcatalog.ChannelTypeVolcEngine, model.TaskOperationPlatformVolcEngine,
			"/v1/videos", "/v1/videos/"},
		{"Doubao video legacy route", channelcatalog.ChannelTypeDoubaoVideo, model.TaskOperationPlatformDoubaoVideo,
			"/v1/video/generations", "/v1/video/generations/"},
		{"Doubao video OpenAI route", channelcatalog.ChannelTypeDoubaoVideo, model.TaskOperationPlatformDoubaoVideo,
			"/v1/videos", "/v1/videos/"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := configureDoubaoLifecycleFixture(t, true, test.channel)
			var calls atomic.Int32
			client := &doubao.Client{HTTPClient: &http.Client{Transport: doubaoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				assert.Equal(t, http.MethodPost, request.Method)
				assert.Equal(t, "/api/v3/contents/generations/tasks", request.URL.Path)
				assert.Equal(t, "Bearer upstream-doubao-key", request.Header.Get("Authorization"))
				return doubaoHTTPResponse(http.StatusOK, `{"id":"provider-route"}`), nil
			})}}
			installDoubaoClient(t, client)
			c, recorder := doubaoLifecycleContext(t, fixture, http.MethodPost, test.submitPath,
				`{"model":"`+doubaoLifecycleBaseModel+`","prompt":"route contract"}`, "")
			RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			taskID := decodeDoubaoPublicTaskID(t, recorder)
			assert.NotContains(t, recorder.Body.String(), "provider-route")
			assert.NotContains(t, recorder.Body.String(), "upstream-doubao-key")

			var task model.Task
			var operation model.TaskOperation
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
			require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
			require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
			assert.Equal(t, test.platform, task.Platform)
			assert.Equal(t, test.platform, operation.Platform)
			assert.Equal(t, model.TaskStatusSubmitted, task.Status)
			assert.Equal(t, model.TaskOperationSubmitted, operation.State)
			assert.True(t, operation.SettlementPending)
			assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reservation.Status)
			assert.True(t, strings.HasPrefix(task.PrivateData, doubaoTaskPrivateDataPrefix))
			assert.NotContains(t, task.PrivateData, "provider-route")
			assert.NotContains(t, task.PrivateData, "upstream-doubao-key")
			assert.NotContains(t, operation.EncryptedProviderTaskID, "provider-route")

			c, recorder = doubaoLifecycleContext(t, fixture, http.MethodGet, test.fetchPath+taskID, "", taskID)
			RelayVideoTaskFetch(c, middleware.CaptureRelayRequestState(c))
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), taskID)
			assert.NotContains(t, recorder.Body.String(), "provider-route")
			assert.Equal(t, int32(1), calls.Load(), "client-visible fetch must stay local")
		})
	}
}

func TestDoubaoDefinitiveRejectionRefundsButAmbiguousDispatchStaysHeld(t *testing.T) {
	t.Run("definitive provider rejection", func(t *testing.T) {
		fixture := configureDoubaoLifecycleFixture(t, true, channelcatalog.ChannelTypeDoubaoVideo)
		var calls atomic.Int32
		installDoubaoClient(t, &doubao.Client{HTTPClient: &http.Client{Transport: doubaoRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return doubaoHTTPResponse(http.StatusBadRequest,
				`{"error":{"code":"invalid_parameter","message":"request rejected"}}`), nil
		})}})
		c, recorder := doubaoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
			`{"model":"`+doubaoLifecycleBaseModel+`","prompt":"reject"}`, "")
		RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.Equal(t, int32(1), calls.Load())

		var task model.Task
		var operation model.TaskOperation
		var reservation model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.First(&task).Error)
		require.NoError(t, model.DB.First(&operation).Error)
		require.NoError(t, model.DB.First(&reservation).Error)
		assert.Equal(t, model.TaskStatusFailure, task.Status)
		assert.Zero(t, task.Quota)
		assert.Equal(t, model.TaskOperationRefunded, operation.State)
		assert.False(t, operation.SettlementPending)
		assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
		var user model.User
		var token model.Token
		require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
		require.NoError(t, model.DB.Unscoped().First(&token, fixture.token.Id).Error)
		assert.Equal(t, 2_000_000, user.Quota)
		assert.Equal(t, 2_000_000, token.RemainQuota)
	})

	t.Run("transport ambiguity requires explicit review", func(t *testing.T) {
		fixture := configureDoubaoLifecycleFixture(t, true, channelcatalog.ChannelTypeVolcEngine)
		transportErr := errors.New("connection reset after request write")
		installDoubaoClient(t, &doubao.Client{HTTPClient: &http.Client{Transport: doubaoRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, transportErr
		})}})
		c, recorder := doubaoLifecycleContext(t, fixture, http.MethodPost, "/v1/video/generations",
			`{"model":"`+doubaoLifecycleBaseModel+`","prompt":"ambiguous"}`, "")
		RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
		require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
		var task model.Task
		var operation model.TaskOperation
		var reservation model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.First(&task).Error)
		require.NoError(t, model.DB.First(&operation).Error)
		require.NoError(t, model.DB.First(&reservation).Error)
		assert.Equal(t, model.TaskOperationPlatformVolcEngine, task.Platform)
		assert.Equal(t, model.TaskStatusUnknown, task.Status)
		assert.Equal(t, model.TaskOperationManualReview, operation.State)
		assert.True(t, operation.SettlementPending)
		assert.Empty(t, operation.EncryptedProviderTaskID)
		assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reservation.Status)
		review, err := billingsvc.GetManualReviewRelayQuotaReservation(reservation.ReservationID)
		require.NoError(t, err)
		assert.Equal(t, billingsvc.RelayQuotaReviewKindDoubaoVideoTask, review.ReviewKind)
		assert.Equal(t, model.TaskOperationPlatformVolcEngine, review.TaskPlatform)
		assert.True(t, review.ResolutionRequired)
		assert.ElementsMatch(t, []string{billingsvc.RelayQuotaReviewResolutionSettle,
			billingsvc.RelayQuotaReviewResolutionRefund}, review.ResolutionOptions)
		assert.False(t, review.ProviderTaskIDPresent)
		assert.False(t, review.Retryable)

		resolved, err := ResolveDoubaoRelayQuotaReservationReview(
			reservation.ReservationID, 9101, billingsvc.RelayQuotaReviewResolutionRefund,
		)
		require.NoError(t, err)
		assert.True(t, resolved.Changed)
		assert.Equal(t, model.RelayQuotaReservationStatusRefunded, resolved.ReservationStatus)
		assert.Equal(t, model.TaskStatusFailure, resolved.TaskStatus)
		assert.Equal(t, model.TaskOperationRefunded, resolved.OperationState)
		replay, err := ResolveDoubaoRelayQuotaReservationReview(
			reservation.ReservationID, 9102, billingsvc.RelayQuotaReviewResolutionRefund,
		)
		require.NoError(t, err)
		assert.False(t, replay.Changed)
		assert.Equal(t, resolved.AuditEventID, replay.AuditEventID)
	})
}

func TestDoubaoAcceptedTaskTerminalFailureRefundsHeldQuotaExactlyOnce(t *testing.T) {
	fixture := configureDoubaoLifecycleFixture(t, true, channelcatalog.ChannelTypeDoubaoVideo)
	var calls atomic.Int32
	installDoubaoClient(t, &doubao.Client{HTTPClient: &http.Client{Transport: doubaoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.Method == http.MethodPost {
			return doubaoHTTPResponse(http.StatusOK, `{"id":"provider-failed"}`), nil
		}
		return doubaoHTTPResponse(http.StatusOK,
			`{"id":"provider-failed","status":"failed","error":{"code":"CONTENT_POLICY","message":"generation rejected"},"usage":{"total_tokens":77}}`), nil
	})}})
	c, recorder := doubaoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"`+doubaoLifecycleBaseModel+`","prompt":"fail later"}`, "")
	RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeDoubaoPublicTaskID(t, recorder)
	forceDoubaoRecoveryDue(t, taskID)
	require.NoError(t, reconcileAsyncDoubaoTasks(context.Background()))

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
	assert.Equal(t, "generation rejected", task.FailReason)
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationRefunded, operation.State)
	assert.False(t, operation.SettlementPending)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
	require.NoError(t, reconcileAsyncDoubaoTasks(context.Background()))
	assert.Equal(t, int32(2), calls.Load(), "terminal failure replay must neither poll nor refund twice")
}

func TestDoubaoPollingUsesImmutableFixedAndRatioPricing(t *testing.T) {
	tests := []struct {
		name        string
		fixed       bool
		model       string
		requestBody string
		units       int
	}{
		{"fixed price ignores provider units", true, doubaoLifecycleBaseModel,
			`{"model":"` + doubaoLifecycleBaseModel + `","prompt":"fixed"}`, 700_000},
		{"ratio price uses total tokens and video multiplier", false, doubaoLifecycleRatioModel,
			`{"model":"` + doubaoLifecycleRatioModel + `","prompt":"ratio","metadata":{"resolution":"1080p","content":[{"type":"video_url","video_url":{"url":"https://assets.example/input.mp4"}}]}}`, 700_000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := configureDoubaoLifecycleFixture(t, test.fixed, channelcatalog.ChannelTypeVolcEngine)
			var providerCalls atomic.Int32
			client := &doubao.Client{HTTPClient: &http.Client{Transport: doubaoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				providerCalls.Add(1)
				assert.Equal(t, "Bearer upstream-doubao-key", request.Header.Get("Authorization"))
				switch request.Method {
				case http.MethodPost:
					assert.Equal(t, "https://ark.example/api/v3/contents/generations/tasks", request.URL.String())
					return doubaoHTTPResponse(http.StatusOK, `{"id":"provider-accounting"}`), nil
				case http.MethodGet:
					assert.Equal(t, "https://ark.example/api/v3/contents/generations/tasks/provider-accounting", request.URL.String())
					return doubaoHTTPResponse(http.StatusOK, `{"id":"provider-accounting","status":"succeeded",`+
						`"content":{"video_url":"https://cdn.example/result.mp4"},`+
						`"usage":{"completion_tokens":999999,"total_tokens":`+strconv.Itoa(test.units)+`},`+
						`"created_at":11,"updated_at":22}`), nil
				default:
					return nil, errors.New("unexpected Doubao method")
				}
			})}}
			installDoubaoClient(t, client)
			c, recorder := doubaoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos", test.requestBody, "")
			RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			taskID := decodeDoubaoPublicTaskID(t, recorder)

			var task model.Task
			var operation model.TaskOperation
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
			require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
			require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
			privateData, err := decodeDoubaoTaskPrivateData(task.PrivateData)
			require.NoError(t, err)
			expectedFinal, err := privateData.Pricing.SettlementQuota(test.units)
			require.NoError(t, err)
			if test.fixed {
				assert.Equal(t, 2_000, reservation.RequestedQuota)
				assert.Equal(t, 2_000, expectedFinal)
			} else {
				assert.Equal(t, 336_956, reservation.RequestedQuota)
				assert.Equal(t, 943_478, expectedFinal)
				properties, err := decodeDoubaoTaskProperties(task.Properties)
				require.NoError(t, err)
				assert.True(t, properties.HasVideoInput)
				assert.Equal(t, "0.6739130434782609", properties.VideoInputRatio)
			}

			// Neither a pricing reload nor channel mutation may change the
			// accepted task's provider or economic coordinates.
			require.NoError(t, setting.UpdateOptions(map[string]string{
				setting.ModelRatioOption:        `{"doubao-seedance-1-0-pro-250528":99,"doubao-seedance-2-0-260128":99}`,
				setting.PerCallModelPriceOption: `{"doubao-seedance-1-0-pro-250528":0.9,"doubao-seedance-2-0-260128":0.9}`,
				setting.GroupRatioOption:        `{"default":9}`,
			}))
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
				Updates(map[string]any{"base_url": "https://changed.example", "key": "changed-key"}).Error)
			forceDoubaoRecoveryDue(t, taskID)
			require.NoError(t, reconcileAsyncDoubaoTasks(context.Background()))
			require.NoError(t, model.DB.Where("id = ?", task.ID).First(&task).Error)
			require.NoError(t, model.DB.Where("id = ?", operation.ID).First(&operation).Error)
			require.NoError(t, model.DB.Where("id = ?", reservation.ID).First(&reservation).Error)
			assert.Equal(t, model.TaskStatusSuccess, task.Status)
			assert.Equal(t, expectedFinal, task.Quota)
			assert.Equal(t, model.TaskOperationTerminal, operation.State)
			assert.False(t, operation.SettlementPending)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
			assert.Equal(t, expectedFinal, reservation.ActualQuota)
			assert.Contains(t, task.Data, "https://cdn.example/result.mp4")
			assert.NotContains(t, task.Data, "provider-accounting")
			assert.NotContains(t, task.PrivateData, "provider-accounting")

			var user model.User
			var token model.Token
			var channel model.Channel
			require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
			require.NoError(t, model.DB.Unscoped().First(&token, fixture.token.Id).Error)
			require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
			assert.Equal(t, 2_000_000-expectedFinal, user.Quota)
			assert.Equal(t, expectedFinal, user.UsedQuota)
			assert.Equal(t, 1, user.RequestCount)
			assert.Equal(t, 2_000_000-expectedFinal, token.RemainQuota)
			assert.Equal(t, expectedFinal, token.UsedQuota)
			assert.Equal(t, int64(expectedFinal), channel.UsedQuota)
			var logs []model.Log
			require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).Find(&logs).Error)
			require.Len(t, logs, 1)
			assert.Equal(t, expectedFinal, logs[0].Quota)
			assert.Equal(t, test.model, logs[0].ModelName)

			require.NoError(t, reconcileAsyncDoubaoTasks(context.Background()))
			assert.Equal(t, int32(2), providerCalls.Load(), "terminal replay must not repoll or rebill")
			var logCount int64
			require.NoError(t, model.LOG_DB.Model(&model.Log{}).
				Where("type = ?", billingsvc.LogTypeConsume).Count(&logCount).Error)
			assert.EqualValues(t, 1, logCount)
		})
	}
}

func TestDoubaoCompletedContentAndFetchAreOwnerAndPlatformScoped(t *testing.T) {
	fixture := configureDoubaoLifecycleFixture(t, true, channelcatalog.ChannelTypeDoubaoVideo)
	client := &doubao.Client{HTTPClient: &http.Client{Transport: doubaoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.Method {
		case http.MethodPost:
			return doubaoHTTPResponse(http.StatusOK, `{"id":"provider-content"}`), nil
		case http.MethodGet:
			return doubaoHTTPResponse(http.StatusOK,
				`{"id":"provider-content","status":"succeeded","content":{"video_url":"https://cdn.example/video.mp4"},"usage":{"total_tokens":9}}`), nil
		default:
			return nil, errors.New("unexpected provider request")
		}
	})}}
	installDoubaoClient(t, client)
	c, recorder := doubaoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"`+doubaoLifecycleBaseModel+`","prompt":"content"}`, "")
	RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeDoubaoPublicTaskID(t, recorder)
	forceDoubaoRecoveryDue(t, taskID)
	require.NoError(t, reconcileAsyncDoubaoTasks(context.Background()))

	var contentCalls atomic.Int32
	previousContentClient := newDoubaoContentHTTPClient
	newDoubaoContentHTTPClient = func() *http.Client {
		return &http.Client{Transport: doubaoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			contentCalls.Add(1)
			assert.Equal(t, "https://cdn.example/video.mp4", request.URL.String())
			assert.Empty(t, request.Header.Get("Authorization"), "provider credentials must not reach result hosts")
			result := doubaoHTTPResponse(http.StatusOK, "doubao-video-bytes")
			result.Header.Set("Content-Type", "video/mp4")
			result.Header.Set("Content-Length", strconv.Itoa(len("doubao-video-bytes")))
			result.ContentLength = int64(len("doubao-video-bytes"))
			result.Request = request
			return result, nil
		})}
	}
	t.Cleanup(func() { newDoubaoContentHTTPClient = previousContentClient })

	c, recorder = doubaoLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID+"/content", "", taskID)
	VideoProxy(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "doubao-video-bytes", recorder.Body.String())
	assert.Equal(t, "video/mp4", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	assert.Contains(t, recorder.Header().Get("Content-Security-Policy"), "sandbox")

	c, recorder = doubaoLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID+"/content", "", taskID)
	requestctx.SetUserId(c, fixture.user.Id+1)
	VideoProxy(c, middleware.CaptureRelayRequestState(c))
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, int32(1), contentCalls.Load())

	foreignID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	require.NoError(t, model.DB.Create(&model.Task{
		CreatedAt: wallclock.NowTimestamp(), UpdatedAt: wallclock.NowTimestamp(), TaskID: foreignID,
		Platform: model.TaskOperationPlatformKling, UserId: fixture.user.Id, Group: userssvc.GroupDefault,
		ChannelId: fixture.channel.Id, Status: model.TaskStatusSubmitted,
	}).Error)
	c, recorder = doubaoLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+foreignID, "", foreignID)
	RelayVideoTaskFetch(c, middleware.CaptureRelayRequestState(c))
	assert.Equal(t, http.StatusNotFound, recorder.Code, "an unrelated task platform must not enter Doubao fetch")
}

func createPreparedDoubaoTaskForRecovery(
	t *testing.T,
	fixture relayAccountingFixture,
	platform string,
) (*model.Task, *billingsvc.RelayQuotaReservation) {
	t.Helper()
	pricing, enabled, err := billingsvc.ResolveReferenceAsyncTaskBillingPlan(
		doubaoLifecycleBaseModel, userssvc.GroupDefault, userssvc.GroupDefault,
	)
	require.NoError(t, err)
	require.True(t, enabled)
	quota, err := pricing.PreConsumeQuota()
	require.NoError(t, err)
	taskID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	properties, err := marshalDoubaoTaskProperties(doubaoTaskProperties{
		Version: doubaoTaskMetadataVersion, Family: "doubao", Prompt: "restart",
		OriginModelName: doubaoLifecycleBaseModel, UpstreamModelName: doubaoLifecycleBaseModel,
		Action: doubao.ActionGenerate, VideoInputRatio: "1",
	})
	require.NoError(t, err)
	now := wallclock.NowTimestamp()
	task := &model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: platform,
		UserId: fixture.user.Id, Group: userssvc.GroupDefault, ChannelId: fixture.channel.Id,
		Quota: quota, Action: string(doubao.ActionGenerate), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: properties, Data: "null",
	}
	baseURL := "https://ark.example"
	encryptedKey, err := asyncTaskEncryptBound("upstream-doubao-key",
		doubaoChannelCredentialBinding(task.TaskID, platform, task.UserId, task.ChannelId, baseURL))
	require.NoError(t, err)
	privateData := doubaoTaskPrivateData{
		Version: doubaoTaskMetadataVersion, ChannelBaseURL: baseURL,
		EncryptedChannelKey: encryptedKey, Pricing: pricing,
	}
	reservation, err := createDoubaoReservedTask(task, &fixture.token, &privateData)
	require.NoError(t, err)
	return task, reservation
}

func TestDoubaoAcceptedJournalPromotesAcrossRestartWithoutPlaintext(t *testing.T) {
	fixture := configureDoubaoLifecycleFixture(t, true, channelcatalog.ChannelTypeDoubaoVideo)
	task, reservation := createPreparedDoubaoTaskForRecovery(t, fixture, model.TaskOperationPlatformDoubaoVideo)
	require.NoError(t, markDoubaoTaskDispatching(task, reservation))
	require.NoError(t, persistAcceptedDoubaoRecoveryJournal(
		task, reservation.ReservationID(), "provider-restart", doubao.ActionGenerate,
	))
	path, err := doubaoRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)
	disk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(disk), "provider-restart")
	assert.NotContains(t, string(disk), "upstream-doubao-key")
	assert.Contains(t, string(disk), `"platform":"54"`)
	require.NoError(t, PromoteDoubaoTaskRecoveryJournalsContext(context.Background()))
	_, err = os.Stat(path)
	assert.True(t, errors.Is(err, os.ErrNotExist))
	var operation model.TaskOperation
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskOperationPlatformDoubaoVideo, operation.Platform)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.True(t, operation.SettlementPending)
	assert.NotEmpty(t, operation.EncryptedProviderTaskID)
	assert.NotContains(t, operation.EncryptedProviderTaskID, "provider-restart")

	installDoubaoClient(t, &doubao.Client{HTTPClient: &http.Client{Transport: doubaoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodGet, request.Method, "restart recovery must never replay submit")
		assert.Equal(t, "/api/v3/contents/generations/tasks/provider-restart", request.URL.Path)
		return doubaoHTTPResponse(http.StatusOK,
			`{"id":"provider-restart","status":"succeeded","content":{"video_url":"https://cdn.example/restart.mp4"},"usage":{"total_tokens":5}}`), nil
	})}})
	forceDoubaoRecoveryDue(t, task.TaskID)
	require.NoError(t, reconcileAsyncDoubaoTasks(context.Background()))
	require.NoError(t, model.DB.Where("id = ?", task.ID).First(task).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
}

func TestDoubaoRecoveryLeasePreventsConcurrentPolls(t *testing.T) {
	fixture := configureDoubaoLifecycleFixture(t, true, channelcatalog.ChannelTypeVolcEngine)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var fetchCalls atomic.Int32
	installDoubaoClient(t, &doubao.Client{HTTPClient: &http.Client{Transport: doubaoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			return doubaoHTTPResponse(http.StatusOK, `{"id":"provider-concurrent"}`), nil
		}
		fetchCalls.Add(1)
		started <- struct{}{}
		<-release
		return doubaoHTTPResponse(http.StatusOK, `{"id":"provider-concurrent","status":"processing"}`), nil
	})}})
	c, recorder := doubaoLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"`+doubaoLifecycleBaseModel+`","prompt":"concurrent"}`, "")
	RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeDoubaoPublicTaskID(t, recorder)
	forceDoubaoRecoveryDue(t, taskID)
	firstDone := make(chan error, 1)
	go func() { firstDone <- reconcileAsyncDoubaoTasks(context.Background()) }()
	<-started
	require.NoError(t, reconcileAsyncDoubaoTasks(context.Background()))
	close(release)
	require.NoError(t, <-firstDone)
	assert.Equal(t, int32(1), fetchCalls.Load())
}
