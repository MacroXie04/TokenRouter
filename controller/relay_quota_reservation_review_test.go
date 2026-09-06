package controller_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func TestRelayQuotaReservationManualReviewRootWorkflow(t *testing.T) {
	handler, do, rootID := setupChannelRead(t, constant.RoleRootUser)
	require.NoError(t, model.DB.AutoMigrate(
		&model.RelayQuotaReservationRecord{}, &model.RelayQuotaReservationReviewEvent{},
		&model.Task{}, &model.TaskOperation{}, &model.JimengTaskOperation{},
	))
	token := model.Token{UserId: rootID, Key: "sk-review-route", Status: service.TokenStatusEnabled, RemainQuota: 1000}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{Name: "review-route-channel", Key: "provider-secret", Status: constant.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(&channel).Error)
	reservation, err := service.NewRelayQuotaReservation(rootID, &token, 10)
	require.NoError(t, err)
	now := common.NowTimestamp()
	require.NoError(t, model.DB.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ?", reservation.ReservationID()).Updates(map[string]any{
		"status":       model.RelayQuotaReservationStatusManualReview,
		"operation":    model.RelayQuotaReservationOperationSettle,
		"actual_quota": 10, "channel_id": channel.Id, "dispatched_at": now - 10,
		"attempts": 8, "next_attempt_at": 0, "completed_at": now,
		"lease_owner": "Bearer lease-owner-secret",
		"last_error":  "password=never-expose-this api_key=also-secret",
	}).Error)

	list := do(http.MethodGet, "/api/relay-quota-reservations/manual-review?page_size=1&operation=settle&user_id="+common.Int2Str(rootID), "")
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	body := decodeBody(t, list)
	assert.Equal(t, true, body["success"])
	page := body["data"].(map[string]any)
	assert.Equal(t, float64(1), page["total"])
	items := page["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	assert.Equal(t, reservation.ReservationID(), item["reservation_id"])
	assert.Equal(t, true, item["retryable"])
	assert.Equal(t, model.RelayQuotaReservationStatusPendingSettlement, item["retry_target_status"])
	assert.NotContains(t, list.Body.String(), "never-expose-this")
	assert.NotContains(t, list.Body.String(), "also-secret")
	assert.NotContains(t, list.Body.String(), "lease-owner-secret")
	assert.NotContains(t, item, "last_error")
	assert.NotContains(t, item, "lease_owner")

	detail := do(http.MethodGet, "/api/relay-quota-reservations/manual-review/"+reservation.ReservationID(), "")
	require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())
	assert.NotContains(t, detail.Body.String(), "never-expose-this")

	badFilter := do(http.MethodGet, "/api/relay-quota-reservations/manual-review?operation=force_charge", "")
	assert.Equal(t, http.StatusBadRequest, badFilter.Code)
	badUser := do(http.MethodGet, "/api/relay-quota-reservations/manual-review?user_id=abc", "")
	assert.Equal(t, http.StatusBadRequest, badUser.Code)

	retry := do(http.MethodPost, "/api/relay-quota-reservations/manual-review/"+reservation.ReservationID()+"/retry", "")
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	retryData := decodeBody(t, retry)["data"].(map[string]any)
	assert.Equal(t, true, retryData["changed"])
	auditID := retryData["audit_event_id"].(string)
	assert.NotEmpty(t, auditID)

	replay := do(http.MethodPost, "/api/relay-quota-reservations/manual-review/"+reservation.ReservationID()+"/retry", "")
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	replayData := decodeBody(t, replay)["data"].(map[string]any)
	assert.Equal(t, false, replayData["changed"])
	assert.Equal(t, auditID, replayData["audit_event_id"])
	var event model.RelayQuotaReservationReviewEvent
	require.NoError(t, model.DB.Where("event_id = ?", auditID).First(&event).Error)
	assert.Equal(t, rootID, event.OperatorUserID)
	var eventCount int64
	require.NoError(t, model.DB.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ?", reservation.ReservationID()).Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount)

	// The resolution route accepts only an explicit settle/refund decision and
	// rejects generic manual-review rows that do not carry the exact Jimeng
	// unresolved-provider tuple.
	resolve := do(http.MethodPost, "/api/relay-quota-reservations/manual-review/"+reservation.ReservationID()+"/resolve", `{}`)
	assert.Equal(t, http.StatusBadRequest, resolve.Code)

	request := httptest.NewRequest(http.MethodGet, "/api/relay-quota-reservations/manual-review", nil)
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, request)
	assert.Equal(t, http.StatusUnauthorized, anonymous.Code)
}

func TestRelayQuotaReservationManualReviewRejectsNonRootAdmin(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleAdminUser)
	request := do(http.MethodGet, "/api/relay-quota-reservations/manual-review", "")
	assert.Equal(t, http.StatusForbidden, request.Code)
	resolution := do(http.MethodPost,
		"/api/relay-quota-reservations/manual-review/not-authorized/resolve", `{"resolution":"settle"}`)
	assert.Equal(t, http.StatusForbidden, resolution.Code)
	retry := do(http.MethodPost,
		"/api/relay-quota-reservations/manual-review/not-authorized/retry", "")
	assert.Equal(t, http.StatusForbidden, retry.Code)
}

func TestGrokViolationFeeManualReviewRootRetryUsesImmutableStoredPlan(t *testing.T) {
	_, do, rootID := setupChannelRead(t, constant.RoleRootUser)
	require.NoError(t, model.DB.AutoMigrate(
		&model.RelayQuotaReservationRecord{}, &model.RelayQuotaReservationReviewEvent{},
		&model.Task{}, &model.TaskOperation{}, &model.JimengTaskOperation{},
	))
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.GrokViolationDeductionEnabledOption: "true",
		setting.GrokViolationDeductionAmountOption:  "0.00002",
	}))
	t.Cleanup(func() { _ = setting.UpdateOptions(setting.GrokOptionDefaults()) })

	token := model.Token{
		UserId: rootID, Key: "sk-grok-review-route", Name: "grok-review-token",
		Status: service.TokenStatusEnabled, RemainQuota: 1000,
	}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{
		Name: "grok-review-xai", Key: "provider-secret-never-return",
		Type: int(constant.ChannelTypeXai), Status: constant.ChannelStatusEnabled, UsedQuota: 7,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	reservation, err := service.NewRelayQuotaReservation(rootID, &token, 5)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	require.NoError(t, reservation.Refund())
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", rootID).Update("quota", 9).Error)
	_, err = service.ChargeGrokViolationFee(service.GrokViolationFeeInput{
		ReservationID: reservation.ReservationID(), ChannelID: channel.Id,
		ModelName: "grok-4", Group: service.GroupDefault,
		RequestID:  "00000000-0000-4000-8000-000000000099",
		StatusCode: http.StatusBadRequest, UseTime: 2, GroupRatio: 1,
	})
	require.ErrorIs(t, err, service.ErrInsufficientQuota)

	list := do(http.MethodGet,
		"/api/relay-quota-reservations/manual-review?reservation_id="+reservation.ReservationID(), "")
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	items := decodeBody(t, list)["data"].(map[string]any)["items"].([]any)
	require.Len(t, items, 1)
	listed := items[0].(map[string]any)
	assert.Equal(t, service.RelayQuotaReviewKindGrokViolationFee, listed["review_kind"])
	assert.Equal(t, true, listed["retryable"])
	assert.Equal(t, model.RelayQuotaViolationFeeStatusCharged, listed["retry_target_status"])
	assert.NotContains(t, list.Body.String(), "provider-secret-never-return")

	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", rootID).Update("quota", 1000).Error)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.GrokViolationDeductionEnabledOption: "false",
		setting.GrokViolationDeductionAmountOption:  "1.25",
	}))
	retry := do(http.MethodPost,
		"/api/relay-quota-reservations/manual-review/"+reservation.ReservationID()+"/retry", "")
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	retryData := decodeBody(t, retry)["data"].(map[string]any)
	assert.Equal(t, true, retryData["changed"])
	assert.NotContains(t, retry.Body.String(), "provider-secret-never-return")
	auditID := retryData["audit_event_id"].(string)

	replay := do(http.MethodPost,
		"/api/relay-quota-reservations/manual-review/"+reservation.ReservationID()+"/retry", "")
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	replayData := decodeBody(t, replay)["data"].(map[string]any)
	assert.Equal(t, false, replayData["changed"])
	assert.Equal(t, auditID, replayData["audit_event_id"])

	var user model.User
	require.NoError(t, model.DB.First(&user, rootID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	assert.Equal(t, 990, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 990, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
	assert.Equal(t, int64(17), channel.UsedQuota)
	var event model.RelayQuotaReservationReviewEvent
	require.NoError(t, model.DB.Where("event_id = ?", auditID).First(&event).Error)
	assert.Equal(t, rootID, event.OperatorUserID)
	assert.Equal(t, model.RelayQuotaReservationReviewActionRetryFee, event.Action)
	assert.Equal(t, 10, event.ViolationFeeQuota)
}

func TestRelayQuotaReservationJimengResolutionRootOnlyAndImmutable(t *testing.T) {
	_, do, rootID := setupChannelRead(t, constant.RoleRootUser)
	require.NoError(t, model.DB.AutoMigrate(
		&model.RelayQuotaReservationRecord{}, &model.RelayQuotaReservationReviewEvent{},
		&model.Task{}, &model.TaskOperation{}, &model.JimengTaskOperation{},
	))
	token := model.Token{
		UserId: rootID, Key: "sk-jimeng-review-route", Status: service.TokenStatusEnabled, RemainQuota: 1000,
	}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{
		Name: "jimeng-review-route", Key: "provider-secret", Type: int(constant.ChannelTypeJimeng),
		Status: constant.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	reservation, err := service.NewRelayQuotaReservation(rootID, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())

	now := common.NowTimestamp()
	taskID := model.GenerateTaskID()
	privateBytes, err := common.Marshal(map[string]any{
		"relay_reservation_id":  reservation.ReservationID(),
		"billing_source":        service.BillingSourceWallet,
		"funding_reserved":      10,
		"token_id":              token.Id,
		"token_reserved":        true,
		"channel_base_url":      "https://provider.example.invalid",
		"encrypted_channel_key": "encrypted-provider-credential",
	})
	require.NoError(t, err)
	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID,
		Platform: common.Int2Str(int(constant.ChannelTypeJimeng)), UserId: rootID,
		Group: "default", ChannelId: channel.Id, Quota: 10, Action: "generate",
		Status: model.TaskStatusUnknown, FailReason: "provider submission outcome requires manual review",
		SubmitTime: now, FinishTime: now, Progress: "100%",
		Properties:  `{"upstream_model_name":"jimeng-review","origin_model_name":"jimeng-review"}`,
		PrivateData: "jimeng-v2:" + string(privateBytes), Data: `null`,
	}
	require.NoError(t, model.DB.Create(&task).Error)
	require.NoError(t, model.DB.Create(&model.JimengTaskOperation{
		TaskID: task.TaskID, ReservationID: reservation.ReservationID(),
		UserID: rootID, ChannelID: channel.Id, State: model.JimengTaskOperationManualReview,
		LastError: "provider_outcome_unresolved", CreatedAt: now, UpdatedAt: now,
		CompletedAt: now,
	}).Error)
	detail := do(http.MethodGet,
		"/api/relay-quota-reservations/manual-review/"+reservation.ReservationID(), "")
	require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())
	detailData := decodeBody(t, detail)["data"].(map[string]any)
	assert.Equal(t, true, detailData["resolution_required"])
	assert.Equal(t, []any{"settle", "refund"}, detailData["resolution_options"])

	// Extraneous economic fields cannot override the immutable durable values.
	settle := do(http.MethodPost,
		"/api/relay-quota-reservations/manual-review/"+reservation.ReservationID()+"/resolve",
		`{"resolution":"settle","actual_quota":999999,"channel_id":999999}`)
	require.Equal(t, http.StatusOK, settle.Code, settle.Body.String())
	settleData := decodeBody(t, settle)["data"].(map[string]any)
	assert.Equal(t, true, settleData["changed"])
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, settleData["reservation_status"])
	assert.Equal(t, model.JimengTaskOperationUnknown, settleData["operation_state"])
	auditID := settleData["audit_event_id"].(string)
	assert.NotEmpty(t, auditID)

	var user model.User
	require.NoError(t, model.DB.First(&user, rootID).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	assert.Equal(t, 990, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 990, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
	assert.Equal(t, int64(10), channel.UsedQuota)

	replay := do(http.MethodPost,
		"/api/relay-quota-reservations/manual-review/"+reservation.ReservationID()+"/resolve",
		`{"resolution":"settle"}`)
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	replayData := decodeBody(t, replay)["data"].(map[string]any)
	assert.Equal(t, false, replayData["changed"])
	assert.Equal(t, auditID, replayData["audit_event_id"])

	conflict := do(http.MethodPost,
		"/api/relay-quota-reservations/manual-review/"+reservation.ReservationID()+"/resolve",
		`{"resolution":"refund"}`)
	assert.Equal(t, http.StatusConflict, conflict.Code)
	invalid := do(http.MethodPost,
		"/api/relay-quota-reservations/manual-review/"+reservation.ReservationID()+"/resolve",
		`{"resolution":"force_charge"}`)
	assert.Equal(t, http.StatusBadRequest, invalid.Code)

	var reviewCount int64
	require.NoError(t, model.DB.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ?", reservation.ReservationID()).Count(&reviewCount).Error)
	assert.EqualValues(t, 1, reviewCount)
	var event model.RelayQuotaReservationReviewEvent
	require.NoError(t, model.DB.Where("event_id = ?", auditID).First(&event).Error)
	assert.Equal(t, rootID, event.OperatorUserID)
	assert.Equal(t, service.RelayQuotaReviewActionResolveSettle, event.Action)
}

func TestRelayQuotaReservationVideoPollReviewListsAndRejectsMissingProviderID(t *testing.T) {
	_, do, rootID := setupChannelRead(t, constant.RoleRootUser)
	require.NoError(t, model.DB.AutoMigrate(
		&model.RelayQuotaReservationRecord{}, &model.RelayQuotaReservationReviewEvent{},
		&model.Task{}, &model.TaskOperation{}, &model.JimengTaskOperation{},
	))
	token := model.Token{
		UserId: rootID, Key: "sk-video-review-route", Status: service.TokenStatusEnabled, RemainQuota: 1000,
	}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{
		Name: "video-review-route", Key: "provider-secret", Type: int(constant.ChannelTypeSora),
		Status: constant.ChannelStatusEnabled, BaseURL: "https://provider.example.invalid",
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	reservation, err := service.NewRelayQuotaReservation(rootID, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	require.NoError(t, reservation.SettleWithChannel(10, channel.Id))
	now := common.NowTimestamp()
	ciphertextFrame := "async-task-v2:review-key:" +
		base64.RawURLEncoding.EncodeToString(make([]byte, 28))
	privateBytes, err := common.Marshal(map[string]any{
		"relay_reservation_id":  reservation.ReservationID(),
		"channel_base_url":      "https://provider.example.invalid",
		"encrypted_channel_key": ciphertextFrame,
		"billing_source":        service.BillingSourceWallet,
		"token_id":              token.Id,
	})
	require.NoError(t, err)
	task := model.Task{
		CreatedAt: now - 10, UpdatedAt: now, TaskID: "task_video_review_route",
		Platform: model.TaskOperationPlatformOpenAI, UserId: rootID, Group: "default",
		ChannelId: channel.Id, Quota: 10, Action: "textGenerate",
		Status: model.TaskStatusUnknown, FailReason: "provider polling needs manual review",
		SubmitTime: now - 10, FinishTime: now, Progress: "17%",
		Properties:  `{"input":"review","upstream_model_name":"sora","origin_model_name":"sora-2","seconds":4,"size":"720x1280"}`,
		PrivateData: "openai-video-v1:" + string(privateBytes), Data: `null`,
	}
	require.NoError(t, model.DB.Create(&task).Error)
	operation := model.TaskOperation{
		TaskID: task.TaskID, ReservationID: reservation.ReservationID(),
		Platform: model.TaskOperationPlatformOpenAI, UserID: rootID, ChannelID: channel.Id,
		State: model.TaskOperationManualReview, Attempts: 20_000,
		LastError: "Bearer do-not-return-video-error", CreatedAt: now - 10,
		UpdatedAt: now, CompletedAt: now,
	}
	require.NoError(t, model.DB.Create(&operation).Error)

	list := do(http.MethodGet, "/api/relay-quota-reservations/manual-review?reservation_id="+reservation.ReservationID(), "")
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	page := decodeBody(t, list)["data"].(map[string]any)
	items := page["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	assert.Equal(t, service.RelayQuotaReviewKindOpenAIVideoPoll, item["review_kind"])
	assert.Equal(t, task.TaskID, item["task_id"])
	assert.Equal(t, false, item["provider_task_id_present"])
	assert.Equal(t, false, item["retryable"])
	assert.Equal(t, "video_provider_id_unavailable", item["diagnostic_code"])
	assert.NotContains(t, list.Body.String(), "do-not-return-video-error")

	retry := do(http.MethodPost,
		"/api/relay-quota-reservations/manual-review/"+reservation.ReservationID()+"/retry", "")
	assert.Equal(t, http.StatusConflict, retry.Code, retry.Body.String())
	require.NoError(t, model.DB.First(&task, task.ID).Error)
	require.NoError(t, model.DB.First(&operation, operation.ID).Error)
	assert.Equal(t, model.TaskStatusUnknown, task.Status)
	assert.Equal(t, model.TaskOperationManualReview, operation.State)
}
