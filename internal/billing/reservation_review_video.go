package billing

import (
	"encoding/base64"
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"strings"
)

func relayQuotaVideoReviewShape(
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
) error {
	if record == nil || task == nil || operation == nil ||
		record.Status != model.RelayQuotaReservationStatusSettled ||
		record.Operation != model.RelayQuotaReservationOperationSettle ||
		record.ChannelID <= 0 || record.CompletedAt <= 0 ||
		record.LeaseOwner != "" || record.LeaseExpiresAt != 0 {
		return errors.New("reservation is not an unleased settled video charge")
	}
	if _, err := relayReservationFromRecord(record); err != nil {
		return err
	}
	if operation.TaskID == "" || operation.ReservationID != record.ReservationID ||
		!model.IsOpenAIVideoTaskOperationPlatform(operation.Platform) ||
		operation.UserID != record.UserID || operation.ChannelID != record.ChannelID ||
		operation.State != model.TaskOperationManualReview || operation.SettlementPending ||
		operation.Attempts < 0 || operation.NextAttemptAt != 0 ||
		operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 ||
		operation.CreatedAt <= 0 || operation.CompletedAt <= 0 {
		return errors.New("video recovery operation is not an inactive manual-review row")
	}
	if task.TaskID != operation.TaskID || task.Platform != operation.Platform ||
		task.UserId != operation.UserID || task.ChannelId != operation.ChannelID ||
		task.Status != model.TaskStatusUnknown || task.Quota != record.ActualQuota ||
		task.FinishTime <= 0 {
		return errors.New("video task does not match the settled recovery operation")
	}
	if _, err := relayQuotaVideoReviewPrivateData(record, task); err != nil {
		return err
	}
	return nil
}

func relayQuotaVideoProviderTaskIDPresent(task *model.Task, operation *model.TaskOperation) bool {
	privateData, err := relayQuotaVideoReviewPrivateData(nil, task)
	if err != nil {
		return false
	}
	present := false
	if operation != nil && operation.EncryptedProviderTaskID != "" {
		if !validRelayQuotaReviewBoundCiphertextFrame(operation.EncryptedProviderTaskID) {
			return false
		}
		present = true
	}
	if privateData.EncryptedUpstreamTaskID != "" {
		if !validRelayQuotaReviewBoundCiphertextFrame(privateData.EncryptedUpstreamTaskID) {
			return false
		}
		present = true
	}
	return present
}

const (
	relayQuotaReviewVideoPrivateDataPrefix = "openai-video-v1:"
	relayQuotaReviewBoundCiphertextPrefix  = "async-task-v2:"
	relayQuotaReviewVideoMetadataMaxBytes  = 64 << 10
	relayQuotaReviewCiphertextMaxBytes     = 16 << 10
	relayQuotaReviewVeoMetadataMaxBytes    = 256 << 10
	relayQuotaReviewVeoCiphertextMaxBytes  = 192 << 10
)

type relayQuotaReviewVideoPrivateData struct {
	RelayReservationID      string `json:"relay_reservation_id"`
	EncryptedUpstreamTaskID string `json:"encrypted_upstream_task_id"`
	ChannelBaseURL          string `json:"channel_base_url"`
	EncryptedChannelKey     string `json:"encrypted_channel_key"`
	SettlementPending       bool   `json:"settlement_pending"`
	FreeModel               bool   `json:"free_model"`
	BillingSource           string `json:"billing_source"`
	SubscriptionID          int    `json:"subscription_id"`
	FundingUsageEpoch       int64  `json:"funding_usage_epoch"`
	TokenID                 int    `json:"token_id"`
}

func relayQuotaVideoReviewPrivateData(
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
) (relayQuotaReviewVideoPrivateData, error) {
	if task == nil || !model.IsOpenAIVideoTaskOperationPlatform(task.Platform) ||
		!strings.HasPrefix(task.PrivateData, relayQuotaReviewVideoPrivateDataPrefix) ||
		len(task.PrivateData) > relayQuotaReviewVideoMetadataMaxBytes {
		return relayQuotaReviewVideoPrivateData{}, errors.New("video task private-data envelope is invalid")
	}
	var privateData relayQuotaReviewVideoPrivateData
	if jsonutil.UnmarshalJsonStr(strings.TrimPrefix(task.PrivateData,
		relayQuotaReviewVideoPrivateDataPrefix), &privateData) != nil ||
		privateData.RelayReservationID == "" || privateData.ChannelBaseURL == "" ||
		!validRelayQuotaReviewBoundCiphertextFrame(privateData.EncryptedChannelKey) ||
		privateData.SettlementPending || privateData.BillingSource == "" ||
		privateData.SubscriptionID < 0 || privateData.FundingUsageEpoch < 0 || privateData.TokenID < 0 {
		return relayQuotaReviewVideoPrivateData{}, errors.New("video task private-data envelope is invalid")
	}
	if record != nil && (privateData.RelayReservationID != record.ReservationID ||
		privateData.BillingSource != record.FundingSource ||
		privateData.FreeModel != (record.FundingSource == BillingSourceFreeModel) ||
		privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch ||
		privateData.TokenID != record.TokenID) {
		return relayQuotaReviewVideoPrivateData{}, errors.New("video task private data does not match accounting")
	}
	return privateData, nil
}

func validRelayQuotaReviewBoundCiphertextFrame(value string) bool {
	return validRelayQuotaReviewBoundCiphertextFrameLimit(value, relayQuotaReviewCiphertextMaxBytes)
}

func validRelayQuotaReviewBoundCiphertextFrameLimit(value string, maximum int) bool {
	if value == "" || strings.TrimSpace(value) != value ||
		maximum <= 0 || len(value) > maximum ||
		!strings.HasPrefix(value, relayQuotaReviewBoundCiphertextPrefix) {
		return false
	}
	keyID, payload, ok := strings.Cut(strings.TrimPrefix(value,
		relayQuotaReviewBoundCiphertextPrefix), ":")
	if !ok || keyID == "" || len(keyID) > 32 || payload == "" {
		return false
	}
	for _, character := range keyID {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	// AES-GCM payloads contain a 12-byte nonce and at least a 16-byte tag.
	return err == nil && len(decoded) >= 28
}

func relayQuotaVideoReviewTaskKey(taskID, platform string) string {
	return taskID + "\x00" + platform
}
