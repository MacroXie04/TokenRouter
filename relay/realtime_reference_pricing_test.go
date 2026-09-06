package relay

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func TestRealtimeReferencePricingUsesOneCapturedAudioSnapshotForEveryResponse(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	configureOrdinaryReferencePricing(t, `{}`, `{"accounting-model":2}`, `{"accounting-model":3}`)
	previous := setting.GetOptions(setting.AudioRatioOption, setting.AudioCompletionRatioOption)
	t.Cleanup(func() { _ = setting.UpdateOptions(previous) })
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.AudioRatioOption:           `{"accounting-model":8}`,
		setting.AudioCompletionRatioOption: `{"accounting-model":2}`,
	}))

	production := productionRealtimeDependencies()
	pricing, err := production.resolvePricing(
		fixture.user.Id, "accounting-model", "default", "default",
	)
	require.NoError(t, err)
	require.True(t, pricing.reference)
	assert.Equal(t, 1_000, pricing.reservationQuota)
	assert.False(t, pricing.freeModel)

	// The WebSocket session has captured its plan; subsequent admin edits must
	// not change any response within this established connection.
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.AudioRatioOption:           `{"accounting-model":1}`,
		setting.AudioCompletionRatioOption: `{"accounting-model":1}`,
	}))

	current := &fakeRealtimeReservation{quota: pricing.reservationQuota}
	next := &fakeRealtimeReservation{quota: pricing.reservationQuota}
	var logged realtimeLogEntry
	reserveCalls := 0
	deps := realtimeDependencies{
		reserveFree: func(_ int, _ *model.Token, quota int, freeModel bool) (realtimeReservation, error) {
			reserveCalls++
			assert.Equal(t, pricing.reservationQuota, quota)
			assert.False(t, freeModel)
			return next, nil
		},
		usageQuota: func(string, string, string, normalizedRealtimeUsage) (int, error) {
			t.Fatal("legacy live pricing must not run for a captured reference plan")
			return 0, nil
		},
		recordLog: func(entry realtimeLogEntry) error {
			logged = entry
			return nil
		},
		limits: realtimeLimits{maxResponses: 4},
		now:    func() time.Time { return time.Unix(1_700_000_001, 0) },
	}
	accountant := newRealtimeAccountant(realtimeAccountantConfig{
		deps: deps, current: current, reservationQuota: pricing.reservationQuota, pricing: pricing,
		token: &fixture.token,
		log: realtimeLogEntry{
			userId: fixture.user.Id, modelName: "accounting-model", userGroup: "default",
			group: "default", channelId: fixture.channel.Id,
		},
		startedAt: time.Unix(1_700_000_000, 0),
	})

	forward, err := accountant.processUpstreamMessage([]byte(`{
		"type":"response.done",
		"response":{"id":"response-1","usage":{
			"total_tokens":15,"input_tokens":10,"output_tokens":5,
			"input_token_details":{"text_tokens":2,"audio_tokens":8},
			"output_token_details":{"text_tokens":1,"audio_tokens":4}
		}}
	}`))
	require.NoError(t, err)
	assert.True(t, forward)
	assert.Equal(t, 1, reserveCalls)
	assert.Equal(t, []int{266}, current.settledWith)
	assert.Equal(t, 266, logged.quota)
	assert.Equal(t, service.BillingModeReference, logged.other["billing_mode"])
	assert.Equal(t, 2.0, logged.other["model_ratio"])
	assert.Equal(t, 3.0, logged.other["completion_ratio"])
	assert.Equal(t, 8.0, logged.other["audio_ratio"])
	assert.Equal(t, 2.0, logged.other["audio_completion_ratio"])
	assert.Equal(t, true, logged.other["ws"])
	require.NoError(t, accountant.close())
	_, refunds := next.snapshot()
	assert.Equal(t, 1, refunds)
}

func TestRealtimeReferenceZeroPriceCapturesExplicitFreeModelPolicy(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	configureOrdinaryReferencePricing(t, `{"accounting-model":0}`, `{}`, `{}`)
	previous := setting.GetOptions(setting.EnableFreeModelPreConsumeOption)
	t.Cleanup(func() { _ = setting.UpdateOptions(previous) })
	require.NoError(t, setting.UpdateOption(setting.EnableFreeModelPreConsumeOption, "false"))

	deps := productionRealtimeDependencies()
	pricing, err := deps.resolvePricing(fixture.user.Id, "accounting-model", "default", "default")
	require.NoError(t, err)
	assert.Zero(t, pricing.reservationQuota)
	assert.True(t, pricing.allowZero)
	assert.True(t, pricing.freeModel)

	reservation, err := reserveRealtimeQuota(deps, fixture.user.Id, &fixture.token, pricing)
	require.NoError(t, err)
	var record model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("reservation_id = ?", reservation.(*service.RelayQuotaReservation).ReservationID()).First(&record).Error)
	assert.Equal(t, service.BillingSourceFreeModel, record.FundingSource)
	assert.Zero(t, record.ReservedQuota)
	assert.Zero(t, record.TokenReserved)
	require.NoError(t, reservation.MarkDispatched())
	require.NoError(t, reservation.SettleWithChannel(0, fixture.channel.Id))
}
