package router_test

import (
	"encoding/base64"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"testing"
)

func TestDoubaoManualReviewResolutionRouteUsesExactProviderTuple(t *testing.T) {
	_, do, rootID := setupChannelRead(t, roles.RoleRootUser)
	require.NoError(t, model.DB.AutoMigrate(
		&model.RelayQuotaReservationRecord{}, &model.RelayQuotaReservationReviewEvent{},
		&model.Task{}, &model.TaskOperation{}, &model.JimengTaskOperation{}, &model.AuditLogOutbox{},
	))
	token := model.Token{
		UserId: rootID, Key: "sk-doubao-review-route", Status: billingsvc.TokenStatusEnabled, RemainQuota: 100,
	}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{
		Name: "doubao-review-route", Key: "provider-secret", Type: int(channelcatalog.ChannelTypeVolcEngine),
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: "https://ark.example",
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	reservation, err := billingsvc.NewRelayQuotaReservation(rootID, &token, 10)
	require.NoError(t, err)
	require.NoError(t, reservation.MarkDispatched())
	now := wallclock.NowTimestamp()
	ciphertextFrame := "async-task-v2:review-key:" +
		base64.RawURLEncoding.EncodeToString(make([]byte, 28))
	privateBytes, err := jsonutil.Marshal(map[string]any{
		"version":               1,
		"relay_reservation_id":  reservation.ReservationID(),
		"channel_base_url":      "https://ark.example",
		"encrypted_channel_key": ciphertextFrame,
		"settlement_pending":    true,
		"completion_units":      0,
		"pricing": map[string]any{
			"version": 1, "model_name": "doubao-seedance-1-0-pro-250528",
			"group_ratio": "1", "use_fixed_price": true, "fixed_price": "0.00002",
		},
		"billing_source":      billingsvc.BillingSourceWallet,
		"subscription_id":     0,
		"funding_usage_epoch": 0,
		"token_id":            token.Id,
	})
	require.NoError(t, err)
	task := model.Task{
		CreatedAt: now - 10, UpdatedAt: now, TaskID: "task_doubao_review_route",
		Platform: model.TaskOperationPlatformVolcEngine, UserId: rootID, Group: "default",
		ChannelId: channel.Id, Quota: 10, Action: "generate",
		Status:     model.TaskStatusUnknown,
		FailReason: "Doubao provider submission outcome requires manual review",
		SubmitTime: now - 10, FinishTime: now, Progress: "100%",
		Properties:  `{"version":1,"family":"doubao","prompt":"review","origin_model_name":"doubao-seedance-1-0-pro-250528","upstream_model_name":"doubao-seedance-1-0-pro-250528","action":"generate","video_input_ratio":"1"}`,
		PrivateData: "doubao-video-v1:" + string(privateBytes), Data: "null",
	}
	require.NoError(t, model.DB.Create(&task).Error)
	require.NoError(t, model.DB.Create(&model.TaskOperation{
		TaskID: task.TaskID, ReservationID: reservation.ReservationID(),
		Platform: model.TaskOperationPlatformVolcEngine, UserID: rootID, ChannelID: channel.Id,
		State: model.TaskOperationManualReview, SettlementPending: true,
		LastError: "Doubao provider submission outcome requires manual review",
		CreatedAt: now - 10, UpdatedAt: now, CompletedAt: now,
	}).Error)

	detail := do(http.MethodGet,
		"/api/relay-quota-reservations/manual-review/"+reservation.ReservationID(), "")
	require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())
	detailData := decodeBody(t, detail)["data"].(map[string]any)
	assert.Equal(t, billingsvc.RelayQuotaReviewKindDoubaoVideoTask, detailData["review_kind"])
	assert.Equal(t, model.TaskOperationPlatformVolcEngine, detailData["task_platform"])
	assert.Equal(t, true, detailData["resolution_required"])
	assert.Equal(t, false, detailData["retryable"])
	assert.NotContains(t, detail.Body.String(), "provider-secret")

	resolved := do(http.MethodPost,
		"/api/relay-quota-reservations/manual-review/"+reservation.ReservationID()+"/resolve",
		`{"resolution":"settle","actual_quota":999999,"channel_id":999999}`)
	require.Equal(t, http.StatusOK, resolved.Code, resolved.Body.String())
	result := decodeBody(t, resolved)["data"].(map[string]any)
	assert.Equal(t, true, result["changed"])
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, result["reservation_status"])
	assert.Equal(t, model.TaskOperationUnknown, result["operation_state"])

	var user model.User
	require.NoError(t, model.DB.First(&user, rootID).Error)
	require.NoError(t, model.DB.Unscoped().First(&token, token.Id).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	assert.Equal(t, 990, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
	assert.Equal(t, int64(10), channel.UsedQuota)

	replay := do(http.MethodPost,
		"/api/relay-quota-reservations/manual-review/"+reservation.ReservationID()+"/resolve",
		`{"resolution":"settle"}`)
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	assert.Equal(t, false, decodeBody(t, replay)["data"].(map[string]any)["changed"])
}
