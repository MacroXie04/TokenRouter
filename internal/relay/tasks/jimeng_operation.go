package tasks

import (
	"context"
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/jimeng"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"math"
	"strings"
	"time"
)

const (
	// A lease spans the 30-second provider fetch timeout plus database work and
	// scheduler jitter. It is renewed immediately before every network fetch.
	jimengOperationLeaseSeconds = int64(90)
	jimengOperationRetrySeconds = int64(30)
	// Jimeng's HTTP client can remain in flight for 60 seconds. Recovery of an
	// at-risk dispatch waits longer than that so a live request cannot be
	// conservatively charged before its definitive rejection arrives.
	jimengDispatchRecoverySeconds = int64(120)
	// Generic reservation expiry may be configured as low as 60 seconds. Jimeng
	// needs enough time for dispatch grace, the complete bounded retry budget,
	// scheduler delay, and operational clock skew. New generic reconcilers also
	// exempt active Jimeng operations, while this horizon protects mixed
	// deployments whose older reconcilers do not know that exemption.
	jimengReservationSafetySeconds = int64(2 * time.Hour / time.Second)
	jimengOperationBatchSize       = 100
	jimengOperationMaxAttempts     = 12
	jimengManualReviewReason       = "provider submission outcome requires manual review"
	jimengPollingReviewReason      = "provider task exceeded automatic polling horizon"
	jimengDefaultMaxPollDays       = 7
	jimengDefaultPassSeconds       = 45
)

type jimengPermanentRecoveryError struct{ cause error }

func (e *jimengPermanentRecoveryError) Error() string { return "permanent Jimeng recovery failure" }
func (e *jimengPermanentRecoveryError) Unwrap() error { return e.cause }

func permanentJimengRecoveryError(err error) error {
	if err == nil {
		return nil
	}
	return &jimengPermanentRecoveryError{cause: err}
}

func isPermanentJimengRecoveryError(err error) bool {
	var permanent *jimengPermanentRecoveryError
	if errors.As(err, &permanent) {
		return true
	}
	var upstream *relaycommon.UpstreamError
	if errors.As(err, &upstream) {
		return upstream.StatusCode >= 400 && upstream.StatusCode < 500 &&
			upstream.StatusCode != 408 && upstream.StatusCode != 409 && upstream.StatusCode != 429
	}
	return false
}

// createJimengReservedTask is the only supported creation path for a Jimeng
// task. The task, its recovery operation, both funding holds, and the durable
// reservation ledger are committed by one primary-database transaction.
func createJimengReservedTask(
	task *model.Task,
	token *model.Token,
	privateData *jimengTaskPrivateData,
) (*billingsvc.RelayQuotaReservation, error) {
	if task == nil || privateData == nil || task.TaskID == "" || task.UserId <= 0 || task.ChannelId <= 0 {
		return nil, errors.New("invalid Jimeng task reservation")
	}
	if err := validateJimengDurableText("task properties", task.Properties, jimengTaskTextMaxBytes); err != nil {
		return nil, err
	}
	return billingsvc.NewRelayQuotaReservationWithPersistence(
		task.UserId, token, task.Quota,
		func(tx *gorm.DB, creation billingsvc.RelayQuotaReservationCreation) error {
			privateData.RelayReservationID = creation.ReservationID
			privateData.BillingSource = creation.Funding.Source
			privateData.SubscriptionID = creation.Funding.SubscriptionId
			privateData.FundingUsageEpoch = creation.Funding.UsageEpoch
			privateData.FundingRequestID = creation.Funding.RequestId
			privateData.FundingReserved = creation.Funding.Reserved
			privateData.TokenID = creation.TokenID
			privateData.TokenReserved = creation.TokenReserved > 0
			privateData.TokenUnlimited = creation.TokenUnlimited
			encoded, err := marshalJimengTaskPrivateData(*privateData)
			if err != nil {
				return err
			}
			task.PrivateData = encoded
			if err := tx.Create(task).Error; err != nil {
				return err
			}
			now, err := model.DatabaseUnixTimestamp(tx)
			if err != nil {
				return err
			}
			minimumExpiry := now + jimengReservationSafetySeconds
			if err := tx.Model(&model.RelayQuotaReservationRecord{}).
				Where("reservation_id = ? AND status = ? AND expires_at < ?", creation.ReservationID,
					model.RelayQuotaReservationStatusHeld, minimumExpiry).
				UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
				return err
			}
			return tx.Create(&model.JimengTaskOperation{
				TaskID: task.TaskID, ReservationID: creation.ReservationID,
				UserID: task.UserId, ChannelID: task.ChannelId,
				State:         model.JimengTaskOperationPrepared,
				NextAttemptAt: now + jimengOperationRetrySeconds,
				CreatedAt:     now, UpdatedAt: now,
			}).Error
		},
	)
}

// markJimengTaskDispatching commits the at-risk accounting marker and all
// channel metadata before the network call. A crash after this function must
// be treated as an unknown provider outcome and must never lead to resubmit.
func markJimengTaskDispatching(
	task *model.Task,
	reservation *billingsvc.RelayQuotaReservation,
	channelID int,
	properties, privateJSON string,
) error {
	if task == nil || reservation == nil || channelID <= 0 {
		return errors.New("invalid Jimeng dispatch transition")
	}
	if err := validateJimengDurableText("task properties", properties, jimengTaskTextMaxBytes); err != nil {
		return err
	}
	if err := validateJimengDurableText("task private data", privateJSON, jimengTaskTextMaxBytes); err != nil {
		return err
	}
	var now int64
	err := reservation.MarkDispatchedWithPersistence(func(tx *gorm.DB) error {
		var err error
		now, err = model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		minimumExpiry := now + jimengReservationSafetySeconds
		if err := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("reservation_id = ? AND status = ? AND expires_at < ?", reservation.ReservationID(),
				model.RelayQuotaReservationStatusDispatched, minimumExpiry).
			UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
			return err
		}
		taskUpdate := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND status = ?", task.ID, task.TaskID, model.TaskStatusNotStart).
			Updates(map[string]any{
				"channel_id": channelID, "properties": properties,
				"private_data": privateJSON, "updated_at": now,
			})
		if taskUpdate.Error != nil {
			return taskUpdate.Error
		}
		if taskUpdate.RowsAffected != 1 {
			// MySQL reports changed rows by default. Atomic creation already wrote
			// this exact dispatch metadata, and its second-resolution updated_at can
			// also match, so a legitimate first dispatch can report zero changes.
			// Verify the full CAS predicate and desired values in this transaction
			// before advancing the operation marker.
			var current model.Task
			if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&current).Error; err != nil ||
				current.Status != model.TaskStatusNotStart || current.ChannelId != channelID ||
				current.Properties != properties || current.PrivateData != privateJSON {
				return errors.New("Jimeng task changed before dispatch")
			}
		}
		opUpdate := tx.Model(&model.JimengTaskOperation{}).
			Where("task_id = ? AND reservation_id = ? AND state = ? AND lease_owner = ?", task.TaskID,
				reservation.ReservationID(), model.JimengTaskOperationPrepared, "").
			Updates(map[string]any{
				"channel_id": channelID, "state": model.JimengTaskOperationDispatching,
				"settlement_pending": true, "next_attempt_at": now + jimengDispatchRecoverySeconds,
				"updated_at": now, "last_error": "",
			})
		if opUpdate.Error != nil {
			return opUpdate.Error
		}
		if opUpdate.RowsAffected != 1 {
			return errors.New("Jimeng recovery marker changed before dispatch")
		}
		return nil
	})
	if err != nil {
		if !jimengDispatchTransitionMatches(task, reservation.ReservationID(), channelID, properties, privateJSON) {
			return err
		}
	}
	task.ChannelId = channelID
	task.Properties = properties
	task.PrivateData = privateJSON
	task.UpdatedAt = now
	return nil
}

func jimengDispatchTransitionMatches(
	task *model.Task,
	reservationID string,
	channelID int,
	properties, privateJSON string,
) bool {
	if task == nil {
		return false
	}
	var storedTask model.Task
	if err := model.DB.First(&storedTask, task.ID).Error; err != nil ||
		storedTask.TaskID != task.TaskID || storedTask.Status != model.TaskStatusNotStart ||
		storedTask.ChannelId != channelID || storedTask.Properties != properties ||
		storedTask.PrivateData != privateJSON {
		return false
	}
	var operation model.JimengTaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ?", task.TaskID, reservationID).
		First(&operation).Error; err != nil {
		return false
	}
	return operation.State == model.JimengTaskOperationDispatching &&
		operation.ChannelID == channelID && operation.SettlementPending && operation.LeaseOwner == ""
}

// markJimengTaskSafeToRefund records the provider client's authoritative
// indication that no work was accepted. If the immediate refund later fails,
// another node will retry the refund rather than conservatively charging an
// unknown submission.
func markJimengTaskSafeToRefund(task *model.Task, reservation *billingsvc.RelayQuotaReservation) error {
	return markJimengTaskSafeToRefundWithTransaction(
		task, reservation,
		func(fn func(tx *gorm.DB) error) error { return model.DB.Transaction(fn) },
	)
}

type jimengOperationTransactionRunner func(fn func(tx *gorm.DB) error) error

func markJimengTaskSafeToRefundWithTransaction(
	task *model.Task,
	reservation *billingsvc.RelayQuotaReservation,
	transact jimengOperationTransactionRunner,
) error {
	if task == nil || reservation == nil {
		return errors.New("invalid Jimeng safe-refund transition")
	}
	if transact == nil {
		return errors.New("Jimeng safe-refund transaction runner is nil")
	}
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lastErr = transact(func(tx *gorm.DB) error {
			now, clockErr := model.DatabaseUnixTimestamp(tx)
			if clockErr != nil {
				return clockErr
			}
			result := tx.Model(&model.JimengTaskOperation{}).
				Where("task_id = ? AND reservation_id = ? AND state = ? AND lease_owner = ?", task.TaskID,
					reservation.ReservationID(), model.JimengTaskOperationDispatching, "").
				Updates(map[string]any{
					"state": model.JimengTaskOperationPrepared, "settlement_pending": false,
					"next_attempt_at": now + jimengOperationRetrySeconds,
					"updated_at":      now, "last_error": "",
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("Jimeng recovery marker changed before safe refund")
			}
			return nil
		})
		if lastErr == nil || jimengSafeRefundTransitionMatches(task.TaskID, reservation.ReservationID()) {
			return nil
		}
		if attempt < maxAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return lastErr
}

func jimengSafeRefundTransitionMatches(taskID, reservationID string) bool {
	var operation model.JimengTaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ?", taskID, reservationID).
		First(&operation).Error; err != nil {
		return false
	}
	return operation.State == model.JimengTaskOperationPrepared && !operation.SettlementPending &&
		operation.LeaseOwner == ""
}

func settleJimengAcceptedTask(
	task *model.Task,
	reservation *billingsvc.RelayQuotaReservation,
	privateJSON string,
	upstreamRaw []byte,
	status, failReason string,
	expectedOperationState string,
	leaseOwner string,
) error {
	if task == nil || reservation == nil {
		return errors.New("invalid Jimeng settlement")
	}
	if status != model.TaskStatusSubmitted && status != model.TaskStatusUnknown {
		return errors.New("invalid Jimeng accepted status")
	}
	if status == model.TaskStatusUnknown {
		storedPrivate, err := decodeJimengTaskPrivateDataStored(privateJSON)
		if err != nil {
			return fmt.Errorf("decode unknown Jimeng settlement metadata: %w", err)
		}
		if strings.TrimSpace(storedPrivate.UpstreamTaskID) == "" &&
			strings.TrimSpace(storedPrivate.EncryptedUpstreamTaskID) == "" {
			// This is a definitive, unpollable accounting outcome. Recovery no
			// longer needs the channel snapshot, so do not retain credentials for
			// the lifetime of the task merely because submission was ambiguous.
			stripJimengTerminalRecoverySecrets(&storedPrivate)
			privateJSON, err = marshalJimengTaskPrivateData(storedPrivate)
			if err != nil {
				return err
			}
		}
	}
	if err := validateJimengDurableText("task properties", task.Properties, jimengTaskTextMaxBytes); err != nil {
		return err
	}
	if err := validateJimengDurableText("task private data", privateJSON, jimengTaskTextMaxBytes); err != nil {
		return err
	}
	upstreamRaw = durableJimengProviderPayload(upstreamRaw)
	failReason = boundedJimengFailReason(failReason)
	operationState := model.JimengTaskOperationSubmitted
	if status == model.TaskStatusUnknown {
		operationState = model.JimengTaskOperationUnknown
	}
	if expectedOperationState != model.JimengTaskOperationDispatching &&
		expectedOperationState != model.JimengTaskOperationSubmitted &&
		expectedOperationState != model.JimengTaskOperationManualReview {
		return errors.New("invalid expected Jimeng operation state")
	}
	var now int64
	err := reservation.SettleWithChannelAndPersistence(
		task.Quota, task.ChannelId,
		func(tx *gorm.DB) error {
			var clockErr error
			now, clockErr = model.DatabaseUnixTimestamp(tx)
			if clockErr != nil {
				return clockErr
			}
			completedAt := int64(0)
			nextAttempt := now + jimengOperationRetrySeconds
			if status == model.TaskStatusUnknown {
				completedAt = now
				nextAttempt = 0
			}
			var current model.Task
			if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&current).Error; err != nil {
				return err
			}
			if current.Status == status && current.PrivateData == privateJSON {
				// The accounting record determines whether this is a true replay;
				// still repair/finish the operation row below when necessary.
			} else if current.Status != model.TaskStatusNotStart &&
				!(current.Status == status && current.ChannelId == task.ChannelId) &&
				!(current.Status == model.TaskStatusUnknown &&
					current.FailReason == jimengManualReviewReason &&
					expectedOperationState == model.JimengTaskOperationManualReview &&
					status == model.TaskStatusSubmitted) {
				return fmt.Errorf("Jimeng task is already in status %s", current.Status)
			}
			taskUpdate := tx.Model(&model.Task{}).Where("id = ?", task.ID).
				Updates(map[string]any{
					"channel_id": task.ChannelId, "private_data": privateJSON,
					"data": string(upstreamRaw), "status": status,
					"fail_reason": failReason, "updated_at": now,
				})
			if taskUpdate.Error != nil {
				return taskUpdate.Error
			}
			opQuery := tx.Model(&model.JimengTaskOperation{}).
				Where("task_id = ? AND reservation_id = ? AND state = ?", task.TaskID,
					reservation.ReservationID(), expectedOperationState)
			if leaseOwner != "" {
				opQuery = opQuery.Where("lease_owner = ?", leaseOwner)
			} else {
				opQuery = opQuery.Where("lease_owner = ?", "")
			}
			opUpdates := map[string]any{
				"state": operationState, "settlement_pending": false,
				"encrypted_provider_task_id": "",
				"next_attempt_at":            nextAttempt, "completed_at": completedAt,
				"updated_at": now, "last_error": "",
			}
			// A background poller keeps its fencing lease across settlement and
			// the subsequent provider fetch. Immediate request settlement and
			// unknown terminal recovery release it here.
			if leaseOwner == "" || status == model.TaskStatusUnknown {
				opUpdates["lease_owner"] = ""
				opUpdates["lease_expires_at"] = 0
			}
			opUpdate := opQuery.Updates(opUpdates)
			if opUpdate.Error != nil {
				return opUpdate.Error
			}
			if opUpdate.RowsAffected != 1 {
				return billingsvc.ErrRelayQuotaReservationBusy
			}
			auditEntry, auditErr := newJimengConsumeAudit(
				*task, privateJSON, upstreamRaw, status, reservation.ReservationID(),
			)
			if auditErr != nil {
				return auditErr
			}
			return billingsvc.EnqueueAuditLogTx(tx, auditEntry)
		},
	)
	if err != nil {
		if !jimengSettlementTransitionMatches(task, reservation.ReservationID(), privateJSON, status) {
			return err
		}
	}
	task.PrivateData = privateJSON
	task.Data = string(upstreamRaw)
	task.Status = status
	task.FailReason = failReason
	task.UpdatedAt = now
	return nil
}

func jimengAuditEventID(reservationID string) string {
	return "jimeng:" + reservationID
}

// newJimengConsumeAudit builds a canonical payload from durable task state.
// It deliberately excludes provider-controlled identifiers and request-local
// metadata so foreground and recovery workers generate byte-identical events.
func newJimengConsumeAudit(
	task model.Task,
	privateJSON string,
	upstreamRaw []byte,
	status, reservationID string,
) (*model.Log, error) {
	if reservationID == "" || task.TaskID == "" {
		return nil, errors.New("invalid Jimeng audit identity")
	}
	privateData, err := decodeJimengTaskPrivateDataStored(privateJSON)
	if err != nil {
		return nil, fmt.Errorf("decode Jimeng audit metadata: %w", err)
	}
	properties, _, err := decodeJimengTaskMetadataChecked(model.Task{
		Properties: task.Properties, PrivateData: privateJSON,
	})
	if err != nil {
		return nil, err
	}
	other := map[string]any{
		"billing_source":       privateData.BillingSource,
		"provider_outcome":     "accepted",
		"relay_reservation_id": reservationID,
		"task_id":              task.TaskID,
		"task_platform":        jimengTaskPlatform,
	}
	if status == model.TaskStatusUnknown {
		other["provider_outcome"] = "unknown"
	}
	if privateData.SubscriptionID > 0 {
		other["subscription_id"] = privateData.SubscriptionID
		other["subscription_usage_epoch"] = privateData.FundingUsageEpoch
	}
	if privateData.BillingSource == billingsvc.BillingSourceSubscription && privateData.FundingReserved > 0 {
		other["subscription_pre_consumed"] = privateData.FundingReserved
	}
	if privateData.HasBillingGroupRatio {
		if privateData.BillingGroupRatio < 0 || math.IsNaN(privateData.BillingGroupRatio) || math.IsInf(privateData.BillingGroupRatio, 0) {
			return nil, errors.New("invalid persisted Jimeng billing group ratio")
		}
		other["group_ratio"] = privateData.BillingGroupRatio
		if privateData.HasSpecialGroupRatio {
			other["user_group_ratio"] = privateData.BillingGroupRatio
		}
	}
	otherJSON, err := jsonutil.Marshal(other)
	if err != nil {
		return nil, fmt.Errorf("marshal Jimeng audit metadata: %w", err)
	}
	eventID := jimengAuditEventID(reservationID)
	if len(eventID) > 64 {
		return nil, errors.New("Jimeng audit event id is invalid")
	}
	createdAt := task.CreatedAt
	if createdAt <= 0 {
		return nil, errors.New("Jimeng audit timestamp is invalid")
	}
	upstreamRequestID := ""
	var accepted struct {
		RequestID          string `json:"request_id"`
		RequestFingerprint string `json:"request_fingerprint"`
	}
	if len(upstreamRaw) > 0 && jsonutil.Unmarshal(upstreamRaw, &accepted) == nil {
		upstreamRequestID = cryptoutil.NormalizeProviderCorrelationID(accepted.RequestFingerprint)
		if upstreamRequestID == "" {
			upstreamRequestID = cryptoutil.NormalizeProviderCorrelationID(accepted.RequestID)
		}
	}
	return &model.Log{
		AuditEventId:      &eventID,
		UserId:            task.UserId,
		CreatedAt:         createdAt,
		Type:              billingsvc.LogTypeConsume,
		ModelName:         properties.OriginModelName,
		Quota:             task.Quota,
		ChannelId:         task.ChannelId,
		TokenId:           privateData.TokenID,
		Group:             task.Group,
		UpstreamRequestId: upstreamRequestID,
		Other:             string(otherJSON),
	}, nil
}

func jimengSettlementTransitionMatches(
	task *model.Task,
	reservationID, privateJSON, status string,
) bool {
	if task == nil {
		return false
	}
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil ||
		record.Status != model.RelayQuotaReservationStatusSettled || record.ActualQuota != task.Quota ||
		record.ChannelID != task.ChannelId {
		return false
	}
	var storedTask model.Task
	if err := model.DB.First(&storedTask, task.ID).Error; err != nil ||
		storedTask.Status != status || storedTask.PrivateData != privateJSON {
		return false
	}
	var operation model.JimengTaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ?", task.TaskID, reservationID).
		First(&operation).Error; err != nil || operation.SettlementPending {
		return false
	}
	expectedState := model.JimengTaskOperationSubmitted
	if status == model.TaskStatusUnknown {
		expectedState = model.JimengTaskOperationUnknown
	}
	return operation.State == expectedState
}

// persistJimengPendingOutcome records the provider outcome without changing
// accounting. It is the recovery write used when the all-in-one settlement
// transaction fails for a counter or storage reason. The already-existing
// dispatching row remains a safe cluster-visible fallback if this write also
// fails.
func persistJimengPendingOutcome(
	task *model.Task,
	privateData jimengTaskPrivateData,
	upstreamRaw []byte,
	status, failReason string,
) error {
	if task == nil || privateData.RelayReservationID == "" {
		return errors.New("invalid pending Jimeng outcome")
	}
	upstreamRaw = durableJimengProviderPayload(upstreamRaw)
	failReason = boundedJimengFailReason(failReason)
	privateData.SettlementPending = true
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	state := model.JimengTaskOperationDispatching
	if status == model.TaskStatusSubmitted && privateData.UpstreamTaskID != "" {
		state = model.JimengTaskOperationSubmitted
	}
	var now int64
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var clockErr error
		now, clockErr = model.DatabaseUnixTimestamp(tx)
		if clockErr != nil {
			return clockErr
		}
		taskUpdate := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND status = ?", task.ID, task.TaskID, model.TaskStatusNotStart).
			Updates(map[string]any{
				"channel_id": task.ChannelId, "private_data": privateJSON,
				"data": string(upstreamRaw), "status": status,
				"fail_reason": failReason, "updated_at": now,
			})
		if taskUpdate.Error != nil {
			return taskUpdate.Error
		}
		if taskUpdate.RowsAffected != 1 {
			var current model.Task
			if err := tx.First(&current, task.ID).Error; err != nil ||
				current.Status != status || current.PrivateData != privateJSON {
				return errors.New("Jimeng task changed before pending outcome")
			}
		}
		opUpdate := tx.Model(&model.JimengTaskOperation{}).
			Where("task_id = ? AND reservation_id = ? AND state IN ?", task.TaskID,
				privateData.RelayReservationID, []string{
					model.JimengTaskOperationDispatching,
					model.JimengTaskOperationSubmitted,
				}).Where("lease_owner = ?", "").
			Updates(map[string]any{
				"state": state, "settlement_pending": true,
				"next_attempt_at": now, "updated_at": now, "last_error": "",
			})
		if opUpdate.Error != nil {
			return opUpdate.Error
		}
		if opUpdate.RowsAffected != 1 {
			return errors.New("Jimeng recovery operation changed before pending outcome")
		}
		return nil
	})
	if err != nil {
		return err
	}
	task.PrivateData = privateJSON
	task.Data = string(upstreamRaw)
	task.Status = status
	task.FailReason = failReason
	task.UpdatedAt = now
	return nil
}

// persistJimengAcceptedOperationFallback preserves an accepted provider id in
// the primary database when the wider task/accounting transaction cannot be
// committed. Keeping this encrypted copy on the recovery operation lets any
// node settle and poll autonomously; the local file journal is never required
// for correctness.
func persistJimengAcceptedOperationFallback(
	task *model.Task,
	reservationID, upstreamTaskID string,
) error {
	if task == nil || reservationID == "" || strings.TrimSpace(upstreamTaskID) == "" {
		return errors.New("invalid accepted Jimeng recovery fallback")
	}
	encryptedTaskID, err := jimengEncrypt(upstreamTaskID)
	if err != nil {
		return errors.New("encrypt accepted Jimeng recovery identifier")
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	result := model.DB.Model(&model.JimengTaskOperation{}).
		Where("task_id = ? AND reservation_id = ? AND state IN ?", task.TaskID, reservationID,
			[]string{model.JimengTaskOperationDispatching, model.JimengTaskOperationSubmitted}).
		Where("lease_owner = ?", "").
		Updates(map[string]any{
			"state": model.JimengTaskOperationSubmitted, "settlement_pending": true,
			"encrypted_provider_task_id": encryptedTaskID,
			"next_attempt_at":            now, "updated_at": now, "last_error": "",
		})
	if result.Error == nil && result.RowsAffected == 1 {
		return nil
	}
	var operation model.JimengTaskOperation
	verifyErr := model.DB.Where("task_id = ? AND reservation_id = ?", task.TaskID, reservationID).
		First(&operation).Error
	if verifyErr == nil && operation.State == model.JimengTaskOperationSubmitted &&
		operation.SettlementPending && operation.EncryptedProviderTaskID == encryptedTaskID &&
		operation.LeaseOwner == "" {
		return nil
	}
	if result.Error != nil {
		return errors.Join(result.Error, verifyErr)
	}
	return errors.Join(errors.New("accepted Jimeng recovery marker changed concurrently"), verifyErr)
}

func refundJimengTaskReservation(
	task *model.Task,
	reservation *billingsvc.RelayQuotaReservation,
	failReason string,
	leaseOwner string,
) error {
	expectedStates := []string{
		model.JimengTaskOperationPrepared,
		model.JimengTaskOperationDispatching,
	}
	if leaseOwner != "" {
		expectedStates = []string{model.JimengTaskOperationPrepared}
	}
	return refundJimengTaskReservationWithFence(
		task, reservation, failReason, expectedStates, leaseOwner,
	)
}

func refundJimengTaskReservationWithFence(
	task *model.Task,
	reservation *billingsvc.RelayQuotaReservation,
	failReason string,
	expectedStates []string,
	leaseOwner string,
) error {
	if task == nil || reservation == nil {
		return errors.New("invalid Jimeng refund")
	}
	if len(expectedStates) == 0 {
		return errors.New("invalid Jimeng refund state")
	}
	failReason = boundedJimengFailReason(failReason)
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lastErr = reservation.RefundWithPersistence(func(tx *gorm.DB) error {
			now, clockErr := model.DatabaseUnixTimestamp(tx)
			if clockErr != nil {
				return clockErr
			}
			var current model.Task
			if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&current).Error; err != nil {
				return err
			}
			allowedStatus := current.Status == model.TaskStatusNotStart ||
				(current.Status == model.TaskStatusUnknown && current.FailReason == jimengManualReviewReason) ||
				current.Status == model.TaskStatusFailure
			if !allowedStatus {
				return errors.New("Jimeng task changed before refund")
			}
			privateData, privateErr := decodeJimengTaskPrivateDataStored(current.PrivateData)
			if privateErr != nil {
				return fmt.Errorf("decode Jimeng refund metadata: %w", privateErr)
			}
			stripJimengTerminalRecoverySecrets(&privateData)
			sanitizedPrivate, privateErr := marshalJimengTaskPrivateData(privateData)
			if privateErr != nil {
				return fmt.Errorf("sanitize Jimeng refund metadata: %w", privateErr)
			}
			taskUpdate := tx.Model(&model.Task{}).
				Where("id = ? AND task_id = ? AND status = ? AND private_data = ?",
					current.ID, current.TaskID, current.Status, current.PrivateData)
			if current.Status == model.TaskStatusUnknown {
				taskUpdate = taskUpdate.Where("fail_reason = ?", jimengManualReviewReason)
			}
			taskUpdate = taskUpdate.
				Updates(map[string]any{
					"status": model.TaskStatusFailure, "fail_reason": failReason,
					"finish_time": now, "progress": "100%", "updated_at": now,
					"private_data": sanitizedPrivate,
				})
			if taskUpdate.Error != nil {
				return taskUpdate.Error
			}
			if taskUpdate.RowsAffected != 1 {
				var verified model.Task
				if err := tx.First(&verified, task.ID).Error; err != nil ||
					verified.Status != model.TaskStatusFailure || verified.PrivateData != sanitizedPrivate {
					return errors.New("Jimeng task changed before refund")
				}
			}
			opQuery := tx.Model(&model.JimengTaskOperation{}).
				Where("task_id = ? AND reservation_id = ? AND state IN ? AND lease_owner = ?",
					task.TaskID, reservation.ReservationID(), expectedStates, leaseOwner)
			opUpdate := opQuery.Updates(map[string]any{
				"state": model.JimengTaskOperationRefunded, "settlement_pending": false,
				"encrypted_provider_task_id": "",
				"next_attempt_at":            0, "completed_at": now, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": 0,
			})
			if opUpdate.Error != nil {
				return opUpdate.Error
			}
			if opUpdate.RowsAffected != 1 {
				return billingsvc.ErrRelayQuotaReservationBusy
			}
			return nil
		})
		if lastErr == nil || jimengRefundTransitionMatches(task, reservation.ReservationID()) {
			return nil
		}
		if attempt < maxAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return lastErr
}

func jimengRefundTransitionMatches(task *model.Task, reservationID string) bool {
	if task == nil {
		return false
	}
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil ||
		record.Status != model.RelayQuotaReservationStatusRefunded {
		return false
	}
	var storedTask model.Task
	if err := model.DB.First(&storedTask, task.ID).Error; err != nil || storedTask.Status != model.TaskStatusFailure {
		return false
	}
	var operation model.JimengTaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ?", task.TaskID, reservationID).
		First(&operation).Error; err != nil {
		return false
	}
	return operation.State == model.JimengTaskOperationRefunded && !operation.SettlementPending
}

func loadJimengTaskOperation(taskID string) (*model.JimengTaskOperation, error) {
	var operation model.JimengTaskOperation
	if err := model.DB.Where("task_id = ?", taskID).First(&operation).Error; err != nil {
		return nil, err
	}
	return &operation, nil
}

func claimJimengTaskOperationForClient(
	taskID, reservationID string,
	allowedStates []string,
) (*model.JimengTaskOperation, bool, error) {
	if taskID == "" || reservationID == "" || len(allowedStates) == 0 {
		return nil, false, errors.New("invalid Jimeng client recovery claim")
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return nil, false, err
	}
	ownerID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, false, err
	}
	owner := "jimeng-client-" + ownerID
	result := model.DB.Model(&model.JimengTaskOperation{}).
		Where("task_id = ? AND reservation_id = ? AND state IN ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			taskID, reservationID, allowedStates, now).
		Updates(map[string]any{
			"lease_owner": owner, "lease_expires_at": now + jimengOperationLeaseSeconds,
			"updated_at": now,
		})
	if result.Error != nil {
		return nil, false, result.Error
	}
	var operation model.JimengTaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ?", taskID, reservationID).
		First(&operation).Error; err != nil {
		return nil, false, err
	}
	claimed := result.RowsAffected == 1 && operation.LeaseOwner == owner
	return &operation, claimed, nil
}

func jimengOperationBackoff(attempt int) int64 {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 10 {
		attempt = 10
	}
	delay := time.Duration(attempt) * time.Duration(jimengOperationRetrySeconds) * time.Second
	return int64(delay / time.Second)
}

func jimengOperationDatabaseNowAtLeast(floor int64) (int64, error) {
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return 0, err
	}
	if now < floor {
		return floor, nil
	}
	return now, nil
}

// ReconcileJimengTaskOperations is the autonomous, cluster-safe recovery and
// polling pass registered with the periodic job runner.
func ReconcileJimengTaskOperations() error {
	return ReconcileJimengTaskOperationsContext(context.Background())
}

func ReconcileJimengTaskOperationsContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return errors.Join(
		PromoteJimengTaskRecoveryContext(ctx),
		reconcileJimengTaskOperationsDatabaseContext(ctx),
	)
}

// PromoteJimengTaskRecoveryContext is intentionally node-local and runs on
// every instance outside the cluster singleton lease. It imports encrypted
// emergency outcomes and cleans that instance's journal.
func PromoteJimengTaskRecoveryContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	now, clockErr := model.DatabaseUnixTimestamp(model.DB.WithContext(ctx))
	var promotionErr error
	if clockErr == nil {
		promotionErr = promoteJimengRecoveryRecords(now, jimengRecoveryCleanupLimit)
	}
	// A failed promotion may be the only surviving evidence that a provider
	// accepted or definitively rejected a submission. Never age-clean journals
	// in the same pass; a later successful transition removes its own record.
	if promotionErr != nil || clockErr != nil {
		return errors.Join(promotionErr, clockErr)
	}
	now, cleanupClockErr := model.DatabaseUnixTimestamp(model.DB.WithContext(ctx))
	var cleanupErr error
	if cleanupClockErr == nil {
		cleanupErr = cleanupJimengRecoveryRecords(now, jimengRecoveryCleanupLimit)
	}
	return errors.Join(promotionErr, clockErr, cleanupClockErr, cleanupErr)
}

func reconcileJimengTaskOperationsDatabaseContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	passContext, cancel := context.WithTimeout(ctx, jimengRecoveryPassDuration())
	defer cancel()
	return reconcileJimengTaskOperationsWithClock(
		passContext, jimengOperationBatchSize,
		func() (int64, error) { return model.DatabaseUnixTimestamp(model.DB.WithContext(passContext)) },
	)
}

func jimengRecoveryPassDuration() time.Duration {
	passSeconds := env.GetEnvInt("JIMENG_RECOVERY_PASS_SECONDS", jimengDefaultPassSeconds)
	if passSeconds < 32 || passSeconds > 90 {
		passSeconds = jimengDefaultPassSeconds
	}
	return time.Duration(passSeconds) * time.Second
}

func reconcileJimengTaskOperationsAt(now int64, limit int) error {
	return reconcileJimengTaskOperationsAtContext(context.Background(), now, limit)
}

func reconcileJimengTaskOperationsAtContext(ctx context.Context, now int64, limit int) error {
	return reconcileJimengTaskOperationsWithClock(ctx, limit, func() (int64, error) { return now, nil })
}

type jimengOperationClock func() (int64, error)

func reconcileJimengTaskOperationsWithClock(ctx context.Context, limit int, clock jimengOperationClock) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if clock == nil {
		return errors.New("Jimeng recovery clock is nil")
	}
	if limit <= 0 {
		limit = jimengOperationBatchSize
	}
	now, err := clock()
	if err != nil {
		return err
	}
	var candidates []model.JimengTaskOperation
	if err := model.DB.WithContext(ctx).Where(
		"state IN ? AND next_attempt_at <= ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
		[]string{
			model.JimengTaskOperationPrepared,
			model.JimengTaskOperationDispatching,
			model.JimengTaskOperationSubmitted,
		}, now, now,
	).Order("next_attempt_at asc, id asc").Limit(limit).Find(&candidates).Error; err != nil {
		return err
	}
	var reconcileErrors []error
	for index := range candidates {
		if err := ctx.Err(); err != nil {
			reconcileErrors = append(reconcileErrors, err)
			break
		}
		if deadline, hasDeadline := ctx.Deadline(); hasDeadline &&
			candidates[index].State != model.JimengTaskOperationPrepared &&
			time.Until(deadline) < 31*time.Second {
			// Do not claim a row unless its complete 30-second provider request and
			// a small fenced-write allowance fit in this pass. An artificial pass
			// deadline must never count as a provider failure.
			continue
		}
		claimNow, clockErr := clock()
		if clockErr != nil {
			reconcileErrors = append(reconcileErrors, clockErr)
			continue
		}
		operation, claimed, err := claimJimengTaskOperation(candidates[index].ID, claimNow)
		if err != nil {
			reconcileErrors = append(reconcileErrors, err)
			continue
		}
		if !claimed {
			continue
		}
		if err := reconcileClaimedJimengTaskOperation(ctx, operation, claimNow); err != nil {
			reconcileErrors = append(reconcileErrors, err)
		}
	}
	return errors.Join(reconcileErrors...)
}

func claimJimengTaskOperation(id int64, now int64) (*model.JimengTaskOperation, bool, error) {
	if id <= 0 {
		return nil, false, errors.New("invalid Jimeng operation")
	}
	ownerID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, false, err
	}
	owner := "jimeng-recovery-" + ownerID
	result := model.DB.Model(&model.JimengTaskOperation{}).
		Where("id = ? AND state IN ? AND next_attempt_at <= ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			id, []string{
				model.JimengTaskOperationPrepared,
				model.JimengTaskOperationDispatching,
				model.JimengTaskOperationSubmitted,
			}, now, now).
		Updates(map[string]any{
			"lease_owner": owner, "lease_expires_at": now + jimengOperationLeaseSeconds,
			"attempts": gorm.Expr("attempts + ?", 1), "updated_at": now,
		})
	if result.Error != nil {
		return nil, false, result.Error
	}
	var operation model.JimengTaskOperation
	if err := model.DB.First(&operation, id).Error; err != nil {
		return nil, false, err
	}
	return &operation, result.RowsAffected == 1 && operation.LeaseOwner == owner, nil
}

func renewJimengTaskOperationLease(operation *model.JimengTaskOperation, floor int64) (int64, error) {
	if operation == nil || operation.LeaseOwner == "" {
		return 0, errors.New("unleased Jimeng recovery operation")
	}
	now, err := jimengOperationDatabaseNowAtLeast(floor)
	if err != nil {
		return 0, err
	}
	expiresAt := now + jimengOperationLeaseSeconds
	if expiresAt <= operation.LeaseExpiresAt {
		expiresAt = operation.LeaseExpiresAt + 1
	}
	result := model.DB.Model(&model.JimengTaskOperation{}).
		Where("id = ? AND lease_owner = ? AND state = ?", operation.ID, operation.LeaseOwner,
			model.JimengTaskOperationSubmitted).
		Updates(map[string]any{"lease_expires_at": expiresAt, "updated_at": now})
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != 1 {
		return 0, billingsvc.ErrRelayQuotaReservationBusy
	}
	operation.LeaseExpiresAt = expiresAt
	operation.UpdatedAt = now
	return now, nil
}

func reconcileClaimedJimengTaskOperation(ctx context.Context, operation *model.JimengTaskOperation, now int64) error {
	if operation == nil || operation.LeaseOwner == "" {
		return errors.New("unleased Jimeng recovery operation")
	}
	var task model.Task
	if err := model.DB.Where("task_id = ? AND user_id = ?", operation.TaskID, operation.UserID).
		First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = permanentJimengRecoveryError(err)
		}
		return failClaimedJimengOperation(operation, now, err)
	}
	terminalTask := isJimengTerminalTaskStatus(task.Status)
	var privateData jimengTaskPrivateData
	var err error
	if terminalTask {
		privateData, err = decodeJimengTaskPrivateDataStored(task.PrivateData)
	} else {
		privateData, err = decodeJimengTaskPrivateData(task.PrivateData)
	}
	if err != nil {
		if isJimengEncryptionKeyUnavailable(err) {
			return releaseClaimedJimengOperationWithoutAttempt(operation, now)
		}
		return failClaimedJimengOperation(operation, now, permanentJimengRecoveryError(err))
	}
	if privateData.RelayReservationID != operation.ReservationID || task.ChannelId != operation.ChannelID {
		return failClaimedJimengOperation(operation, now,
			permanentJimengRecoveryError(errors.New("Jimeng operation metadata mismatch")))
	}
	if !terminalTask && privateData.UpstreamTaskID == "" && operation.EncryptedProviderTaskID != "" {
		upstreamTaskID, decryptErr := jimengDecrypt(operation.EncryptedProviderTaskID)
		if isJimengEncryptionKeyUnavailable(decryptErr) {
			return releaseClaimedJimengOperationWithoutAttempt(operation, now)
		}
		if decryptErr != nil || strings.TrimSpace(upstreamTaskID) == "" {
			if decryptErr == nil {
				decryptErr = errors.New("empty encrypted Jimeng provider identifier")
			}
			return failClaimedJimengOperation(operation, now, permanentJimengRecoveryError(decryptErr))
		}
		privateData.UpstreamTaskID = upstreamTaskID
	}
	reservation, err := billingsvc.RestoreRelayQuotaReservation(operation.ReservationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = permanentJimengRecoveryError(err)
		}
		return failClaimedJimengOperation(operation, now, err)
	}
	if terminalTask {
		return settleAndTerminalizeClaimedJimengTask(operation, &task, reservation, now)
	}

	switch operation.State {
	case model.JimengTaskOperationPrepared:
		if err := refundJimengTaskReservation(
			&task, reservation, "submission was not dispatched before recovery", operation.LeaseOwner,
		); err != nil {
			return failClaimedJimengOperation(operation, now, err)
		}
		_ = removeJimengRecovery(task.TaskID)
		return nil
	case model.JimengTaskOperationDispatching:
		if task.Status == model.TaskStatusUnknown && privateData.SettlementPending &&
			operation.SettlementPending {
			// The Task row and operation independently record the foreground's
			// durable ambiguous outcome. That is sufficient cluster-visible
			// evidence to settle UNKNOWN without consulting a local journal.
			privateData.SettlementPending = false
			privateJSON, encodeErr := marshalJimengTaskPrivateData(privateData)
			if encodeErr != nil {
				return failClaimedJimengOperation(operation, now, encodeErr)
			}
			if err := settleJimengAcceptedTask(
				&task, reservation, privateJSON, []byte(task.Data), model.TaskStatusUnknown,
				task.FailReason, model.JimengTaskOperationDispatching, operation.LeaseOwner,
			); err != nil {
				return failClaimedJimengOperation(operation, now, err)
			}
			return nil
		}
		// The primary encrypted provider-id field is authoritative. The local
		// journal is consulted only as an emergency secondary source when both
		// accepted-response database writes failed on this same filesystem.
		envelope, journalErr := loadJimengRecovery(task.TaskID)
		if journalErr == nil {
			if envelope.UserID != operation.UserID || envelope.TaskID != operation.TaskID ||
				envelope.ChannelID != operation.ChannelID {
				return failClaimedJimengOperation(operation, now,
					permanentJimengRecoveryError(errors.New("Jimeng recovery journal identity mismatch")))
			}
			if envelope.Status == model.TaskStatusFailure {
				if err := refundJimengTaskReservationWithFence(
					&task, reservation, "provider rejected submission before recovery",
					[]string{model.JimengTaskOperationDispatching}, operation.LeaseOwner,
				); err != nil {
					return failClaimedJimengOperation(operation, now, err)
				}
				if removeErr := removeJimengRecovery(task.TaskID); removeErr != nil {
					logging.SysError("Jimeng recovery journal cleanup remains pending for " + task.TaskID)
				}
				return nil
			}
			if envelope.Status != model.TaskStatusSubmitted ||
				strings.TrimSpace(envelope.UpstreamTaskID) == "" {
				return failClaimedJimengOperation(operation, now,
					permanentJimengRecoveryError(errors.New("Jimeng recovery journal outcome is invalid")))
			}
			privateData.UpstreamTaskID = envelope.UpstreamTaskID
			privateData.SettlementPending = false
			privateJSON, err := marshalJimengTaskPrivateData(privateData)
			if err != nil {
				return failClaimedJimengOperation(operation, now, permanentJimengRecoveryError(err))
			}
			if err := settleJimengAcceptedTask(
				&task, reservation, privateJSON, []byte(task.Data), model.TaskStatusSubmitted,
				task.FailReason, model.JimengTaskOperationDispatching, operation.LeaseOwner,
			); err != nil {
				return failClaimedJimengOperation(operation, now, err)
			}
			operation.State = model.JimengTaskOperationSubmitted
			operation.SettlementPending = false
			if removeErr := removeJimengRecovery(task.TaskID); removeErr != nil {
				logging.SysError("Jimeng recovery journal cleanup remains pending for " + task.TaskID)
			}
			return pollClaimedJimengTask(ctx, operation, &task, privateData, now)
		}
		if !jimengRecoveryNotFound(journalErr) {
			return failClaimedJimengOperation(operation, now,
				permanentJimengRecoveryError(errors.New("Jimeng recovery journal cannot be decoded")))
		}
		// A dispatch marker alone cannot distinguish a provider acceptance from
		// a definitive rejection whose post-response database write failed. Nor
		// can this node know whether another node has an encrypted accepted-id
		// emergency journal. Never turn that absence of information into a charge:
		// retain both holds and expose a bounded manual-review state. A source node
		// can still promote its journal before the generic 2-hour review horizon.
		return holdClaimedJimengUnknownForReview(operation, &task, now)
	case model.JimengTaskOperationSubmitted:
		if strings.TrimSpace(privateData.UpstreamTaskID) == "" {
			return failClaimedJimengOperation(operation, now,
				permanentJimengRecoveryError(errors.New("submitted Jimeng task has no provider id")))
		}
		if operation.SettlementPending {
			privateData.SettlementPending = false
			privateJSON, err := marshalJimengTaskPrivateData(privateData)
			if err != nil {
				return failClaimedJimengOperation(operation, now, err)
			}
			if err := settleJimengAcceptedTask(
				&task, reservation, privateJSON, []byte(task.Data), model.TaskStatusSubmitted,
				task.FailReason, model.JimengTaskOperationSubmitted, operation.LeaseOwner,
			); err != nil {
				return failClaimedJimengOperation(operation, now, err)
			}
			// The settlement transaction preserved this worker's lease.
			operation.SettlementPending = false
		}
		return pollClaimedJimengTask(ctx, operation, &task, privateData, now)
	default:
		return releaseClaimedJimengOperation(operation, now, nil)
	}
}

func holdClaimedJimengUnknownForReview(
	operation *model.JimengTaskOperation,
	task *model.Task,
	now int64,
) error {
	if operation == nil || task == nil || operation.LeaseOwner == "" ||
		operation.State != model.JimengTaskOperationDispatching {
		return errors.New("invalid Jimeng unknown-review transition")
	}
	transitionNow, err := jimengOperationDatabaseNowAtLeast(now)
	if err != nil {
		return failClaimedJimengOperation(operation, now, err)
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		taskUpdate := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND status NOT IN ?", task.ID, task.TaskID,
				[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).
			Updates(map[string]any{
				"status": model.TaskStatusUnknown, "progress": "100%",
				"finish_time": transitionNow, "updated_at": transitionNow,
				"fail_reason": jimengManualReviewReason,
			})
		if taskUpdate.Error != nil {
			return taskUpdate.Error
		}
		if taskUpdate.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		opUpdate := tx.Model(&model.JimengTaskOperation{}).
			Where("id = ? AND task_id = ? AND state = ? AND lease_owner = ?",
				operation.ID, task.TaskID, model.JimengTaskOperationDispatching, operation.LeaseOwner).
			Updates(map[string]any{
				"state": model.JimengTaskOperationManualReview, "settlement_pending": false,
				"next_attempt_at": 0, "completed_at": transitionNow,
				"updated_at": transitionNow, "last_error": "provider_outcome_unresolved",
				"lease_owner": "", "lease_expires_at": 0,
			})
		if opUpdate.Error != nil {
			return opUpdate.Error
		}
		if opUpdate.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err != nil {
		return failClaimedJimengOperation(operation, transitionNow, err)
	}
	task.Status = model.TaskStatusUnknown
	task.Progress = "100%"
	task.FinishTime = transitionNow
	task.UpdatedAt = transitionNow
	task.FailReason = jimengManualReviewReason
	return nil
}

// settleAndTerminalizeClaimedJimengTask repairs rows produced by older nodes
// that wrote a terminal Task without closing its recovery operation. It never
// calls the provider, never changes the terminal Task, and atomically finishes
// any still-pending accounting before closing the operation.
func settleAndTerminalizeClaimedJimengTask(
	operation *model.JimengTaskOperation,
	task *model.Task,
	reservation *billingsvc.RelayQuotaReservation,
	now int64,
) error {
	transitionNow := now
	err := reservation.SettleWithChannelAndPersistence(task.Quota, task.ChannelId, func(tx *gorm.DB) error {
		var clockErr error
		transitionNow, clockErr = model.DatabaseUnixTimestamp(tx)
		if clockErr != nil {
			return clockErr
		}
		var current model.Task
		if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&current).Error; err != nil {
			return err
		}
		if !isJimengTerminalTaskStatus(current.Status) {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		currentPrivate, privateErr := decodeJimengTaskPrivateDataStored(current.PrivateData)
		if privateErr != nil {
			return privateErr
		}
		stripJimengTerminalRecoverySecrets(&currentPrivate)
		sanitizedPrivate, privateErr := marshalJimengTaskPrivateData(currentPrivate)
		if privateErr != nil {
			return privateErr
		}
		if current.PrivateData != sanitizedPrivate {
			taskMetadata := tx.Model(&model.Task{}).
				Where("id = ? AND task_id = ? AND status = ? AND private_data = ?",
					current.ID, current.TaskID, current.Status, current.PrivateData).
				Updates(map[string]any{"private_data": sanitizedPrivate, "updated_at": transitionNow})
			if taskMetadata.Error != nil {
				return taskMetadata.Error
			}
			if taskMetadata.RowsAffected != 1 {
				return billingsvc.ErrRelayQuotaReservationBusy
			}
			current.PrivateData = sanitizedPrivate
			current.UpdatedAt = transitionNow
		}
		result := tx.Model(&model.JimengTaskOperation{}).
			Where("id = ? AND lease_owner = ? AND state = ?", operation.ID, operation.LeaseOwner,
				operation.State).
			Updates(map[string]any{
				"state": model.JimengTaskOperationTerminal, "settlement_pending": false,
				"encrypted_provider_task_id": "", "completed_at": transitionNow,
				"next_attempt_at": 0, "updated_at": transitionNow, "last_error": "",
				"lease_owner": "", "lease_expires_at": 0,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		eventID := jimengAuditEventID(reservation.ReservationID())
		var existingAudit model.AuditLogOutbox
		auditLookup := tx.Select("id", "payload").Where("event_id = ?", eventID).
			Limit(1).Find(&existingAudit)
		if auditLookup.Error != nil {
			return auditLookup.Error
		}
		if auditLookup.RowsAffected == 1 {
			if strings.TrimSpace(existingAudit.Payload) == "" {
				return errors.New("existing Jimeng consume audit payload is empty")
			}
			// The initial accounting commit owns the immutable consume event. A
			// later terminal fetch legitimately replaces Task.Data with a different
			// request fingerprint; rebuilding the same event ID from that terminal
			// payload would create a false idempotency conflict.
			return nil
		}
		auditEntry, auditErr := newJimengConsumeAudit(
			current, current.PrivateData, []byte(current.Data), model.TaskStatusSubmitted,
			reservation.ReservationID(),
		)
		if auditErr != nil {
			return auditErr
		}
		return billingsvc.EnqueueAuditLogTx(tx, auditEntry)
	})
	if err != nil {
		return failClaimedJimengOperation(operation, transitionNow, err)
	}
	return nil
}

func pollClaimedJimengTask(
	parentContext context.Context,
	operation *model.JimengTaskOperation,
	task *model.Task,
	privateData jimengTaskPrivateData,
	now int64,
) error {
	if parentContext == nil {
		parentContext = context.Background()
	}
	if deadline, hasDeadline := parentContext.Deadline(); hasDeadline &&
		time.Until(deadline) < 31*time.Second {
		return releaseClaimedJimengOperationWithoutAttempt(operation, now)
	}
	maxPollDays := env.GetEnvInt("JIMENG_MAX_POLL_DAYS", jimengDefaultMaxPollDays)
	if maxPollDays < 1 || maxPollDays > 90 {
		maxPollDays = jimengDefaultMaxPollDays
	}
	if operation.CreatedAt <= 0 {
		return failClaimedJimengOperation(operation, now,
			permanentJimengRecoveryError(errors.New("Jimeng operation has no database creation timestamp")))
	}
	if now-operation.CreatedAt >= int64(maxPollDays*24*60*60) {
		return holdClaimedJimengPollingForReview(operation, task, now)
	}
	properties, _, err := decodeJimengTaskMetadataChecked(*task)
	if err != nil {
		if isJimengEncryptionKeyUnavailable(err) {
			return releaseClaimedJimengOperationWithoutAttempt(operation, now)
		}
		return failClaimedJimengOperation(operation, now, permanentJimengRecoveryError(err))
	}
	baseURL, channelKey, err := resolveJimengTaskChannel(*task, privateData)
	if err != nil {
		if isJimengEncryptionKeyUnavailable(err) {
			return releaseClaimedJimengOperationWithoutAttempt(operation, now)
		}
		return failClaimedJimengOperation(operation, now, permanentJimengRecoveryError(err))
	}
	mappedModel := properties.UpstreamModelName
	if mappedModel == "" {
		mappedModel = properties.OriginModelName
	}
	fetchStartedAt, err := renewJimengTaskOperationLease(operation, now)
	if err != nil {
		return failClaimedJimengOperation(operation, now, err)
	}
	ctx, cancel := context.WithTimeout(parentContext, 30*time.Second)
	defer cancel()
	result, raw, err := (&jimeng.Client{}).Fetch(
		ctx, baseURL, channelKey, mappedModel, privateData.UpstreamTaskID,
	)
	result, raw, err = normalizeJimengFetchOutcome(result, raw, err)
	if err != nil {
		if parentContext.Err() != nil {
			// Loss of the scheduler's fenced outer lease (or the bounded pass)
			// is not a provider failure. Release this exact row claim and undo its
			// artificial attempt so another healthy owner can resume immediately.
			// A child-only 30-second provider timeout still consumes the budget.
			return releaseClaimedJimengOperationWithoutAttempt(operation, fetchStartedAt)
		}
		return failClaimedJimengOperation(operation, fetchStartedAt, err)
	}
	previousStatus := task.Status
	if err := applyJimengTaskResult(task, result, raw, &privateData); err != nil {
		return failClaimedJimengOperation(operation, fetchStartedAt, err)
	}
	terminal := task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure
	transitionNow, err := jimengOperationDatabaseNowAtLeast(fetchStartedAt)
	if err != nil {
		return failClaimedJimengOperation(operation, fetchStartedAt, err)
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND status = ? AND status NOT IN ?", task.ID, previousStatus,
				[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).
			Updates(map[string]any{
				"status": task.Status, "progress": task.Progress, "fail_reason": task.FailReason,
				"start_time": task.StartTime, "finish_time": task.FinishTime,
				"private_data": task.PrivateData, "data": task.Data, "updated_at": task.UpdatedAt,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			var current model.Task
			if err := tx.First(&current, task.ID).Error; err != nil ||
				!jimengTaskResultMatches(current, *task) {
				return billingsvc.ErrRelayQuotaReservationBusy
			}
		}
		operationUpdate := map[string]any{
			"lease_owner": "", "lease_expires_at": 0, "updated_at": transitionNow,
			"last_error": "", "attempts": 0, "settlement_pending": false,
		}
		if terminal {
			operationUpdate["state"] = model.JimengTaskOperationTerminal
			operationUpdate["encrypted_provider_task_id"] = ""
			operationUpdate["completed_at"] = transitionNow
			operationUpdate["next_attempt_at"] = 0
		} else {
			operationUpdate["state"] = model.JimengTaskOperationSubmitted
			operationUpdate["next_attempt_at"] = transitionNow + jimengOperationRetrySeconds
		}
		opResult := tx.Model(&model.JimengTaskOperation{}).
			Where("id = ? AND lease_owner = ? AND state = ?", operation.ID, operation.LeaseOwner,
				model.JimengTaskOperationSubmitted).
			Updates(operationUpdate)
		if opResult.Error != nil {
			return opResult.Error
		}
		if opResult.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err != nil {
		return failClaimedJimengOperation(operation, transitionNow, err)
	}
	return nil
}

func releaseClaimedJimengOperationWithoutAttempt(operation *model.JimengTaskOperation, now int64) error {
	if operation == nil || operation.LeaseOwner == "" {
		return errors.New("invalid Jimeng budget release")
	}
	releaseNow, err := jimengOperationDatabaseNowAtLeast(now)
	if err != nil {
		return err
	}
	result := model.DB.Model(&model.JimengTaskOperation{}).
		Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, operation.State, operation.LeaseOwner).
		Updates(map[string]any{
			"lease_owner": "", "lease_expires_at": 0, "next_attempt_at": releaseNow,
			"updated_at": releaseNow,
			"attempts":   gorm.Expr("CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END"),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return billingsvc.ErrRelayQuotaReservationBusy
	}
	return nil
}

func holdClaimedJimengPollingForReview(
	operation *model.JimengTaskOperation,
	task *model.Task,
	now int64,
) error {
	if operation == nil || task == nil || operation.LeaseOwner == "" ||
		operation.State != model.JimengTaskOperationSubmitted {
		return errors.New("invalid Jimeng polling-review transition")
	}
	transitionNow, err := jimengOperationDatabaseNowAtLeast(now)
	if err != nil {
		return failClaimedJimengOperation(operation, now, err)
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		taskUpdate := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND status NOT IN ?", task.ID, task.TaskID,
				[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).
			Updates(map[string]any{
				"status": model.TaskStatusUnknown, "progress": "100%",
				"finish_time": transitionNow, "updated_at": transitionNow,
				"fail_reason": jimengPollingReviewReason,
			})
		if taskUpdate.Error != nil {
			return taskUpdate.Error
		}
		if taskUpdate.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		opUpdate := tx.Model(&model.JimengTaskOperation{}).
			Where("id = ? AND task_id = ? AND state = ? AND lease_owner = ?",
				operation.ID, task.TaskID, model.JimengTaskOperationSubmitted, operation.LeaseOwner).
			Updates(map[string]any{
				"state": model.JimengTaskOperationManualReview, "settlement_pending": false,
				"next_attempt_at": 0, "completed_at": transitionNow,
				"updated_at": transitionNow, "last_error": "poll_horizon_exceeded",
				"lease_owner": "", "lease_expires_at": 0,
			})
		if opUpdate.Error != nil {
			return opUpdate.Error
		}
		if opUpdate.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err != nil {
		return failClaimedJimengOperation(operation, transitionNow, err)
	}
	task.Status = model.TaskStatusUnknown
	task.Progress = "100%"
	task.FinishTime = transitionNow
	task.UpdatedAt = transitionNow
	task.FailReason = jimengPollingReviewReason
	return nil
}

func failClaimedJimengOperation(operation *model.JimengTaskOperation, now int64, cause error) error {
	if operation == nil || cause == nil {
		return cause
	}
	databaseNow, clockErr := jimengOperationDatabaseNowAtLeast(now)
	if clockErr != nil {
		return errors.Join(cause, clockErr)
	}
	now = databaseNow
	// Never persist or log the upstream error text: it is provider-controlled
	// and can contain credentials or opaque task identifiers.
	safeError := fmt.Sprintf("%T", cause)
	safeCause := errors.New("Jimeng recovery operation failed: " + safeError)
	manualReview := isPermanentJimengRecoveryError(cause) || operation.Attempts >= jimengOperationMaxAttempts
	updates := map[string]any{
		"lease_owner": "", "lease_expires_at": 0,
		"next_attempt_at": now + jimengOperationBackoff(operation.Attempts),
		"updated_at":      now, "last_error": safeError,
	}
	if manualReview {
		updates["state"] = model.JimengTaskOperationManualReview
		updates["next_attempt_at"] = 0
		updates["completed_at"] = now
	}
	result := model.DB.Model(&model.JimengTaskOperation{}).
		Where("id = ? AND lease_owner = ? AND state = ?", operation.ID, operation.LeaseOwner,
			operation.State).
		Updates(updates)
	if result.Error != nil {
		return errors.Join(safeCause, result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.Join(safeCause, billingsvc.ErrRelayQuotaReservationBusy)
	}
	if manualReview {
		logging.SysError("Jimeng recovery requires manual review for " + operation.TaskID + " (" + safeError + ")")
	} else {
		logging.SysError("Jimeng recovery failed for " + operation.TaskID + " (" + safeError + ")")
	}
	return safeCause
}

func releaseClaimedJimengOperation(operation *model.JimengTaskOperation, now int64, cause error) error {
	if operation == nil {
		return cause
	}
	databaseNow, clockErr := jimengOperationDatabaseNowAtLeast(now)
	if clockErr != nil {
		return errors.Join(cause, clockErr)
	}
	now = databaseNow
	result := model.DB.Model(&model.JimengTaskOperation{}).
		Where("id = ? AND lease_owner = ? AND state = ?", operation.ID, operation.LeaseOwner,
			operation.State).
		Updates(map[string]any{
			"lease_owner": "", "lease_expires_at": 0,
			"next_attempt_at": now + jimengOperationRetrySeconds, "updated_at": now,
		})
	if result.Error != nil {
		return errors.Join(cause, result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.Join(cause, billingsvc.ErrRelayQuotaReservationBusy)
	}
	return cause
}
