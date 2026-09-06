package relay

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

type relayAccountingFixture struct {
	user    model.User
	token   model.Token
	channel model.Channel
}

func newRelayAccountingFixture(t *testing.T, userQuota, tokenQuota int) relayAccountingFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	oldDB, oldLogDB := model.DB, model.LOG_DB
	dsn := "file:" + filepath.Join(t.TempDir(), "relay-accounting.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{}, &model.AuditLogOutbox{}, &model.PerfMetric{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{},
		&model.RelayQuotaReservationRecord{},
		&model.Option{},
	))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init())
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	previousSpecialRatios := service.ExportedGroupGroupRatios()
	service.SetGroupRatios(map[string]float64{"default": 1})
	service.SetGroupGroupRatios(map[string]map[string]float64{})
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
		service.SetGroupGroupRatios(previousSpecialRatios)
		model.DB = oldDB
		model.LOG_DB = oldLogDB
	})

	user := model.User{
		Username: "accounting-user", Password: "x", Role: constant.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: "default", Quota: userQuota, AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-accounting", Name: "accounting-token",
		Status: service.TokenStatusEnabled, RemainQuota: tokenQuota,
	}
	require.NoError(t, db.Create(&token).Error)
	weight := uint(1)
	channel := model.Channel{
		Name: "accounting-channel", Type: int(constant.ChannelTypeOpenAI), Key: "upstream-key",
		Status: constant.ChannelStatusEnabled, BaseURL: "https://example.invalid", Models: "accounting-model",
		Group: "default", Weight: &weight,
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group: "default", Model: "accounting-model", ChannelId: channel.Id, Enabled: true, Weight: 1,
	}).Error)
	require.NoError(t, service.InitAbilityCache())
	return relayAccountingFixture{user: user, token: token, channel: channel}
}

func TestXAIExactSafetyViolationSkipsRetryRefundsThenChargesAtomically(t *testing.T) {
	t.Setenv("RETRY_TIMES", "2")
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.GrokViolationDeductionEnabledOption: "true",
		setting.GrokViolationDeductionAmountOption:  "0.00002",
	}))
	t.Cleanup(func() { _ = setting.UpdateOptions(setting.GrokOptionDefaults()) })
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
		Update("type", int(constant.ChannelTypeXai)).Error)
	second := model.Channel{
		Name: "second-xai", Type: int(constant.ChannelTypeXai), Key: "upstream-2",
		Status: constant.ChannelStatusEnabled, BaseURL: "https://example.invalid", Models: "accounting-model",
		Group: "default", Weight: fixture.channel.Weight,
	}
	require.NoError(t, model.DB.Create(&second).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "accounting-model", ChannelId: second.Id, Enabled: true, Weight: 1,
	}).Error)
	require.NoError(t, service.InitAbilityCache())

	var dispatches atomic.Int32
	selectedChannel := 0
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)
	err := relayAndSettleWithDispatch(c, info, func(_ *gin.Context, selected *RelayInfo) (*protocolkit.Usage, error) {
		dispatches.Add(1)
		selectedChannel = selected.Channel.Id
		return nil, &relaycommon.UpstreamError{
			StatusCode: http.StatusBadGateway,
			Body:       `{"error":{"message":"private request content: Failed check: SAFETY_CHECK_TYPE private-secret"}}`,
		}
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), dispatches.Load(), "the stable violation classification must bypass ordinary retries")
	assert.Equal(t, http.StatusBadGateway, recorder.Code)
	assert.Contains(t, recorder.Body.String(), relaycommon.GrokViolationErrorCode)
	assert.NotContains(t, recorder.Body.String(), "private")
	assert.NotContains(t, recorder.Body.String(), relaycommon.GrokCSAMViolationMarker)

	var user model.User
	var token model.Token
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	assert.Equal(t, 99_990, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 99_990, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
	var channels []model.Channel
	require.NoError(t, model.DB.Where("id IN ?", []int{fixture.channel.Id, second.Id}).Find(&channels).Error)
	var totalUsed int64
	for _, channel := range channels {
		totalUsed += channel.UsedQuota
		if channel.Id == selectedChannel {
			assert.Equal(t, int64(10), channel.UsedQuota)
		}
	}
	assert.Equal(t, int64(10), totalUsed)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Order("id desc").First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	assert.Equal(t, selectedChannel, reservation.ViolationFeeChannelID)
	assert.Equal(t, 10, reservation.ViolationFeeQuota)
	var logs, outboxes int64
	require.NoError(t, model.DB.Model(&model.Log{}).Count(&logs).Error)
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).Count(&outboxes).Error)
	assert.Equal(t, int64(1), logs)
	assert.Equal(t, int64(1), outboxes)
}

func TestNonXAISafetyMarkerCannotTriggerViolationCharge(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.GrokViolationDeductionEnabledOption: "true",
		setting.GrokViolationDeductionAmountOption:  "0.00002",
	}))
	t.Cleanup(func() { _ = setting.UpdateOptions(setting.GrokOptionDefaults()) })
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)
	require.NoError(t, relayAndSettleWithDispatch(c, info, func(_ *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		return nil, &relaycommon.UpstreamError{
			StatusCode: http.StatusBadRequest,
			Body:       `{"error":{"message":"Failed check: SAFETY_CHECK_TYPE"}}`,
		}
	}))
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), relaycommon.GrokViolationErrorCode)
	var user model.User
	var token model.Token
	var channel model.Channel
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
	assert.Equal(t, 100_000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 100_000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Zero(t, channel.UsedQuota)
	var outboxes int64
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).Count(&outboxes).Error)
	assert.Zero(t, outboxes)
}

func newRelayAccountingContext(t *testing.T, token *model.Token, maxTokens int) (*gin.Context, *httptest.ResponseRecorder, *RelayInfo) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"accounting-model"}`))
	common.SetUserId(c, token.UserId)
	common.SetUsername(c, "accounting-user")
	common.SetUserGroup(c, "default")
	middleware.SetupRelayTokenContext(c, token)
	request := &protocolkit.GeneralOpenAIRequest{
		Model:     "accounting-model",
		MaxTokens: &maxTokens,
		Extra:     map[string]any{"model": "accounting-model", "max_tokens": maxTokens},
	}
	return c, recorder, &RelayInfo{
		Mode: constant.RelayModeChatCompletions, Format: constant.RelayFormatOpenAI,
		ModelName: request.Model, Request: request, UserGroup: "default", Group: "default",
	}
}

func relayReservationForTest(t *testing.T, maxTokens int) int {
	t.Helper()
	request := &protocolkit.GeneralOpenAIRequest{Model: "accounting-model", MaxTokens: &maxTokens}
	info := &RelayInfo{ModelName: request.Model, Request: request, Group: "default"}
	info.PromptTokens = relaycommon.EstimatePromptTokens(request)
	quota, err := estimateReservationChecked(info)
	require.NoError(t, err)
	return quota
}

func TestOrdinaryRelayReservationPreventsConcurrentTokenOverspend(t *testing.T) {
	const maxTokens = 64
	// Compute the exact pre-dispatch amount and leave room for only one
	// in-flight request on the limited token.
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	reserved := relayReservationForTest(t, maxTokens)
	require.Positive(t, reserved)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("key = ?", "sk-accounting").Update("remain_quota", reserved).Error)

	var token model.Token
	require.NoError(t, model.DB.Where("key = ?", "sk-accounting").First(&token).Error)
	firstContext, firstRecorder, firstInfo := newRelayAccountingContext(t, &token, maxTokens)
	secondToken := token
	secondContext, secondRecorder, secondInfo := newRelayAccountingContext(t, &secondToken, maxTokens)

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var dispatches atomic.Int32
	dispatch := func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		dispatches.Add(1)
		started <- struct{}{}
		<-release
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return &protocolkit.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}, nil
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- relayAndSettleWithDispatch(firstContext, firstInfo, dispatch) }()
	<-started // the first reservation is held while its upstream is in flight

	secondErr := relayAndSettleWithDispatch(secondContext, secondInfo, dispatch)
	require.NoError(t, secondErr)
	assert.Equal(t, http.StatusBadRequest, secondRecorder.Code)
	assert.Contains(t, secondRecorder.Body.String(), "insufficient_quota")
	assert.Equal(t, int32(1), dispatches.Load(), "the rejected request must never contact an upstream")

	close(release)
	require.NoError(t, <-firstDone)
	require.Equal(t, http.StatusOK, firstRecorder.Code)

	actual := service.ComputeQuota("accounting-model", "default", 1, 1)
	var gotUser model.User
	var gotToken model.Token
	var gotChannel model.Channel
	require.NoError(t, model.DB.First(&gotUser, token.UserId).Error)
	require.NoError(t, model.DB.Unscoped().First(&gotToken, token.Id).Error)
	require.NoError(t, model.DB.First(&gotChannel, fixture.channel.Id).Error)
	assert.Equal(t, 100_000-actual, gotUser.Quota)
	assert.Equal(t, actual, gotUser.UsedQuota)
	assert.Equal(t, 1, gotUser.RequestCount)
	assert.Equal(t, reserved-actual, gotToken.RemainQuota)
	assert.Equal(t, actual, gotToken.UsedQuota)
	assert.Equal(t, int64(actual), gotChannel.UsedQuota)
	var logs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", service.LogTypeConsume).Count(&logs).Error)
	assert.Equal(t, int64(1), logs)
}

func TestOrdinaryRelayTieredMultimodalUsageSettlesExactAccounting(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	require.NoError(t, setting.Init())
	previousPrices := service.ExportedModelPrices()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{
		"accounting-model": {Prompt: 2, Completion: 2},
	})
	t.Cleanup(func() { service.SetModelPriceRegistry(previousPrices) })
	require.NoError(t, setting.UpdateOptions(map[string]string{
		"ModelBillingMode": `{"accounting-model":"tiered_expr"}`,
		"ModelBillingExpr": `{"accounting-model":"p * 2 + c * 4 + img * 10 + ai * 14 + img_o * 18 + ao * 22"}`,
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(map[string]string{"ModelBillingMode": `{}`, "ModelBillingExpr": `{}`})
	})

	const (
		reservedQuota = 512
		actualQuota   = 279
	)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 512)
	usage := &protocolkit.Usage{
		PromptTokens:     31,
		CompletionTokens: 29,
		TotalTokens:      60,
		PromptTokensDetails: &protocolkit.InputTokenDetails{
			ImageTokens: 3, AudioTokens: 5,
		},
		CompletionTokensDetails: &protocolkit.OutputTokenDetails{
			ImageTokens: 7, AudioTokens: 11,
		},
	}
	err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		// The flat $2/M reservation holds 512 quota before upstream contact.
		var held model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.Order("id desc").First(&held).Error)
		assert.Equal(t, model.RelayQuotaReservationStatusDispatched, held.Status)
		assert.Equal(t, reservedQuota, held.RequestedQuota)
		assert.Equal(t, reservedQuota, held.ReservedQuota)
		assert.Equal(t, reservedQuota, held.TokenReserved)
		assert.Equal(t, reservedQuota, held.ActualQuota)
		var heldUser model.User
		var heldToken model.Token
		require.NoError(t, model.DB.First(&heldUser, fixture.user.Id).Error)
		require.NoError(t, model.DB.First(&heldToken, fixture.token.Id).Error)
		assert.Equal(t, 100_000-reservedQuota, heldUser.Quota)
		assert.Equal(t, 100_000-reservedQuota, heldToken.RemainQuota)
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return usage, nil
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	// p=(31-3-5), c=(29-7-11); cost is
	// 23*2 + 11*4 + 3*10 + 5*14 + 7*18 + 11*22 = 558 price-units.
	// At 500,000 quota/USD this rounds exactly to 279 quota. Each modality is
	// removed once from p/c and then charged once at its independent rate.
	require.Nil(t, info.QuotaClamp)
	assert.Equal(t, actualQuota, info.Quota)
	var user model.User
	var token model.Token
	var channel model.Channel
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
	assert.Equal(t, 100_000-actualQuota, user.Quota)
	assert.Equal(t, actualQuota, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 100_000-actualQuota, token.RemainQuota,
		"the unused 233-quota hold is refunded exactly once")
	assert.Equal(t, actualQuota, token.UsedQuota)
	assert.Equal(t, int64(actualQuota), channel.UsedQuota)

	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, model.RelayQuotaReservationOperationSettle, reservation.Operation)
	assert.Equal(t, reservedQuota, reservation.RequestedQuota)
	assert.Equal(t, reservedQuota, reservation.ReservedQuota)
	assert.Equal(t, reservedQuota, reservation.TokenReserved)
	assert.Equal(t, actualQuota, reservation.ActualQuota)
	assert.Equal(t, fixture.channel.Id, reservation.ChannelID)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", service.LogTypeConsume).First(&log).Error)
	assert.Equal(t, fixture.user.Id, log.UserId)
	assert.Equal(t, fixture.token.Id, log.TokenId)
	assert.Equal(t, fixture.channel.Id, log.ChannelId)
	assert.Equal(t, "accounting-model", log.ModelName)
	assert.Equal(t, "default", log.Group)
	assert.Equal(t, 31, log.PromptTokens)
	assert.Equal(t, 29, log.CompletionTokens)
	assert.Equal(t, actualQuota, log.Quota)
	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
	assert.Equal(t, service.BillingSourceWallet, other["billing_source"])
	assert.Equal(t, reservation.ReservationID, other["relay_reservation_id"])
	var logCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", service.LogTypeConsume).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)
}

func TestOrdinaryRelayTieredMultimodalBucketsPriceIndependently(t *testing.T) {
	modalUsage := func() *protocolkit.Usage {
		return &protocolkit.Usage{
			PromptTokens: 31, CompletionTokens: 29, TotalTokens: 60,
			PromptTokensDetails: &protocolkit.InputTokenDetails{
				ImageTokens: 3, AudioTokens: 5,
			},
			CompletionTokensDetails: &protocolkit.OutputTokenDetails{
				ImageTokens: 7, AudioTokens: 11,
			},
		}
	}
	tests := []struct {
		name     string
		expr     string
		usage    func() *protocolkit.Usage
		expected int
	}{
		{
			name: "ordinary text remains on p and c", expr: `p * 2 + c * 4`,
			usage: func() *protocolkit.Usage {
				return &protocolkit.Usage{PromptTokens: 31, CompletionTokens: 29, TotalTokens: 60}
			},
			expected: 89,
		},
		{name: "unreferenced modalities remain on p and c", expr: `p * 2 + c * 4`, usage: modalUsage, expected: 89},
		{name: "image input", expr: `p * 2 + c * 4 + img * 10`, usage: modalUsage, expected: 101},
		{name: "audio input", expr: `p * 2 + c * 4 + ai * 14`, usage: modalUsage, expected: 119},
		{name: "image output", expr: `p * 2 + c * 4 + img_o * 18`, usage: modalUsage, expected: 138},
		{name: "audio output", expr: `p * 2 + c * 4 + ao * 22`, usage: modalUsage, expected: 188},
		{
			name:  "all independent buckets without double counting",
			expr:  `p * 2 + c * 4 + img * 10 + ai * 14 + img_o * 18 + ao * 22`,
			usage: modalUsage, expected: 279,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRelayAccountingFixture(t, 100_000, 100_000)
			require.NoError(t, setting.Init())
			previousPrices := service.ExportedModelPrices()
			service.SetModelPriceRegistry(map[string]service.ModelPrice{
				"accounting-model": {Prompt: 2, Completion: 2},
			})
			t.Cleanup(func() { service.SetModelPriceRegistry(previousPrices) })
			expressions, err := json.Marshal(map[string]string{"accounting-model": test.expr})
			require.NoError(t, err)
			require.NoError(t, setting.UpdateOptions(map[string]string{
				"ModelBillingMode": `{"accounting-model":"tiered_expr"}`,
				"ModelBillingExpr": string(expressions),
			}))
			t.Cleanup(func() {
				_ = setting.UpdateOptions(map[string]string{"ModelBillingMode": `{}`, "ModelBillingExpr": `{}`})
			})

			c, recorder, info := newRelayAccountingContext(t, &fixture.token, 512)
			err = relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
				c.JSON(http.StatusOK, gin.H{"ok": true})
				return test.usage(), nil
			})
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, test.expected, info.Quota)
			require.Nil(t, info.QuotaClamp)

			var user model.User
			var token model.Token
			var reservation model.RelayQuotaReservationRecord
			var log model.Log
			require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
			require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
			require.NoError(t, model.DB.First(&reservation).Error)
			require.NoError(t, model.LOG_DB.Where("type = ?", service.LogTypeConsume).First(&log).Error)
			assert.Equal(t, 100_000-test.expected, user.Quota)
			assert.Equal(t, test.expected, user.UsedQuota)
			assert.Equal(t, 100_000-test.expected, token.RemainQuota)
			assert.Equal(t, test.expected, token.UsedQuota)
			assert.Equal(t, 512, reservation.ReservedQuota)
			assert.Equal(t, 512, reservation.TokenReserved)
			assert.Equal(t, test.expected, reservation.ActualQuota)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
			assert.Equal(t, test.expected, log.Quota)
			assert.Equal(t, 31, log.PromptTokens)
			assert.Equal(t, 29, log.CompletionTokens)
		})
	}
}

func TestQuotaSaturationMarkerPersistsInConsumptionAuditLog(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	c, _, info := newRelayAccountingContext(t, &fixture.token, 64)
	info.QuotaClamp = &common.QuotaClamp{Reason: "overflow", Value: "1e100"}

	require.NoError(t, service.RecordConsumeLogChecked(
		fixture.user.Id, fixture.user.Username, fixture.token.Name, "accounting-model",
		1, 1, 2, 3, false, fixture.channel.Id, "default", "127.0.0.1",
		"request-saturation", "", fixture.token.Id, buildLogOther(c, info, nil),
	))
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("request_id = ?", "request-saturation").First(&log).Error)
	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
	marker, ok := other["quota_saturation"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "overflow", marker["reason"])
	assert.Equal(t, "1e100", marker["value"])
}

func TestSuccessfulStreamWithoutProviderUsageSettlesPromptEstimate(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)
	info.Request.Stream = true
	info.Request.Extra["stream"] = true
	expectedPrompt := relaycommon.EstimatePromptTokens(info.Request)
	expectedQuota := service.ComputeQuota("accounting-model", "default", expectedPrompt, 0)

	err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		c.Header("Content-Type", "text/event-stream")
		c.Status(http.StatusOK)
		_, _ = c.Writer.WriteString("data: [DONE]\n\n")
		return nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)

	var user model.User
	var token model.Token
	var channel model.Channel
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
	assert.Equal(t, expectedQuota, user.UsedQuota)
	assert.Equal(t, expectedQuota, token.UsedQuota)
	assert.Equal(t, int64(expectedQuota), channel.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
}

func TestOrdinaryRelayPersistsDispatchedStateBeforeUpstreamCall(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 24)
	var observedId string

	err := relayAndSettleWithDispatch(c, info, func(_ *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		var record model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.Order("id desc").First(&record).Error)
		observedId = record.ReservationID
		assert.Equal(t, model.RelayQuotaReservationStatusDispatched, record.Status)
		assert.Positive(t, record.DispatchedAt)
		return nil, errors.New("injected upstream failure")
	})
	require.NoError(t, err)
	assert.NotEmpty(t, observedId)
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	var record model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("reservation_id = ?", observedId).First(&record).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, record.Status,
		"an explicit pre-response failure may still release a dispatched reservation")
}

func TestOrdinaryRelayDispatchMarkerFailureNeverContactsUpstream(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 24)
	var failMarker atomic.Bool
	failMarker.Store(true)
	callbackName := "test:fail_ordinary_dispatch_marker"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.RelayQuotaReservationRecord{}).TableName() && failMarker.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected dispatch marker failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })
	var dispatched atomic.Bool

	err := relayAndSettleWithDispatch(c, info, func(_ *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		dispatched.Store(true)
		return nil, nil
	})
	require.ErrorContains(t, err, "mark relay quota reservation dispatched")
	assert.False(t, dispatched.Load())
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "pre_consume_failed")
	var user model.User
	var token model.Token
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	assert.Equal(t, 100_000, user.Quota)
	assert.Equal(t, 100_000, token.RemainQuota)
	var record model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Order("id desc").First(&record).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, record.Status)
}

func TestAutoGroupRelayUsesAuthorizedFallbackAndActualGroupBilling(t *testing.T) {
	const maxTokens = 64
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	service.SetGroupRatios(map[string]float64{"default": 1, "staff": 3, "vip": 2})
	require.NoError(t, model.DB.Where(map[string]any{"group": "default", "model": "accounting-model"}).Delete(&model.Ability{}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "vip", Model: "accounting-model", ChannelId: fixture.channel.Id, Enabled: true, Weight: 1,
	}).Error)
	require.NoError(t, service.InitAbilityCache())
	fixture.token.Group = service.GroupAuto
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, maxTokens)
	middleware.SetRelayGroupPolicy(c, service.RelayGroupPolicy{
		Groups: []string{"staff", "vip"}, Auto: true, CrossGroupRetry: true,
	})

	err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, selected *RelayInfo) (*protocolkit.Usage, error) {
		assert.Equal(t, "vip", selected.Group)
		assert.Equal(t, "vip", middleware.GetTokenGroup(c))
		staffWorstCase, estimateErr := estimateReservationForGroupChecked(selected, "staff")
		require.NoError(t, estimateErr)
		vipEstimate, estimateErr := estimateReservationForGroupChecked(selected, "vip")
		require.NoError(t, estimateErr)
		require.Greater(t, staffWorstCase, vipEstimate)
		var durableHold model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.Order("id desc").First(&durableHold).Error)
		assert.Equal(t, staffWorstCase, durableHold.RequestedQuota,
			"the pre-dispatch hold must cover the highest authorized retry-group price")
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return &protocolkit.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6}, nil
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	actual := service.ComputeQuota("accounting-model", "vip", 4, 2)
	var gotUser model.User
	require.NoError(t, model.DB.First(&gotUser, fixture.user.Id).Error)
	assert.Equal(t, 100_000-actual, gotUser.Quota)
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", service.LogTypeConsume).First(&log).Error)
	assert.Equal(t, "vip", log.Group)
	assert.NotEqual(t, service.GroupAuto, log.Group)
}

func TestOrdinaryRelayUsesUserGroupSpecialRatioForHoldSettlementAndLog(t *testing.T) {
	const maxTokens = 64
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	service.SetModelPriceRegistry(map[string]service.ModelPrice{
		"accounting-model": {Prompt: 2, Completion: 4},
	})
	service.SetGroupRatios(map[string]float64{"default": 1, "staff": 3, "vip": 2})
	service.SetGroupGroupRatios(map[string]map[string]float64{
		"default": {"staff": 0.25, "vip": 1.5},
	})
	require.NoError(t, model.DB.Where(map[string]any{"group": "default", "model": "accounting-model"}).
		Delete(&model.Ability{}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "vip", Model: "accounting-model", ChannelId: fixture.channel.Id, Enabled: true, Weight: 1,
	}).Error)
	require.NoError(t, service.InitAbilityCache())

	fixture.token.Group = service.GroupAuto
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, maxTokens)
	middleware.SetRelayGroupPolicy(c, service.RelayGroupPolicy{
		Groups: []string{"staff", "vip"}, Auto: true, CrossGroupRetry: true,
	})

	err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, selected *RelayInfo) (*protocolkit.Usage, error) {
		assert.Equal(t, "default", selected.UserGroup)
		assert.Equal(t, "vip", selected.Group)
		staffEstimate, estimateErr := estimateReservationForGroupChecked(selected, "staff")
		require.NoError(t, estimateErr)
		vipEstimate, estimateErr := estimateReservationForGroupChecked(selected, "vip")
		require.NoError(t, estimateErr)
		require.Greater(t, vipEstimate, staffEstimate,
			"worst-case reservation must compare effective special ratios, not base group ratios")
		var durableHold model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.Order("id desc").First(&durableHold).Error)
		assert.Equal(t, vipEstimate, durableHold.RequestedQuota)
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return &protocolkit.Usage{PromptTokens: 1_000, CompletionTokens: 100, TotalTokens: 1_100}, nil
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	actual := service.ComputeQuotaForUser("accounting-model", "default", "vip", 1_000, 100)
	assert.Equal(t, 1_800, actual)
	var gotUser model.User
	require.NoError(t, model.DB.First(&gotUser, fixture.user.Id).Error)
	assert.Equal(t, 100_000-actual, gotUser.Quota)
	assert.Equal(t, actual, gotUser.UsedQuota)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", service.LogTypeConsume).First(&log).Error)
	assert.Equal(t, "vip", log.Group)
	assert.Equal(t, actual, log.Quota)
	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
	assert.Equal(t, 1.5, other["group_ratio"])
	assert.Equal(t, 1.5, other["user_group_ratio"])
}

func TestAutoGroupRetryRecordsAffinityForActualGroup(t *testing.T) {
	t.Setenv("RETRY_TIMES", "1")
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	service.SetGroupRatios(map[string]float64{"staff": 1, "vip": 2})
	require.NoError(t, setting.Init())
	rules, err := common.Marshal([]setting.ChannelAffinityRule{{
		Name:              "actual-group",
		ModelRegex:        []string{"^accounting-model$"},
		PathRegex:         []string{"^/v1/chat/completions$"},
		KeySources:        []setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Affinity-Key"}},
		TTLSeconds:        60,
		IncludeUsingGroup: true,
		IncludeRuleName:   true,
	}})
	require.NoError(t, err)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ChannelAffinityEnabledOption:         "true",
		setting.ChannelAffinitySwitchOnSuccessOption: "false",
		setting.ChannelAffinityRulesOption:           string(rules),
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(setting.ChannelAffinityOptionDefaults())
		service.ClearChannelAffinityCacheAll()
	})
	service.ClearChannelAffinityCacheAll()

	require.NoError(t, model.DB.Where(map[string]any{"group": "default", "model": "accounting-model"}).
		Delete(&model.Ability{}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "staff", Model: "accounting-model", ChannelId: fixture.channel.Id, Enabled: true, Weight: 1,
	}).Error)
	weight := uint(1)
	vipChannel := model.Channel{
		Name: "vip-channel", Type: int(constant.ChannelTypeOpenAI), Key: "vip-upstream-key",
		Status: constant.ChannelStatusEnabled, BaseURL: "https://example.invalid",
		Models: "accounting-model", Group: "vip", Weight: &weight,
	}
	require.NoError(t, model.DB.Create(&vipChannel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "vip", Model: "accounting-model", ChannelId: vipChannel.Id, Enabled: true, Weight: 1,
	}).Error)
	require.NoError(t, service.InitAbilityCache())

	fixture.token.Group = service.GroupAuto
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 32)
	c.Request.Header.Set("X-Affinity-Key", "same-session")
	middleware.SetRelayGroupPolicy(c, service.RelayGroupPolicy{
		Groups: []string{"staff", "vip"}, Auto: true, CrossGroupRetry: true,
	})
	dispatches := 0
	err = relayAndSettleWithDispatch(c, info, func(c *gin.Context, selected *RelayInfo) (*protocolkit.Usage, error) {
		dispatches++
		if dispatches == 1 {
			assert.Equal(t, "staff", selected.Group)
			return nil, errors.New("retry staff upstream")
		}
		assert.Equal(t, "vip", selected.Group)
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return &protocolkit.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}, nil
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, 2, dispatches)

	lookupRecorder := httptest.NewRecorder()
	lookup, _ := gin.CreateTestContext(lookupRecorder)
	lookup.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	lookup.Request.Header.Set("X-Affinity-Key", "same-session")
	preferred, found := service.GetPreferredChannelByAffinity(lookup, "accounting-model", "vip", nil)
	require.True(t, found)
	assert.Equal(t, vipChannel.Id, preferred, "the vip affinity key must never retain the failed staff channel")
}

func TestOrdinaryRelaySettlementFailureRollsBackUserAndTokenTogether(t *testing.T) {
	const maxTokens = 32
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	reserved := relayReservationForTest(t, maxTokens)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, maxTokens)

	var failSettlement atomic.Bool
	callbackName := "test:fail_ordinary_relay_settlement"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if failSettlement.Load() && tx.Statement.Table == "users" {
			tx.AddError(errors.New("injected settlement failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		failSettlement.Store(true)
		c.JSON(http.StatusOK, gin.H{"upstream": "accepted"})
		return &protocolkit.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6}, nil
	})
	require.ErrorContains(t, err, "settle relay quota")
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "accepted", "a non-stream response must not be exposed before settlement commits")
	assert.Contains(t, recorder.Body.String(), "settlement_failed")

	var gotUser model.User
	var gotToken model.Token
	require.NoError(t, model.DB.First(&gotUser, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&gotToken, fixture.token.Id).Error)
	assert.Equal(t, 100_000-reserved, gotUser.Quota, "the original reservation stays held for reconciliation")
	assert.Zero(t, gotUser.UsedQuota)
	assert.Zero(t, gotUser.RequestCount)
	assert.Equal(t, 100_000-reserved, gotToken.RemainQuota)
	assert.Zero(t, gotToken.UsedQuota)
	var logs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&logs).Error)
	assert.Zero(t, logs)
}

func TestOrdinaryRelayRefundFailureIsReturnedAndNeverOverCredits(t *testing.T) {
	const maxTokens = 24
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	reserved := relayReservationForTest(t, maxTokens)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, maxTokens)

	var failRefund atomic.Bool
	callbackName := "test:fail_ordinary_relay_refund"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if failRefund.Load() && tx.Statement.Table == "users" {
			tx.AddError(errors.New("injected refund failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	err := relayAndSettleWithDispatch(c, info, func(_ *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		failRefund.Store(true)
		return nil, errors.New("upstream unavailable")
	})
	require.ErrorContains(t, err, "refund relay quota")
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "refund_failed")

	var gotUser model.User
	var gotToken model.Token
	require.NoError(t, model.DB.First(&gotUser, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&gotToken, fixture.token.Id).Error)
	assert.Equal(t, 100_000-reserved, gotUser.Quota, "a failed refund must not be retried into a double credit")
	assert.Equal(t, 100_000-reserved, gotToken.RemainQuota, "funding and token refunds roll back atomically")
	assert.Zero(t, gotUser.UsedQuota)
	assert.Zero(t, gotToken.UsedQuota)
}

func TestOrdinaryRelayLogSinkFailureFallsBackWithoutTurningSuccessIntoRetry(t *testing.T) {
	const maxTokens = 40
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, maxTokens)
	oldLogDB := model.LOG_DB
	t.Cleanup(func() { model.LOG_DB = oldLogDB })

	err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		model.LOG_DB = nil
		c.JSON(http.StatusOK, gin.H{"upstream": "accepted"})
		return &protocolkit.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}, nil
	})
	require.NoError(t, err, "the primary audit outbox makes a log-sink outage durable")
	assert.Equal(t, http.StatusOK, recorder.Code, "the charge committed, so the client must not be told to retry")
	assert.Contains(t, recorder.Body.String(), "accepted")
	var outbox []model.AuditLogOutbox
	require.NoError(t, model.DB.Where("status = ?", model.AuditLogOutboxStatusPending).Find(&outbox).Error)
	require.Len(t, outbox, 1)
	assert.NotContains(t, outbox[0].Payload, fixture.token.Key)

	actual := service.ComputeQuota("accounting-model", "default", 3, 2)
	var gotUser model.User
	var gotToken model.Token
	require.NoError(t, model.DB.First(&gotUser, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&gotToken, fixture.token.Id).Error)
	assert.Equal(t, 100_000-actual, gotUser.Quota)
	assert.Equal(t, actual, gotUser.UsedQuota)
	assert.Equal(t, 1, gotUser.RequestCount)
	assert.Equal(t, 100_000-actual, gotToken.RemainQuota)
	assert.Equal(t, actual, gotToken.UsedQuota)
}

func TestOrdinaryRelayPartialStreamIsSettledAndNeverRetriedOrRefunded(t *testing.T) {
	t.Setenv("RETRY_TIMES", "3")
	const maxTokens = 40
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, maxTokens)
	info.Request.Stream = true

	var dispatches atomic.Int32
	usage := &protocolkit.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}
	err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		dispatches.Add(1)
		c.Status(http.StatusOK)
		c.Header("Content-Type", "text/event-stream")
		_, writeErr := c.Writer.WriteString("data: {\"delta\":\"partial\"}\n\n")
		require.NoError(t, writeErr)
		c.Writer.Flush()
		return usage, errors.New("upstream stream truncated")
	})
	require.ErrorContains(t, err, "upstream failed after relay response started")
	assert.Equal(t, int32(1), dispatches.Load())
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "partial")

	actual := service.ComputeQuota("accounting-model", "default", usage.PromptTokens, usage.CompletionTokens)
	var gotUser model.User
	var gotToken model.Token
	require.NoError(t, model.DB.First(&gotUser, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&gotToken, fixture.token.Id).Error)
	assert.Equal(t, 100_000-actual, gotUser.Quota, "accepted partial output is reconciled to observed usage")
	assert.Equal(t, actual, gotUser.UsedQuota)
	assert.Equal(t, 1, gotUser.RequestCount)
	assert.Equal(t, 100_000-actual, gotToken.RemainQuota)
	assert.Equal(t, actual, gotToken.UsedQuota)
	var logs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", service.LogTypeConsume).Count(&logs).Error)
	assert.Equal(t, int64(1), logs)
}

func TestOrdinaryRelayUsageBearingFailureIsChargedOnceAndNeverRetried(t *testing.T) {
	t.Setenv("RETRY_TIMES", "3")
	const maxTokens = 40
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, maxTokens)

	var dispatches atomic.Int32
	usage := &protocolkit.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}
	err := relayAndSettleWithDispatch(c, info, func(_ *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		dispatches.Add(1)
		return usage, errors.New("upstream response transport failed after usage")
	})
	require.ErrorContains(t, err, "billable usage")
	assert.Equal(t, int32(1), dispatches.Load(), "reported usage proves accepted work and forbids retry")
	assert.GreaterOrEqual(t, recorder.Code, http.StatusInternalServerError)

	actual := service.ComputeQuota("accounting-model", "default", usage.PromptTokens, usage.CompletionTokens)
	var gotUser model.User
	var gotToken model.Token
	require.NoError(t, model.DB.First(&gotUser, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&gotToken, fixture.token.Id).Error)
	assert.Equal(t, 100_000-actual, gotUser.Quota)
	assert.Equal(t, actual, gotUser.UsedQuota)
	assert.Equal(t, 1, gotUser.RequestCount)
	assert.Equal(t, 100_000-actual, gotToken.RemainQuota)
	assert.Equal(t, actual, gotToken.UsedQuota)
	var logs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", service.LogTypeConsume).Count(&logs).Error)
	assert.Equal(t, int64(1), logs)
}

func TestOrdinaryRelayRejectsInvalidQuotaInputsBeforeUnsafeSettlement(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)

	for name, maxTokens := range map[string]int{
		"negative":  -1,
		"too large": maxRelayOutputTokens + 1,
	} {
		t.Run(name, func(t *testing.T) {
			c, recorder, info := newRelayAccountingContext(t, &fixture.token, maxTokens)
			var dispatched atomic.Bool
			err := relayAndSettleWithDispatch(c, info, func(_ *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
				dispatched.Store(true)
				return nil, nil
			})
			require.NoError(t, err)
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Contains(t, recorder.Body.String(), "invalid_quota")
			assert.False(t, dispatched.Load())
		})
	}

	t.Run("negative upstream usage", func(t *testing.T) {
		c, recorder, info := newRelayAccountingContext(t, &fixture.token, 16)
		err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
			c.JSON(http.StatusOK, gin.H{"upstream": "invalid usage"})
			return &protocolkit.Usage{PromptTokens: -1, CompletionTokens: 1}, nil
		})
		require.ErrorContains(t, err, "invalid upstream usage")
		assert.Equal(t, http.StatusInternalServerError, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "settlement_failed")
	})
}

func TestOrdinaryRelayRejectsOversizedUsageWithoutRefundingAcceptedWork(t *testing.T) {
	oversized := int(common.MaxQuota + 1)
	tests := []struct {
		name  string
		usage *protocolkit.Usage
	}{
		{
			name:  "aggregate",
			usage: &protocolkit.Usage{PromptTokens: oversized, CompletionTokens: 1, TotalTokens: oversized},
		},
		{
			name: "canonical modality detail",
			usage: &protocolkit.Usage{
				PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
				PromptTokensDetails: &protocolkit.InputTokenDetails{ImageTokens: oversized},
			},
		},
		{
			name: "OpenAI alias detail",
			usage: &protocolkit.Usage{
				PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
				InputTokensDetails: &protocolkit.InputTokenDetails{AudioTokens: oversized},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRelayAccountingFixture(t, 100_000, 100_000)
			previousPrices := service.ExportedModelPrices()
			service.SetModelPriceRegistry(map[string]service.ModelPrice{
				"accounting-model": {Prompt: 2, Completion: 2},
			})
			t.Cleanup(func() { service.SetModelPriceRegistry(previousPrices) })
			const reservedQuota = 16
			c, recorder, info := newRelayAccountingContext(t, &fixture.token, 16)
			err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
				c.JSON(http.StatusOK, gin.H{"upstream": "accepted invalid usage"})
				return test.usage, nil
			})
			require.ErrorContains(t, err, "invalid upstream usage")
			assert.Equal(t, http.StatusInternalServerError, recorder.Code)
			assert.Contains(t, recorder.Body.String(), "settlement_failed")

			var user model.User
			var token model.Token
			var channel model.Channel
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
			require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
			require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
			require.NoError(t, model.DB.First(&reservation).Error)
			assert.Equal(t, 100_000-reservedQuota, user.Quota,
				"accepted work keeps the bounded hold for manual reconciliation")
			assert.Zero(t, user.UsedQuota)
			assert.Zero(t, user.RequestCount)
			assert.Equal(t, 100_000-reservedQuota, token.RemainQuota)
			assert.Zero(t, token.UsedQuota)
			assert.Zero(t, channel.UsedQuota)
			assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reservation.Status)
			assert.Equal(t, reservedQuota, reservation.ReservedQuota)
			assert.Equal(t, reservedQuota, reservation.TokenReserved)
			assert.Equal(t, reservedQuota, reservation.ActualQuota)
			var logCount int64
			require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", service.LogTypeConsume).Count(&logCount).Error)
			assert.Zero(t, logCount)
		})
	}
}
