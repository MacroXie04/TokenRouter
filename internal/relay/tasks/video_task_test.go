package tasks

import (
	"context"
	"encoding/json"
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
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/sora"
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

type videoTaskFixture struct {
	router  *gin.Engine
	db      *gorm.DB
	user    model.User
	token   model.Token
	channel model.Channel
}

func newVideoTaskFixture(t *testing.T, upstreamURL string) videoTaskFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "video-test=0123456789abcdef0123456789abcdef")
	t.Setenv("VIDEO_TASK_RECOVERY_DIR", t.TempDir())
	httpx.InitSSRF()

	oldDB, oldLogDB := model.DB, model.LOG_DB
	dsn := "file:" + filepath.Join(t.TempDir(), "video-task.db") +
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

	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	previousSpecialRatios := billingsvc.ExportedGroupGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"sora-2": {Prompt: 0.3}, "sora-2-pro": {Prompt: 0.5},
	})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	billingsvc.SetGroupGroupRatios(map[string]map[string]float64{})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
		billingsvc.SetGroupGroupRatios(previousSpecialRatios)
	})

	user := model.User{
		Username: "video-owner", Password: "x", Role: roles.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: "default", Quota: 2_000_000, AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-video-owner", Name: "video-token",
		Status: billingsvc.TokenStatusEnabled, RemainQuota: 2_000_000,
	}
	require.NoError(t, db.Create(&token).Error)
	weight := uint(1)
	channel := model.Channel{
		Name: "sora-mock", Type: int(channelcatalog.ChannelTypeSora), Key: "upstream-video-key",
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: upstreamURL,
		Models: "sora-2,sora-2-pro", Group: "default", Weight: &weight,
		ModelMapping: `{"sora-2":"upstream-sora"}`,
	}
	require.NoError(t, db.Create(&channel).Error)
	for _, modelName := range []string{"sora-2", "sora-2-pro"} {
		require.NoError(t, db.Create(&model.Ability{
			Group: "default", Model: modelName, ChannelId: channel.Id, Enabled: true, Weight: 1,
		}).Error)
	}
	require.NoError(t, channelssvc.InitAbilityCache())

	router := gin.New()
	video := router.Group("/v1")
	video.Use(middleware.TokenAuth())
	video.POST("/video/generations", RelayVideoTask)
	video.GET("/video/generations/:task_id", RelayVideoTaskFetch)
	video.POST("/videos/:video_id/remix", RelayVideoTask)
	video.POST("/videos", RelayVideoTask)
	video.GET("/videos/:task_id", RelayVideoTaskFetch)
	content := router.Group("/v1")
	content.Use(middleware.TokenOrUserAuth())
	content.GET("/videos/:task_id/content", VideoProxy)
	return videoTaskFixture{router: router, db: db, user: user, token: token, channel: channel}
}

func (fixture videoTaskFixture) request(method, path, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	fixture.router.ServeHTTP(recorder, request)
	return recorder
}

func decodeCreatedVideoTask(t *testing.T, response *httptest.ResponseRecorder) openAIVideoResponse {
	t.Helper()
	var created openAIVideoResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &created), response.Body.String())
	require.NoError(t, validateVideoTaskPublicID(created.ID))
	return created
}

func forceVideoTaskRecoveryDue(t *testing.T, db *gorm.DB, taskID string) {
	t.Helper()
	result := db.Model(&model.TaskOperation{}).Where("task_id = ?", taskID).
		Updates(map[string]any{"next_attempt_at": 0, "lease_owner": "", "lease_expires_at": 0})
	require.NoError(t, result.Error)
	require.Equal(t, int64(1), result.RowsAffected)
}

func createPreparedVideoTaskForRecovery(
	t *testing.T,
	fixture videoTaskFixture,
) (*model.Task, *billingsvc.RelayQuotaReservation) {
	t.Helper()
	properties, err := marshalVideoTaskProperties(videoTaskProperties{
		Input: "recover a video", UpstreamModelName: "upstream-sora", OriginModelName: "sora-2",
		Seconds: 4, Size: "720x1280",
	})
	require.NoError(t, err)
	taskID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	now := wallclock.NowTimestamp()
	task := &model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID,
		Platform: videoTaskPlatform, UserId: fixture.user.Id, Group: "default",
		ChannelId: fixture.channel.Id, Quota: 600_000, Action: "textGenerate",
		Status: model.TaskStatusNotStart, SubmitTime: now, Progress: "0%",
		Properties: properties, Data: "null",
	}
	encryptedKey, err := asyncTaskEncryptBound(
		"upstream-video-key",
		videoChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, fixture.channel.BaseURL),
	)
	require.NoError(t, err)
	privateData := videoTaskPrivateData{
		ChannelBaseURL: fixture.channel.BaseURL, EncryptedChannelKey: encryptedKey,
	}
	reservation, err := createVideoReservedTask(task, &fixture.token, &privateData)
	require.NoError(t, err)
	return task, reservation
}

func TestVideoTaskSubmitPollFetchRemixAndContentLifecycle(t *testing.T) {
	var submitCalls, fetchCalls, remixCalls, contentCalls, rotatedChannelCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "Bearer upstream-video-key", request.Header.Get("Authorization"))
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/videos":
			submitCalls.Add(1)
			assert.True(t, strings.HasPrefix(request.Header.Get("Idempotency-Key"), "task_"))
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, "upstream-sora", body["model"])
			_, _ = writer.Write([]byte(`{"id":"provider-video-1","status":"queued","progress":0}`))
		case request.Method == http.MethodGet && request.URL.Path == "/v1/videos/provider-video-1":
			fetchCalls.Add(1)
			_, _ = writer.Write([]byte(`{"id":"provider-video-1","status":"completed","progress":100}`))
		case request.Method == http.MethodGet && request.URL.Path == "/v1/videos/provider-video-1/content":
			contentCalls.Add(1)
			writer.Header().Set("Content-Type", "video/mp4")
			writer.Header().Set("Content-Disposition", `attachment; filename="result.mp4"`)
			_, _ = writer.Write([]byte("video-bytes"))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/videos/provider-video-1/remix":
			remixCalls.Add(1)
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, "upstream-sora", body["model"])
			_, _ = writer.Write([]byte(`{"id":"provider-video-remix","status":"queued"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(upstream.Close)
	fixture := newVideoTaskFixture(t, upstream.URL)

	submit := fixture.request(http.MethodPost, "/v1/video/generations",
		`{"model":"sora-2","prompt":"a paper boat","seconds":4,"size":"720x1280"}`,
		fixture.token.Key)
	require.Equal(t, http.StatusOK, submit.Code, submit.Body.String())
	created := decodeCreatedVideoTask(t, submit)
	assert.Equal(t, "queued", created.Status)
	assert.NotContains(t, submit.Body.String(), "provider-video-1")
	assert.Equal(t, int32(1), submitCalls.Load())
	journalPath, err := videoRecoveryJournalPath(created.ID)
	require.NoError(t, err)
	_, err = os.Stat(journalPath)
	assert.ErrorIs(t, err, os.ErrNotExist, "committed settlement must clean its emergency journal")

	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", created.ID).First(&operation).Error)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.NotEmpty(t, operation.EncryptedProviderTaskID)
	assert.NotContains(t, operation.EncryptedProviderTaskID, "provider-video-1")
	var task model.Task
	require.NoError(t, fixture.db.Where("task_id = ?", created.ID).First(&task).Error)
	assert.NotContains(t, task.PrivateData, "upstream-video-key")
	assert.NotContains(t, task.PrivateData, "provider-video-1")

	openAISubmit := fixture.request(http.MethodPost, "/v1/videos",
		`{"model":"sora-2","prompt":"a second paper boat","seconds":4,"size":"720x1280"}`,
		fixture.token.Key)
	require.Equal(t, http.StatusOK, openAISubmit.Code, openAISubmit.Body.String())
	openAICreated := decodeCreatedVideoTask(t, openAISubmit)
	assert.NotEqual(t, created.ID, openAICreated.ID)
	assert.Equal(t, "queued", openAICreated.Status)
	assert.NotContains(t, openAISubmit.Body.String(), "provider-video-1")
	assert.Equal(t, int32(2), submitCalls.Load())

	legacyFetch := fixture.request(http.MethodGet, "/v1/video/generations/"+created.ID, "", fixture.token.Key)
	require.Equal(t, http.StatusOK, legacyFetch.Code, legacyFetch.Body.String())
	assert.Contains(t, legacyFetch.Body.String(), `"code":"success"`)
	assert.Contains(t, legacyFetch.Body.String(), `"task_id":"`+created.ID+`"`)
	assert.NotContains(t, legacyFetch.Body.String(), "provider-video-1")

	forceVideoTaskRecoveryDue(t, fixture.db, created.ID)
	require.NoError(t, reconcileAsyncVideoTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())
	fetched := fixture.request(http.MethodGet, "/v1/videos/"+created.ID, "", fixture.token.Key)
	require.Equal(t, http.StatusOK, fetched.Code, fetched.Body.String())
	completed := decodeCreatedVideoTask(t, fetched)
	assert.Equal(t, "completed", completed.Status)
	assert.Equal(t, 100, completed.Progress)
	assert.NotContains(t, fetched.Body.String(), "provider-video-1")

	content := fixture.request(http.MethodGet, "/v1/videos/"+created.ID+"/content", "", fixture.token.Key)
	require.Equal(t, http.StatusOK, content.Code, content.Body.String())
	assert.Equal(t, "video/mp4", content.Header().Get("Content-Type"))
	assert.Equal(t, "private, max-age=86400", content.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", content.Header().Get("X-Content-Type-Options"))
	assert.Contains(t, content.Header().Get("Content-Security-Policy"), "sandbox")
	assert.Equal(t, "video-bytes", content.Body.String())
	assert.Equal(t, int32(1), contentCalls.Load())

	rotatedUpstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rotatedChannelCalls.Add(1)
		http.Error(writer, "rotated channel snapshot must not be used for remix", http.StatusBadGateway)
	}))
	t.Cleanup(rotatedUpstream.Close)
	require.NoError(t, fixture.db.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Updates(map[string]any{
			"base_url":      rotatedUpstream.URL,
			"key":           "rotated-upstream-key",
			"model_mapping": `{"sora-2":"rotated-upstream-model"}`,
		}).Error)

	remix := fixture.request(http.MethodPost, "/v1/videos/"+created.ID+"/remix",
		`{"prompt":"make the boat blue"}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, remix.Code, remix.Body.String())
	remixed := decodeCreatedVideoTask(t, remix)
	assert.NotEqual(t, created.ID, remixed.ID)
	assert.Equal(t, created.ID, remixed.RemixedFromVideoID)
	assert.NotContains(t, remix.Body.String(), "provider-video-remix")
	assert.Equal(t, int32(1), remixCalls.Load())
	assert.Zero(t, rotatedChannelCalls.Load(), "remix must retain the origin provider account snapshot")
	var remixedTask model.Task
	require.NoError(t, fixture.db.Where("task_id = ?", remixed.ID).First(&remixedTask).Error)
	remixedProperties, err := decodeVideoTaskProperties(remixedTask.Properties)
	require.NoError(t, err)
	assert.Equal(t, "upstream-sora", remixedProperties.UpstreamModelName)
	remixedPrivateData, err := decodeVideoTaskPrivateData(remixedTask.PrivateData)
	require.NoError(t, err)
	assert.Equal(t, upstream.URL, remixedPrivateData.ChannelBaseURL)
	remixedKey, err := asyncTaskDecryptBound(
		remixedPrivateData.EncryptedChannelKey,
		videoChannelCredentialBinding(remixedTask.TaskID, remixedTask.UserId, remixedTask.ChannelId,
			remixedPrivateData.ChannelBaseURL),
	)
	require.NoError(t, err)
	assert.Equal(t, "upstream-video-key", remixedKey)

	restrictedToken := model.Token{
		UserId: fixture.user.Id, Key: "sk-video-model-restricted", Status: billingsvc.TokenStatusEnabled,
		UnlimitedQuota: true, ModelLimitsEnabled: true, ModelLimits: "sora-2-pro",
	}
	require.NoError(t, fixture.db.Create(&restrictedToken).Error)
	assert.Equal(t, http.StatusForbidden, fixture.request(
		http.MethodGet, "/v1/videos/"+created.ID, "", restrictedToken.Key,
	).Code)
	assert.Equal(t, http.StatusForbidden, fixture.request(
		http.MethodGet, "/v1/videos/"+created.ID+"/content", "", restrictedToken.Key,
	).Code)
	require.NoError(t, fixture.db.Model(&model.Task{}).Where("task_id = ?", created.ID).
		Update("group", "disallowed-task-group").Error)
	assert.Equal(t, http.StatusForbidden, fixture.request(
		http.MethodGet, "/v1/videos/"+created.ID, "", fixture.token.Key,
	).Code)
	assert.Equal(t, http.StatusForbidden, fixture.request(
		http.MethodGet, "/v1/videos/"+created.ID+"/content", "", fixture.token.Key,
	).Code)
	require.NoError(t, fixture.db.Model(&model.Task{}).Where("task_id = ?", created.ID).
		Update("group", "default").Error)

	otherUser := model.User{
		Username: "video-other-user", Status: model.UserStatusEnabled, Group: "default", Quota: 1,
	}
	require.NoError(t, fixture.db.Create(&otherUser).Error)
	otherToken := model.Token{
		UserId: otherUser.Id, Key: "sk-video-other", Status: billingsvc.TokenStatusEnabled, UnlimitedQuota: true,
	}
	require.NoError(t, fixture.db.Create(&otherToken).Error)
	forbiddenContent := fixture.request(
		http.MethodGet, "/v1/videos/"+created.ID+"/content", "", otherToken.Key,
	)
	assert.Equal(t, http.StatusNotFound, forbiddenContent.Code)
	forbiddenFetch := fixture.request(http.MethodGet, "/v1/videos/"+created.ID, "", otherToken.Key)
	assert.Equal(t, http.StatusNotFound, forbiddenFetch.Code)
	assert.Equal(t, int32(1), contentCalls.Load(), "cross-user lookup must stop before provider access")
}

func TestVideoTaskPersistsOpenAIChannelPlatformAndRejectsForeignFormat(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/videos":
			_, _ = writer.Write([]byte(`{"id":"provider-openai-video","status":"queued"}`))
		case request.Method == http.MethodGet && request.URL.Path == "/v1/videos/provider-openai-video":
			_, _ = writer.Write([]byte(`{"id":"provider-openai-video","status":"completed","progress":100}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(upstream.Close)
	fixture := newVideoTaskFixture(t, upstream.URL)
	require.NoError(t, fixture.db.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Update("type", int(channelcatalog.ChannelTypeOpenAI)).Error)

	submit := fixture.request(http.MethodPost, "/v1/videos",
		`{"model":"sora-2","prompt":"openai channel provenance","seconds":4}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, submit.Code, submit.Body.String())
	created := decodeCreatedVideoTask(t, submit)
	var task model.Task
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", created.ID).First(&task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", created.ID).First(&operation).Error)
	assert.Equal(t, videoTaskOpenAIPlatform, task.Platform)
	assert.Equal(t, task.Platform, operation.Platform)

	forceVideoTaskRecoveryDue(t, fixture.db, task.TaskID)
	require.NoError(t, reconcileAsyncVideoTasks(context.Background()))
	fetch := fixture.request(http.MethodGet, "/v1/videos/"+task.TaskID, "", fixture.token.Key)
	require.Equal(t, http.StatusOK, fetch.Code, fetch.Body.String())
	assert.Equal(t, "completed", decodeCreatedVideoTask(t, fetch).Status)

	foreignID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	foreign := model.Task{
		TaskID: foreignID, Platform: videoTaskOpenAIPlatform, UserId: fixture.user.Id,
		ChannelId: fixture.channel.Id, Status: model.TaskStatusSuccess, Properties: task.Properties,
		PrivateData: `{"foreign":"reference-task-format"}`,
	}
	require.NoError(t, fixture.db.Create(&foreign).Error)
	foreignFetch := fixture.request(http.MethodGet, "/v1/videos/"+foreignID, "", fixture.token.Key)
	assert.Equal(t, http.StatusNotFound, foreignFetch.Code, foreignFetch.Body.String())
}

func TestVideoTaskContentRejectsActiveSameOriginPayloads(t *testing.T) {
	var contentCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		contentCalls.Add(1)
		assert.Equal(t, "/v1/videos/provider-active-content/content", request.URL.Path)
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte(`<script>window.parent.fetch('/api/user/self')</script>`))
	}))
	t.Cleanup(upstream.Close)
	fixture := newVideoTaskFixture(t, upstream.URL)
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	require.NoError(t, markVideoTaskDispatching(task, reservation))
	completed := &sora.Response{ID: "provider-active-content", Status: "completed", Progress: 100}
	require.NoError(t, settleAcceptedVideoTask(
		task, reservation, completed, completed.ID, model.TaskOperationDispatching, "",
	))

	response := fixture.request(http.MethodGet, "/v1/videos/"+task.TaskID+"/content", "", fixture.token.Key)
	assert.Equal(t, http.StatusBadGateway, response.Code, response.Body.String())
	assert.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
	assert.Contains(t, response.Header().Get("Content-Security-Policy"), "sandbox")
	assert.NotContains(t, response.Body.String(), "window.parent")
	assert.Equal(t, int32(1), contentCalls.Load())
}

func TestVideoTaskDefinitiveProviderRejectionRefundsWithoutUsage(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"message":"rejected"}}`))
	}))
	t.Cleanup(upstream.Close)
	fixture := newVideoTaskFixture(t, upstream.URL)

	response := fixture.request(http.MethodPost, "/v1/videos",
		`{"model":"sora-2","prompt":"reject me"}`, fixture.token.Key)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	assert.Equal(t, int32(1), calls.Load())

	var task model.Task
	require.NoError(t, fixture.db.Order("id desc").First(&task).Error)
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationRefunded, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)

	var user model.User
	var token model.Token
	var channel model.Channel
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&channel, fixture.channel.Id).Error)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, 2_000_000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Zero(t, channel.UsedQuota)
}

func TestVideoTaskTerminalAccountingReplaysPreserveCommittedEvidence(t *testing.T) {
	t.Run("pre-dispatch refund", func(t *testing.T) {
		fixture := newVideoTaskFixture(t, "https://video.example.invalid")
		task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
		require.NoError(t, markVideoTaskDispatching(task, reservation))
		require.NoError(t, refundRejectedVideoTask(task, reservation, "first rejection reason"))
		firstReason, firstFinish := task.FailReason, task.FinishTime
		require.NoError(t, refundRejectedVideoTask(task, reservation, "different replay reason"))
		assert.Equal(t, firstReason, task.FailReason)
		assert.Equal(t, firstFinish, task.FinishTime)
		assert.Zero(t, task.Quota)
	})

	t.Run("unknown dispatch settlement", func(t *testing.T) {
		fixture := newVideoTaskFixture(t, "https://video.example.invalid")
		task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
		require.NoError(t, markVideoTaskDispatching(task, reservation))
		require.NoError(t, settleUnknownVideoDispatch(
			task, reservation, "first unknown reason", model.TaskOperationDispatching, "",
		))
		firstReason, firstData, firstFinish := task.FailReason, task.Data, task.FinishTime
		require.NoError(t, settleUnknownVideoDispatch(
			task, reservation, "different replay reason", model.TaskOperationDispatching, "",
		))
		assert.Equal(t, firstReason, task.FailReason)
		assert.Equal(t, firstData, task.Data)
		assert.Equal(t, firstFinish, task.FinishTime)
		var auditCount int64
		var operation model.TaskOperation
		require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
		require.NoError(t, fixture.db.Model(&model.AuditLogOutbox{}).
			Where("event_id = ?", "video:"+operation.ReservationID).Count(&auditCount).Error)
		assert.Equal(t, int64(1), auditCount)
	})
}

func TestVideoTaskTerminalFailureReversesSettledQuotaExactlyOnce(t *testing.T) {
	var submitCalls, fetchCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPost:
			submitCalls.Add(1)
			_, _ = writer.Write([]byte(`{"id":"provider-video-failed","status":"queued"}`))
		case http.MethodGet:
			fetchCalls.Add(1)
			_, _ = writer.Write([]byte(`{
				"id":"provider-video-failed","status":"failed","progress":100,
				"error":{"message":"provider generation failed","code":"generation_failed"}
			}`))
		}
	}))
	t.Cleanup(upstream.Close)
	fixture := newVideoTaskFixture(t, upstream.URL)

	submit := fixture.request(http.MethodPost, "/v1/videos",
		`{"model":"sora-2","prompt":"a failed scene","seconds":4}`, fixture.token.Key)
	require.Equal(t, http.StatusOK, submit.Code, submit.Body.String())
	created := decodeCreatedVideoTask(t, submit)
	forceVideoTaskRecoveryDue(t, fixture.db, created.ID)
	require.NoError(t, reconcileAsyncVideoTasks(context.Background()))
	assert.Equal(t, int32(1), submitCalls.Load())
	assert.Equal(t, int32(1), fetchCalls.Load())

	var task model.Task
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", created.ID).First(&task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", created.ID).First(&operation).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationReversed, operation.State)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusReversed, reservation.Status)

	var user model.User
	var token model.Token
	var channel model.Channel
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	require.NoError(t, fixture.db.First(&channel, fixture.channel.Id).Error)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 600_000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 2_000_000, token.RemainQuota)
	assert.Equal(t, 600_000, token.UsedQuota)
	assert.Equal(t, int64(600_000), channel.UsedQuota)

	require.NoError(t, reconcileAsyncVideoTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load(), "terminal failure must never be polled or refunded twice")
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 2_000_000, token.RemainQuota)

	var consumeAudit, refundAudit int64
	require.NoError(t, fixture.db.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", "video:"+operation.ReservationID).Count(&consumeAudit).Error)
	require.NoError(t, fixture.db.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", "video-refund:"+operation.ReservationID).Count(&refundAudit).Error)
	assert.Equal(t, int64(1), consumeAudit)
	assert.Equal(t, int64(1), refundAudit)
	var refundOutbox model.AuditLogOutbox
	require.NoError(t, fixture.db.Where("event_id = ?", "video-refund:"+operation.ReservationID).
		First(&refundOutbox).Error)
	var refundEnvelope struct {
		Log model.Log `json:"log"`
	}
	require.NoError(t, json.Unmarshal([]byte(refundOutbox.Payload), &refundEnvelope))
	assert.Equal(t, task.FinishTime, refundEnvelope.Log.CreatedAt,
		"refund reporting belongs to the reversal period, not reservation creation")

	dataBeforeReplay, finishBeforeReplay := task.Data, task.FinishTime
	require.NoError(t, reverseFailedVideoTask(&task, &operation, &sora.Response{
		ID: "provider-video-failed", Status: "failed", Progress: 100,
		Error: &sora.ResponseError{Message: "provider generation failed", Code: "generation_failed"},
	}, "provider generation failed"))
	require.NoError(t, fixture.db.Where("task_id = ?", created.ID).First(&task).Error)
	assert.Equal(t, dataBeforeReplay, task.Data)
	assert.Equal(t, finishBeforeReplay, task.FinishTime)
	require.NoError(t, fixture.db.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", "video-refund:"+operation.ReservationID).Count(&refundAudit).Error)
	assert.Equal(t, int64(1), refundAudit)
}

func TestVideoTaskMismatchedProviderPollCannotCompleteOrRefundAnotherJob(t *testing.T) {
	var fetchCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fetchCalls.Add(1)
		assert.Equal(t, "/v1/videos/provider-expected", request.URL.Path)
		_, _ = writer.Write([]byte(`{"id":"provider-other","status":"failed","progress":100}`))
	}))
	t.Cleanup(upstream.Close)
	fixture := newVideoTaskFixture(t, upstream.URL)
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	require.NoError(t, markVideoTaskDispatching(task, reservation))
	accepted := &sora.Response{ID: "provider-expected", Status: "queued"}
	require.NoError(t, settleAcceptedVideoTask(
		task, reservation, accepted, accepted.ID, model.TaskOperationDispatching, "",
	))
	forceVideoTaskRecoveryDue(t, fixture.db, task.TaskID)

	require.Error(t, reconcileAsyncVideoTasks(context.Background()))
	assert.Equal(t, int32(1), fetchCalls.Load())

	var operation model.TaskOperation
	var record model.RelayQuotaReservationRecord
	var user model.User
	var token model.Token
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", reservation.ReservationID()).First(&record).Error)
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	assert.Equal(t, model.TaskStatusQueued, task.Status)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	assert.Equal(t, 1_400_000, user.Quota)
	assert.Equal(t, 600_000, user.UsedQuota)
	assert.Equal(t, 1_400_000, token.RemainQuota)
	assert.Equal(t, 600_000, token.UsedQuota)
}

func TestVideoTaskAmbiguousDispatchChargesOnceAndNeverResubmits(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			panic("test server does not support hijacking")
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			panic(err)
		}
		_ = connection.Close()
	}))
	t.Cleanup(upstream.Close)
	fixture := newVideoTaskFixture(t, upstream.URL)

	response := fixture.request(http.MethodPost, "/v1/videos",
		`{"model":"sora-2","prompt":"unknown outcome","seconds":4}`, fixture.token.Key)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "submit_outcome_unknown")
	assert.Equal(t, int32(1), calls.Load())

	var task model.Task
	require.NoError(t, fixture.db.Order("id desc").First(&task).Error)
	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.TaskStatusUnknown, task.Status)
	assert.Equal(t, model.TaskOperationUnknown, operation.State)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)

	var user model.User
	var token model.Token
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	assert.Equal(t, 1_400_000, user.Quota)
	assert.Equal(t, 600_000, user.UsedQuota)
	assert.Equal(t, 1_400_000, token.RemainQuota)
	assert.Equal(t, 600_000, token.UsedQuota)

	require.NoError(t, reconcileAsyncVideoTasks(context.Background()))
	assert.Equal(t, int32(1), calls.Load(), "an ambiguous dispatch is never automatically submitted twice")
}

func TestVideoTaskRecoveryRefundsPreparedCrashBeforeDispatch(t *testing.T) {
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	task, _ := createPreparedVideoTaskForRecovery(t, fixture)
	forceVideoTaskRecoveryDue(t, fixture.db, task.TaskID)
	require.NoError(t, reconcileAsyncVideoTasks(context.Background()))

	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	var user model.User
	var token model.Token
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Contains(t, task.FailReason, "before provider dispatch")
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationRefunded, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
}

func TestVideoTaskPreparedRecoveryRollsBackIfTaskTransitionIsNotProvable(t *testing.T) {
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	require.NoError(t, fixture.db.Model(&model.Task{}).Where("id = ?", task.ID).
		Update("status", model.TaskStatusSubmitted).Error)
	forceVideoTaskRecoveryDue(t, fixture.db, task.TaskID)

	require.Error(t, reconcileAsyncVideoTasks(context.Background()))

	var operation model.TaskOperation
	var record model.RelayQuotaReservationRecord
	var user model.User
	var token model.Token
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", reservation.ReservationID()).First(&record).Error)
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	assert.Equal(t, model.TaskStatusSubmitted, task.Status)
	assert.Equal(t, model.TaskOperationPrepared, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusHeld, record.Status)
	assert.Equal(t, 1_400_000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 1_400_000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
}

func TestVideoTaskRecoveryRefundsPreparedOperationAtAttemptLimit(t *testing.T) {
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	task, _ := createPreparedVideoTaskForRecovery(t, fixture)
	result := fixture.db.Model(&model.TaskOperation{}).Where("task_id = ?", task.TaskID).
		Updates(map[string]any{
			"attempts":         videoOperationMaxAttempts,
			"next_attempt_at":  0,
			"lease_owner":      "",
			"lease_expires_at": 0,
		})
	require.NoError(t, result.Error)
	require.Equal(t, int64(1), result.RowsAffected)

	require.NoError(t, reconcileAsyncVideoTasks(context.Background()))

	var operation model.TaskOperation
	var reservation model.RelayQuotaReservationRecord
	var user model.User
	var token model.Token
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&reservation).Error)
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Contains(t, task.FailReason, "before provider dispatch")
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationRefunded, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
}

func TestVideoTaskRecoveryDoesNotStarveDueWorkBehindBrokenQuarantineRow(t *testing.T) {
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	brokenTask, _ := createPreparedVideoTaskForRecovery(t, fixture)
	dueTask, _ := createPreparedVideoTaskForRecovery(t, fixture)

	result := fixture.db.Model(&model.TaskOperation{}).Where("task_id = ?", brokenTask.TaskID).
		Updates(map[string]any{
			"attempts":         videoOperationMaxAttempts,
			"next_attempt_at":  0,
			"lease_owner":      "",
			"lease_expires_at": 0,
		})
	require.NoError(t, result.Error)
	require.Equal(t, int64(1), result.RowsAffected)
	var brokenOperation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", brokenTask.TaskID).First(&brokenOperation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", brokenOperation.ReservationID).
		Delete(&model.RelayQuotaReservationRecord{}).Error)
	forceVideoTaskRecoveryDue(t, fixture.db, dueTask.TaskID)

	require.Error(t, reconcileAsyncVideoTasks(context.Background()),
		"the broken row remains observable while unrelated work is drained")

	var dueOperation model.TaskOperation
	var dueReservation model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("task_id = ?", dueTask.TaskID).First(dueTask).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", dueTask.TaskID).First(&dueOperation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", dueOperation.ReservationID).
		First(&dueReservation).Error)
	assert.Equal(t, model.TaskStatusFailure, dueTask.Status)
	assert.Equal(t, model.TaskOperationRefunded, dueOperation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, dueReservation.Status)
}

func TestVideoTaskRecoveryRefundsKnownFailurePastPollingHorizon(t *testing.T) {
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	require.NoError(t, markVideoTaskDispatching(task, reservation))
	failed := &sora.Response{
		ID: "provider-known-failure", Status: "failed", Progress: 100,
		Error: &sora.ResponseError{Message: "provider rejected generation", Code: "generation_failed"},
	}
	require.NoError(t, settleAcceptedVideoTask(
		task, reservation, failed, failed.ID, model.TaskOperationDispatching, "",
	))
	require.Equal(t, model.TaskStatusFailure, task.Status)
	result := fixture.db.Model(&model.TaskOperation{}).Where("task_id = ?", task.TaskID).
		Updates(map[string]any{
			"attempts":         videoOperationMaxAttempts,
			"next_attempt_at":  0,
			"lease_owner":      "",
			"lease_expires_at": 0,
		})
	require.NoError(t, result.Error)
	require.Equal(t, int64(1), result.RowsAffected)

	require.NoError(t, reconcileAsyncVideoTasks(context.Background()))

	var operation model.TaskOperation
	var record model.RelayQuotaReservationRecord
	var user model.User
	var token model.Token
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", reservation.ReservationID()).First(&record).Error)
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Zero(t, task.Quota)
	assert.Equal(t, model.TaskOperationReversed, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusReversed, record.Status)
	assert.Equal(t, 2_000_000, user.Quota)
	assert.Equal(t, 600_000, user.UsedQuota)
	assert.Equal(t, 2_000_000, token.RemainQuota)
	assert.Equal(t, 600_000, token.UsedQuota)
}

func TestVideoTaskRecoveryConservativelySettlesDispatchingCrash(t *testing.T) {
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	require.NoError(t, markVideoTaskDispatching(task, reservation))
	forceVideoTaskRecoveryDue(t, fixture.db, task.TaskID)
	require.NoError(t, reconcileAsyncVideoTasks(context.Background()))

	var operation model.TaskOperation
	var record model.RelayQuotaReservationRecord
	var user model.User
	var token model.Token
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&record).Error)
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	assert.Equal(t, model.TaskStatusUnknown, task.Status)
	assert.Equal(t, model.TaskOperationUnknown, operation.State)
	assert.Empty(t, operation.EncryptedProviderTaskID)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	assert.Equal(t, 1_400_000, user.Quota)
	assert.Equal(t, 600_000, user.UsedQuota)
	assert.Equal(t, 1_400_000, token.RemainQuota)
	assert.Equal(t, 600_000, token.UsedQuota)

	require.NoError(t, reconcileAsyncVideoTasks(context.Background()))
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	assert.Equal(t, 1_400_000, user.Quota, "unknown dispatch accounting is terminal and exact-once")
}

func TestVideoTaskRecoverySettlesAcceptedFallbackBeforePolling(t *testing.T) {
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	require.NoError(t, markVideoTaskDispatching(task, reservation))
	provider := &sora.Response{ID: "provider-accepted-crash", Status: "queued"}
	require.NoError(t, persistAcceptedVideoFallback(task, reservation.ReservationID(), provider.ID, provider))
	forceVideoTaskRecoveryDue(t, fixture.db, task.TaskID)
	require.NoError(t, reconcileAsyncVideoTasks(context.Background()))

	var operation model.TaskOperation
	var record model.RelayQuotaReservationRecord
	var user model.User
	var token model.Token
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(task).Error)
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, fixture.db.Where("reservation_id = ?", operation.ReservationID).First(&record).Error)
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	assert.Equal(t, model.TaskStatusQueued, task.Status)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.False(t, operation.SettlementPending)
	assert.NotEmpty(t, operation.EncryptedProviderTaskID)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	assert.Equal(t, 1_400_000, user.Quota)
	assert.Equal(t, 600_000, user.UsedQuota)
	assert.Equal(t, 1_400_000, token.RemainQuota)
	assert.Equal(t, 600_000, token.UsedQuota)
}

func TestVideoTaskAcceptedFallbackIsIdempotentAndRejectsProviderIDConflict(t *testing.T) {
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	require.NoError(t, markVideoTaskDispatching(task, reservation))
	accepted := &sora.Response{ID: "provider-fallback-stable", Status: "queued"}
	require.NoError(t, persistAcceptedVideoFallback(
		task, reservation.ReservationID(), accepted.ID, accepted,
	))
	require.NoError(t, persistAcceptedVideoFallback(
		task, reservation.ReservationID(), accepted.ID, accepted,
	), "the same accepted provider identity must be replay-safe")

	conflict := &sora.Response{ID: "provider-fallback-conflict", Status: "queued"}
	require.Error(t, persistAcceptedVideoFallback(
		task, reservation.ReservationID(), conflict.ID, conflict,
	))

	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	providerID, err := asyncTaskDecryptBound(
		operation.EncryptedProviderTaskID,
		videoProviderTaskBinding(task.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID),
	)
	require.NoError(t, err)
	assert.Equal(t, accepted.ID, providerID)
	assert.Equal(t, model.TaskOperationSubmitted, operation.State)
	assert.True(t, operation.SettlementPending)
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	require.NoError(t, err)
	privateProviderID, err := asyncTaskDecryptBound(
		privateData.EncryptedUpstreamTaskID,
		videoProviderTaskBinding(task.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID),
	)
	require.NoError(t, err)
	assert.Equal(t, accepted.ID, privateProviderID)
}

func TestVideoTaskOperationProtectsActiveReservationFromGenericExpiry(t *testing.T) {
	t.Setenv("RELAY_RESERVATION_HOLD_SECONDS", "60")
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	databaseNow, err := model.DatabaseUnixTimestamp(fixture.db)
	require.NoError(t, err)
	require.NoError(t, markVideoTaskDispatching(task, reservation))

	var record model.RelayQuotaReservationRecord
	require.NoError(t, fixture.db.Where("reservation_id = ?", reservation.ReservationID()).First(&record).Error)
	assert.GreaterOrEqual(t, record.ExpiresAt, databaseNow+videoReservationSafetySeconds)
	require.NoError(t, fixture.db.Model(&model.RelayQuotaReservationRecord{}).
		Where("id = ?", record.ID).Update("expires_at", databaseNow-1).Error)
	require.NoError(t, billingsvc.ReconcileRelayQuotaReservations())
	require.NoError(t, fixture.db.First(&record, record.ID).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, record.Status,
		"generic expiry must not preempt the provider-specific recovery journal")

	require.NoError(t, fixture.db.Model(&model.TaskOperation{}).Where("task_id = ?", task.TaskID).
		Updates(map[string]any{
			"state":        model.TaskOperationManualReview,
			"completed_at": databaseNow - 8*24*60*60,
		}).Error)
	require.NoError(t, billingsvc.ReconcileRelayQuotaReservations())
	require.NoError(t, fixture.db.First(&record, record.ID).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusManualReview, record.Status,
		"expired journal protection must remain bounded")
}

func TestVideoTaskAcceptedSettlementIsIdempotentAndRejectsProviderIDConflict(t *testing.T) {
	fixture := newVideoTaskFixture(t, "https://video.example.invalid")
	task, reservation := createPreparedVideoTaskForRecovery(t, fixture)
	require.NoError(t, markVideoTaskDispatching(task, reservation))
	accepted := &sora.Response{ID: "provider-stable-id", Status: "queued"}
	require.NoError(t, settleAcceptedVideoTask(
		task, reservation, accepted, accepted.ID, model.TaskOperationDispatching, "",
	))
	require.NoError(t, settleAcceptedVideoTask(
		task, reservation, accepted, accepted.ID, model.TaskOperationDispatching, "",
	), "a replay with the same provider identity must verify the committed transition")

	conflict := &sora.Response{ID: "provider-conflicting-id", Status: "queued"}
	err := settleAcceptedVideoTask(
		task, reservation, conflict, conflict.ID, model.TaskOperationDispatching, "",
	)
	require.Error(t, err)

	var operation model.TaskOperation
	require.NoError(t, fixture.db.Where("task_id = ?", task.TaskID).First(&operation).Error)
	providerID, err := asyncTaskDecryptBound(
		operation.EncryptedProviderTaskID,
		videoProviderTaskBinding(task.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID),
	)
	require.NoError(t, err)
	assert.Equal(t, accepted.ID, providerID)
	var user model.User
	var token model.Token
	require.NoError(t, fixture.db.First(&user, fixture.user.Id).Error)
	require.NoError(t, fixture.db.First(&token, fixture.token.Id).Error)
	assert.Equal(t, 1_400_000, user.Quota)
	assert.Equal(t, 600_000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 1_400_000, token.RemainQuota)
	assert.Equal(t, 600_000, token.UsedQuota)
}
