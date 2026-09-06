package billing

import (
	"encoding/json"
	"errors"
	"github.com/shopspring/decimal"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"strings"
)

const (
	relayQuotaReviewKlingPrivateDataPrefix  = "kling-video-v1:"
	relayQuotaReviewSunoPrivateDataPrefix   = "suno-task-v1:"
	relayQuotaReviewViduPrivateDataPrefix   = "vidu-task-v1:"
	relayQuotaReviewHailuoPrivateDataPrefix = "hailuo-task-v1:"
	relayQuotaReviewAliWanPrivateDataPrefix = "ali-video-v1:"
	relayQuotaReviewGeminiPrivateDataPrefix = "veo-task-v1:"
	relayQuotaReviewDoubaoPrivateDataPrefix = "doubao-video-v1:"
	relayQuotaReviewMidjourneyPrivatePrefix = "midjourney-task-v1:"
)

type relayQuotaReviewAsyncPrivateData struct {
	Version                 int             `json:"version"`
	RelayReservationID      string          `json:"relay_reservation_id"`
	EncryptedUpstreamTaskID string          `json:"encrypted_upstream_task_id"`
	EncryptedProviderTaskID string          `json:"encrypted_provider_task_id"`
	ChannelBaseURL          string          `json:"channel_base_url"`
	RoutingSnapshot         string          `json:"routing_snapshot"`
	EncryptedChannelKey     string          `json:"encrypted_channel_key"`
	SettlementPending       bool            `json:"settlement_pending"`
	Pricing                 json.RawMessage `json:"pricing"`
	BillingSource           string          `json:"billing_source"`
	SubscriptionID          int             `json:"subscription_id"`
	FundingUsageEpoch       int64           `json:"funding_usage_epoch"`
	TokenID                 int             `json:"token_id"`
}

func relayQuotaAsyncReviewShape(
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
) error {
	if record == nil || task == nil || operation == nil || task.TaskID == "" ||
		task.TaskID != operation.TaskID || task.Platform != operation.Platform ||
		task.UserId != operation.UserID || task.ChannelId != operation.ChannelID ||
		operation.ReservationID != record.ReservationID || operation.UserID != record.UserID ||
		operation.State != model.TaskOperationManualReview || operation.Attempts < 0 ||
		operation.NextAttemptAt != 0 || operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 ||
		operation.CreatedAt <= 0 || operation.CompletedAt <= 0 || task.Status != model.TaskStatusUnknown ||
		task.FinishTime <= 0 {
		return errors.New("async task review tuple is inconsistent")
	}
	if _, err := relayReservationFromRecord(record); err != nil {
		return err
	}
	privateData, err := relayQuotaAsyncReviewPrivateData(record, task, operation)
	if err != nil {
		return err
	}
	switch operation.Platform {
	case model.TaskOperationPlatformKling:
		if record.Status != model.RelayQuotaReservationStatusDispatched ||
			record.Operation != model.RelayQuotaReservationOperationSettle || record.CompletedAt != 0 ||
			record.ChannelID != 0 || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
			record.ActualQuota != record.ReservedQuota || task.Quota != record.RequestedQuota ||
			!operation.SettlementPending || !privateData.SettlementPending {
			return errors.New("Kling review does not retain the dispatched accounting hold")
		}
	case model.TaskOperationPlatformVolcEngine, model.TaskOperationPlatformDoubaoVideo:
		if record.Status != model.RelayQuotaReservationStatusDispatched ||
			record.Operation != model.RelayQuotaReservationOperationSettle || record.CompletedAt != 0 ||
			record.ChannelID != 0 || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
			record.ActualQuota != record.ReservedQuota || task.Quota != record.RequestedQuota ||
			!operation.SettlementPending || !privateData.SettlementPending {
			return errors.New("Doubao video review does not retain the dispatched accounting hold")
		}
	case model.TaskOperationPlatformMidjourney:
		if record.Status != model.RelayQuotaReservationStatusDispatched ||
			record.Operation != model.RelayQuotaReservationOperationSettle || record.CompletedAt != 0 ||
			record.ChannelID != 0 || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
			record.ActualQuota != record.ReservedQuota || task.Quota != record.RequestedQuota ||
			!operation.SettlementPending || !privateData.SettlementPending {
			return errors.New("Midjourney review does not retain the dispatched accounting hold")
		}
	case model.TaskOperationPlatformSuno:
		if record.Status != model.RelayQuotaReservationStatusSettled ||
			record.Operation != model.RelayQuotaReservationOperationSettle || record.CompletedAt <= 0 ||
			record.ChannelID != task.ChannelId || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
			task.Quota != record.ActualQuota || operation.SettlementPending || privateData.SettlementPending {
			return errors.New("Suno review does not match settled accounting")
		}
	case model.TaskOperationPlatformVidu:
		if operation.SettlementPending {
			if record.Status != model.RelayQuotaReservationStatusDispatched ||
				record.Operation != model.RelayQuotaReservationOperationSettle || record.CompletedAt != 0 ||
				record.ChannelID != 0 || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
				record.ActualQuota != record.ReservedQuota || task.Quota != record.RequestedQuota ||
				!privateData.SettlementPending {
				return errors.New("Vidu review does not retain the dispatched accounting hold")
			}
		} else if record.Status != model.RelayQuotaReservationStatusSettled ||
			record.Operation != model.RelayQuotaReservationOperationSettle || record.CompletedAt <= 0 ||
			record.ChannelID != task.ChannelId || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
			task.Quota != record.ActualQuota || privateData.SettlementPending {
			return errors.New("Vidu review does not match settled accounting")
		}
	case model.TaskOperationPlatformHailuo:
		if operation.SettlementPending {
			if record.Status != model.RelayQuotaReservationStatusDispatched ||
				record.Operation != model.RelayQuotaReservationOperationSettle || record.CompletedAt != 0 ||
				record.ChannelID != 0 || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
				record.ActualQuota != record.ReservedQuota || task.Quota != record.RequestedQuota ||
				!privateData.SettlementPending {
				return errors.New("Hailuo review does not retain the dispatched accounting hold")
			}
		} else if record.Status != model.RelayQuotaReservationStatusSettled ||
			record.Operation != model.RelayQuotaReservationOperationSettle || record.CompletedAt <= 0 ||
			record.ChannelID != task.ChannelId || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
			task.Quota != record.ActualQuota || privateData.SettlementPending {
			return errors.New("Hailuo review does not match settled accounting")
		}
	case model.TaskOperationPlatformAliWan:
		if operation.SettlementPending {
			if record.Status != model.RelayQuotaReservationStatusDispatched ||
				record.Operation != model.RelayQuotaReservationOperationSettle || record.CompletedAt != 0 ||
				record.ChannelID != 0 || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
				record.ActualQuota != record.ReservedQuota || task.Quota != record.RequestedQuota ||
				!privateData.SettlementPending {
				return errors.New("Alibaba Wan review does not retain the dispatched accounting hold")
			}
		} else if record.Status != model.RelayQuotaReservationStatusSettled ||
			record.Operation != model.RelayQuotaReservationOperationSettle || record.CompletedAt <= 0 ||
			record.ChannelID != task.ChannelId || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
			task.Quota != record.ActualQuota || privateData.SettlementPending {
			return errors.New("Alibaba Wan review does not match settled accounting")
		}
	case model.TaskOperationPlatformGeminiVeo, model.TaskOperationPlatformVertexVeo:
		if operation.SettlementPending {
			if record.Status != model.RelayQuotaReservationStatusDispatched ||
				record.Operation != model.RelayQuotaReservationOperationSettle || record.CompletedAt != 0 ||
				record.ChannelID != 0 || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
				record.ActualQuota != record.ReservedQuota || task.Quota != record.RequestedQuota ||
				!privateData.SettlementPending {
				return errors.New("Gemini Veo review does not retain the dispatched accounting hold")
			}
		} else if record.Status != model.RelayQuotaReservationStatusSettled ||
			record.Operation != model.RelayQuotaReservationOperationSettle || record.CompletedAt <= 0 ||
			record.ChannelID != task.ChannelId || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
			task.Quota != record.ActualQuota || privateData.SettlementPending {
			return errors.New("Gemini Veo review does not match settled accounting")
		}
	default:
		return errors.New("unsupported async task review platform")
	}
	return nil
}

func relayQuotaAsyncReviewPrivateData(
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
) (relayQuotaReviewAsyncPrivateData, error) {
	prefix := ""
	switch operation.Platform {
	case model.TaskOperationPlatformKling:
		prefix = relayQuotaReviewKlingPrivateDataPrefix
	case model.TaskOperationPlatformSuno:
		prefix = relayQuotaReviewSunoPrivateDataPrefix
	case model.TaskOperationPlatformVidu:
		prefix = relayQuotaReviewViduPrivateDataPrefix
	case model.TaskOperationPlatformHailuo:
		prefix = relayQuotaReviewHailuoPrivateDataPrefix
	case model.TaskOperationPlatformAliWan:
		prefix = relayQuotaReviewAliWanPrivateDataPrefix
	case model.TaskOperationPlatformGeminiVeo, model.TaskOperationPlatformVertexVeo:
		prefix = relayQuotaReviewGeminiPrivateDataPrefix
	case model.TaskOperationPlatformVolcEngine, model.TaskOperationPlatformDoubaoVideo:
		prefix = relayQuotaReviewDoubaoPrivateDataPrefix
	case model.TaskOperationPlatformMidjourney:
		prefix = relayQuotaReviewMidjourneyPrivatePrefix
	default:
		return relayQuotaReviewAsyncPrivateData{}, errors.New("unsupported async task private-data platform")
	}
	metadataLimit := relayQuotaReviewVideoMetadataMaxBytes
	if model.IsVeoTaskOperationPlatform(operation.Platform) {
		metadataLimit = relayQuotaReviewVeoMetadataMaxBytes
	}
	if !strings.HasPrefix(task.PrivateData, prefix) || len(task.PrivateData) > metadataLimit {
		return relayQuotaReviewAsyncPrivateData{}, errors.New("async task private-data envelope is invalid")
	}
	var privateData relayQuotaReviewAsyncPrivateData
	credentialCipherLimit := relayQuotaReviewCiphertextMaxBytes
	if model.IsVeoTaskOperationPlatform(operation.Platform) {
		credentialCipherLimit = relayQuotaReviewVeoCiphertextMaxBytes
	}
	if jsonutil.UnmarshalJsonStr(strings.TrimPrefix(task.PrivateData, prefix), &privateData) != nil {
		return relayQuotaReviewAsyncPrivateData{}, errors.New("async task private data is invalid")
	}
	validRoute := privateData.ChannelBaseURL != ""
	if model.IsVeoTaskOperationPlatform(operation.Platform) {
		validRoute = privateData.RoutingSnapshot != "" && len(privateData.RoutingSnapshot) <= 64<<10
	}
	if privateData.RelayReservationID != record.ReservationID || !validRoute ||
		!validRelayQuotaReviewBoundCiphertextFrameLimit(privateData.EncryptedChannelKey, credentialCipherLimit) ||
		privateData.BillingSource != record.FundingSource ||
		privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch || privateData.TokenID != record.TokenID {
		return relayQuotaReviewAsyncPrivateData{}, errors.New("async task private data does not match accounting")
	}
	providerCipher := privateData.EncryptedUpstreamTaskID
	if operation.Platform == model.TaskOperationPlatformSuno ||
		operation.Platform == model.TaskOperationPlatformMidjourney ||
		operation.Platform == model.TaskOperationPlatformVidu ||
		operation.Platform == model.TaskOperationPlatformHailuo ||
		operation.Platform == model.TaskOperationPlatformAliWan ||
		model.IsVeoTaskOperationPlatform(operation.Platform) {
		providerCipher = privateData.EncryptedProviderTaskID
	}
	if operation.Platform != model.TaskOperationPlatformSuno &&
		operation.Platform != model.TaskOperationPlatformMidjourney &&
		(privateData.Version != 1 || !validRelayQuotaAsyncReviewPricing(operation.Platform, privateData.Pricing)) {
		return relayQuotaReviewAsyncPrivateData{}, errors.New("async task pricing snapshot is invalid")
	}
	if providerCipher != "" && !validRelayQuotaReviewBoundCiphertextFrame(providerCipher) {
		return relayQuotaReviewAsyncPrivateData{}, errors.New("async provider task ciphertext is invalid")
	}
	if operation.EncryptedProviderTaskID != "" && !validRelayQuotaReviewBoundCiphertextFrame(operation.EncryptedProviderTaskID) {
		return relayQuotaReviewAsyncPrivateData{}, errors.New("async recovery provider ciphertext is invalid")
	}
	if providerCipher != "" && operation.EncryptedProviderTaskID != "" &&
		providerCipher != operation.EncryptedProviderTaskID {
		return relayQuotaReviewAsyncPrivateData{}, errors.New("async provider task ciphertext copies conflict")
	}
	return privateData, nil
}

func relayQuotaAsyncProviderTaskIDPresent(
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
) bool {
	if record == nil || task == nil || operation == nil {
		return false
	}
	privateData, err := relayQuotaAsyncReviewPrivateData(record, task, operation)
	if err != nil {
		return false
	}
	privateCipher := privateData.EncryptedUpstreamTaskID
	if operation.Platform == model.TaskOperationPlatformSuno ||
		operation.Platform == model.TaskOperationPlatformMidjourney ||
		operation.Platform == model.TaskOperationPlatformVidu ||
		operation.Platform == model.TaskOperationPlatformHailuo ||
		operation.Platform == model.TaskOperationPlatformAliWan ||
		model.IsVeoTaskOperationPlatform(operation.Platform) {
		privateCipher = privateData.EncryptedProviderTaskID
	}
	return operation.EncryptedProviderTaskID != "" || privateCipher != ""
}

func validRelayQuotaAsyncReviewPricing(platform string, raw json.RawMessage) bool {
	if len(raw) == 0 || len(raw) > relayQuotaReviewVideoMetadataMaxBytes {
		return false
	}
	if !model.IsVeoTaskOperationPlatform(platform) && platform != model.TaskOperationPlatformAliWan {
		var pricing ReferenceAsyncTaskBillingPlan
		return jsonutil.Unmarshal(raw, &pricing) == nil && pricing.Validate() == nil
	}
	var pricing struct {
		Plan                 ReferenceAsyncTaskBillingPlan `json:"plan"`
		Duration             int                           `json:"duration"`
		ResolutionMultiplier string                        `json:"resolution_multiplier"`
	}
	if jsonutil.Unmarshal(raw, &pricing) != nil || pricing.Plan.Validate() != nil ||
		(model.IsVeoTaskOperationPlatform(platform) && pricing.Duration != 4 && pricing.Duration != 6 && pricing.Duration != 8) ||
		(platform == model.TaskOperationPlatformAliWan && (pricing.Duration < 1 || pricing.Duration > 10)) ||
		pricing.ResolutionMultiplier == "" || len(pricing.ResolutionMultiplier) > 64 ||
		strings.TrimSpace(pricing.ResolutionMultiplier) != pricing.ResolutionMultiplier {
		return false
	}
	if !quotamath.IsSafeDecimalLiteral(pricing.ResolutionMultiplier) {
		return false
	}
	multiplier, err := decimal.NewFromString(pricing.ResolutionMultiplier)
	maximum := decimal.NewFromInt(10)
	if platform == model.TaskOperationPlatformAliWan {
		maximum = decimal.NewFromInt(100)
	}
	return err == nil && multiplier.IsPositive() && multiplier.LessThanOrEqual(maximum)
}
