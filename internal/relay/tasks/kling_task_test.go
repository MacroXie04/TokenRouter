package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/kling"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

type klingRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn klingRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func configureKlingLifecycleFixture(t *testing.T, fixed bool) relayAccountingFixture {
	t.Helper()
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "kling-test=0123456789abcdef0123456789abcdef")
	t.Setenv("KLING_TASK_RECOVERY_DIR", t.TempDir())
	fixture := newRelayAccountingFixture(t, 2_000_000, 2_000_000)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Task{}, &model.TaskOperation{}, &model.JimengTaskOperation{}, &model.AuditLogOutbox{},
		&model.RelayQuotaReservationReviewEvent{},
	))
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", fixture.token.Id).
		Update("group", userssvc.GroupDefault).Error)
	fixture.token.Group = userssvc.GroupDefault
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Updates(map[string]any{
			"type": int(channelcatalog.ChannelTypeKling), "key": "sk-upstream",
			"base_url": "https://relay.example", "models": "kling-v1",
		}).Error)
	require.NoError(t, model.DB.Where("channel_id = ?", fixture.channel.Id).Delete(&model.Ability{}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: userssvc.GroupDefault, Model: "kling-v1", ChannelId: fixture.channel.Id,
		Enabled: true, Weight: 1,
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())

	keys := []string{
		setting.ModelBillingModeOption, setting.PerCallModelPriceOption, setting.ModelRatioOption,
		setting.CompletionRatioOption, setting.GroupRatioOption, setting.GroupGroupRatioOption,
	}
	previous := setting.GetOptions(keys...)
	t.Cleanup(func() { _ = setting.UpdateOptions(previous) })
	price, ratio := `{"kling-v1":0.004}`, `{}`
	if !fixed {
		price, ratio = `{}`, `{"kling-v1":2}`
	}
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"kling-v1":"reference"}`,
		setting.PerCallModelPriceOption: price,
		setting.ModelRatioOption:        ratio,
		setting.CompletionRatioOption:   `{"kling-v1":99}`,
		setting.GroupRatioOption:        `{"default":1}`,
		setting.GroupGroupRatioOption:   `{}`,
	}))
	return fixture
}

func klingLifecycleContext(
	t *testing.T,
	fixture relayAccountingFixture,
	method, path, body, taskID string,
) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
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

func klingHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

func decodeKlingSubmitTaskID(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response klingSubmitResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.NotEmpty(t, response.TaskID)
	return response.TaskID
}

func TestKlingFourRoutesUseBodyDrivenActionAndLocalFetch(t *testing.T) {
	fixture := configureKlingLifecycleFixture(t, true)
	var providerCalls atomic.Int32
	var paths []string
	client := &kling.Client{HTTPClient: &http.Client{Transport: klingRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := providerCalls.Add(1)
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "Bearer sk-upstream", request.Header.Get("Authorization"))
		paths = append(paths, request.URL.Path)
		return klingHTTPResponse(http.StatusOK, `{"code":0,"data":{"task_id":"provider-`+
			strconv.Itoa(int(call))+`","task_status":"submitted","created_at":11,"updated_at":12}}`), nil
	})}}
	previousClient := newKlingTaskClient
	newKlingTaskClient = func() *kling.Client { return client }
	t.Cleanup(func() { newKlingTaskClient = previousClient })

	tests := []struct {
		name           string
		submitPath     string
		body           string
		expectedAction kling.Action
	}{
		{name: "text route and text body", submitPath: "/kling/v1/videos/text2video", body: `{"model":"kling-v1","prompt":"text"}`, expectedAction: kling.ActionTextToVideo},
		{name: "image route and image body", submitPath: "/kling/v1/videos/image2video", body: `{"model_name":"kling-v1","prompt":"image","image":"https://assets.example/input.png"}`, expectedAction: kling.ActionImageToVideo},
		{name: "text route does not override image body", submitPath: "/kling/v1/videos/text2video", body: `{"model":"kling-v1","prompt":"image wins","metadata":{"model":"kling-v1","image":"https://assets.example/meta.png"}}`, expectedAction: kling.ActionImageToVideo},
		{name: "image route does not invent an image", submitPath: "/kling/v1/videos/image2video", body: `{"model":"kling-v1","prompt":"still text"}`, expectedAction: kling.ActionTextToVideo},
	}
	taskIDs := make([]string, 0, len(tests))
	for _, test := range tests {
		c, recorder := klingLifecycleContext(t, fixture, http.MethodPost, test.submitPath, test.body, "")
		RelayKlingTask(c)
		require.Equal(t, http.StatusOK, recorder.Code, test.name+": "+recorder.Body.String())
		taskID := decodeKlingSubmitTaskID(t, recorder)
		assert.NotContains(t, recorder.Body.String(), "provider-")
		taskIDs = append(taskIDs, taskID)

		var task model.Task
		require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
		assert.Equal(t, klingTaskPlatform, task.Platform)
		assert.Equal(t, string(test.expectedAction), task.Action)
		assert.Equal(t, model.TaskStatusSubmitted, task.Status)
		assert.True(t, strings.HasPrefix(task.PrivateData, klingTaskPrivateDataPrefix))
		assert.NotContains(t, task.PrivateData, "sk-upstream")
		assert.NotContains(t, task.PrivateData, "provider-")
		assert.NotContains(t, task.Data, "provider-")
	}
	require.Equal(t, []string{
		"/kling/v1/videos/text2video", "/kling/v1/videos/image2video",
		"/kling/v1/videos/image2video", "/kling/v1/videos/text2video",
	}, paths)

	for index, test := range tests {
		fetchPath := "/kling/v1/videos/" + string(test.expectedAction) + "/" + taskIDs[index]
		c, recorder := klingLifecycleContext(t, fixture, http.MethodGet, fetchPath, "", taskIDs[index])
		RelayKlingTaskFetch(c)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), taskIDs[index])
		assert.NotContains(t, recorder.Body.String(), "provider-")
	}
	assert.Equal(t, int32(len(tests)), providerCalls.Load(), "client fetches must never contact Kling")

	wrongPath := "/kling/v1/videos/image2video/" + taskIDs[0]
	c, recorder := klingLifecycleContext(t, fixture, http.MethodGet, wrongPath, "", taskIDs[0])
	RelayKlingTaskFetch(c)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, int32(len(tests)), providerCalls.Load())
}

func TestKlingDirectChannelUsesJWTAndOfficialRoute(t *testing.T) {
	fixture := configureKlingLifecycleFixture(t, true)
	directKey := "access-id|0123456789abcdef0123456789abcdef"
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Updates(map[string]any{"key": directKey, "base_url": ""}).Error)
	client := &kling.Client{HTTPClient: &http.Client{Transport: klingRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/v1/videos/text2video", request.URL.Path)
		authorization := request.Header.Get("Authorization")
		assert.True(t, strings.HasPrefix(authorization, "Bearer ey"))
		assert.NotContains(t, authorization, directKey)
		return klingHTTPResponse(http.StatusOK,
			`{"code":0,"data":{"task_id":"provider-direct","task_status":"submitted"}}`), nil
	})}}
	previousClient := newKlingTaskClient
	newKlingTaskClient = func() *kling.Client { return client }
	t.Cleanup(func() { newKlingTaskClient = previousClient })

	c, recorder := klingLifecycleContext(t, fixture, http.MethodPost, "/kling/v1/videos/text2video",
		`{"model":"kling-v1","prompt":"direct"}`, "")
	RelayKlingTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "provider-direct")
}

func TestKlingBackgroundPollSettlesImmutableRatioExactlyOnce(t *testing.T) {
	fixture := configureKlingLifecycleFixture(t, false)
	var providerCalls atomic.Int32
	client := &kling.Client{HTTPClient: &http.Client{Transport: klingRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		providerCalls.Add(1)
		switch request.Method {
		case http.MethodPost:
			assert.Equal(t, "/kling/v1/videos/text2video", request.URL.Path)
			return klingHTTPResponse(http.StatusOK, `{"code":0,"data":{"task_id":"provider-ratio","task_status":"submitted"}}`), nil
		case http.MethodGet:
			assert.Equal(t, "/kling/v1/videos/text2video/provider-ratio", request.URL.Path)
			return klingHTTPResponse(http.StatusOK, `{"code":0,"data":{"task_id":"provider-ratio","task_status":"succeed","task_result":{"videos":[{"url":"https://cdn.example/result.mp4"}]},"final_unit_deduction":"1.01","created_at":11,"updated_at":22}}`), nil
		default:
			return nil, errors.New("unexpected Kling method")
		}
	})}}
	previousClient := newKlingTaskClient
	newKlingTaskClient = func() *kling.Client { return client }
	t.Cleanup(func() { newKlingTaskClient = previousClient })

	c, recorder := klingLifecycleContext(t, fixture, http.MethodPost, "/kling/v1/videos/text2video",
		`{"model":"kling-v1","prompt":"ratio"}`, "")
	RelayKlingTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeKlingSubmitTaskID(t, recorder)

	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reservation.Status)
	assert.Equal(t, quotamath.QuotaPerUnit, reservation.RequestedQuota)
	var operation model.TaskOperation
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.True(t, operation.SettlementPending)
	require.NoError(t, model.DB.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).
		Update("next_attempt_at", 0).Error)

	// In-flight settings changes must not alter the captured task charge.
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelRatioOption: `{"kling-v1":99}`,
		setting.GroupRatioOption: `{"default":9}`,
	}))
	require.NoError(t, reconcileAsyncKlingTasks(context.Background()))

	var task model.Task
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	require.NoError(t, model.DB.Where("id = ?", operation.ID).First(&operation).Error)
	require.NoError(t, model.DB.Where("id = ?", reservation.ID).First(&reservation).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.Equal(t, 4, task.Quota)
	assert.Equal(t, model.TaskOperationTerminal, operation.State)
	assert.False(t, operation.SettlementPending)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, 4, reservation.ActualQuota)
	assert.NotContains(t, task.PrivateData, "provider-ratio")
	assert.NotContains(t, task.Data, "provider-ratio")
	assert.Contains(t, task.Data, "https://cdn.example/result.mp4")

	var user model.User
	var token model.Token
	var channel model.Channel
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.Unscoped().First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
	assert.Equal(t, 2_000_000-4, user.Quota)
	assert.Equal(t, 4, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 2_000_000-4, token.RemainQuota)
	assert.Equal(t, 4, token.UsedQuota)
	assert.Equal(t, int64(4), channel.UsedQuota)
	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).Find(&logs).Error)
	require.Len(t, logs, 1)
	assert.Equal(t, 4, logs[0].Quota)
	assert.Equal(t, "kling-v1", logs[0].ModelName)

	require.NoError(t, reconcileAsyncKlingTasks(context.Background()))
	assert.Equal(t, int32(2), providerCalls.Load(), "terminal replay must neither repoll nor double charge")
	var logCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", billingsvc.LogTypeConsume).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)

	fetchPath := "/kling/v1/videos/text2video/" + taskID
	c, recorder = klingLifecycleContext(t, fixture, http.MethodGet, fetchPath, "", taskID)
	RelayKlingTaskFetch(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "https://cdn.example/result.mp4")
	assert.Equal(t, int32(2), providerCalls.Load())
}

func TestKlingTerminalFailureRefundsAndAmbiguousDispatchStaysHeld(t *testing.T) {
	t.Run("authoritative provider failure refunds", func(t *testing.T) {
		fixture := configureKlingLifecycleFixture(t, true)
		client := &kling.Client{HTTPClient: &http.Client{Transport: klingRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return klingHTTPResponse(http.StatusOK, `{"code":0,"data":{"task_id":"provider-failed","task_status":"failed","task_status_msg":"generation rejected"}}`), nil
		})}}
		previousClient := newKlingTaskClient
		newKlingTaskClient = func() *kling.Client { return client }
		t.Cleanup(func() { newKlingTaskClient = previousClient })

		c, recorder := klingLifecycleContext(t, fixture, http.MethodPost, "/kling/v1/videos/text2video",
			`{"model":"kling-v1","prompt":"fail"}`, "")
		RelayKlingTask(c)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		taskID := decodeKlingSubmitTaskID(t, recorder)
		var task model.Task
		var operation model.TaskOperation
		var reservation model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
		require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
		require.NoError(t, model.DB.First(&reservation).Error)
		assert.Equal(t, model.TaskStatusFailure, task.Status)
		assert.Zero(t, task.Quota)
		assert.Equal(t, model.TaskOperationRefunded, operation.State)
		assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
		var user model.User
		var token model.Token
		require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
		require.NoError(t, model.DB.Unscoped().First(&token, fixture.token.Id).Error)
		assert.Equal(t, 2_000_000, user.Quota)
		assert.Equal(t, 2_000_000, token.RemainQuota)
	})

	t.Run("ambiguous network outcome is fenced for review", func(t *testing.T) {
		fixture := configureKlingLifecycleFixture(t, true)
		client := &kling.Client{HTTPClient: &http.Client{Transport: klingRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset after request write")
		})}}
		previousClient := newKlingTaskClient
		newKlingTaskClient = func() *kling.Client { return client }
		t.Cleanup(func() { newKlingTaskClient = previousClient })

		c, recorder := klingLifecycleContext(t, fixture, http.MethodPost, "/kling/v1/videos/text2video",
			`{"model":"kling-v1","prompt":"ambiguous"}`, "")
		RelayKlingTask(c)
		require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
		var task model.Task
		var operation model.TaskOperation
		var reservation model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.First(&task).Error)
		require.NoError(t, model.DB.First(&operation).Error)
		require.NoError(t, model.DB.First(&reservation).Error)
		assert.Equal(t, model.TaskStatusUnknown, task.Status)
		assert.Equal(t, "null", task.Data)
		assert.Equal(t, model.TaskOperationManualReview, operation.State)
		assert.True(t, operation.SettlementPending)
		assert.Empty(t, operation.EncryptedProviderTaskID)
		assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reservation.Status)
		assert.Equal(t, 2_000, reservation.RequestedQuota)
		review, err := billingsvc.GetManualReviewRelayQuotaReservation(reservation.ReservationID)
		require.NoError(t, err)
		assert.Equal(t, billingsvc.RelayQuotaReviewKindKlingTask, review.ReviewKind)
		assert.Equal(t, klingTaskPlatform, review.TaskPlatform)
		assert.False(t, review.ProviderTaskIDPresent)
		assert.False(t, review.Retryable)
		assert.True(t, review.ResolutionRequired)
		assert.ElementsMatch(t, []string{
			billingsvc.RelayQuotaReviewResolutionSettle,
			billingsvc.RelayQuotaReviewResolutionRefund,
		}, review.ResolutionOptions)
		var user model.User
		var token model.Token
		require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
		require.NoError(t, model.DB.Unscoped().First(&token, fixture.token.Id).Error)
		assert.Equal(t, 2_000_000-2_000, user.Quota)
		assert.Equal(t, 2_000_000-2_000, token.RemainQuota)

		resolved, err := ResolveKlingRelayQuotaReservationReview(
			reservation.ReservationID, 8101, billingsvc.RelayQuotaReviewResolutionRefund,
		)
		require.NoError(t, err)
		assert.True(t, resolved.Changed)
		assert.Equal(t, model.RelayQuotaReservationStatusRefunded, resolved.ReservationStatus)
		assert.Equal(t, model.TaskStatusFailure, resolved.TaskStatus)
		assert.Equal(t, model.TaskOperationRefunded, resolved.OperationState)
		require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
		require.NoError(t, model.DB.Unscoped().First(&token, fixture.token.Id).Error)
		assert.Equal(t, 2_000_000, user.Quota)
		assert.Equal(t, 2_000_000, token.RemainQuota)
		replay, err := ResolveKlingRelayQuotaReservationReview(
			reservation.ReservationID, 8102, billingsvc.RelayQuotaReviewResolutionRefund,
		)
		require.NoError(t, err)
		assert.False(t, replay.Changed)
		assert.Equal(t, resolved.AuditEventID, replay.AuditEventID)
		_, err = ResolveKlingRelayQuotaReservationReview(
			reservation.ReservationID, 8102, billingsvc.RelayQuotaReviewResolutionSettle,
		)
		assert.ErrorIs(t, err, billingsvc.ErrRelayQuotaReviewInvalidState)
	})
}

func TestKlingAmbiguousDispatchManualSettleIsAtomicAndIdempotent(t *testing.T) {
	fixture := configureKlingLifecycleFixture(t, true)
	client := &kling.Client{HTTPClient: &http.Client{Transport: klingRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection closed after Kling request write")
	})}}
	previousClient := newKlingTaskClient
	newKlingTaskClient = func() *kling.Client { return client }
	t.Cleanup(func() { newKlingTaskClient = previousClient })

	c, recorder := klingLifecycleContext(t, fixture, http.MethodPost, "/kling/v1/videos/text2video",
		`{"model":"kling-v1","prompt":"ambiguous settle"}`, "")
	RelayKlingTask(c)
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	var task model.Task
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&task).Error)
	require.NoError(t, model.DB.First(&reservation).Error)

	resolved, err := ResolveKlingRelayQuotaReservationReview(
		reservation.ReservationID, 8201, billingsvc.RelayQuotaReviewResolutionSettle,
	)
	require.NoError(t, err)
	assert.True(t, resolved.Changed)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, resolved.ReservationStatus)
	assert.Equal(t, model.TaskStatusUnknown, resolved.TaskStatus)
	assert.Equal(t, model.TaskOperationUnknown, resolved.OperationState)
	require.NoError(t, model.DB.Where("reservation_id = ?", reservation.ReservationID).First(&reservation).Error)
	require.NoError(t, model.DB.First(&task, task.ID).Error)
	assert.Equal(t, 2_000, reservation.ActualQuota)
	assert.Equal(t, fixture.channel.Id, reservation.ChannelID)
	assert.Equal(t, 2_000, task.Quota)
	assert.Equal(t, klingManualSettlementReason, task.FailReason)

	var user model.User
	var token model.Token
	var channel model.Channel
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.Unscoped().First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
	assert.Equal(t, 2_000_000-2_000, user.Quota)
	assert.Equal(t, 2_000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 2_000_000-2_000, token.RemainQuota)
	assert.Equal(t, 2_000, token.UsedQuota)
	assert.Equal(t, int64(2_000), channel.UsedQuota)
	var auditCount int64
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", klingAuditEventID(reservation.ReservationID)).Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)
	var reviewEvent model.RelayQuotaReservationReviewEvent
	require.NoError(t, model.DB.Where("event_id = ?", resolved.AuditEventID).First(&reviewEvent).Error)
	assert.Equal(t, 8201, reviewEvent.OperatorUserID)
	assert.Equal(t, billingsvc.RelayQuotaReviewActionResolveSettle, reviewEvent.Action)

	replay, err := ResolveKlingRelayQuotaReservationReview(
		reservation.ReservationID, 8202, billingsvc.RelayQuotaReviewResolutionSettle,
	)
	require.NoError(t, err)
	assert.False(t, replay.Changed)
	assert.Equal(t, resolved.AuditEventID, replay.AuditEventID)
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", klingAuditEventID(reservation.ReservationID)).Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)
}

func TestKlingPlatformNeverEntersSoraFamily(t *testing.T) {
	assert.True(t, model.IsKlingTaskOperationPlatform(model.TaskOperationPlatformKling))
	assert.False(t, model.IsOpenAIVideoTaskOperationPlatform(model.TaskOperationPlatformKling))
	assert.NotContains(t, model.OpenAIVideoTaskOperationPlatforms(), model.TaskOperationPlatformKling)
}
