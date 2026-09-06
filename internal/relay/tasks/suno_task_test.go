package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/suno"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type sunoTaskFixture struct {
	router  *gin.Engine
	db      *gorm.DB
	user    model.User
	token   model.Token
	channel model.Channel
}

type sunoUndispatchedProvider struct{}

func (sunoUndispatchedProvider) Submit(context.Context, string, string, *suno.PreparedRequest) (string, error) {
	return "", &suno.RequestError{Err: errors.New("local provider configuration rejected"), Dispatched: false}
}

func (sunoUndispatchedProvider) Fetch(context.Context, string, string, []string) ([]suno.TaskResult, error) {
	return nil, errors.New("unexpected fetch")
}

func newSunoTaskFixture(t *testing.T, upstream *httptest.Server) sunoTaskFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "suno-test=0123456789abcdef0123456789abcdef")
	t.Setenv("SUNO_TASK_RECOVERY_DIR", t.TempDir())
	oldDB, oldLogDB := model.DB, model.LOG_DB
	dsn := "file:" + filepath.Join(t.TempDir(), "suno-task.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{},
		&model.Task{}, &model.TaskOperation{}, &model.Log{}, &model.AuditLogOutbox{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{},
		&model.RelayQuotaReservationRecord{}, &model.Option{},
	))
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLogDB })
	require.NoError(t, setting.Init())
	require.NoError(t, billingsvc.UpdateGroupRatioOption(`{"default":1,"vip":1}`))
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"suno_music":"reference","suno_lyrics":"reference"}`,
		setting.PerCallModelPriceOption: `{"suno_music":0.1,"suno_lyrics":0.01}`,
		setting.ModelRatioOption:        `{}`,
		setting.CompletionRatioOption:   `{}`,
		setting.UserUsableGroupsOption:  `{"vip":"VIP"}`,
	}))

	user := model.User{
		Username: "suno-owner", Password: "x", Role: roles.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: "default", Quota: 2_000_000, AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-suno-owner", Name: "suno-token",
		Status: billingsvc.TokenStatusEnabled, RemainQuota: 2_000_000,
	}
	require.NoError(t, db.Create(&token).Error)
	weight := uint(1)
	channel := model.Channel{
		Name: "suno-mock", Type: int(channelcatalog.ChannelTypeSunoAPI), Key: "upstream-suno-key",
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: upstream.URL,
		Models: "suno_music,suno_lyrics", Group: "default", Weight: &weight,
	}
	require.NoError(t, db.Create(&channel).Error)
	for _, modelName := range suno.ModelList {
		require.NoError(t, db.Create(&model.Ability{
			Group: "default", Model: modelName, ChannelId: channel.Id, Enabled: true, Weight: 1,
		}).Error)
	}
	require.NoError(t, channelssvc.InitAbilityCache())

	previousFactory := newSunoProvider
	newSunoProvider = func() sunoProvider { return &suno.Client{HTTPClient: upstream.Client()} }
	t.Cleanup(func() { newSunoProvider = previousFactory })
	router := gin.New()
	group := router.Group("/suno")
	group.Use(middleware.TokenAuth())
	group.POST("/submit/:action", RelaySunoTask)
	group.POST("/fetch", RelaySunoTaskFetch)
	group.GET("/fetch/:id", RelaySunoTaskFetch)
	return sunoTaskFixture{router: router, db: db, user: user, token: token, channel: channel}
}

func (fixture sunoTaskFixture) request(method, path, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	fixture.router.ServeHTTP(recorder, request)
	return recorder
}

func decodeSunoSubmitID(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.NoError(t, validateSunoTaskPublicID(response.Data))
	return response.Data
}

func forceSunoTaskRecoveryDue(t *testing.T, db *gorm.DB, taskIDs ...string) {
	t.Helper()
	result := db.Model(&model.TaskOperation{}).Where("task_id IN ?", taskIDs).
		Updates(map[string]any{"next_attempt_at": 0, "lease_owner": "", "lease_expires_at": 0})
	require.NoError(t, result.Error)
	require.Equal(t, int64(len(taskIDs)), result.RowsAffected)
}

func createPreparedSunoTaskForRecovery(
	t *testing.T,
	fixture sunoTaskFixture,
	action suno.Action,
) (*model.Task, *billingsvc.RelayQuotaReservation) {
	t.Helper()
	modelName, ok := suno.ModelForAction(action)
	require.True(t, ok)
	pricing, enabled, err := billingsvc.ResolveReferenceAsyncTaskBillingPlan(modelName, "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	quota, err := pricing.PreConsumeQuota()
	require.NoError(t, err)
	properties, err := marshalSunoTaskProperties(sunoTaskProperties{
		Version: 1, OriginModelName: modelName, Action: action, Pricing: pricing,
	})
	require.NoError(t, err)
	taskID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	now := wallclock.NowTimestamp()
	task := &model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: sunoTaskPlatform,
		UserId: fixture.user.Id, Group: "default", ChannelId: fixture.channel.Id,
		Quota: quota, Action: string(action), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: properties, Data: "null",
	}
	encryptedKey, err := asyncTaskEncryptBound("upstream-suno-key",
		sunoChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, fixture.channel.BaseURL))
	require.NoError(t, err)
	privateData := sunoTaskPrivateData{ChannelBaseURL: fixture.channel.BaseURL, EncryptedChannelKey: encryptedKey}
	reservation, err := createSunoReservedTask(task, &fixture.token, &privateData)
	require.NoError(t, err)
	return task, reservation
}

func createSunoPollManualReviewFixture(
	t *testing.T,
) (sunoTaskFixture, *model.Task, *billingsvc.RelayQuotaReservation, model.TaskOperation) {
	t.Helper()
	upstream := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(upstream.Close)
	fixture := newSunoTaskFixture(t, upstream)
	require.NoError(t, fixture.db.AutoMigrate(&model.RelayQuotaReservationReviewEvent{}))
	task, reservation := createPreparedSunoTaskForRecovery(t, fixture, suno.ActionMusic)
	require.NoError(t, markSunoTaskDispatching(task, reservation))
	require.NoError(t, settleAcceptedSunoTask(
		task, reservation, "provider-manual-review", model.TaskOperationDispatching, "",
	))
	now, err := model.DatabaseUnixTimestamp(fixture.db)
	require.NoError(t, err)
	require.NoError(t, fixture.db.Model(&model.Task{}).Where("id = ?", task.ID).
		Updates(map[string]any{
			"status": model.TaskStatusUnknown, "fail_reason": sunoPollingManualReviewReason,
			"progress": "100%", "finish_time": now, "updated_at": now,
		}).Error)
	require.NoError(t, fixture.db.Model(&model.TaskOperation{}).Where("task_id = ?", task.TaskID).
		Updates(map[string]any{
			"state": model.TaskOperationManualReview, "attempts": sunoOperationMaxAttempts,
			"next_attempt_at": 0, "completed_at": now, "updated_at": now,
			"last_error": sunoPollingManualReviewReason, "lease_owner": "", "lease_expires_at": 0,
		}).Error)
	require.NoError(t, fixture.db.First(task, task.ID).Error)
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ? AND platform = ?", task.TaskID, sunoTaskPlatform).
		First(&operation).Error)
	return fixture, task, reservation, operation
}

func TestRetrySunoTaskManualReviewIsAuditedIdempotentAndDoesNotRebill(t *testing.T) {
	fixture, task, reservation, beforeOperation := createSunoPollManualReviewFixture(t)
	var beforeRecord model.RelayQuotaReservationRecord
	var beforeUser model.User
	var beforeToken model.Token
	var beforeChannel model.Channel
	require.NoError(t, fixture.db.Where("reservation_id = ?", reservation.ReservationID()).First(&beforeRecord).Error)
	require.NoError(t, fixture.db.First(&beforeUser, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&beforeToken, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&beforeChannel, fixture.channel.Id).Error)

	result, err := RetrySunoTaskManualReview(reservation.ReservationID(), 8501)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Changed)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, result.ReservationStatus)
	assert.Equal(t, model.TaskStatusSubmitted, result.TaskStatus)
	assert.Equal(t, model.TaskOperationSubmitted, result.OperationState)
	assert.NotEmpty(t, result.AuditEventID)

	var afterRecord model.RelayQuotaReservationRecord
	var afterTask model.Task
	var afterOperation model.TaskOperation
	var afterUser model.User
	var afterToken model.Token
	var afterChannel model.Channel
	require.NoError(t, fixture.db.Where("reservation_id = ?", reservation.ReservationID()).First(&afterRecord).Error)
	require.NoError(t, fixture.db.First(&afterTask, task.ID).Error)
	require.NoError(t, fixture.db.First(&afterOperation, beforeOperation.ID).Error)
	require.NoError(t, fixture.db.First(&afterUser, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&afterToken, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&afterChannel, fixture.channel.Id).Error)
	assert.Equal(t, beforeRecord, afterRecord, "poll retry must not mutate the settled ledger")
	assert.Equal(t, beforeUser, afterUser)
	assert.Equal(t, beforeToken, afterToken)
	assert.Equal(t, beforeChannel, afterChannel)
	assert.Equal(t, model.TaskStatusSubmitted, afterTask.Status)
	assert.Empty(t, afterTask.FailReason)
	assert.Zero(t, afterTask.FinishTime)
	assert.Equal(t, model.TaskOperationSubmitted, afterOperation.State)
	assert.False(t, afterOperation.SettlementPending)
	assert.Zero(t, afterOperation.Attempts)
	assert.Zero(t, afterOperation.CompletedAt)
	assert.Equal(t, beforeOperation.EncryptedProviderTaskID, afterOperation.EncryptedProviderTaskID)
	assert.Greater(t, afterOperation.CreatedAt, int64(0))

	replay, err := RetrySunoTaskManualReview(reservation.ReservationID(), 8502)
	require.NoError(t, err)
	require.NotNil(t, replay)
	assert.False(t, replay.Changed)
	assert.Equal(t, result.AuditEventID, replay.AuditEventID)
	var eventCount int64
	require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ? AND action = ?", reservation.ReservationID(),
			model.RelayQuotaReservationReviewActionRetryTaskPoll).Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount)
}

func TestRetrySunoTaskManualReviewRejectsAmbiguousOrCrossPlatformState(t *testing.T) {
	t.Run("duplicate task identity", func(t *testing.T) {
		fixture, task, reservation, _ := createSunoPollManualReviewFixture(t)
		duplicate := *task
		duplicate.ID = 0
		require.NoError(t, fixture.db.Create(&duplicate).Error)

		result, err := RetrySunoTaskManualReview(reservation.ReservationID(), 8503)
		assert.Nil(t, result)
		assert.ErrorIs(t, err, billingsvc.ErrRelayQuotaReviewUnsafeRetry)
		var operation model.TaskOperation
		require.NoError(t, fixture.db.Where("reservation_id = ?", reservation.ReservationID()).First(&operation).Error)
		assert.Equal(t, model.TaskOperationManualReview, operation.State)
		var eventCount int64
		require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationReviewEvent{}).
			Where("reservation_id = ?", reservation.ReservationID()).Count(&eventCount).Error)
		assert.Zero(t, eventCount)
	})

	t.Run("exact platform allowlist", func(t *testing.T) {
		fixture, _, reservation, operation := createSunoPollManualReviewFixture(t)
		require.NoError(t, fixture.db.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).
			Update("platform", model.TaskOperationPlatformOpenAI).Error)

		result, err := RetrySunoTaskManualReview(reservation.ReservationID(), 8504)
		assert.Nil(t, result)
		assert.ErrorIs(t, err, billingsvc.ErrRelayQuotaReviewNotFound)
		var eventCount int64
		require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationReviewEvent{}).
			Where("reservation_id = ?", reservation.ReservationID()).Count(&eventCount).Error)
		assert.Zero(t, eventCount)
	})
}

func TestSunoSubmitPollFetchLifecycleAndSecretSeparation(t *testing.T) {
	var db *gorm.DB
	var submitCalls, fetchCalls atomic.Int32
	var dispatchObserved atomic.Bool
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/suno/submit/MUSIC":
			submitCalls.Add(1)
			assert.Equal(t, "Bearer upstream-suno-key", request.Header.Get("Authorization"))
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, suno.DefaultMusicModelVersion, body["mv"])
			var operation model.TaskOperation
			if db != nil && db.Where("platform = ?", sunoTaskPlatform).Order("id desc").First(&operation).Error == nil {
				var reservation model.RelayQuotaReservationRecord
				if db.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error == nil &&
					operation.State == model.TaskOperationDispatching && operation.SettlementPending &&
					reservation.Status == model.RelayQuotaReservationStatusDispatched {
					dispatchObserved.Store(true)
				}
			}
			_, _ = writer.Write([]byte(`{"code":"success","message":"","data":"provider-song-1"}`))
		case "/suno/fetch":
			fetchCalls.Add(1)
			var body struct {
				IDs []string `json:"ids"`
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, []string{"provider-song-1"}, body.IDs)
			_, _ = writer.Write([]byte(`{"code":"success","message":"","data":[{
				"task_id":"provider-song-1","action":"MUSIC","status":"success","fail_reason":"",
				"submit_time":11,"start_time":12,"finish_time":13,
				"data":[{"id":"clip-1","audio_url":"https://cdn.example/song.mp3"}]
			}]}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	db = fixture.db

	submit := fixture.request(http.MethodPost, "/suno/submit/music", `{}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, submit.Code, submit.Body.String())
	publicID := decodeSunoSubmitID(t, submit)
	assert.NotContains(t, submit.Body.String(), "provider-song-1")
	assert.True(t, dispatchObserved.Load(), "reservation and no-retry fence must exist before upstream I/O")
	assert.Equal(t, int32(1), submitCalls.Load())

	var task model.Task
	require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&task).Error)
	assert.Equal(t, sunoTaskPlatform, task.Platform)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
	assert.Equal(t, 50_000, task.Quota)
	assert.NotContains(t, task.PrivateData, "upstream-suno-key")
	assert.NotContains(t, task.PrivateData, "provider-song-1")
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&operation).Error)
	assert.Equal(t, sunoTaskPlatform, operation.Platform)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.False(t, operation.SettlementPending)
	assert.NotEmpty(t, operation.EncryptedProviderTaskID)
	assert.NotContains(t, operation.EncryptedProviderTaskID, "provider-song-1")
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, 50_000, reservation.ActualQuota)

	beforePoll := fixture.request(http.MethodGet, "/suno/fetch/"+publicID, "", fixture.token.Key)
	require.Equal(t, http.StatusOK, beforePoll.Code, beforePoll.Body.String())
	assert.Contains(t, beforePoll.Body.String(), `"platform":"suno"`)
	assert.Contains(t, beforePoll.Body.String(), `"status":"SUBMITTED"`)
	assert.Contains(t, beforePoll.Body.String(), `"properties":{"input":"","origin_model_name":"suno_music"}`)
	assert.NotContains(t, beforePoll.Body.String(), "provider-song-1")
	assert.NotContains(t, beforePoll.Body.String(), "upstream-suno-key")
	assert.NotContains(t, beforePoll.Body.String(), "pricing")
	var exactFetch struct {
		Code    string                     `json:"code"`
		Message string                     `json:"message"`
		Data    map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(beforePoll.Body.Bytes(), &exactFetch))
	assert.Equal(t, "success", exactFetch.Code)
	assert.Empty(t, exactFetch.Message)
	keys := make([]string, 0, len(exactFetch.Data))
	for key := range exactFetch.Data {
		keys = append(keys, key)
	}
	assert.ElementsMatch(t, []string{
		"id", "created_at", "updated_at", "task_id", "platform", "user_id", "group",
		"channel_id", "quota", "action", "status", "fail_reason", "submit_time",
		"start_time", "finish_time", "progress", "properties", "data",
	}, keys, "GET-by-id must retain the exact local task DTO shape")
	var publicProperties map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(exactFetch.Data["properties"], &publicProperties))
	assert.ElementsMatch(t, []string{"input", "origin_model_name"}, mapKeys(publicProperties))

	forceSunoTaskRecoveryDue(t, fixture.db, publicID)
	require.NoError(t, reconcileAsyncSunoTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())
	require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&task).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.Equal(t, "100%", task.Progress)
	assert.Equal(t, int64(11), task.SubmitTime)
	assert.Equal(t, int64(12), task.StartTime)
	assert.Equal(t, int64(13), task.FinishTime)
	require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&operation).Error)
	assert.Equal(t, model.TaskOperationTerminal, operation.State)

	batch := fixture.request(http.MethodPost, "/suno/fetch", `{"ids":["`+publicID+`"],"action":"MUSIC"}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, batch.Code, batch.Body.String())
	assert.Contains(t, batch.Body.String(), `"task_id":"`+publicID+`"`)
	assert.Contains(t, batch.Body.String(), `"audio_url":"https://cdn.example/song.mp3"`)
	assert.NotContains(t, batch.Body.String(), "provider-song-1")

	var user model.User
	var token model.Token
	var channel model.Channel
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&channel, fixture.channel.Id).Error)
	assert.Equal(t, 1_950_000, user.Quota)
	assert.Equal(t, 1_950_000, token.RemainQuota)
	assert.Equal(t, int64(50_000), channel.UsedQuota)
}

func TestSunoDefinitiveRejectionRefundsAndAmbiguousSubmitNeverRetries(t *testing.T) {
	t.Run("definitive rejection", func(t *testing.T) {
		upstream := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"code":"invalid","message":"denied","data":null}`))
		}))
		defer upstream.Close()
		fixture := newSunoTaskFixture(t, upstream)
		response := fixture.request(http.MethodPost, "/suno/submit/MUSIC", `{}`, fixture.token.Key)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		var task model.Task
		require.NoError(t, fixture.db.Where("platform = ?", sunoTaskPlatform).First(&task).Error)
		assert.Equal(t, model.TaskStatusFailure, task.Status)
		assert.Zero(t, task.Quota)
		var operation model.TaskOperation
		require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		assert.Equal(t, model.TaskOperationRefunded, operation.State)
		var user model.User
		var token model.Token
		require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
		require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
		assert.Equal(t, 2_000_000, user.Quota)
		assert.Equal(t, 2_000_000, token.RemainQuota)
	})

	t.Run("ambiguous response", func(t *testing.T) {
		var calls atomic.Int32
		upstream := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			_, _ = writer.Write([]byte(`{"accepted":true}`))
		}))
		defer upstream.Close()
		fixture := newSunoTaskFixture(t, upstream)
		response := fixture.request(http.MethodPost, "/suno/submit/MUSIC", `{}`, fixture.token.Key)
		require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
		publicID := decodeSunoSubmitID(t, response)
		var task model.Task
		var operation model.TaskOperation
		require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&task).Error)
		require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&operation).Error)
		assert.Equal(t, model.TaskStatusUnknown, task.Status)
		assert.Equal(t, model.TaskOperationUnknown, operation.State)
		assert.Equal(t, 50_000, task.Quota, "ambiguous accepted work remains conservatively charged")
		forceSunoTaskRecoveryDue(t, fixture.db, publicID)
		require.NoError(t, reconcileAsyncSunoTasks(context.Background()))
		assert.Equal(t, int32(1), calls.Load(), "an ambiguous submit must never be replayed or polled without an id")
	})
}

func TestSunoUndispatchedClientFailureRefundsInsteadOfChargingUnknown(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("undispatched client failure must not reach an upstream")
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	previousFactory := newSunoProvider
	newSunoProvider = func() sunoProvider { return sunoUndispatchedProvider{} }
	t.Cleanup(func() { newSunoProvider = previousFactory })

	response := fixture.request(http.MethodPost, "/suno/submit/MUSIC", `{}`, fixture.token.Key)
	require.Equal(t, http.StatusBadGateway, response.Code, response.Body.String())
	var task model.Task
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("platform = ?", sunoTaskPlatform).First(&task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationRefunded, operation.State)
}

func TestSunoDefinitiveRejectionJournalRecoversFailedRefundTransaction(t *testing.T) {
	var providerRejected atomic.Bool
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		providerRejected.Store(true)
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"code":"invalid","message":"denied","data":null}`))
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	var injected atomic.Bool
	const callbackName = "test:suno_rejected_refund_outage"
	require.NoError(t, fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if providerRejected.Load() && tx.Statement.Table == (model.RelayQuotaReservationRecord{}).TableName() &&
			injected.CompareAndSwap(false, true) {
			tx.AddError(errors.New("injected definitive-rejection refund outage"))
		}
	}))
	t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })

	response := fixture.request(http.MethodPost, "/suno/submit/MUSIC", `{}`, fixture.token.Key)
	require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
	var task model.Task
	require.NoError(t, fixture.db.Where("platform = ?", sunoTaskPlatform).First(&task).Error)
	path, err := sunoRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.NoError(t, err, "authoritative rejection evidence must survive a failed primary refund")
	require.NoError(t, fixture.db.Callback().Update().Remove(callbackName))

	require.NoError(t, PromoteSunoTaskRecoveryJournalsContext(context.Background()))
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
	var operation model.TaskOperation
	var ledger model.RelayQuotaReservationRecord
	var user model.User
	var token model.Token
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&ledger).Error)
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationRefunded, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, ledger.Status)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
}

func TestSunoFailurePollReversesExactlyOnceAndCannotRegress(t *testing.T) {
	var fetchCalls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/suno/submit/") {
			_, _ = writer.Write([]byte(`{"code":"success","message":"","data":"provider-failed"}`))
			return
		}
		fetchCalls.Add(1)
		_, _ = writer.Write([]byte(`{"code":"success","message":"","data":[{
			"task_id":"provider-failed","action":"LYRICS","status":"failed","fail_reason":"generation failed",
			"submit_time":1,"start_time":2,"finish_time":3,"data":{"error":"safe"}
		}]}`))
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	response := fixture.request(http.MethodPost, "/suno/submit/LYRICS", `{"prompt":"lyrics"}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	publicID := decodeSunoSubmitID(t, response)
	forceSunoTaskRecoveryDue(t, fixture.db, publicID)
	require.NoError(t, reconcileAsyncSunoTasks(context.Background()))
	var task model.Task
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&operation).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationReversed, operation.State)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusReversed, reservation.Status)
	forceSunoTaskRecoveryDue(t, fixture.db, publicID)
	require.NoError(t, reconcileAsyncSunoTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())
	require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&task).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	var user model.User
	var token model.Token
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
}

func TestSunoFetchOwnerGroupAndModelAuthorization(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"code":"success","message":"","data":"provider-authz"}`))
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	response := fixture.request(http.MethodPost, "/suno/submit/MUSIC", `{}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	publicID := decodeSunoSubmitID(t, response)

	other := model.User{Username: "other-suno", Password: "x", Status: model.UserStatusEnabled, Group: "default", Quota: 1000, AuthVersion: 1}
	require.NoError(t, fixture.db.Create(&other).Error)
	otherToken := model.Token{UserId: other.Id, Key: "sk-other-suno", Name: "other", Status: billingsvc.TokenStatusEnabled, RemainQuota: 1000}
	require.NoError(t, fixture.db.Create(&otherToken).Error)
	ownerDenied := fixture.request(http.MethodGet, "/suno/fetch/"+publicID, "", otherToken.Key)
	assert.Equal(t, http.StatusBadRequest, ownerDenied.Code)
	assert.Contains(t, ownerDenied.Body.String(), "task_not_exist")

	modelToken := model.Token{UserId: fixture.user.Id, Key: "sk-suno-lyrics-only", Name: "limited",
		Status: billingsvc.TokenStatusEnabled, RemainQuota: 1000, ModelLimitsEnabled: true, ModelLimits: "suno_lyrics"}
	require.NoError(t, fixture.db.Create(&modelToken).Error)
	modelDenied := fixture.request(http.MethodGet, "/suno/fetch/"+publicID, "", modelToken.Key)
	assert.Equal(t, http.StatusForbidden, modelDenied.Code)
	assert.Contains(t, modelDenied.Body.String(), "model_not_allowed")

	groupToken := model.Token{UserId: fixture.user.Id, Key: "sk-suno-vip", Name: "vip",
		Status: billingsvc.TokenStatusEnabled, RemainQuota: 1000, Group: "vip"}
	require.NoError(t, fixture.db.Create(&groupToken).Error)
	groupDenied := fixture.request(http.MethodGet, "/suno/fetch/"+publicID, "", groupToken.Key)
	assert.Equal(t, http.StatusForbidden, groupDenied.Code)
	assert.Contains(t, groupDenied.Body.String(), "group_not_allowed")
}

func TestSunoLocalFetchStrictRequestAndResponseBounds(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("local fetch validation must not contact the provider")
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	for _, body := range []string{
		``, `{"unknown":true}`, `{"ids":[],"ids":[]}`, `{"ids":[]} {}`,
		`{"ids":["bad"]}`, `{"ids":[],"action":"OTHER"}`,
		`{"ids":["task_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","task_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]}`,
	} {
		response := fixture.request(http.MethodPost, "/suno/fetch", body, fixture.token.Key)
		assert.Equal(t, http.StatusBadRequest, response.Code, body+" "+response.Body.String())
	}
	tooLarge := fixture.request(http.MethodPost, "/suno/fetch",
		`{"ids":[],"padding":"`+strings.Repeat("x", int(sunoFetchRequestMaxBytes))+`"}`,
		fixture.token.Key)
	assert.Equal(t, http.StatusRequestEntityTooLarge, tooLarge.Code, tooLarge.Body.String())
	empty := fixture.request(http.MethodPost, "/suno/fetch", `{"ids":[]}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, empty.Code, empty.Body.String())
	assert.JSONEq(t, `{"code":"success","message":"","data":[]}`, empty.Body.String())

	router := gin.New()
	router.GET("/oversized", func(c *gin.Context) {
		writeSunoTaskSuccess(c, strings.Repeat("x", sunoFetchResponseMaxBytes+1))
	})
	request := httptest.NewRequest(http.MethodGet, "/oversized", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), strings.Repeat("x", 128))
}

func TestSunoStoredTaskDataRejectsDuplicateTrailingAndOversizedJSON(t *testing.T) {
	for _, raw := range []string{
		`{"nested":{"key":1,"key":2}}`,
		`{"ok":true} {}`,
		`{"unterminated":`,
		strings.Repeat("x", suno.MaxProviderDataBytes+1),
	} {
		_, err := normalizeSunoTaskData(raw)
		assert.Error(t, err, raw[:min(len(raw), 64)])
	}
	data, err := normalizeSunoTaskData(` {"nested":{"key":1},"items":[1,2]} `)
	require.NoError(t, err)
	assert.JSONEq(t, `{"nested":{"key":1},"items":[1,2]}`, data)
}

func TestSunoAcceptedFallbackUsesImmutablePricingSnapshotAndIsPolled(t *testing.T) {
	var fetchCalls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/suno/fetch", request.URL.Path)
		fetchCalls.Add(1)
		var body struct {
			IDs []string `json:"ids"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.Equal(t, []string{"accepted-before-db-failure"}, body.IDs)
		_, _ = writer.Write([]byte(`{"code":"success","message":"","data":[{
			"task_id":"accepted-before-db-failure","action":"MUSIC","status":"success",
			"fail_reason":"","submit_time":1,"start_time":2,"finish_time":3,"data":{"ok":true}
		}]}`))
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	task, reservation := createPreparedSunoTaskForRecovery(t, fixture, suno.ActionMusic)
	require.Equal(t, 50_000, task.Quota)
	require.NoError(t, markSunoTaskDispatching(task, reservation))
	require.NoError(t, persistAcceptedSunoFallback(task, reservation.ReservationID(), "accepted-before-db-failure"))

	// A hot price change must not alter this already-created task. Recovery can
	// only use the immutable snapshot stored before dispatch.
	require.NoError(t, setting.UpdateOption(setting.PerCallModelPriceOption,
		`{"suno_music":9,"suno_lyrics":8}`))
	forceSunoTaskRecoveryDue(t, fixture.db, task.TaskID)
	require.NoError(t, reconcileAsyncSunoTasks(context.Background()))
	var operation model.TaskOperation
	var durableTask model.Task
	var durableReservation model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&durableTask).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", reservation.ReservationID()).First(&durableReservation).Error)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.False(t, operation.SettlementPending)
	assert.Equal(t, 50_000, durableTask.Quota)
	assert.Equal(t, 50_000, durableReservation.ActualQuota)
	assert.Equal(t, int32(0), fetchCalls.Load(), "settlement releases its lease before a later poll pass")

	forceSunoTaskRecoveryDue(t, fixture.db, task.TaskID)
	require.NoError(t, reconcileAsyncSunoTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&durableTask).Error)
	assert.Equal(t, model.TaskStatusSuccess, durableTask.Status)
	assert.NotContains(t, durableTask.PrivateData, "accepted-before-db-failure")
}

func TestSunoPreparedRecoveryRefundAndLeaseFence(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("prepared recovery must not contact Suno")
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	task, _ := createPreparedSunoTaskForRecovery(t, fixture, suno.ActionLyrics)
	forceSunoTaskRecoveryDue(t, fixture.db, task.TaskID)
	require.NoError(t, reconcileAsyncSunoTasks(context.Background()))
	var durableTask model.Task
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&durableTask).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskStatusFailure, durableTask.Status)
	assert.Zero(t, durableTask.Quota)
	assert.Equal(t, model.TaskOperationRefunded, operation.State)

	// Even if stale workers race to claim the same due row, one fencing token wins.
	require.NoError(t, fixture.db.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).
		Updates(map[string]any{
			"state": model.TaskOperationSubmitted, "next_attempt_at": 0,
			"lease_owner": "", "lease_expires_at": 0, "attempts": 0,
		}).Error)
	require.NoError(t, fixture.db.Model(&model.Task{}).Where("id = ?", durableTask.ID).
		Updates(map[string]any{"status": model.TaskStatusSubmitted, "quota": 5_000}).Error)
	candidate := operation
	candidate.State = model.TaskOperationSubmitted
	candidate.NextAttemptAt = 0
	candidate.LeaseOwner = ""
	candidate.Attempts = 0
	wins := make(chan bool, 8)
	for index := 0; index < cap(wins); index++ {
		go func() {
			_, ok, _ := claimSunoTaskOperation(context.Background(), &candidate, wallclock.NowTimestamp())
			wins <- ok
		}()
	}
	winnerCount := 0
	for index := 0; index < cap(wins); index++ {
		if <-wins {
			winnerCount++
		}
	}
	assert.Equal(t, 1, winnerCount)
}

func TestSunoRejectsMissingExplicitPricingBeforePersistenceOrIO(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	require.NoError(t, setting.UpdateOption(setting.ModelBillingModeOption, `{}`))
	response := fixture.request(http.MethodPost, "/suno/submit/MUSIC", `{}`, fixture.token.Key)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "model_price_error")
	assert.Zero(t, calls.Load())
	var taskCount, reservationCount int64
	require.NoError(t, fixture.db.Model(&model.Task{}).Where("platform = ?", sunoTaskPlatform).Count(&taskCount).Error)
	require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationRecord{}).Count(&reservationCount).Error)
	assert.Zero(t, taskCount)
	assert.Zero(t, reservationCount)
}

func TestSunoRecoveryBatchesByCredentialAndRejectsTerminalRegression(t *testing.T) {
	var fetchCalls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fetchCalls.Add(1)
		var body struct {
			IDs []string `json:"ids"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.ElementsMatch(t, []string{"provider-batch-a", "provider-batch-b"}, body.IDs)
		_, _ = writer.Write([]byte(`{"code":"success","message":"","data":[
			{"task_id":"provider-batch-a","action":"MUSIC","status":"success","fail_reason":"","submit_time":1,"start_time":2,"finish_time":3,"data":{"which":"a"}},
			{"task_id":"provider-batch-b","action":"MUSIC","status":"success","fail_reason":"","submit_time":1,"start_time":2,"finish_time":3,"data":{"which":"b"}}
		]}`))
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	taskA, reservationA := createPreparedSunoTaskForRecovery(t, fixture, suno.ActionMusic)
	taskB, reservationB := createPreparedSunoTaskForRecovery(t, fixture, suno.ActionMusic)
	require.NoError(t, markSunoTaskDispatching(taskA, reservationA))
	require.NoError(t, markSunoTaskDispatching(taskB, reservationB))
	require.NoError(t, persistAcceptedSunoFallback(taskA, reservationA.ReservationID(), "provider-batch-a"))
	require.NoError(t, persistAcceptedSunoFallback(taskB, reservationB.ReservationID(), "provider-batch-b"))

	forceSunoTaskRecoveryDue(t, fixture.db, taskA.TaskID, taskB.TaskID)
	require.NoError(t, reconcileAsyncSunoTasks(context.Background()))
	assert.Zero(t, fetchCalls.Load(), "pending settlements are completed before polling")
	forceSunoTaskRecoveryDue(t, fixture.db, taskA.TaskID, taskB.TaskID)
	require.NoError(t, reconcileAsyncSunoTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load(), "same credential snapshot must use one batch fetch")

	var durableTask model.Task
	var terminalOperation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", taskA.TaskID).First(&durableTask).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", taskA.TaskID).First(&terminalOperation).Error)
	assert.Equal(t, model.TaskStatusSuccess, durableTask.Status)
	assert.Equal(t, model.TaskOperationTerminal, terminalOperation.State)
	staleClaim := &claimedSunoTask{Task: durableTask, Operation: terminalOperation}
	staleClaim.Operation.State = model.TaskOperationSubmitted
	staleClaim.Operation.LeaseOwner = "expired-worker"
	err := persistSunoPollResult(staleClaim, &suno.TaskResult{
		ProviderTaskID: "provider-batch-a", Action: "MUSIC", Status: suno.StatusProcessing,
		Data: json.RawMessage(`{"stale":true}`),
	}, wallclock.NowTimestamp())
	assert.Error(t, err)
	require.NoError(t, fixture.db.Where("task_id = ?", taskA.TaskID).First(&durableTask).Error)
	assert.Equal(t, model.TaskStatusSuccess, durableTask.Status)
	assert.JSONEq(t, `{"which":"a"}`, durableTask.Data)
}

func TestSunoPollLeaseIsRenewedImmediatelyBeforeIOAndExpiredWorkerCannotReviveIt(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("lease unit test must not contact the provider")
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	task, reservation := createPreparedSunoTaskForRecovery(t, fixture, suno.ActionMusic)
	require.NoError(t, markSunoTaskDispatching(task, reservation))
	require.NoError(t, persistAcceptedSunoFallback(task, reservation.ReservationID(), "provider-lease"))
	forceSunoTaskRecoveryDue(t, fixture.db, task.TaskID)
	require.NoError(t, reconcileAsyncSunoTasks(context.Background()))
	forceSunoTaskRecoveryDue(t, fixture.db, task.TaskID)

	var candidate model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&candidate).Error)
	now, err := model.DatabaseUnixTimestamp(fixture.db)
	require.NoError(t, err)
	operation, won, err := claimSunoTaskOperation(context.Background(), &candidate, now)
	require.NoError(t, err)
	require.True(t, won)
	claimed, ready, err := prepareClaimedSunoTask(context.Background(), operation, now)
	require.NoError(t, err)
	require.True(t, ready)

	renewed, _, err := renewSunoPollGroupLeases(context.Background(), groupClaimedSunoTasks([]*claimedSunoTask{claimed})[0])
	require.NoError(t, err)
	require.Len(t, renewed.Tasks, 1)
	var renewedOperation model.TaskOperation
	require.NoError(t, fixture.db.First(&renewedOperation, operation.ID).Error)
	assert.Greater(t, renewedOperation.LeaseExpiresAt, now+sunoOperationLeaseSeconds-1)

	// Once the lease is expired, the old owner cannot extend it again. A fresh
	// worker can claim the row with a different fencing token.
	require.NoError(t, fixture.db.Model(&model.TaskOperation{}).Where("id = ?", operation.ID).
		Update("lease_expires_at", now).Error)
	claimed.Operation.LeaseExpiresAt = now
	renewed, _, err = renewSunoPollGroupLeases(context.Background(), groupClaimedSunoTasks([]*claimedSunoTask{claimed})[0])
	require.NoError(t, err)
	assert.Empty(t, renewed.Tasks)
	require.NoError(t, fixture.db.First(&candidate, operation.ID).Error)
	replacement, won, err := claimSunoTaskOperation(context.Background(), &candidate, now)
	require.NoError(t, err)
	require.True(t, won)
	assert.NotEqual(t, operation.LeaseOwner, replacement.LeaseOwner)
}

func TestSunoContinuationResolvesOwnedPublicIDToEncryptedProviderID(t *testing.T) {
	var submits atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/suno/submit/MUSIC":
			call := submits.Add(1)
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			if call == 1 {
				assert.NotContains(t, body, "task_id")
				_, _ = writer.Write([]byte(`{"code":"success","message":"","data":"provider-origin"}`))
				return
			}
			assert.Equal(t, "provider-origin", body["task_id"])
			assert.Equal(t, "clip-origin", body["continue_clip_id"])
			_, _ = writer.Write([]byte(`{"code":"success","message":"","data":"provider-continuation"}`))
		case "/suno/fetch":
			_, _ = writer.Write([]byte(`{"code":"success","message":"","data":[{
				"task_id":"provider-origin","action":"MUSIC","status":"success","fail_reason":"",
				"submit_time":1,"start_time":2,"finish_time":3,"data":{"id":"clip-origin"}
			}]}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer upstream.Close()
	fixture := newSunoTaskFixture(t, upstream)
	originResponse := fixture.request(http.MethodPost, "/suno/submit/MUSIC", `{}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, originResponse.Code, originResponse.Body.String())
	originPublicID := decodeSunoSubmitID(t, originResponse)
	forceSunoTaskRecoveryDue(t, fixture.db, originPublicID)
	require.NoError(t, reconcileAsyncSunoTasks(context.Background()))

	continuation := fixture.request(http.MethodPost, "/suno/submit/MUSIC",
		`{"task_id":"`+originPublicID+`","continue_clip_id":"clip-origin"}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, continuation.Code, continuation.Body.String())
	continuationPublicID := decodeSunoSubmitID(t, continuation)
	assert.NotEqual(t, originPublicID, continuationPublicID)
	assert.NotContains(t, continuation.Body.String(), "provider-continuation")
	var continuedTask model.Task
	require.NoError(t, fixture.db.Where("task_id = ?", continuationPublicID).First(&continuedTask).Error)
	assert.NotContains(t, continuedTask.PrivateData, "provider-origin")
	assert.NotContains(t, continuedTask.PrivateData, "provider-continuation")

	// Raw provider IDs are never accepted on the public surface.
	rejected := fixture.request(http.MethodPost, "/suno/submit/MUSIC",
		`{"task_id":"provider-origin","continue_clip_id":"clip-origin"}`, fixture.token.Key)
	assert.Equal(t, http.StatusBadRequest, rejected.Code)
	assert.Equal(t, int32(2), submits.Load())
}

func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
