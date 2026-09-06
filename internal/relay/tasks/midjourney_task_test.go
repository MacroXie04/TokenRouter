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
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/midjourney"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type mockMidjourneyProvider struct {
	submit    func(context.Context, string, string, *midjourney.PreparedRequest) (*midjourney.Response, error)
	upload    func(context.Context, string, string, *midjourney.PreparedRequest) (*midjourney.UploadResponse, error)
	fetch     func(context.Context, string, string, []string) ([]midjourney.TaskResult, error)
	imageSeed func(context.Context, string, string, string) (*midjourney.Response, error)
}

func (provider *mockMidjourneyProvider) Submit(ctx context.Context, baseURL, key string,
	prepared *midjourney.PreparedRequest) (*midjourney.Response, error) {
	if provider.submit == nil {
		return nil, errors.New("unexpected Midjourney submit")
	}
	return provider.submit(ctx, baseURL, key, prepared)
}

func (provider *mockMidjourneyProvider) Upload(ctx context.Context, baseURL, key string,
	prepared *midjourney.PreparedRequest) (*midjourney.UploadResponse, error) {
	if provider.upload == nil {
		return nil, errors.New("unexpected Midjourney upload")
	}
	return provider.upload(ctx, baseURL, key, prepared)
}

func (provider *mockMidjourneyProvider) Fetch(ctx context.Context, baseURL, key string,
	ids []string) ([]midjourney.TaskResult, error) {
	if provider.fetch == nil {
		return nil, errors.New("unexpected Midjourney fetch")
	}
	return provider.fetch(ctx, baseURL, key, ids)
}

func (provider *mockMidjourneyProvider) ImageSeed(ctx context.Context, baseURL, key,
	providerTaskID string) (*midjourney.Response, error) {
	if provider.imageSeed == nil {
		return nil, errors.New("unexpected Midjourney image seed")
	}
	return provider.imageSeed(ctx, baseURL, key, providerTaskID)
}

type midjourneyTaskFixture struct {
	router  *gin.Engine
	db      *gorm.DB
	user    model.User
	token   model.Token
	channel model.Channel
}

func newMidjourneyTaskFixture(t *testing.T, provider *mockMidjourneyProvider) midjourneyTaskFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "midjourney-test=0123456789abcdef0123456789abcdef")
	t.Setenv("MIDJOURNEY_TASK_RECOVERY_DIR", t.TempDir())
	oldDB, oldLogDB := model.DB, model.LOG_DB
	dsn := "file:" + filepath.Join(t.TempDir(), "midjourney-task.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{},
		&model.Task{}, &model.Midjourney{}, &model.TaskOperation{}, &model.Log{},
		&model.AuditLogOutbox{}, &model.SubscriptionPlan{}, &model.UserSubscription{},
		&model.SubscriptionPreConsumeRecord{}, &model.RelayQuotaReservationRecord{}, &model.Option{},
	))
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLogDB })
	require.NoError(t, setting.Init())
	require.NoError(t, billingsvc.UpdateGroupRatioOption(`{"default":1,"vip":1}`))
	modes := make(map[string]string, len(midjourney.Models))
	prices := make(map[string]float64, len(midjourney.Models))
	for _, modelName := range midjourney.Models {
		modes[modelName] = billingsvc.BillingModeReference
		prices[modelName] = 0.1
	}
	modesJSON, err := json.Marshal(modes)
	require.NoError(t, err)
	pricesJSON, err := json.Marshal(prices)
	require.NoError(t, err)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption: string(modesJSON), setting.PerCallModelPriceOption: string(pricesJSON),
		setting.ModelRatioOption: `{}`, setting.CompletionRatioOption: `{}`,
		setting.UserUsableGroupsOption: `{"default":"Default","vip":"VIP"}`,
	}))

	user := model.User{Username: "midjourney-owner", Password: "x", Role: roles.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: "default", Quota: 2_000_000, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-midjourney-owner", Name: "midjourney-token",
		Status: billingsvc.TokenStatusEnabled, RemainQuota: 2_000_000}
	require.NoError(t, db.Create(&token).Error)
	weight := uint(1)
	channel := model.Channel{Name: "midjourney-mock", Type: int(channelcatalog.ChannelTypeMidjourney),
		Key: "upstream-midjourney-key", Status: channelcatalog.ChannelStatusEnabled,
		BaseURL: "https://midjourney.provider.example/api", Models: strings.Join(midjourney.Models, ","),
		Group: "default", Weight: &weight}
	require.NoError(t, db.Create(&channel).Error)
	for _, modelName := range midjourney.Models {
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: modelName,
			ChannelId: channel.Id, Enabled: true, Weight: 1}).Error)
	}
	require.NoError(t, channelssvc.InitAbilityCache())

	previousFactory := newMidjourneyProvider
	newMidjourneyProvider = func() midjourneyProvider { return provider }
	t.Cleanup(func() { newMidjourneyProvider = previousFactory })
	router := gin.New()
	mj := router.Group("/mj")
	mj.Use(middleware.TokenAuth())
	mj.POST("/submit/action", RelayMidjourney)
	mj.POST("/submit/imagine", RelayMidjourney)
	mj.POST("/submit/change", RelayMidjourney)
	mj.POST("/submit/upload-discord-images", RelayMidjourney)
	mj.GET("/task/:id/fetch", RelayMidjourney)
	mj.GET("/task/:id/image-seed", RelayMidjourney)
	mj.POST("/task/list-by-condition", RelayMidjourney)
	return midjourneyTaskFixture{router: router, db: db, user: user, token: token, channel: channel}
}

func (fixture midjourneyTaskFixture) request(method, path, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		request.Header.Set("mj-api-secret", key)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	fixture.router.ServeHTTP(recorder, request)
	return recorder
}

func decodeMidjourneySubmitID(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response midjourney.Response
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.NoError(t, validateMidjourneyTaskPublicID(response.Result))
	return response.Result
}

func forceMidjourneyTaskRecoveryDue(t *testing.T, db *gorm.DB, taskIDs ...string) {
	t.Helper()
	result := db.Model(&model.TaskOperation{}).Where("task_id IN ?", taskIDs).
		Updates(map[string]any{"next_attempt_at": 0, "lease_owner": "", "lease_expires_at": 0})
	require.NoError(t, result.Error)
	require.Equal(t, int64(len(taskIDs)), result.RowsAffected)
}

func createPreparedMidjourneyTaskForRecovery(t *testing.T, fixture midjourneyTaskFixture,
	action midjourney.Action) (*model.Task, *billingsvc.RelayQuotaReservation) {
	t.Helper()
	modelName, ok := midjourney.ModelForAction(action)
	require.True(t, ok)
	pricing, enabled, err := billingsvc.ResolveReferenceAsyncTaskBillingPlan(modelName, "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	quota, err := pricing.PreConsumeQuota()
	require.NoError(t, err)
	taskID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	properties, err := marshalMidjourneyTaskProperties(midjourneyTaskProperties{
		Version: midjourneyTaskMetadataVersion, OriginModelName: modelName,
		Action: action, Operation: "imagine", Pricing: pricing,
	})
	require.NoError(t, err)
	now := wallclock.NowTimestamp()
	task := &model.Task{CreatedAt: now, UpdatedAt: now, TaskID: taskID,
		Platform: midjourneyTaskPlatform, UserId: fixture.user.Id, Group: "default",
		ChannelId: fixture.channel.Id, Quota: quota, Action: string(action),
		Status: model.TaskStatusNotStart, SubmitTime: now, Progress: "0%", Properties: properties, Data: "{}"}
	mirror := &model.Midjourney{UserId: fixture.user.Id, Action: string(action), MjId: taskID,
		SubmitTime: now * 1000, Status: model.TaskStatusNotStart, Progress: "0%",
		ChannelId: fixture.channel.Id, Quota: quota}
	encryptedKey, err := asyncTaskEncryptBound("upstream-midjourney-key",
		midjourneyChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, fixture.channel.BaseURL))
	require.NoError(t, err)
	privateData := midjourneyTaskPrivateData{ChannelBaseURL: fixture.channel.BaseURL,
		EncryptedChannelKey: encryptedKey}
	reservation, err := createMidjourneyReservedTask(task, mirror, &fixture.token, &privateData)
	require.NoError(t, err)
	return task, reservation
}

func TestMidjourneySubmitPollFetchLifecycleAndSecretSeparation(t *testing.T) {
	var fixture midjourneyTaskFixture
	var submitCalls, fetchCalls atomic.Int32
	var dispatchObserved atomic.Bool
	provider := &mockMidjourneyProvider{}
	provider.submit = func(_ context.Context, baseURL, key string,
		prepared *midjourney.PreparedRequest) (*midjourney.Response, error) {
		submitCalls.Add(1)
		assert.Equal(t, "https://midjourney.provider.example/api", baseURL)
		assert.Equal(t, "upstream-midjourney-key", key)
		assert.Equal(t, "/mj/submit/imagine", prepared.Path)
		assert.JSONEq(t, `{"prompt":"a cat"}`, string(prepared.Body))
		var operation model.TaskOperation
		if fixture.db != nil && fixture.db.Where("platform = ?", midjourneyTaskPlatform).
			Order("id desc").First(&operation).Error == nil {
			var reservation model.RelayQuotaReservationRecord
			if fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error == nil &&
				operation.State == model.TaskOperationDispatching && operation.SettlementPending &&
				reservation.Status == model.RelayQuotaReservationStatusDispatched {
				dispatchObserved.Store(true)
			}
		}
		return &midjourney.Response{Code: 22, Description: "queued", Result: "provider-mj-1",
			Properties: json.RawMessage(`{"numberOfQueues":1}`)}, nil
	}
	provider.fetch = func(_ context.Context, baseURL, key string, ids []string) ([]midjourney.TaskResult, error) {
		fetchCalls.Add(1)
		assert.Equal(t, "https://midjourney.provider.example/api", baseURL)
		assert.Equal(t, "upstream-midjourney-key", key)
		assert.Equal(t, []string{"provider-mj-1"}, ids)
		return []midjourney.TaskResult{{ProviderTaskID: "provider-mj-1", Action: "IMAGINE",
			Prompt: "a cat", PromptEn: "a cat", Description: "done", State: "", SubmitTime: 11,
			StartTime: 12, FinishTime: 13, ImageURL: "https://cdn.example/image.png",
			Status: model.TaskStatusSuccess, Progress: "100%", Properties: json.RawMessage(`{"finalPrompt":"a cat"}`)}}, nil
	}
	fixture = newMidjourneyTaskFixture(t, provider)

	submit := fixture.request(http.MethodPost, "/mj/submit/imagine", `{"prompt":"a cat"}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, submit.Code, submit.Body.String())
	publicID := decodeMidjourneySubmitID(t, submit)
	assert.Contains(t, submit.Body.String(), `"code":1`)
	assert.NotContains(t, submit.Body.String(), "provider-mj-1")
	assert.NotContains(t, submit.Body.String(), "upstream-midjourney-key")
	assert.True(t, dispatchObserved.Load(), "reservation and no-retry fence must exist before provider I/O")
	assert.Equal(t, int32(1), submitCalls.Load())

	var task model.Task
	var mirror model.Midjourney
	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&task).Error)
	require.NoError(t, fixture.db.Where("mj_id = ?", publicID).First(&mirror).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&operation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, midjourneyTaskPlatform, task.Platform)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
	assert.Equal(t, 50_000, task.Quota)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.True(t, operation.SettlementPending)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reservation.Status)
	for _, durable := range []string{task.PrivateData, operation.EncryptedProviderTaskID} {
		assert.NotContains(t, durable, "provider-mj-1")
		assert.NotContains(t, durable, "upstream-midjourney-key")
	}
	assert.Equal(t, publicID, mirror.MjId)

	local := fixture.request(http.MethodGet, "/mj/task/"+publicID+"/fetch", "", fixture.token.Key)
	require.Equal(t, http.StatusOK, local.Code, local.Body.String())
	assert.Equal(t, int32(0), fetchCalls.Load(), "public fetch must be local-only")
	assert.Contains(t, local.Body.String(), `"id":"`+publicID+`"`)
	assert.Contains(t, local.Body.String(), `"status":"SUBMITTED"`)
	assert.NotContains(t, local.Body.String(), "provider-mj-1")
	assert.NotContains(t, local.Body.String(), "upstream-midjourney-key")

	forceMidjourneyTaskRecoveryDue(t, fixture.db, publicID)
	require.NoError(t, reconcileAsyncMidjourneyTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())
	require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&operation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.Equal(t, model.TaskOperationTerminal, operation.State)
	assert.False(t, operation.SettlementPending)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, 50_000, reservation.ActualQuota)
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

func TestMidjourneyFailureRefundAmbiguityAndUndispatchedSafety(t *testing.T) {
	t.Run("definitive rejection refunds without leaking provider details", func(t *testing.T) {
		provider := &mockMidjourneyProvider{submit: func(context.Context, string, string,
			*midjourney.PreparedRequest) (*midjourney.Response, error) {
			return nil, &midjourney.RequestError{Dispatched: true, Err: &midjourney.ProviderError{
				StatusCode: http.StatusBadRequest, Code: 24, Definitive: true,
				Cause: errors.New("upstream-midjourney-key private rejection"),
			}}
		}}
		fixture := newMidjourneyTaskFixture(t, provider)
		response := fixture.request(http.MethodPost, "/mj/submit/imagine", `{"prompt":"x"}`, fixture.token.Key)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		assert.NotContains(t, response.Body.String(), "upstream-midjourney-key")
		assert.NotContains(t, response.Body.String(), "private rejection")
		var task model.Task
		var operation model.TaskOperation
		require.NoError(t, fixture.db.Where("platform = ?", midjourneyTaskPlatform).First(&task).Error)
		require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		assert.Zero(t, task.Quota)
		assert.Equal(t, model.TaskOperationRefunded, operation.State)
	})

	t.Run("terminal failure refunds", func(t *testing.T) {
		provider := &mockMidjourneyProvider{}
		provider.submit = func(context.Context, string, string, *midjourney.PreparedRequest) (*midjourney.Response, error) {
			return &midjourney.Response{Code: 1, Description: "accepted", Result: "provider-failed"}, nil
		}
		provider.fetch = func(context.Context, string, string, []string) ([]midjourney.TaskResult, error) {
			return []midjourney.TaskResult{{ProviderTaskID: "provider-failed", Action: "IMAGINE",
				Status: model.TaskStatusFailure, Progress: "100%", FailReason: "generation failed"}}, nil
		}
		fixture := newMidjourneyTaskFixture(t, provider)
		response := fixture.request(http.MethodPost, "/mj/submit/imagine", `{"prompt":"x"}`, fixture.token.Key)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		publicID := decodeMidjourneySubmitID(t, response)
		forceMidjourneyTaskRecoveryDue(t, fixture.db, publicID)
		require.NoError(t, reconcileAsyncMidjourneyTasks(context.Background()))
		var task model.Task
		var operation model.TaskOperation
		require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&task).Error)
		require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&operation).Error)
		assert.Equal(t, model.TaskStatusFailure, task.Status)
		assert.Zero(t, task.Quota)
		assert.Equal(t, model.TaskOperationRefunded, operation.State)
		var user model.User
		var token model.Token
		require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
		require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
		assert.Equal(t, 2_000_000, user.Quota)
		assert.Equal(t, 2_000_000, token.RemainQuota)
	})

	t.Run("ambiguous dispatch is held for manual review and never replayed", func(t *testing.T) {
		var calls atomic.Int32
		provider := &mockMidjourneyProvider{submit: func(context.Context, string, string,
			*midjourney.PreparedRequest) (*midjourney.Response, error) {
			calls.Add(1)
			return nil, &midjourney.RequestError{Err: errors.New("safe transport failure"), Dispatched: true}
		}}
		fixture := newMidjourneyTaskFixture(t, provider)
		response := fixture.request(http.MethodPost, "/mj/submit/imagine", `{"prompt":"x"}`, fixture.token.Key)
		require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
		publicID := decodeMidjourneySubmitID(t, response)
		var task model.Task
		var operation model.TaskOperation
		require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&task).Error)
		require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&operation).Error)
		assert.Equal(t, model.TaskStatusUnknown, task.Status)
		assert.Equal(t, model.TaskOperationManualReview, operation.State)
		assert.Equal(t, 50_000, task.Quota)
		forceMidjourneyTaskRecoveryDue(t, fixture.db, publicID)
		require.NoError(t, reconcileAsyncMidjourneyTasks(context.Background()))
		assert.Equal(t, int32(1), calls.Load())
	})

	t.Run("undispatched failure refunds", func(t *testing.T) {
		provider := &mockMidjourneyProvider{submit: func(context.Context, string, string,
			*midjourney.PreparedRequest) (*midjourney.Response, error) {
			return nil, &midjourney.RequestError{Err: errors.New("request construction failed")}
		}}
		fixture := newMidjourneyTaskFixture(t, provider)
		response := fixture.request(http.MethodPost, "/mj/submit/imagine", `{"prompt":"x"}`, fixture.token.Key)
		require.Equal(t, http.StatusBadGateway, response.Code, response.Body.String())
		var task model.Task
		var operation model.TaskOperation
		require.NoError(t, fixture.db.Where("platform = ?", midjourneyTaskPlatform).First(&task).Error)
		require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		assert.Equal(t, model.TaskStatusFailure, task.Status)
		assert.Zero(t, task.Quota)
		assert.Equal(t, model.TaskOperationRefunded, operation.State)
	})
}

func TestMidjourneyAcceptedJournalPromotesManualDispatchWithoutPlaintext(t *testing.T) {
	fixture := newMidjourneyTaskFixture(t, &mockMidjourneyProvider{})
	task, reservation := createPreparedMidjourneyTaskForRecovery(t, fixture, midjourney.ActionImagine)
	require.NoError(t, markMidjourneyTaskDispatching(task, reservation))
	require.NoError(t, persistAcceptedMidjourneyRecoveryJournal(task, reservation.ReservationID(),
		"provider-journal", midjourney.ActionImagine))
	path, err := midjourneyRecoveryJournalPath(task.TaskID)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "provider-journal")
	assert.NotContains(t, string(raw), "upstream-midjourney-key")
	require.NoError(t, markMidjourneyAmbiguousDispatch(task, reservation.ReservationID(),
		midjourneyUnknownDispatchReason))
	require.NoError(t, PromoteMidjourneyTaskRecoveryJournalsContext(context.Background()))
	_, err = os.Stat(path)
	assert.True(t, errors.Is(err, os.ErrNotExist))
	var durableTask model.Task
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&durableTask).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskStatusSubmitted, durableTask.Status)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.True(t, operation.SettlementPending)
	assert.NotEmpty(t, operation.EncryptedProviderTaskID)
	assert.NotContains(t, durableTask.PrivateData, "provider-journal")
	assert.NotContains(t, operation.EncryptedProviderTaskID, "provider-journal")
}

func TestMidjourneyTaskBoundActionUsesEncryptedParentProviderIDAndOriginalChannel(t *testing.T) {
	var calls atomic.Int32
	provider := &mockMidjourneyProvider{}
	provider.submit = func(_ context.Context, baseURL, key string,
		prepared *midjourney.PreparedRequest) (*midjourney.Response, error) {
		call := calls.Add(1)
		assert.Equal(t, "https://midjourney.provider.example/api", baseURL)
		assert.Equal(t, "upstream-midjourney-key", key)
		if call == 1 {
			return &midjourney.Response{Code: 21, Description: "exists", Result: "provider-parent",
				Properties: json.RawMessage(`{"status":"SUCCESS","imageUrl":"https://cdn.example/parent.png"}`)}, nil
		}
		assert.Equal(t, "/mj/submit/change", prepared.Path)
		assert.JSONEq(t, `{"action":"UPSCALE","index":1,"taskId":"provider-parent"}`, string(prepared.Body))
		assert.Equal(t, "parent prompt", prepared.Value.Prompt)
		return &midjourney.Response{Code: 1, Description: "accepted", Result: "provider-child"}, nil
	}
	fixture := newMidjourneyTaskFixture(t, provider)
	parent := fixture.request(http.MethodPost, "/mj/submit/imagine", `{"prompt":"parent prompt"}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, parent.Code, parent.Body.String())
	parentID := decodeMidjourneySubmitID(t, parent)
	child := fixture.request(http.MethodPost, "/mj/submit/change",
		`{"taskId":"`+parentID+`","action":"UPSCALE","index":1}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, child.Code, child.Body.String())
	childID := decodeMidjourneySubmitID(t, child)
	assert.NotEqual(t, parentID, childID)
	assert.NotContains(t, child.Body.String(), "provider-child")
	var childTask model.Task
	var childMirror model.Midjourney
	require.NoError(t, fixture.db.Where("task_id = ?", childID).First(&childTask).Error)
	require.NoError(t, fixture.db.Where("mj_id = ?", childID).First(&childMirror).Error)
	assert.Equal(t, fixture.channel.Id, childTask.ChannelId)
	assert.Equal(t, "parent prompt", childMirror.Prompt)
	assert.NotContains(t, childTask.PrivateData, "provider-parent")
	assert.NotContains(t, childTask.PrivateData, "provider-child")
}

func TestMidjourneyUploadSettlesAtomicallyWithExactResponseShape(t *testing.T) {
	provider := &mockMidjourneyProvider{upload: func(_ context.Context, baseURL, key string,
		prepared *midjourney.PreparedRequest) (*midjourney.UploadResponse, error) {
		assert.Equal(t, "https://midjourney.provider.example/api", baseURL)
		assert.Equal(t, "upstream-midjourney-key", key)
		assert.Equal(t, "/mj/submit/upload-discord-images", prepared.Path)
		assert.JSONEq(t, `{"base64Array":["YQ=="]}`, string(prepared.Body))
		return &midjourney.UploadResponse{Code: 1, Description: "ok",
			Result: []string{"https://cdn.example/upload.png"}}, nil
	}}
	fixture := newMidjourneyTaskFixture(t, provider)
	response := fixture.request(http.MethodPost, "/mj/submit/upload-discord-images",
		`{"base64Array":["YQ=="]}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.JSONEq(t, `{"code":1,"description":"ok","result":["https://cdn.example/upload.png"]}`,
		response.Body.String())
	assert.NotContains(t, response.Body.String(), "upstream-midjourney-key")
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("platform = ?", midjourneyTaskPlatform).First(&operation).Error)
	assert.Equal(t, model.TaskOperationTerminal, operation.State)
	assert.False(t, operation.SettlementPending)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
}

func TestMidjourneyLocalFetchOwnerAndStrictQueryBounds(t *testing.T) {
	provider := &mockMidjourneyProvider{submit: func(context.Context, string, string,
		*midjourney.PreparedRequest) (*midjourney.Response, error) {
		return &midjourney.Response{Code: 1, Description: "accepted", Result: "provider-owner"}, nil
	}}
	fixture := newMidjourneyTaskFixture(t, provider)
	response := fixture.request(http.MethodPost, "/mj/submit/imagine", `{"prompt":"x"}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	publicID := decodeMidjourneySubmitID(t, response)

	other := model.User{Username: "other-midjourney", Password: "x", Status: model.UserStatusEnabled,
		Group: "default", Quota: 1000, AuthVersion: 1}
	require.NoError(t, fixture.db.Create(&other).Error)
	otherToken := model.Token{UserId: other.Id, Key: "sk-other-midjourney", Name: "other",
		Status: billingsvc.TokenStatusEnabled, RemainQuota: 1000}
	require.NoError(t, fixture.db.Create(&otherToken).Error)
	denied := fixture.request(http.MethodGet, "/mj/task/"+publicID+"/fetch", "", otherToken.Key)
	assert.Equal(t, http.StatusBadRequest, denied.Code)
	assert.NotContains(t, denied.Body.String(), "provider-owner")

	for _, body := range []string{
		``, `{"unknown":true}`, `{"ids":[],"ids":[]}`, `{"ids":[]} {}`,
		`{"ids":["bad"]}`, `{"ids":["` + publicID + `","` + publicID + `"]}`,
	} {
		result := fixture.request(http.MethodPost, "/mj/task/list-by-condition", body, fixture.token.Key)
		assert.Equal(t, http.StatusBadRequest, result.Code, body+" "+result.Body.String())
	}
	tooLarge := fixture.request(http.MethodPost, "/mj/task/list-by-condition",
		`{"ids":[],"padding":"`+strings.Repeat("x", int(midjourneyFetchRequestMaxBytes))+`"}`,
		fixture.token.Key)
	assert.Equal(t, http.StatusRequestEntityTooLarge, tooLarge.Code)
	empty := fixture.request(http.MethodPost, "/mj/task/list-by-condition", `{"ids":[]}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, empty.Code, empty.Body.String())
	assert.JSONEq(t, `[]`, empty.Body.String())
	batch := fixture.request(http.MethodPost, "/mj/task/list-by-condition",
		`{"ids":["`+publicID+`"]}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, batch.Code, batch.Body.String())
	assert.Contains(t, batch.Body.String(), publicID)
	assert.NotContains(t, batch.Body.String(), "provider-owner")
}

func TestMidjourneyConcurrentReconciliationHasOneProviderPollAndTerminalWrite(t *testing.T) {
	var fetchCalls atomic.Int32
	provider := &mockMidjourneyProvider{}
	provider.submit = func(context.Context, string, string, *midjourney.PreparedRequest) (*midjourney.Response, error) {
		return &midjourney.Response{Code: 1, Result: "provider-race"}, nil
	}
	provider.fetch = func(context.Context, string, string, []string) ([]midjourney.TaskResult, error) {
		fetchCalls.Add(1)
		return []midjourney.TaskResult{{ProviderTaskID: "provider-race", Action: "IMAGINE",
			Status: model.TaskStatusSuccess, Progress: "100%"}}, nil
	}
	fixture := newMidjourneyTaskFixture(t, provider)
	response := fixture.request(http.MethodPost, "/mj/submit/imagine", `{"prompt":"x"}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	publicID := decodeMidjourneySubmitID(t, response)
	forceMidjourneyTaskRecoveryDue(t, fixture.db, publicID)

	const workers = 8
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		go func() {
			defer wait.Done()
			_ = reconcileAsyncMidjourneyTasks(context.Background())
		}()
	}
	wait.Wait()
	assert.Equal(t, int32(1), fetchCalls.Load())
	var task model.Task
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", publicID).First(&operation).Error)
	assert.Equal(t, model.TaskStatusSuccess, task.Status)
	assert.Equal(t, model.TaskOperationTerminal, operation.State)
}

func TestMidjourneyImageSeedUsesOwnedEncryptedProviderIdentity(t *testing.T) {
	provider := &mockMidjourneyProvider{}
	provider.submit = func(context.Context, string, string, *midjourney.PreparedRequest) (*midjourney.Response, error) {
		return &midjourney.Response{Code: 21, Result: "provider-seed",
			Properties: json.RawMessage(`{"status":"SUCCESS","imageUrl":"https://cdn.example/seed.png"}`)}, nil
	}
	provider.imageSeed = func(_ context.Context, baseURL, key, providerID string) (*midjourney.Response, error) {
		assert.Equal(t, "https://midjourney.provider.example/api", baseURL)
		assert.Equal(t, "upstream-midjourney-key", key)
		assert.Equal(t, "provider-seed", providerID)
		return &midjourney.Response{Code: 1, Description: "ok", Result: "12345"}, nil
	}
	fixture := newMidjourneyTaskFixture(t, provider)
	response := fixture.request(http.MethodPost, "/mj/submit/imagine", `{"prompt":"x"}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	publicID := decodeMidjourneySubmitID(t, response)
	seed := fixture.request(http.MethodGet, "/mj/task/"+publicID+"/image-seed", "", fixture.token.Key)
	require.Equal(t, http.StatusOK, seed.Code, seed.Body.String())
	assert.JSONEq(t, `{"code":1,"description":"ok","properties":null,"result":"12345"}`, seed.Body.String())
	assert.NotContains(t, seed.Body.String(), "provider-seed")
}
