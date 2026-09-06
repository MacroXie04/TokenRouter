package channels

import (
	"errors"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"strings"
)

const (
	ChannelTransitionReasonChannelError = "channel_error"
	ChannelTransitionReasonStatusCode   = "status_code"
	ChannelTransitionReasonKeyword      = "keyword"
	ChannelTransitionReasonLatency      = "latency"
	ChannelTransitionReasonRecovery     = "recovery"
)

// ChannelFailure is the bounded, provider-independent information used by
// disable and retry policy. ErrorPresent is explicit so an invalid/zero HTTP
// status can still represent a transport failure.
type ChannelFailure struct {
	ErrorPresent    bool
	ChannelError    bool
	SkipRetry       bool
	BadResponseBody bool
	StatusCode      int
	Message         string
}

// AutomaticDisableReason returns the first reference-compatible policy reason
// for disabling a channel. An empty string means the failure must not disable.
func AutomaticDisableReason(policy setting.ChannelReliabilitySetting, failure ChannelFailure) string {
	if !policy.AutomaticDisableChannelEnabled || !failure.ErrorPresent {
		return ""
	}
	if failure.ChannelError {
		return ChannelTransitionReasonChannelError
	}
	if failure.SkipRetry {
		return ""
	}
	if policy.ShouldDisableForStatus(failure.StatusCode) {
		return ChannelTransitionReasonStatusCode
	}
	if policy.MatchesAutomaticDisableKeyword(failure.Message) {
		return ChannelTransitionReasonKeyword
	}
	return ""
}

// RetryPolicyInput contains request-scoped guards that sit ahead of the
// configured status-code ranges in the ordinary relay retry state machine.
type RetryPolicyInput struct {
	Failure          ChannelFailure
	AffinityFailure  bool
	RemainingRetries int
	SpecificChannel  bool
}

// ShouldRetryChannelFailure mirrors the ordinary reference relay decision
// order. It does not apply to non-idempotent async submissions, whose dispatch
// proof and idempotency rules remain authoritative.
func ShouldRetryChannelFailure(policy setting.ChannelReliabilitySetting, input RetryPolicyInput) bool {
	failure := input.Failure
	if !failure.ErrorPresent {
		return false
	}
	if input.AffinityFailure {
		return false
	}
	if failure.ChannelError {
		return true
	}
	if failure.SkipRetry {
		return false
	}
	if input.RemainingRetries <= 0 {
		return false
	}
	if input.SpecificChannel {
		return false
	}
	if failure.StatusCode >= 200 && failure.StatusCode < 300 {
		return false
	}
	if failure.StatusCode < 100 || failure.StatusCode > 599 {
		return true
	}
	if failure.BadResponseBody {
		return false
	}
	return policy.ShouldRetryForStatus(failure.StatusCode)
}

// ChannelHealthPolicyInput is the pure state-machine input produced by a
// channel health probe. A nil Failure is a successful provider result.
type ChannelHealthPolicyInput struct {
	Status              int
	AutoBan             bool
	AllowDisable        bool
	LocalError          bool
	LatencyMilliseconds int
	Failure             *ChannelFailure
}

// ChannelStatusTransition describes one status mutation decision. Persistence,
// ability-cache refresh, and operator notification deliberately remain outside
// this pure classifier.
type ChannelStatusTransition struct {
	PreviousStatus int
	NextStatus     int
	Reason         string
}

func (transition ChannelStatusTransition) Changed() bool {
	return transition.PreviousStatus != transition.NextStatus
}

// DecideChannelHealthTransition applies the independent enable/disable gates.
// Manually disabled channels and unrelated statuses are never changed.
func DecideChannelHealthTransition(policy setting.ChannelReliabilitySetting, input ChannelHealthPolicyInput) ChannelStatusTransition {
	decision := ChannelStatusTransition{PreviousStatus: input.Status, NextStatus: input.Status}
	failureReason := ""
	if input.Failure != nil {
		failureReason = AutomaticDisableReason(policy, *input.Failure)
	}
	latencyExceeded := policy.AutomaticDisableChannelEnabled && policy.ChannelDisableThreshold > 0 &&
		input.LatencyMilliseconds >= 0 &&
		float64(input.LatencyMilliseconds) > policy.ChannelDisableThreshold*1000
	if input.AllowDisable && input.AutoBan && input.Status == channelcatalog.ChannelStatusEnabled &&
		policy.AutomaticDisableChannelEnabled && (failureReason != "" || latencyExceeded) {
		decision.NextStatus = channelcatalog.ChannelStatusAutoDisabled
		decision.Reason = failureReason
		if decision.Reason == "" {
			decision.Reason = ChannelTransitionReasonLatency
		}
		return decision
	}
	// The reference treats an otherwise successful but over-threshold probe as
	// a failure even in passive mode: it must not revive an auto-disabled
	// channel merely because passive mode suppresses the disable transition.
	if input.Failure == nil && !latencyExceeded && !input.LocalError &&
		policy.AutomaticEnableChannelEnabled && input.Status == channelcatalog.ChannelStatusAutoDisabled {
		decision.NextStatus = channelcatalog.ChannelStatusEnabled
		decision.Reason = ChannelTransitionReasonRecovery
	}
	return decision
}

// SelectChannelsForReliabilityTest returns a stable-order, testable subset for
// one scheduled or manual sweep. Invalid modes fail closed rather than being
// interpreted as a full sweep.
func SelectChannelsForReliabilityTest(channels []model.Channel, mode string) ([]model.Channel, error) {
	switch mode {
	case setting.ChannelTestModeScheduledAll, setting.ChannelTestModeAutoBanOnly, setting.ChannelTestModePassiveRecovery:
	default:
		return nil, errors.New("invalid channel test mode")
	}
	selected := make([]model.Channel, 0, len(channels))
	for i := range channels {
		channel := channels[i]
		if channel.Status == channelcatalog.ChannelStatusManuallyDisabled {
			continue
		}
		if strings.TrimSpace(channel.TestModel) == "" && strings.TrimSpace(channel.Models) == "" {
			continue
		}
		if !channelHealthSupported(channelcatalog.ChannelType(channel.Type)) {
			continue
		}
		if mode == setting.ChannelTestModeAutoBanOnly && !channelAutoBanEnabled(channel) {
			continue
		}
		if mode == setting.ChannelTestModePassiveRecovery && channel.Status != channelcatalog.ChannelStatusAutoDisabled {
			continue
		}
		selected = append(selected, channel)
	}
	return selected, nil
}

func channelAutoBanEnabled(channel model.Channel) bool {
	return channel.AutoBan != nil && *channel.AutoBan == 1
}
