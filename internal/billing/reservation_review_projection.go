package billing

import (
	"crypto/sha256"
	"encoding/hex"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
)

func relayQuotaReservationReviewItem(
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	taskOperation *model.TaskOperation,
) RelayQuotaReservationReviewItem {
	item := RelayQuotaReservationReviewItem{
		ReviewKind:    RelayQuotaReviewKindReservationAccounting,
		ReservationID: record.ReservationID, UserID: record.UserID, TokenID: record.TokenID,
		TokenUnlimited: record.TokenUnlimited, TrustQuotaBypassed: record.TrustQuotaBypassed,
		ChannelID:     record.ChannelID,
		FundingSource: record.FundingSource, RequestedQuota: record.RequestedQuota,
		ReservedQuota: record.ReservedQuota, TokenReserved: record.TokenReserved,
		SubscriptionID: record.SubscriptionID, Status: record.Status, Operation: record.Operation,
		ActualQuota: record.ActualQuota, Attempts: record.Attempts, NextAttemptAt: record.NextAttemptAt,
		ExpiresAt: record.ExpiresAt, DispatchedAt: record.DispatchedAt,
		LeaseExpiresAt: record.LeaseExpiresAt, CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt, CompletedAt: record.CompletedAt,
	}
	if record.ViolationFeeStatus != "" {
		item.ReviewKind = RelayQuotaReviewKindGrokViolationFee
		item.ViolationFeeStatus = record.ViolationFeeStatus
		item.ViolationFeeCode = record.ViolationFeeCode
		item.ViolationFeeFailure = record.ViolationFeeFailureCode
		item.ViolationFeeQuota = record.ViolationFeeQuota
		item.ViolationFeeChannelID = record.ViolationFeeChannelID
		item.ViolationFeeAttempts = record.ViolationFeeAttempts
		item.ViolationFeeUpdatedAt = record.ViolationFeeUpdatedAt
		switch record.ViolationFeeStatus {
		case model.RelayQuotaViolationFeeStatusPending:
			item.DiagnosticCode = "grok_violation_fee_pending"
			item.Retryable = true
			item.RetryTargetStatus = model.RelayQuotaViolationFeeStatusCharged
		case model.RelayQuotaViolationFeeStatusManualReview:
			item.DiagnosticCode = record.ViolationFeeFailureCode
			item.Retryable = true
			item.RetryTargetStatus = model.RelayQuotaViolationFeeStatusCharged
		case model.RelayQuotaViolationFeeStatusCharged:
			item.DiagnosticCode = "grok_violation_fee_charged"
		}
	} else if record.Status == model.RelayQuotaReservationStatusManualReview {
		target, err := manualReviewRetryTarget(record)
		item.Retryable = err == nil
		if err == nil {
			item.RetryTargetStatus = target
		}
		item.DiagnosticCode = relayQuotaReviewDiagnosticCode(record.LastError, err)
		if record.LastError != "" {
			digest := sha256.Sum256([]byte(record.LastError))
			item.DiagnosticFingerprint = hex.EncodeToString(digest[:])[:relayQuotaReviewFingerprintN]
		}
	} else if taskOperation != nil {
		item.TaskID = taskOperation.TaskID
		item.TaskPlatform = taskOperation.Platform
		item.TaskOperationState = taskOperation.State
		item.TaskOperationAttempts = taskOperation.Attempts
		if task != nil {
			item.TaskStatus = task.Status
		}
		switch {
		case model.IsOpenAIVideoTaskOperationPlatform(taskOperation.Platform):
			item.ReviewKind = RelayQuotaReviewKindOpenAIVideoPoll
			item.ProviderTaskIDPresent = relayQuotaVideoProviderTaskIDPresent(task, taskOperation)
			shapeErr := relayQuotaVideoReviewShape(record, task, taskOperation)
			item.Retryable = shapeErr == nil && item.ProviderTaskIDPresent
			if item.Retryable {
				item.RetryTargetStatus = model.TaskOperationSubmitted
				item.DiagnosticCode = "video_polling_manual_review"
			} else if shapeErr != nil {
				item.DiagnosticCode = "invalid_video_review_record"
			} else {
				item.DiagnosticCode = "video_provider_id_unavailable"
			}
		case model.IsKlingTaskOperationPlatform(taskOperation.Platform):
			item.ReviewKind = RelayQuotaReviewKindKlingTask
			item.ProviderTaskIDPresent = relayQuotaAsyncProviderTaskIDPresent(record, task, taskOperation)
			shapeErr := relayQuotaAsyncReviewShape(record, task, taskOperation)
			if shapeErr != nil {
				item.DiagnosticCode = "invalid_kling_review_record"
			} else if item.ProviderTaskIDPresent {
				item.DiagnosticCode = "kling_polling_manual_review"
				item.Retryable = true
				item.RetryTargetStatus = model.TaskOperationSubmitted
			} else {
				item.DiagnosticCode = "kling_provider_outcome_unresolved"
				item.ResolutionRequired = true
				item.ResolutionOptions = []string{
					RelayQuotaReviewResolutionSettle,
					RelayQuotaReviewResolutionRefund,
				}
			}
		case model.IsSunoTaskOperationPlatform(taskOperation.Platform):
			item.ReviewKind = RelayQuotaReviewKindSunoTask
			item.ProviderTaskIDPresent = relayQuotaAsyncProviderTaskIDPresent(record, task, taskOperation)
			shapeErr := relayQuotaAsyncReviewShape(record, task, taskOperation)
			if shapeErr != nil {
				item.DiagnosticCode = "invalid_suno_review_record"
			} else if item.ProviderTaskIDPresent {
				item.DiagnosticCode = "suno_polling_manual_review"
				item.Retryable = true
				item.RetryTargetStatus = model.TaskOperationSubmitted
			} else {
				item.DiagnosticCode = "suno_provider_id_unavailable"
			}
		case model.IsViduTaskOperationPlatform(taskOperation.Platform):
			item.ReviewKind = RelayQuotaReviewKindViduTask
			item.ProviderTaskIDPresent = relayQuotaAsyncProviderTaskIDPresent(record, task, taskOperation)
			shapeErr := relayQuotaAsyncReviewShape(record, task, taskOperation)
			if shapeErr != nil {
				item.DiagnosticCode = "invalid_vidu_review_record"
			} else if item.ProviderTaskIDPresent {
				item.DiagnosticCode = "vidu_polling_manual_review"
				item.Retryable = !taskOperation.SettlementPending
				if item.Retryable {
					item.RetryTargetStatus = model.TaskOperationSubmitted
				}
			} else {
				item.DiagnosticCode = "vidu_provider_outcome_unresolved"
				item.ResolutionRequired = true
				item.ResolutionOptions = []string{
					RelayQuotaReviewResolutionSettle,
					RelayQuotaReviewResolutionRefund,
				}
			}
		case model.IsHailuoTaskOperationPlatform(taskOperation.Platform):
			item.ReviewKind = RelayQuotaReviewKindHailuoTask
			item.ProviderTaskIDPresent = relayQuotaAsyncProviderTaskIDPresent(record, task, taskOperation)
			shapeErr := relayQuotaAsyncReviewShape(record, task, taskOperation)
			if shapeErr != nil {
				item.DiagnosticCode = "invalid_hailuo_review_record"
			} else if item.ProviderTaskIDPresent {
				item.DiagnosticCode = "hailuo_polling_manual_review"
				item.Retryable = !taskOperation.SettlementPending
				if item.Retryable {
					item.RetryTargetStatus = model.TaskOperationSubmitted
				}
			} else {
				item.DiagnosticCode = "hailuo_provider_outcome_unresolved"
				item.ResolutionRequired = true
				item.ResolutionOptions = []string{
					RelayQuotaReviewResolutionSettle,
					RelayQuotaReviewResolutionRefund,
				}
			}
		case model.IsAliWanTaskOperationPlatform(taskOperation.Platform):
			item.ReviewKind = RelayQuotaReviewKindAliWanTask
			item.ProviderTaskIDPresent = relayQuotaAsyncProviderTaskIDPresent(record, task, taskOperation)
			shapeErr := relayQuotaAsyncReviewShape(record, task, taskOperation)
			if shapeErr != nil {
				item.DiagnosticCode = "invalid_ali_wan_review_record"
			} else if item.ProviderTaskIDPresent {
				item.DiagnosticCode = "ali_wan_polling_manual_review"
				item.Retryable = !taskOperation.SettlementPending
				if item.Retryable {
					item.RetryTargetStatus = model.TaskOperationSubmitted
				}
			} else {
				item.DiagnosticCode = "ali_wan_provider_outcome_unresolved"
				item.ResolutionRequired = true
				item.ResolutionOptions = []string{
					RelayQuotaReviewResolutionSettle,
					RelayQuotaReviewResolutionRefund,
				}
			}
		case model.IsVeoTaskOperationPlatform(taskOperation.Platform):
			item.ReviewKind = RelayQuotaReviewKindGeminiVeoTask
			item.ProviderTaskIDPresent = relayQuotaAsyncProviderTaskIDPresent(record, task, taskOperation)
			shapeErr := relayQuotaAsyncReviewShape(record, task, taskOperation)
			if shapeErr != nil {
				item.DiagnosticCode = "invalid_gemini_veo_review_record"
			} else if item.ProviderTaskIDPresent {
				item.DiagnosticCode = "gemini_veo_polling_manual_review"
				item.Retryable = !taskOperation.SettlementPending
				if item.Retryable {
					item.RetryTargetStatus = model.TaskOperationSubmitted
				}
			} else {
				item.DiagnosticCode = "gemini_veo_provider_outcome_unresolved"
				item.ResolutionRequired = true
				item.ResolutionOptions = []string{
					RelayQuotaReviewResolutionSettle,
					RelayQuotaReviewResolutionRefund,
				}
			}
		case model.IsDoubaoVideoTaskOperationPlatform(taskOperation.Platform):
			item.ReviewKind = RelayQuotaReviewKindDoubaoVideoTask
			item.ProviderTaskIDPresent = relayQuotaAsyncProviderTaskIDPresent(record, task, taskOperation)
			shapeErr := relayQuotaAsyncReviewShape(record, task, taskOperation)
			if shapeErr != nil {
				item.DiagnosticCode = "invalid_doubao_video_review_record"
			} else if item.ProviderTaskIDPresent {
				item.DiagnosticCode = "doubao_video_polling_manual_review"
				item.Retryable = true
				item.RetryTargetStatus = model.TaskOperationSubmitted
			} else {
				item.DiagnosticCode = "doubao_video_provider_outcome_unresolved"
				item.ResolutionRequired = true
				item.ResolutionOptions = []string{
					RelayQuotaReviewResolutionSettle,
					RelayQuotaReviewResolutionRefund,
				}
			}
		case model.IsMidjourneyTaskOperationPlatform(taskOperation.Platform):
			item.ReviewKind = RelayQuotaReviewKindMidjourneyTask
			item.ProviderTaskIDPresent = relayQuotaAsyncProviderTaskIDPresent(record, task, taskOperation)
			shapeErr := relayQuotaAsyncReviewShape(record, task, taskOperation)
			if shapeErr != nil {
				item.DiagnosticCode = "invalid_midjourney_review_record"
			} else if item.ProviderTaskIDPresent {
				item.DiagnosticCode = "midjourney_polling_manual_review"
				item.Retryable = true
				item.RetryTargetStatus = model.TaskOperationSubmitted
			} else {
				item.DiagnosticCode = "midjourney_provider_outcome_unresolved"
			}
		default:
			item.DiagnosticCode = "unsupported_async_task_review"
		}
		if taskOperation.LastError != "" {
			digest := sha256.Sum256([]byte(taskOperation.LastError))
			item.DiagnosticFingerprint = hex.EncodeToString(digest[:])[:relayQuotaReviewFingerprintN]
		}
	} else if record.Status == model.RelayQuotaReservationStatusDispatched {
		item.ReviewKind = RelayQuotaReviewKindJimengProviderOutcome
		item.DiagnosticCode = "jimeng_provider_outcome_unresolved"
		item.ResolutionRequired = true
		item.ResolutionOptions = []string{
			RelayQuotaReviewResolutionSettle,
			RelayQuotaReviewResolutionRefund,
		}
	}
	return item
}

func relayQuotaManualReviewScope(tx *gorm.DB) *gorm.DB {
	return tx.Model(&model.RelayQuotaReservationRecord{}).
		Where(`violation_fee_status IN ? OR status = ? OR (
			status = ? AND operation = ? AND
			EXISTS (
				SELECT 1 FROM jimeng_task_operations AS jimeng_review_operations
				WHERE jimeng_review_operations.reservation_id = relay_quota_reservations.reservation_id
					AND jimeng_review_operations.state = ?
					AND jimeng_review_operations.settlement_pending = ?
					AND jimeng_review_operations.encrypted_provider_task_id = ?
					AND jimeng_review_operations.last_error = ?
					AND jimeng_review_operations.lease_owner = ?
					AND jimeng_review_operations.lease_expires_at = ?
			)
		) OR EXISTS (
				SELECT 1 FROM task_operations AS async_review_operations
				WHERE async_review_operations.reservation_id = relay_quota_reservations.reservation_id
					AND async_review_operations.platform IN ?
					AND async_review_operations.state = ?
		)`,
			[]string{model.RelayQuotaViolationFeeStatusPending, model.RelayQuotaViolationFeeStatusManualReview},
			model.RelayQuotaReservationStatusManualReview,
			model.RelayQuotaReservationStatusDispatched,
			model.RelayQuotaReservationOperationSettle,
			model.JimengTaskOperationManualReview,
			false,
			"",
			relayQuotaReviewJimengReason,
			"",
			int64(0),
			asyncTaskOperationReviewPlatforms(),
			model.TaskOperationManualReview,
		)
}

func relayQuotaVideoReviewContextsTx(
	tx *gorm.DB,
	records []model.RelayQuotaReservationRecord,
) (map[string]*model.TaskOperation, map[string]*model.Task, error) {
	operationsByReservation := make(map[string]*model.TaskOperation)
	tasksByID := make(map[string]*model.Task)
	if tx == nil || len(records) == 0 {
		return operationsByReservation, tasksByID, nil
	}
	reservationIDs := make([]string, 0, len(records))
	for index := range records {
		reservationIDs = append(reservationIDs, records[index].ReservationID)
	}
	var operations []model.TaskOperation
	if err := tx.Where("reservation_id IN ? AND platform IN ? AND state = ?", reservationIDs,
		asyncTaskOperationReviewPlatforms(), model.TaskOperationManualReview).
		Find(&operations).Error; err != nil {
		return nil, nil, err
	}
	taskIDs := make([]string, 0, len(operations))
	for index := range operations {
		operation := operations[index]
		operationsByReservation[operation.ReservationID] = &operation
		taskIDs = append(taskIDs, operation.TaskID)
	}
	if len(taskIDs) == 0 {
		return operationsByReservation, tasksByID, nil
	}
	var tasks []model.Task
	if err := tx.Where("task_id IN ? AND platform IN ?", taskIDs,
		asyncTaskOperationReviewPlatforms()).Find(&tasks).Error; err != nil {
		return nil, nil, err
	}
	for index := range tasks {
		task := tasks[index]
		key := relayQuotaVideoReviewTaskKey(task.TaskID, task.Platform)
		if _, exists := tasksByID[key]; !exists {
			tasksByID[key] = &task
		}
	}
	return operationsByReservation, tasksByID, nil
}

func asyncTaskOperationReviewPlatforms() []string {
	platforms := model.OpenAIVideoTaskOperationPlatforms()
	platforms = append(platforms, model.TaskOperationPlatformKling, model.TaskOperationPlatformSuno,
		model.TaskOperationPlatformVidu, model.TaskOperationPlatformHailuo, model.TaskOperationPlatformAliWan,
		model.TaskOperationPlatformGeminiVeo, model.TaskOperationPlatformVertexVeo,
		model.TaskOperationPlatformMidjourney)
	return append(platforms, model.DoubaoVideoTaskOperationPlatforms()...)
}

func relayQuotaReviewDiagnosticCode(lastError string, validationErr error) string {
	if validationErr != nil {
		return "invalid_record"
	}
	normalized := strings.ToLower(lastError)
	switch {
	case strings.Contains(normalized, "dispatched reservation expired"):
		return "upstream_usage_unverified"
	case strings.Contains(normalized, "attempt limit"):
		return "retry_limit_reached"
	case strings.Contains(normalized, "invalid durable reservation"):
		return "invalid_record"
	default:
		return relayQuotaReviewUnknownReason
	}
}
