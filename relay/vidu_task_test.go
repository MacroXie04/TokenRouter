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
	"sync"
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
	"github.com/tokenrouter/tokenrouter/relay/channel/vidu"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

type fakeViduProvider struct {
	submit func(context.Context, string, string, *vidu.PreparedRequest) (*vidu.Task, []byte, error)
	fetch  func(context.Context, string, string, string) (*vidu.Task, []byte, error)
}

type viduRelayRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn viduRelayRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func (provider *fakeViduProvider) Submit(ctx context.Context, baseURL, key string,
	prepared *vidu.PreparedRequest) (*vidu.Task, []byte, error) {
	return provider.submit(ctx, baseURL, key, prepared)
}

func (provider *fakeViduProvider) Fetch(ctx context.Context, baseURL, key, id string) (*vidu.Task, []byte, error) {
	return provider.fetch(ctx, baseURL, key, id)
}

func configureViduLifecycleFixture(t *testing.T, fixed bool) relayAccountingFixture {
	t.Helper()
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "vidu-test=0123456789abcdef0123456789abcdef")
	t.Setenv("VIDU_TASK_RECOVERY_DIR", t.TempDir())
	fixture := newRelayAccountingFixture(t, 2_000_000, 2_000_000)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Task{}, &model.TaskOperation{}, &model.JimengTaskOperation{},
		&model.AuditLogOutbox{}, &model.RelayQuotaReservationReviewEvent{},
	))
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", fixture.token.Id).
		Update("group", service.GroupDefault).Error)
	fixture.token.Group = service.GroupDefault
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Updates(map[string]any{"type": int(constant.ChannelTypeVidu), "key": "upstream-vidu-key",
			"base_url": "https://vidu.example", "models": strings.Join(vidu.ModelList, ",")}).Error)
	require.NoError(t, model.DB.Where("channel_id = ?", fixture.channel.Id).Delete(&model.Ability{}).Error)
	for _, modelName := range vidu.ModelList {
		require.NoError(t, model.DB.Create(&model.Ability{Group: service.GroupDefault, Model: modelName,
			ChannelId: fixture.channel.Id, Enabled: true, Weight: 1}).Error)
	}
	require.NoError(t, service.InitAbilityCache())
	keys := []string{setting.ModelBillingModeOption, setting.PerCallModelPriceOption,
		setting.ModelRatioOption, setting.CompletionRatioOption, setting.GroupRatioOption,
		setting.GroupGroupRatioOption}
	previous := setting.GetOptions(keys...)
	t.Cleanup(func() { _ = setting.UpdateOptions(previous) })
	price, ratio := `{"viduq1":0.004,"viduq2":0.004,"vidu2.0":0.004,"vidu1.5":0.004}`, `{}`
	if !fixed {
		price, ratio = `{}`, `{"viduq1":2,"viduq2":2,"vidu2.0":2,"vidu1.5":2}`
	}
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"viduq1":"reference","viduq2":"reference","vidu2.0":"reference","vidu1.5":"reference"}`,
		setting.PerCallModelPriceOption: price,
		setting.ModelRatioOption:        ratio,
		setting.CompletionRatioOption:   `{"viduq1":99}`,
		setting.GroupRatioOption:        `{"default":1}`,
		setting.GroupGroupRatioOption:   `{}`,
	}))
	return fixture
}

func viduLifecycleContext(t *testing.T, fixture relayAccountingFixture, method, path, body, taskID string) (*gin.Context, *httptest.ResponseRecorder) {
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

func decodeViduPublicID(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response viduVideoResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.NoError(t, validateViduTaskPublicID(response.ID))
	return response.ID
}

func installViduProvider(t *testing.T, provider viduProvider) {
	t.Helper()
	previous := newViduProvider
	newViduProvider = func() viduProvider { return provider }
	t.Cleanup(func() { newViduProvider = previous })
}

func forceViduRecoveryDue(t *testing.T, taskID string) {
	t.Helper()
	require.NoError(t, model.DB.Model(&model.TaskOperation{}).Where("task_id = ?", taskID).
		Updates(map[string]any{"next_attempt_at": 0, "lease_owner": "", "lease_expires_at": 0}).Error)
}

func TestViduGenericRoutesSubmitAndFetchStayLocal(t *testing.T) {
	fixture := configureViduLifecycleFixture(t, true)
	var submitCalls, fetchCalls atomic.Int32
	var mu sync.Mutex
	actions := make([]vidu.Action, 0, 4)
	provider := &fakeViduProvider{
		submit: func(_ context.Context, baseURL, key string, prepared *vidu.PreparedRequest) (*vidu.Task, []byte, error) {
			assert.Equal(t, "https://vidu.example", baseURL)
			assert.Equal(t, "upstream-vidu-key", key)
			call := submitCalls.Add(1)
			mu.Lock()
			actions = append(actions, prepared.Action)
			mu.Unlock()
			return &vidu.Task{ProviderTaskID: "provider-" + string(rune('0'+call)), Status: vidu.StatusSubmitted}, nil, nil
		},
		fetch: func(context.Context, string, string, string) (*vidu.Task, []byte, error) {
			fetchCalls.Add(1)
			return nil, nil, errors.New("local fetch contacted provider")
		},
	}
	installViduProvider(t, provider)
	tests := []struct {
		path string
		body string
	}{
		{"/v1/videos", `{"model":"viduq1","prompt":"text"}`},
		{"/v1/video/generations", `{"model":"viduq1","prompt":"image","images":["a"]}`},
		{"/v1/videos", `{"model":"viduq1","prompt":"ends","images":["a","b"]}`},
		{"/v1/video/generations", `{"model":"viduq1","prompt":"refs","images":["a","b","c"]}`},
	}
	publicIDs := make([]string, 0, len(tests))
	for _, test := range tests {
		c, recorder := viduLifecycleContext(t, fixture, http.MethodPost, test.path, test.body, "")
		RelayVideoTask(c)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		publicID := decodeViduPublicID(t, recorder)
		publicIDs = append(publicIDs, publicID)
		assert.NotContains(t, recorder.Body.String(), "provider-")
		assert.NotContains(t, recorder.Body.String(), "upstream-vidu-key")
		var task model.Task
		require.NoError(t, model.DB.Where("task_id = ?", publicID).First(&task).Error)
		assert.Equal(t, viduTaskPlatform, task.Platform)
		assert.Equal(t, model.TaskStatusSubmitted, task.Status)
		assert.Equal(t, 2_000, task.Quota)
		assert.NotContains(t, task.PrivateData, "provider-")
		assert.NotContains(t, task.PrivateData, "upstream-vidu-key")
		var operation model.TaskOperation
		require.NoError(t, model.DB.Where("task_id = ?", publicID).First(&operation).Error)
		assert.Equal(t, model.TaskOperationSubmitted, operation.State)
		assert.False(t, operation.SettlementPending)
		var reservation model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
		assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
		assert.Equal(t, 2_000, reservation.ActualQuota)
	}
	assert.Equal(t, []vidu.Action{vidu.ActionTextGenerate, vidu.ActionGenerate,
		vidu.ActionFirstTailGenerate, vidu.ActionReferenceGenerate}, actions)

	for _, route := range []string{"/v1/video/generations/", "/v1/videos/"} {
		c, recorder := viduLifecycleContext(t, fixture, http.MethodGet, route+publicIDs[0], "", publicIDs[0])
		RelayVideoTaskFetch(c)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), publicIDs[0])
		assert.NotContains(t, recorder.Body.String(), "provider-")
	}
	assert.Zero(t, fetchCalls.Load())
	assert.Equal(t, int32(4), submitCalls.Load())

	c, recorder := viduLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+publicIDs[0], "", publicIDs[0])
	common.SetUserId(c, fixture.user.Id+1000)
	RelayVideoTaskFetch(c)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestViduAcceptedRatioIgnoresCreditsAndPollsExactlyOnce(t *testing.T) {
	fixture := configureViduLifecycleFixture(t, false)
	var fetchCalls atomic.Int32
	providerID := "provider-ratio"
	provider := &fakeViduProvider{
		submit: func(context.Context, string, string, *vidu.PreparedRequest) (*vidu.Task, []byte, error) {
			return &vidu.Task{ProviderTaskID: providerID, Status: vidu.StatusSubmitted}, nil, nil
		},
		fetch: func(_ context.Context, baseURL, key, id string) (*vidu.Task, []byte, error) {
			fetchCalls.Add(1)
			assert.Equal(t, "https://vidu.example", baseURL)
			assert.Equal(t, "upstream-vidu-key", key)
			assert.Equal(t, providerID, id)
			return &vidu.Task{ProviderTaskID: id, Status: vidu.StatusSucceeded, Credits: 987654,
				Creations: []vidu.Creation{{ID: "creation-1", URL: "https://cdn.example/result.mp4"}},
				ResultURL: "https://cdn.example/result.mp4"}, nil, nil
		},
	}
	installViduProvider(t, provider)
	c, recorder := viduLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"viduq1","prompt":"ratio"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeViduPublicID(t, recorder)
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, common.QuotaPerUnit, reservation.ActualQuota)

	// Hot pricing changes cannot alter the immutable half-unit Vidu charge.
	require.NoError(t, setting.UpdateOptions(map[string]string{setting.ModelRatioOption: `{"viduq1":99}`,
		setting.GroupRatioOption: `{"default":9}`}))
	// Channel edits after acceptance cannot redirect or re-authenticate recovery.
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Updates(map[string]any{"base_url": "https://changed.example", "key": "changed-key"}).Error)
	forceViduRecoveryDue(t, taskID)
	require.NoError(t, reconcileAsyncViduTasks(context.Background()))
	var task model.Task
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	require.NoError(t, model.DB.Where("id = ?", operation.ID).First(&operation).Error)
	require.NoError(t, model.DB.Where("id = ?", reservation.ID).First(&reservation).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.Equal(t, common.QuotaPerUnit, task.Quota)
	assert.Equal(t, common.QuotaPerUnit, reservation.ActualQuota)
	assert.Equal(t, model.TaskOperationTerminal, operation.State)
	assert.Contains(t, task.Data, `"credits":987654`)
	assert.Contains(t, task.Data, "https://cdn.example/result.mp4")
	require.NoError(t, reconcileAsyncViduTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())
}

func TestViduCompletedContentIsOwnerScopedAndUsesSafeLocalProxy(t *testing.T) {
	fixture := configureViduLifecycleFixture(t, true)
	var contentCalls atomic.Int32
	previousContentClient := newViduContentHTTPClient
	newViduContentHTTPClient = func() *http.Client {
		return &http.Client{Transport: viduRelayRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			contentCalls.Add(1)
			assert.Empty(t, request.Header.Get("Authorization"), "channel credentials must never reach a result host")
			assert.Equal(t, "https://cdn.example/result.mp4", request.URL.String())
			return &http.Response{StatusCode: http.StatusOK,
				Header: http.Header{"Content-Type": []string{"video/mp4"}},
				Body:   io.NopCloser(strings.NewReader("vidu-video-bytes")), Request: request}, nil
		})}
	}
	t.Cleanup(func() { newViduContentHTTPClient = previousContentClient })
	provider := &fakeViduProvider{
		submit: func(context.Context, string, string, *vidu.PreparedRequest) (*vidu.Task, []byte, error) {
			return &vidu.Task{ProviderTaskID: "provider-content", Status: vidu.StatusSubmitted}, nil, nil
		},
		fetch: func(_ context.Context, _, _, id string) (*vidu.Task, []byte, error) {
			return &vidu.Task{ProviderTaskID: id, Status: vidu.StatusSucceeded,
				Creations: []vidu.Creation{{URL: "https://cdn.example/result.mp4"}},
				ResultURL: "https://cdn.example/result.mp4"}, nil, nil
		},
	}
	installViduProvider(t, provider)
	c, recorder := viduLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"viduq1","prompt":"content"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeViduPublicID(t, recorder)
	forceViduRecoveryDue(t, taskID)
	require.NoError(t, reconcileAsyncViduTasks(context.Background()))

	c, recorder = viduLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID+"/content", "", taskID)
	VideoProxy(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "video/mp4", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	assert.Contains(t, recorder.Header().Get("Content-Security-Policy"), "sandbox")
	assert.Equal(t, "vidu-video-bytes", recorder.Body.String())

	c, recorder = viduLifecycleContext(t, fixture, http.MethodGet, "/v1/videos/"+taskID+"/content", "", taskID)
	common.SetUserId(c, fixture.user.Id+1)
	VideoProxy(c)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, int32(1), contentCalls.Load())
}

func TestViduDefinitiveRejectionRefundsAndInsufficientQuotaNeverDispatches(t *testing.T) {
	t.Run("definitive rejection", func(t *testing.T) {
		fixture := configureViduLifecycleFixture(t, true)
		var submitCalls atomic.Int32
		installViduProvider(t, &fakeViduProvider{
			submit: func(context.Context, string, string, *vidu.PreparedRequest) (*vidu.Task, []byte, error) {
				submitCalls.Add(1)
				return nil, nil, &vidu.RequestError{Err: &relaycommon.UpstreamError{StatusCode: http.StatusBadRequest}, Dispatched: true}
			},
			fetch: func(context.Context, string, string, string) (*vidu.Task, []byte, error) {
				return nil, nil, errors.New("unexpected fetch")
			},
		})
		c, recorder := viduLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
			`{"model":"viduq1","prompt":"reject"}`, "")
		RelayVideoTask(c)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.Equal(t, int32(1), submitCalls.Load())
		var task model.Task
		var operation model.TaskOperation
		var reservation model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.First(&task).Error)
		require.NoError(t, model.DB.First(&operation).Error)
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

	t.Run("authoritative failed response without id", func(t *testing.T) {
		fixture := configureViduLifecycleFixture(t, true)
		installViduProvider(t, &fakeViduProvider{
			submit: func(context.Context, string, string, *vidu.PreparedRequest) (*vidu.Task, []byte, error) {
				return &vidu.Task{Status: vidu.StatusFailed}, nil, nil
			},
			fetch: func(context.Context, string, string, string) (*vidu.Task, []byte, error) {
				return nil, nil, errors.New("unexpected fetch")
			},
		})
		c, recorder := viduLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
			`{"model":"viduq1","prompt":"failed body"}`, "")
		RelayVideoTask(c)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var task model.Task
		var operation model.TaskOperation
		var reservation model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.First(&task).Error)
		require.NoError(t, model.DB.First(&operation).Error)
		require.NoError(t, model.DB.First(&reservation).Error)
		assert.Equal(t, model.TaskStatusFailure, task.Status)
		assert.Equal(t, model.TaskOperationRefunded, operation.State)
		assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	})

	t.Run("insufficient quota", func(t *testing.T) {
		fixture := configureViduLifecycleFixture(t, true)
		require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", fixture.user.Id).Update("quota", 0).Error)
		require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", fixture.token.Id).Update("remain_quota", 0).Error)
		fixture.token.RemainQuota = 0
		var submitCalls atomic.Int32
		installViduProvider(t, &fakeViduProvider{
			submit: func(context.Context, string, string, *vidu.PreparedRequest) (*vidu.Task, []byte, error) {
				submitCalls.Add(1)
				return nil, nil, errors.New("must not dispatch")
			},
			fetch: func(context.Context, string, string, string) (*vidu.Task, []byte, error) {
				return nil, nil, errors.New("must not fetch")
			},
		})
		c, recorder := viduLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
			`{"model":"viduq1","prompt":"no quota"}`, "")
		RelayVideoTask(c)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.Zero(t, submitCalls.Load())
		for table, destination := range map[string]any{
			"tasks": &model.Task{}, "task operations": &model.TaskOperation{},
			"reservations": &model.RelayQuotaReservationRecord{},
		} {
			var count int64
			require.NoError(t, model.DB.Model(destination).Count(&count).Error, table)
			assert.Zero(t, count, table)
		}
	})
}

func TestViduTerminalFailureReversesSettledCharge(t *testing.T) {
	fixture := configureViduLifecycleFixture(t, true)
	var fetchCalls atomic.Int32
	provider := &fakeViduProvider{
		submit: func(context.Context, string, string, *vidu.PreparedRequest) (*vidu.Task, []byte, error) {
			return &vidu.Task{ProviderTaskID: "provider-failure", Status: vidu.StatusSubmitted}, nil, nil
		},
		fetch: func(context.Context, string, string, string) (*vidu.Task, []byte, error) {
			fetchCalls.Add(1)
			return &vidu.Task{ProviderTaskID: "provider-failure", Status: vidu.StatusFailed,
				ErrorCode: "CONTENT_POLICY", Credits: 77}, nil, nil
		},
	}
	installViduProvider(t, provider)
	c, recorder := viduLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"viduq1","prompt":"fail later"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeViduPublicID(t, recorder)
	forceViduRecoveryDue(t, taskID)
	require.NoError(t, reconcileAsyncViduTasks(context.Background()))
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
	assert.Equal(t, "CONTENT_POLICY", task.FailReason)
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationReversed, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusReversed, reservation.Status)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
	var refundAudit int64
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", "vidu-refund:"+reservation.ReservationID).Count(&refundAudit).Error)
	assert.EqualValues(t, 1, refundAudit)
	require.NoError(t, reconcileAsyncViduTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())
}

func TestViduAmbiguousDispatchIsHeldForExplicitReview(t *testing.T) {
	fixture := configureViduLifecycleFixture(t, true)
	provider := &fakeViduProvider{
		submit: func(context.Context, string, string, *vidu.PreparedRequest) (*vidu.Task, []byte, error) {
			return nil, nil, &vidu.RequestError{Err: errors.New("connection reset after write"), Dispatched: true}
		},
		fetch: func(context.Context, string, string, string) (*vidu.Task, []byte, error) {
			return nil, nil, errors.New("unexpected fetch")
		},
	}
	installViduProvider(t, provider)
	c, recorder := viduLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"viduq1","prompt":"ambiguous"}`, "")
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
	review, err := service.GetManualReviewRelayQuotaReservation(reservation.ReservationID)
	require.NoError(t, err)
	assert.Equal(t, service.RelayQuotaReviewKindViduTask, review.ReviewKind)
	assert.Equal(t, viduTaskPlatform, review.TaskPlatform)
	assert.True(t, review.ResolutionRequired)
	assert.False(t, review.ProviderTaskIDPresent)

	resolved, err := ResolveViduRelayQuotaReservationReview(reservation.ReservationID, 9101,
		service.RelayQuotaReviewResolutionRefund)
	require.NoError(t, err)
	assert.True(t, resolved.Changed)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, resolved.ReservationStatus)
	assert.Equal(t, model.TaskStatusFailure, resolved.TaskStatus)
	replay, err := ResolveViduRelayQuotaReservationReview(reservation.ReservationID, 9102,
		service.RelayQuotaReviewResolutionRefund)
	require.NoError(t, err)
	assert.False(t, replay.Changed)
	assert.Equal(t, resolved.AuditEventID, replay.AuditEventID)
}

func TestViduAmbiguousDispatchCanBeExplicitlySettledOnce(t *testing.T) {
	fixture := configureViduLifecycleFixture(t, true)
	installViduProvider(t, &fakeViduProvider{
		submit: func(context.Context, string, string, *vidu.PreparedRequest) (*vidu.Task, []byte, error) {
			return nil, nil, &vidu.RequestError{Err: errors.New("ambiguous write"), Dispatched: true}
		},
		fetch: func(context.Context, string, string, string) (*vidu.Task, []byte, error) {
			return nil, nil, errors.New("unexpected fetch")
		},
	})
	c, recorder := viduLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"viduq1","prompt":"operator settle"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&reservation).Error)
	resolved, err := ResolveViduRelayQuotaReservationReview(reservation.ReservationID, 9103,
		service.RelayQuotaReviewResolutionSettle)
	require.NoError(t, err)
	assert.True(t, resolved.Changed)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, resolved.ReservationStatus)
	assert.Equal(t, model.TaskStatusUnknown, resolved.TaskStatus)
	assert.Equal(t, model.TaskOperationUnknown, resolved.OperationState)

	var user model.User
	var token model.Token
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.Unscoped().First(&token, fixture.token.Id).Error)
	assert.Equal(t, 2_000_000-2_000, user.Quota)
	assert.Equal(t, 2_000_000-2_000, token.RemainQuota)
	var consumeAudit int64
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", viduAuditEventID(reservation.ReservationID)).Count(&consumeAudit).Error)
	assert.EqualValues(t, 1, consumeAudit)
	replay, err := ResolveViduRelayQuotaReservationReview(reservation.ReservationID, 9104,
		service.RelayQuotaReviewResolutionSettle)
	require.NoError(t, err)
	assert.False(t, replay.Changed)
	assert.Equal(t, resolved.AuditEventID, replay.AuditEventID)
}

func createPreparedViduTaskForRecovery(t *testing.T, fixture relayAccountingFixture) (*model.Task, *service.RelayQuotaReservation) {
	t.Helper()
	pricing, enabled, err := service.ResolveReferenceAsyncTaskBillingPlan("viduq1", "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	quota, err := pricing.PreConsumeQuota()
	require.NoError(t, err)
	taskID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	properties, err := marshalViduTaskProperties(viduTaskProperties{Version: 1, Family: "vidu", Input: "restart",
		OriginModelName: "viduq1", UpstreamModelName: "viduq1", Action: vidu.ActionTextGenerate,
		Duration: vidu.DefaultDuration, Resolution: vidu.DefaultResolution, Pricing: pricing})
	require.NoError(t, err)
	now := common.NowTimestamp()
	task := &model.Task{CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: viduTaskPlatform,
		UserId: fixture.user.Id, Group: "default", ChannelId: fixture.channel.Id, Quota: quota,
		Action: string(vidu.ActionTextGenerate), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: properties, Data: "null"}
	baseURL := "https://vidu.example"
	encryptedKey, err := asyncTaskEncryptBound("upstream-vidu-key",
		viduChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, baseURL))
	require.NoError(t, err)
	privateData := viduTaskPrivateData{Version: 1, ChannelBaseURL: baseURL,
		EncryptedChannelKey: encryptedKey, Pricing: pricing}
	reservation, err := createViduReservedTask(task, &fixture.token, &privateData)
	require.NoError(t, err)
	return task, reservation
}

func TestViduAcceptedJournalPromotesAcrossRestart(t *testing.T) {
	fixture := configureViduLifecycleFixture(t, true)
	task, reservation := createPreparedViduTaskForRecovery(t, fixture)
	require.NoError(t, markViduTaskDispatching(task, reservation))
	require.NoError(t, persistAcceptedViduRecoveryJournal(task, reservation.ReservationID(),
		"provider-restart", vidu.ActionTextGenerate))
	path, err := viduRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)
	disk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(disk), "provider-restart")
	assert.NotContains(t, string(disk), "upstream-vidu-key")
	require.NoError(t, PromoteViduTaskRecoveryJournalsContext(context.Background()))
	_, err = os.Stat(path)
	assert.True(t, errors.Is(err, os.ErrNotExist))
	var operation model.TaskOperation
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.True(t, operation.SettlementPending)
	assert.NotEmpty(t, operation.EncryptedProviderTaskID)
	assert.NotContains(t, operation.EncryptedProviderTaskID, "provider-restart")

	provider := &fakeViduProvider{
		submit: func(context.Context, string, string, *vidu.PreparedRequest) (*vidu.Task, []byte, error) {
			return nil, nil, errors.New("unexpected submit")
		},
		fetch: func(_ context.Context, _, _, id string) (*vidu.Task, []byte, error) {
			return &vidu.Task{ProviderTaskID: id, Status: vidu.StatusSucceeded,
				Creations: []vidu.Creation{{URL: "https://cdn.example/restart.mp4"}},
				ResultURL: "https://cdn.example/restart.mp4"}, nil, nil
		},
	}
	installViduProvider(t, provider)
	forceViduRecoveryDue(t, task.TaskID)
	require.NoError(t, reconcileAsyncViduTasks(context.Background())) // settles immutable hold
	forceViduRecoveryDue(t, task.TaskID)
	require.NoError(t, reconcileAsyncViduTasks(context.Background())) // polls provider
	require.NoError(t, model.DB.Where("id = ?", task.ID).First(task).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
}

func TestViduRecoveryLeasePreventsConcurrentPolls(t *testing.T) {
	fixture := configureViduLifecycleFixture(t, true)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var fetchCalls atomic.Int32
	provider := &fakeViduProvider{
		submit: func(context.Context, string, string, *vidu.PreparedRequest) (*vidu.Task, []byte, error) {
			return &vidu.Task{ProviderTaskID: "provider-concurrent", Status: vidu.StatusSubmitted}, nil, nil
		},
		fetch: func(_ context.Context, _, _, id string) (*vidu.Task, []byte, error) {
			fetchCalls.Add(1)
			started <- struct{}{}
			<-release
			return &vidu.Task{ProviderTaskID: id, Status: vidu.StatusProcessing}, nil, nil
		},
	}
	installViduProvider(t, provider)
	c, recorder := viduLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"viduq1","prompt":"concurrent"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeViduPublicID(t, recorder)
	forceViduRecoveryDue(t, taskID)
	firstDone := make(chan error, 1)
	go func() { firstDone <- reconcileAsyncViduTasks(context.Background()) }()
	<-started
	require.NoError(t, reconcileAsyncViduTasks(context.Background()))
	close(release)
	require.NoError(t, <-firstDone)
	assert.Equal(t, int32(1), fetchCalls.Load())
}

func TestRetryViduTaskManualReviewIsAuditedIdempotentAndDoesNotRebill(t *testing.T) {
	fixture := configureViduLifecycleFixture(t, true)
	provider := &fakeViduProvider{
		submit: func(context.Context, string, string, *vidu.PreparedRequest) (*vidu.Task, []byte, error) {
			return &vidu.Task{ProviderTaskID: "provider-manual", Status: vidu.StatusSubmitted}, nil, nil
		},
		fetch: func(context.Context, string, string, string) (*vidu.Task, []byte, error) {
			return nil, nil, errors.New("poll should not run during review retry")
		},
	}
	installViduProvider(t, provider)
	c, recorder := viduLifecycleContext(t, fixture, http.MethodPost, "/v1/videos",
		`{"model":"viduq1","prompt":"manual poll"}`, "")
	RelayVideoTask(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	taskID := decodeViduPublicID(t, recorder)

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
		"status": model.TaskStatusUnknown, "fail_reason": viduPollingManualReviewReason,
		"finish_time": now, "updated_at": now,
	}).Error)
	require.NoError(t, model.DB.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
		"state": model.TaskOperationManualReview, "settlement_pending": false,
		"attempts": viduOperationMaxAttempts, "next_attempt_at": 0,
		"lease_owner": "", "lease_expires_at": 0, "last_error": viduPollingManualReviewReason,
		"created_at": oldCreatedAt, "updated_at": now, "completed_at": now,
	}).Error)

	review, err := service.GetManualReviewRelayQuotaReservation(reservation.ReservationID)
	require.NoError(t, err)
	assert.Equal(t, service.RelayQuotaReviewKindViduTask, review.ReviewKind)
	assert.True(t, review.ProviderTaskIDPresent)
	assert.True(t, review.Retryable)
	assert.Equal(t, model.TaskOperationSubmitted, review.RetryTargetStatus)

	var beforeUser model.User
	var beforeToken model.Token
	var beforeChannel model.Channel
	require.NoError(t, model.DB.First(&beforeUser, fixture.user.Id).Error)
	require.NoError(t, model.DB.Unscoped().First(&beforeToken, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&beforeChannel, fixture.channel.Id).Error)
	result, err := RetryViduTaskManualReview(reservation.ReservationID, 8401)
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Equal(t, taskID, result.TaskID)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, result.ReservationStatus)
	assert.Equal(t, model.TaskStatusSubmitted, result.TaskStatus)
	assert.Equal(t, model.TaskOperationSubmitted, result.OperationState)
	assert.NotEmpty(t, result.AuditEventID)

	require.NoError(t, model.DB.Where("id = ?", operation.ID).First(&operation).Error)
	assert.Zero(t, operation.Attempts)
	assert.Zero(t, operation.CompletedAt)
	assert.Greater(t, operation.CreatedAt, oldCreatedAt)
	assert.Equal(t, operation.CreatedAt, operation.NextAttemptAt)
	replay, err := RetryViduTaskManualReview(reservation.ReservationID, 8402)
	require.NoError(t, err)
	assert.False(t, replay.Changed)
	assert.Equal(t, result.AuditEventID, replay.AuditEventID)

	var eventCount int64
	require.NoError(t, model.DB.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ? AND action = ?", reservation.ReservationID,
			model.RelayQuotaReservationReviewActionRetryTaskPoll).Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount)
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

func TestViduPlatformNeverEntersSoraFamily(t *testing.T) {
	assert.True(t, model.IsViduTaskOperationPlatform(model.TaskOperationPlatformVidu))
	assert.False(t, model.IsOpenAIVideoTaskOperationPlatform(model.TaskOperationPlatformVidu))
	assert.NotContains(t, model.OpenAIVideoTaskOperationPlatforms(), model.TaskOperationPlatformVidu)
}
