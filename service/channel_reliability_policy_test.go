package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

func reliabilityPolicyForTest() setting.ChannelReliabilitySetting {
	return setting.DefaultChannelReliabilitySetting()
}

func TestShouldRetryChannelFailureReferenceDecisionOrder(t *testing.T) {
	policy := reliabilityPolicyForTest()
	base := RetryPolicyInput{
		Failure:          ChannelFailure{ErrorPresent: true, StatusCode: http.StatusTooManyRequests},
		RemainingRetries: 1,
	}
	tests := []struct {
		name  string
		input RetryPolicyInput
		want  bool
	}{
		{name: "no error", input: RetryPolicyInput{RemainingRetries: 1}, want: false},
		{name: "default 429", input: base, want: true},
		{name: "default 401", input: retryInputWithStatus(base, 401), want: true},
		{name: "default 500", input: retryInputWithStatus(base, 500), want: true},
		{name: "default 599", input: retryInputWithStatus(base, 599), want: true},
		{name: "400 excluded", input: retryInputWithStatus(base, 400), want: false},
		{name: "408 excluded", input: retryInputWithStatus(base, 408), want: false},
		{name: "504 permanently excluded", input: retryInputWithStatus(base, 504), want: false},
		{name: "524 permanently excluded", input: retryInputWithStatus(base, 524), want: false},
		{name: "2xx excluded", input: retryInputWithStatus(base, 204), want: false},
		{name: "invalid status transport failure", input: retryInputWithStatus(base, 0), want: true},
		{name: "affinity guard wins", input: RetryPolicyInput{Failure: ChannelFailure{ErrorPresent: true, ChannelError: true}, AffinityFailure: true}, want: false},
		{name: "channel error precedes remaining count", input: RetryPolicyInput{Failure: ChannelFailure{ErrorPresent: true, ChannelError: true}, RemainingRetries: 0}, want: true},
		{name: "channel error precedes skip flag", input: RetryPolicyInput{Failure: ChannelFailure{ErrorPresent: true, ChannelError: true, SkipRetry: true}, RemainingRetries: 1}, want: true},
		{name: "skip retry", input: RetryPolicyInput{Failure: ChannelFailure{ErrorPresent: true, SkipRetry: true, StatusCode: 500}, RemainingRetries: 1}, want: false},
		{name: "remaining exhausted", input: RetryPolicyInput{Failure: ChannelFailure{ErrorPresent: true, StatusCode: 500}}, want: false},
		{name: "specific channel", input: RetryPolicyInput{Failure: ChannelFailure{ErrorPresent: true, StatusCode: 500}, RemainingRetries: 1, SpecificChannel: true}, want: false},
		{name: "bad body skips valid status", input: RetryPolicyInput{Failure: ChannelFailure{ErrorPresent: true, BadResponseBody: true, StatusCode: 500}, RemainingRetries: 1}, want: false},
		{name: "invalid status precedes bad body", input: RetryPolicyInput{Failure: ChannelFailure{ErrorPresent: true, BadResponseBody: true}, RemainingRetries: 1}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, ShouldRetryChannelFailure(policy, test.input))
		})
	}
}

func retryInputWithStatus(input RetryPolicyInput, status int) RetryPolicyInput {
	input.Failure.StatusCode = status
	return input
}

func TestShouldRetryChannelFailureHonorsHotPolicyRanges(t *testing.T) {
	policy := reliabilityPolicyForTest()
	policy.AutomaticRetryStatusCodes = []setting.HTTPStatusCodeRange{{Start: 418, End: 418}}

	assert.True(t, ShouldRetryChannelFailure(policy, RetryPolicyInput{
		Failure: ChannelFailure{ErrorPresent: true, StatusCode: 418}, RemainingRetries: 1,
	}))
	assert.False(t, ShouldRetryChannelFailure(policy, RetryPolicyInput{
		Failure: ChannelFailure{ErrorPresent: true, StatusCode: 429}, RemainingRetries: 1,
	}))
	policy.AutomaticRetryStatusCodes = []setting.HTTPStatusCodeRange{{Start: 500, End: 599}}
	assert.False(t, ShouldRetryChannelFailure(policy, RetryPolicyInput{
		Failure: ChannelFailure{ErrorPresent: true, StatusCode: 504}, RemainingRetries: 1,
	}))
}

func TestAutomaticDisableReasonUsesIndependentGlobalAndFailureGates(t *testing.T) {
	policy := reliabilityPolicyForTest()
	failure := ChannelFailure{ErrorPresent: true, StatusCode: 401, Message: "permission denied"}
	assert.Empty(t, AutomaticDisableReason(policy, failure), "safe default is disabled")

	policy.AutomaticDisableChannelEnabled = true
	assert.Equal(t, ChannelTransitionReasonStatusCode, AutomaticDisableReason(policy, failure))
	assert.Equal(t, ChannelTransitionReasonChannelError, AutomaticDisableReason(policy, ChannelFailure{
		ErrorPresent: true, ChannelError: true, SkipRetry: true,
	}))
	assert.Empty(t, AutomaticDisableReason(policy, ChannelFailure{
		ErrorPresent: true, SkipRetry: true, StatusCode: 401, Message: "permission denied",
	}))
	assert.Equal(t, ChannelTransitionReasonKeyword, AutomaticDisableReason(policy, ChannelFailure{
		ErrorPresent: true, StatusCode: 400, Message: "Provider says PERMISSION DENIED for this account",
	}))
	assert.Empty(t, AutomaticDisableReason(policy, ChannelFailure{
		ErrorPresent: true, StatusCode: 400, Message: "temporary request failure",
	}))
}

func TestDecideChannelHealthTransition(t *testing.T) {
	policy := reliabilityPolicyForTest()
	policy.AutomaticDisableChannelEnabled = true
	policy.AutomaticEnableChannelEnabled = true
	statusFailure := &ChannelFailure{ErrorPresent: true, StatusCode: 401}
	ordinaryFailure := &ChannelFailure{ErrorPresent: true, StatusCode: 400, Message: "temporary"}

	tests := []struct {
		name   string
		input  ChannelHealthPolicyInput
		status int
		reason string
	}{
		{name: "configured status disables", input: ChannelHealthPolicyInput{Status: constant.ChannelStatusEnabled, AutoBan: true, AllowDisable: true, Failure: statusFailure}, status: constant.ChannelStatusAutoDisabled, reason: ChannelTransitionReasonStatusCode},
		{name: "autoban off", input: ChannelHealthPolicyInput{Status: constant.ChannelStatusEnabled, AllowDisable: true, Failure: statusFailure}, status: constant.ChannelStatusEnabled},
		{name: "passive recovery never disables", input: ChannelHealthPolicyInput{Status: constant.ChannelStatusEnabled, AutoBan: true, Failure: statusFailure}, status: constant.ChannelStatusEnabled},
		{name: "manual status unchanged", input: ChannelHealthPolicyInput{Status: constant.ChannelStatusManuallyDisabled, AutoBan: true, AllowDisable: true, Failure: statusFailure}, status: constant.ChannelStatusManuallyDisabled},
		{name: "latency above threshold disables", input: ChannelHealthPolicyInput{Status: constant.ChannelStatusEnabled, AutoBan: true, AllowDisable: true, LatencyMilliseconds: 5001, Failure: ordinaryFailure}, status: constant.ChannelStatusAutoDisabled, reason: ChannelTransitionReasonLatency},
		{name: "successful but slow probe disables", input: ChannelHealthPolicyInput{Status: constant.ChannelStatusEnabled, AutoBan: true, AllowDisable: true, LatencyMilliseconds: 5001}, status: constant.ChannelStatusAutoDisabled, reason: ChannelTransitionReasonLatency},
		{name: "latency equal threshold stays", input: ChannelHealthPolicyInput{Status: constant.ChannelStatusEnabled, AutoBan: true, AllowDisable: true, LatencyMilliseconds: 5000, Failure: ordinaryFailure}, status: constant.ChannelStatusEnabled},
		{name: "successful recovery", input: ChannelHealthPolicyInput{Status: constant.ChannelStatusAutoDisabled}, status: constant.ChannelStatusEnabled, reason: ChannelTransitionReasonRecovery},
		{name: "slow passive probe cannot recover", input: ChannelHealthPolicyInput{Status: constant.ChannelStatusAutoDisabled, LatencyMilliseconds: 5001}, status: constant.ChannelStatusAutoDisabled},
		{name: "local success cannot recover", input: ChannelHealthPolicyInput{Status: constant.ChannelStatusAutoDisabled, LocalError: true}, status: constant.ChannelStatusAutoDisabled},
		{name: "enabled success unchanged", input: ChannelHealthPolicyInput{Status: constant.ChannelStatusEnabled}, status: constant.ChannelStatusEnabled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := DecideChannelHealthTransition(policy, test.input)
			assert.Equal(t, test.input.Status, got.PreviousStatus)
			assert.Equal(t, test.status, got.NextStatus)
			assert.Equal(t, test.reason, got.Reason)
			assert.Equal(t, test.input.Status != test.status, got.Changed())
		})
	}

	policy.ChannelDisableThreshold = 0
	got := DecideChannelHealthTransition(policy, ChannelHealthPolicyInput{
		Status: constant.ChannelStatusEnabled, AutoBan: true, AllowDisable: true,
		LatencyMilliseconds: 100000, Failure: ordinaryFailure,
	})
	assert.False(t, got.Changed(), "zero disables the latency rule")

	policy.AutomaticDisableChannelEnabled = false
	got = DecideChannelHealthTransition(policy, ChannelHealthPolicyInput{
		Status: constant.ChannelStatusEnabled, AutoBan: true, AllowDisable: true, Failure: statusFailure,
	})
	assert.False(t, got.Changed(), "global safe default wins")
}

func TestSelectChannelsForReliabilityTestModes(t *testing.T) {
	autoBanOn := 1
	autoBanOff := 0
	channels := []model.Channel{
		{Id: 1, Type: int(constant.ChannelTypeOpenAI), Status: constant.ChannelStatusEnabled, Models: "gpt-4", AutoBan: &autoBanOn},
		{Id: 2, Type: int(constant.ChannelTypeOpenAI), Status: constant.ChannelStatusAutoDisabled, TestModel: "gpt-4", AutoBan: &autoBanOn},
		{Id: 3, Type: int(constant.ChannelTypeOpenAI), Status: constant.ChannelStatusEnabled, Models: "gpt-4", AutoBan: &autoBanOff},
		{Id: 4, Type: int(constant.ChannelTypeOpenAI), Status: constant.ChannelStatusManuallyDisabled, Models: "gpt-4", AutoBan: &autoBanOn},
		{Id: 5, Type: int(constant.ChannelTypeMidjourney), Status: constant.ChannelStatusEnabled, Models: "mj", AutoBan: &autoBanOn},
		{Id: 6, Type: int(constant.ChannelTypeOpenAI), Status: constant.ChannelStatusEnabled, AutoBan: &autoBanOn},
	}
	tests := []struct {
		mode string
		ids  []int
	}{
		{mode: setting.ChannelTestModeScheduledAll, ids: []int{1, 2, 3}},
		{mode: setting.ChannelTestModeAutoBanOnly, ids: []int{1, 2}},
		{mode: setting.ChannelTestModePassiveRecovery, ids: []int{2}},
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			selected, err := SelectChannelsForReliabilityTest(channels, test.mode)
			require.NoError(t, err)
			ids := make([]int, len(selected))
			for i := range selected {
				ids[i] = selected[i].Id
			}
			assert.Equal(t, test.ids, ids)
		})
	}
	selected, err := SelectChannelsForReliabilityTest(channels, "")
	require.Error(t, err)
	assert.Nil(t, selected)
}
