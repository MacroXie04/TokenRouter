package engine

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"sync/atomic"
	"testing"
)

func configureOrdinaryReferencePricing(t *testing.T, fixedPrices, modelRatios, completionRatios string) {
	t.Helper()
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"accounting-model":"reference"}`,
		setting.PerCallModelPriceOption: fixedPrices,
		setting.ModelRatioOption:        modelRatios,
		setting.CompletionRatioOption:   completionRatios,
		setting.PreConsumedQuotaOption:  "500",
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(map[string]string{
			setting.ModelBillingModeOption:  `{}`,
			setting.PerCallModelPriceOption: `{}`,
			setting.ModelRatioOption:        `{}`,
			setting.CompletionRatioOption:   `{}`,
			setting.PreConsumedQuotaOption:  "500",
		})
	})
}

func TestOrdinaryRelayReferenceFixedPriceUsesSnapshotAndWritesExactAccounting(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	configureOrdinaryReferencePricing(t,
		`{"accounting-model":0.004}`,
		`{"accounting-model":99}`,
		`{"accounting-model":99}`,
	)

	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)
	err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		var held model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.Order("id desc").First(&held).Error)
		assert.Equal(t, model.RelayQuotaReservationStatusDispatched, held.Status)
		assert.Equal(t, 2_000, held.RequestedQuota)
		assert.Equal(t, 2_000, held.ReservedQuota)
		assert.Equal(t, 2_000, held.TokenReserved)

		// A live edit after dispatch must only affect the next request. The
		// current request settles and logs the snapshot that owned its hold.
		require.NoError(t, setting.UpdateOption(
			setting.PerCallModelPriceOption, `{"accounting-model":0.02}`,
		))
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return &protocolkit.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}, nil
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, 2_000, info.Quota)
	assert.Nil(t, info.QuotaClamp)

	var user model.User
	var token model.Token
	var channel model.Channel
	var reservation model.RelayQuotaReservationRecord
	var log model.Log
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
	require.NoError(t, model.DB.First(&reservation).Error)
	require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 98_000, user.Quota)
	assert.Equal(t, 2_000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 98_000, token.RemainQuota)
	assert.Equal(t, 2_000, token.UsedQuota)
	assert.Equal(t, int64(2_000), channel.UsedQuota)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, 2_000, reservation.ActualQuota)
	assert.Equal(t, 2_000, log.Quota)

	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
	assert.Equal(t, billingsvc.BillingModeReference, other["billing_mode"])
	assert.Equal(t, true, other["use_price"])
	assert.Equal(t, 0.004, other["model_price"])
	assert.Equal(t, 1.0, other["group_ratio"])
	assert.Equal(t, reservation.ReservationID, other["relay_reservation_id"])

	nextPlan, enabled, err := billingsvc.ResolveOrdinaryReferenceBillingPlan(
		"accounting-model", "default", "default",
	)
	require.NoError(t, err)
	require.True(t, enabled)
	nextCharge, _, err := nextPlan.SettlementQuota(1, 0)
	require.NoError(t, err)
	assert.Equal(t, 10_000, nextCharge)
}

func TestOrdinaryRelayReferenceUnsetRatioHonorsOwnerPreference(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	configureOrdinaryReferencePricing(t, `{}`, `{}`, `{}`)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", fixture.user.Id).Update(
		"setting", `{"accept_unset_model_ratio_model":true}`,
	).Error)

	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 1)
	dispatches := atomic.Int32{}
	err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		dispatches.Add(1)
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return &protocolkit.Usage{PromptTokens: 1, TotalTokens: 1}, nil
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, int32(1), dispatches.Load())
	assert.Equal(t, 38, info.Quota)
	assert.Equal(t, fixture.user.Id, info.UserID)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).First(&log).Error)
	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
	assert.Equal(t, 37.5, other["model_ratio"])
}

func TestReferencePricingLogNeverMixesHotReloadedGroupRatios(t *testing.T) {
	_ = newRelayAccountingFixture(t, 100_000, 100_000)
	keys := []string{
		setting.ModelBillingModeOption,
		setting.PerCallModelPriceOption,
		setting.GroupRatioOption,
		setting.GroupGroupRatioOption,
	}
	previous := setting.GetOptions(keys...)
	t.Cleanup(func() { _ = setting.UpdateOptions(previous) })
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"accounting-model":"reference"}`,
		setting.PerCallModelPriceOption: `{"accounting-model":0.004}`,
		setting.GroupRatioOption:        `{"vip":0.25}`,
		setting.GroupGroupRatioOption:   `{}`,
	}))
	plan, enabled, err := billingsvc.ResolveOrdinaryReferenceBillingPlan("accounting-model", "default", "vip")
	require.NoError(t, err)
	require.True(t, enabled)

	// The live configuration becomes a nested special ratio after dispatch.
	// Logging must use only the immutable request plan, not mix the new special
	// marker with the old ordinary group-ratio value.
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.GroupRatioOption:      `{"vip":7}`,
		setting.GroupGroupRatioOption: `{"default":{"vip":9}}`,
	}))
	other := buildLogOther(nil, &RelayInfo{
		ModelName: "accounting-model", UserGroup: "default", Group: "vip",
		ReferencePricing: map[string]billingsvc.ReferenceBillingPlan{"vip": plan},
	}, nil)
	assert.Equal(t, 0.25, other["group_ratio"])
	assert.NotContains(t, other, "user_group_ratio")
}

func TestOrdinaryRelayReferenceRatioFinalCorrectionRefundShortfallAndStreamingFallback(t *testing.T) {
	tests := []struct {
		name           string
		maxTokens      int
		stream         bool
		usage          *protocolkit.Usage
		expectedActual func(promptEstimate int) int
	}{
		{
			name: "refunds an over-reservation", maxTokens: 100,
			usage:          &protocolkit.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
			expectedActual: func(int) int { return 260 },
		},
		{
			name: "deducts an accepted-usage shortfall", maxTokens: 1,
			usage:          &protocolkit.Usage{PromptTokens: 100, CompletionTokens: 500, TotalTokens: 600},
			expectedActual: func(int) int { return 3_200 },
		},
		{
			name: "usage-less stream charges the captured prompt estimate", maxTokens: 100, stream: true,
			expectedActual: func(promptEstimate int) int { return promptEstimate * 2 },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRelayAccountingFixture(t, 100_000, 100_000)
			configureOrdinaryReferencePricing(t, `{}`, `{"accounting-model":2}`, `{"accounting-model":3}`)
			c, recorder, info := newRelayAccountingContext(t, &fixture.token, test.maxTokens)
			if test.stream {
				info.Request.Stream = true
				info.Request.Extra["stream"] = true
			}

			err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, selected *RelayInfo) (*protocolkit.Usage, error) {
				var held model.RelayQuotaReservationRecord
				require.NoError(t, model.DB.Order("id desc").First(&held).Error)
				expectedHold := (500 + test.maxTokens) * 2
				assert.Equal(t, expectedHold, held.RequestedQuota)
				assert.Equal(t, expectedHold, held.ReservedQuota)
				assert.Equal(t, expectedHold, held.TokenReserved)
				if test.stream {
					c.Header("Content-Type", "text/event-stream")
					c.Status(http.StatusOK)
					_, _ = c.Writer.WriteString("data: [DONE]\n\n")
				} else {
					c.JSON(http.StatusOK, gin.H{"ok": true})
				}
				return test.usage, nil
			})
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

			expectedActual := test.expectedActual(info.PromptTokens)
			assert.Equal(t, expectedActual, info.Quota)
			var user model.User
			var token model.Token
			var channel model.Channel
			var reservation model.RelayQuotaReservationRecord
			var log model.Log
			require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
			require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
			require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
			require.NoError(t, model.DB.First(&reservation).Error)
			require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).First(&log).Error)
			assert.Equal(t, 100_000-expectedActual, user.Quota)
			assert.Equal(t, expectedActual, user.UsedQuota)
			assert.Equal(t, 1, user.RequestCount)
			assert.Equal(t, 100_000-expectedActual, token.RemainQuota)
			assert.Equal(t, expectedActual, token.UsedQuota)
			assert.Equal(t, int64(expectedActual), channel.UsedQuota)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
			assert.Equal(t, expectedActual, reservation.ActualQuota)
			assert.Equal(t, expectedActual, log.Quota)

			var other map[string]any
			require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
			assert.Equal(t, billingsvc.BillingModeReference, other["billing_mode"])
			assert.Equal(t, false, other["use_price"])
			assert.Equal(t, 2.0, other["model_ratio"])
			assert.Equal(t, 3.0, other["completion_ratio"])
		})
	}
}

func TestOrdinaryRelayReferenceFixedPriceFailureRefundsBothHolds(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	configureOrdinaryReferencePricing(t, `{"accounting-model":0.004}`, `{}`, `{}`)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)

	err := relayAndSettleWithDispatch(c, info, func(*gin.Context, *RelayInfo) (*protocolkit.Usage, error) {
		var held model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.Order("id desc").First(&held).Error)
		assert.Equal(t, 2_000, held.ReservedQuota)
		assert.Equal(t, 2_000, held.TokenReserved)
		return nil, errors.New("injected upstream failure")
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)

	var user model.User
	var token model.Token
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&reservation).Error)
	assert.Equal(t, 100_000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, 100_000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	var logs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", billingsvc.LogTypeConsume).Count(&logs).Error)
	assert.Zero(t, logs)
}

func TestOrdinaryRelayReferenceZeroFixedPriceSettlesDurablyWithoutACharge(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	configureOrdinaryReferencePricing(t, `{"accounting-model":0}`, `{"accounting-model":99}`, `{}`)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)

	err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		var held model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.Order("id desc").First(&held).Error)
		assert.Zero(t, held.RequestedQuota)
		assert.Zero(t, held.ReservedQuota)
		assert.Zero(t, held.TokenReserved)
		assert.False(t, held.TrustQuotaBypassed,
			"a configured free request is distinct from a positive-price trust bypass")
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return &protocolkit.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}, nil
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Zero(t, info.Quota)

	var user model.User
	var token model.Token
	var reservation model.RelayQuotaReservationRecord
	var log model.Log
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&reservation).Error)
	require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 100_000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 100_000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Zero(t, reservation.ActualQuota)
	assert.False(t, reservation.TrustQuotaBypassed)
	assert.Zero(t, log.Quota)
}

func TestOrdinaryRelayReferenceMissingPricingFailsBeforeDispatch(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	configureOrdinaryReferencePricing(t, `{}`, `{}`, `{}`)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)
	var dispatched atomic.Bool

	err := relayAndSettleWithDispatch(c, info, func(*gin.Context, *RelayInfo) (*protocolkit.Usage, error) {
		dispatched.Store(true)
		return nil, nil
	})
	require.NoError(t, err)
	assert.False(t, dispatched.Load())
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "invalid_quota")

	var user model.User
	var token model.Token
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	assert.Equal(t, 100_000, user.Quota)
	assert.Equal(t, 100_000, token.RemainQuota)
	var reservations int64
	require.NoError(t, model.DB.Model(&model.RelayQuotaReservationRecord{}).Count(&reservations).Error)
	assert.Zero(t, reservations)
}

func TestOrdinaryRelayReferenceFixedPriceConcurrentReservationsCannotOverspend(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	configureOrdinaryReferencePricing(t, `{"accounting-model":0.002}`, `{}`, `{}`)
	require.NoError(t, model.DB.Model(&model.Token{}).
		Where("id = ?", fixture.token.Id).Update("remain_quota", 1_000).Error)
	require.NoError(t, model.DB.First(&fixture.token, fixture.token.Id).Error)

	firstContext, firstRecorder, firstInfo := newRelayAccountingContext(t, &fixture.token, 64)
	secondToken := fixture.token
	secondContext, secondRecorder, secondInfo := newRelayAccountingContext(t, &secondToken, 64)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var dispatches atomic.Int32
	dispatch := func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		dispatches.Add(1)
		started <- struct{}{}
		<-release
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return &protocolkit.Usage{PromptTokens: 1, TotalTokens: 1}, nil
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- relayAndSettleWithDispatch(firstContext, firstInfo, dispatch) }()
	<-started
	secondErr := relayAndSettleWithDispatch(secondContext, secondInfo, dispatch)
	require.NoError(t, secondErr)
	assert.Equal(t, http.StatusBadRequest, secondRecorder.Code)
	assert.Contains(t, secondRecorder.Body.String(), "insufficient_quota")
	assert.Equal(t, int32(1), dispatches.Load())

	close(release)
	require.NoError(t, <-firstDone)
	require.Equal(t, http.StatusOK, firstRecorder.Code, firstRecorder.Body.String())
	var user model.User
	var token model.Token
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	assert.Equal(t, 99_000, user.Quota)
	assert.Equal(t, 1_000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Zero(t, token.RemainQuota)
	assert.Equal(t, 1_000, token.UsedQuota)
	var settled int64
	require.NoError(t, model.DB.Model(&model.RelayQuotaReservationRecord{}).
		Where("status = ?", model.RelayQuotaReservationStatusSettled).Count(&settled).Error)
	assert.Equal(t, int64(1), settled)
}
