package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

var (
	ErrRelayQuotaReviewQueryInvalid = errors.New("invalid relay quota review query")
	ErrRelayQuotaReviewNotFound     = errors.New("relay quota reservation review record not found")
	ErrRelayQuotaReviewInvalidState = errors.New("relay quota reservation is not awaiting manual review")
	ErrRelayQuotaReviewUnsafeRetry  = errors.New("relay quota reservation cannot be retried safely")
)

// Manual retry is a rare operator action. Serializing it in-process avoids
// SQLite read-to-write upgrade races; MySQL/PostgreSQL and concurrent nodes
// remain protected by the row lock, full-snapshot compare-and-swap, and the
// unique audit revision.
var relayQuotaReviewRetryMu sync.Mutex

const (
	maxRelayQuotaReviewPage       = 1_000_000
	maxRelayQuotaReviewPageSize   = 100
	defaultRelayQuotaReviewPage   = 1
	defaultRelayQuotaReviewSize   = 20
	relayQuotaReviewFingerprintN  = 16
	relayQuotaReviewUnknownReason = "reconciliation_failed"
	relayQuotaReviewJimengReason  = "provider_outcome_unresolved"
)

const (
	// RelayQuotaReviewResolutionSettle and Refund are the only terminal
	// dispositions an authenticated root operator may choose for a provider
	// outcome that cannot be reconstructed automatically. Callers never supply
	// an amount or channel: those are derived from the immutable reservation.
	RelayQuotaReviewResolutionSettle = model.RelayQuotaReservationOperationSettle
	RelayQuotaReviewResolutionRefund = model.RelayQuotaReservationOperationRefund

	RelayQuotaReviewActionResolveSettle = "resolve_settle"
	RelayQuotaReviewActionResolveRefund = "resolve_refund"

	RelayQuotaReviewKindReservationAccounting = "reservation_accounting"
	RelayQuotaReviewKindJimengProviderOutcome = "jimeng_provider_outcome"
	RelayQuotaReviewKindOpenAIVideoPoll       = "openai_video_poll"
	RelayQuotaReviewKindKlingTask             = "kling_task"
	RelayQuotaReviewKindSunoTask              = "suno_task"
	RelayQuotaReviewKindViduTask              = "vidu_task"
	RelayQuotaReviewKindHailuoTask            = "hailuo_task"
	RelayQuotaReviewKindAliWanTask            = "ali_wan_task"
	RelayQuotaReviewKindGeminiVeoTask         = "gemini_veo_task"
	RelayQuotaReviewKindDoubaoVideoTask       = "doubao_video_task"
	RelayQuotaReviewKindMidjourneyTask        = "midjourney_task"
	RelayQuotaReviewKindGrokViolationFee      = "grok_violation_fee"
)

// RelayQuotaReservationReviewFilter is the bounded, exact-match query surface
// for operator review. Status is intentionally not caller-controlled: this
// API enumerates generic manual_review rows plus Jimeng dispatched holds whose
// fenced recovery operation carries the unresolved-provider marker.
type RelayQuotaReservationReviewFilter struct {
	Page          int
	PageSize      int
	Operation     string
	UserID        int
	ReservationID string
}

// RelayQuotaReservationReviewItem is the secret-free operator view. Raw
// LastError and LeaseOwner values are deliberately excluded because database
// and provider errors may contain credentials or user-controlled data.
type RelayQuotaReservationReviewItem struct {
	ReviewKind            string `json:"review_kind"`
	ReservationID         string `json:"reservation_id"`
	UserID                int    `json:"user_id"`
	TokenID               int    `json:"token_id"`
	TokenUnlimited        bool   `json:"token_unlimited"`
	TrustQuotaBypassed    bool   `json:"trust_quota_bypassed"`
	ChannelID             int    `json:"channel_id"`
	FundingSource         string `json:"funding_source"`
	RequestedQuota        int    `json:"requested_quota"`
	ReservedQuota         int    `json:"reserved_quota"`
	TokenReserved         int    `json:"token_reserved"`
	SubscriptionID        int    `json:"subscription_id"`
	Status                string `json:"status"`
	Operation             string `json:"operation"`
	ActualQuota           int    `json:"actual_quota"`
	Attempts              int    `json:"attempts"`
	NextAttemptAt         int64  `json:"next_attempt_at"`
	ExpiresAt             int64  `json:"expires_at"`
	DispatchedAt          int64  `json:"dispatched_at"`
	LeaseExpiresAt        int64  `json:"lease_expires_at"`
	CreatedAt             int64  `json:"created_at"`
	UpdatedAt             int64  `json:"updated_at"`
	CompletedAt           int64  `json:"completed_at"`
	TaskID                string `json:"task_id,omitempty"`
	TaskPlatform          string `json:"task_platform,omitempty"`
	TaskStatus            string `json:"task_status,omitempty"`
	TaskOperationState    string `json:"task_operation_state,omitempty"`
	TaskOperationAttempts int    `json:"task_operation_attempts,omitempty"`
	ProviderTaskIDPresent bool   `json:"provider_task_id_present"`
	ViolationFeeStatus    string `json:"violation_fee_status,omitempty"`
	ViolationFeeCode      string `json:"violation_fee_code,omitempty"`
	ViolationFeeFailure   string `json:"violation_fee_failure_code,omitempty"`
	ViolationFeeQuota     int    `json:"violation_fee_quota,omitempty"`
	ViolationFeeChannelID int    `json:"violation_fee_channel_id,omitempty"`
	ViolationFeeAttempts  int    `json:"violation_fee_attempts,omitempty"`
	ViolationFeeUpdatedAt int64  `json:"violation_fee_updated_at,omitempty"`

	DiagnosticCode        string   `json:"diagnostic_code"`
	DiagnosticFingerprint string   `json:"diagnostic_fingerprint,omitempty"`
	Retryable             bool     `json:"retryable"`
	RetryTargetStatus     string   `json:"retry_target_status,omitempty"`
	ResolutionRequired    bool     `json:"resolution_required,omitempty"`
	ResolutionOptions     []string `json:"resolution_options,omitempty"`
}

// RelayQuotaReservationRetryResult reports an exact, auditable transition.
// Changed is false for a replay after a prior retry already moved (or fully
// reconciled) the same immutable accounting snapshot.
type RelayQuotaReservationRetryResult struct {
	Reservation  RelayQuotaReservationReviewItem `json:"reservation"`
	AuditEventID string                          `json:"audit_event_id"`
	Changed      bool                            `json:"changed"`
}

// ListManualReviewRelayQuotaReservations returns newest-first manual-review
// rows with exact filters and a hard page-size/page-number bound.
func ListManualReviewRelayQuotaReservations(filter RelayQuotaReservationReviewFilter) ([]RelayQuotaReservationReviewItem, int64, error) {
	filter = normalizeRelayQuotaReviewFilter(filter)
	if err := validateRelayQuotaReviewFilter(filter); err != nil {
		return nil, 0, err
	}
	if model.DB == nil {
		return nil, 0, errors.New("relay quota reservation database is unavailable")
	}

	records := make([]model.RelayQuotaReservationRecord, 0)
	items := make([]RelayQuotaReservationReviewItem, 0)
	var total int64
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		base := func() *gorm.DB {
			query := relayQuotaManualReviewScope(tx)
			if filter.Operation != "" {
				query = query.Where("operation = ?", filter.Operation)
			}
			if filter.UserID > 0 {
				query = query.Where("user_id = ?", filter.UserID)
			}
			if filter.ReservationID != "" {
				query = query.Where("reservation_id = ?", filter.ReservationID)
			}
			return query
		}
		if err := base().Count(&total).Error; err != nil {
			return err
		}
		offset := (filter.Page - 1) * filter.PageSize
		if err := base().Order("id DESC").Limit(filter.PageSize).Offset(offset).Find(&records).Error; err != nil {
			return err
		}
		operations, tasks, err := relayQuotaVideoReviewContextsTx(tx, records)
		if err != nil {
			return err
		}
		items = make([]RelayQuotaReservationReviewItem, 0, len(records))
		for index := range records {
			operation := operations[records[index].ReservationID]
			var task *model.Task
			if operation != nil {
				task = tasks[relayQuotaVideoReviewTaskKey(operation.TaskID, operation.Platform)]
			}
			items = append(items, relayQuotaReservationReviewItem(&records[index], task, operation))
		}
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list relay quota reservation reviews: %w", err)
	}

	return items, total, nil
}

// GetManualReviewRelayQuotaReservation returns one secret-free review item.
func GetManualReviewRelayQuotaReservation(reservationID string) (*RelayQuotaReservationReviewItem, error) {
	if !validRelayQuotaReviewReservationID(reservationID) {
		return nil, ErrRelayQuotaReviewQueryInvalid
	}
	if model.DB == nil {
		return nil, errors.New("relay quota reservation database is unavailable")
	}
	var item RelayQuotaReservationReviewItem
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		result := relayQuotaManualReviewScope(tx).
			Where("reservation_id = ?", reservationID).Limit(1).Find(&record)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRelayQuotaReviewNotFound
		}
		operations, tasks, err := relayQuotaVideoReviewContextsTx(tx, []model.RelayQuotaReservationRecord{record})
		if err != nil {
			return err
		}
		operation := operations[record.ReservationID]
		var task *model.Task
		if operation != nil {
			task = tasks[relayQuotaVideoReviewTaskKey(operation.TaskID, operation.Platform)]
		}
		item = relayQuotaReservationReviewItem(&record, task, operation)
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRelayQuotaReviewNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("get relay quota reservation review: %w", err)
	}
	return &item, nil
}

// RetryManualReviewRelayQuotaReservation atomically appends a primary-DB audit
// event and returns a structurally valid manual-review row to the pending state
// implied by its existing operation. It never accepts replacement accounting
// fields and the compare-and-swap predicate covers the complete immutable
// economic snapshot.
func RetryManualReviewRelayQuotaReservation(reservationID string, operatorUserID int) (*RelayQuotaReservationRetryResult, error) {
	if !validRelayQuotaReviewReservationID(reservationID) || operatorUserID <= 0 {
		return nil, ErrRelayQuotaReviewQueryInvalid
	}
	if model.DB == nil {
		return nil, errors.New("relay quota reservation database is unavailable")
	}
	relayQuotaReviewRetryMu.Lock()
	defer relayQuotaReviewRetryMu.Unlock()
	var current model.RelayQuotaReservationRecord
	lookup := model.DB.Where("reservation_id = ?", reservationID).Limit(1).Find(&current)
	if lookup.Error != nil {
		return nil, lookup.Error
	}
	if lookup.RowsAffected != 1 {
		return nil, ErrRelayQuotaReviewNotFound
	}
	if !emptyGrokViolationFeeIntent(&current) {
		return retryGrokViolationFeeReviewLocked(&current, operatorUserID)
	}

	eventID, err := common.SecureRandomUUID()
	if err != nil {
		return nil, err
	}
	var outcome *RelayQuotaReservationRetryResult
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := subscriptionLockForUpdate(tx).Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRelayQuotaReviewNotFound
			}
			return err
		}

		if record.Status != model.RelayQuotaReservationStatusManualReview {
			idempotent, err := relayQuotaReviewIdempotentResultTx(tx, &record)
			if err != nil {
				return err
			}
			if idempotent == nil {
				return fmt.Errorf("%w: current status is %s", ErrRelayQuotaReviewInvalidState, record.Status)
			}
			outcome = idempotent
			return nil
		}

		targetStatus, validationErr := manualReviewRetryTarget(&record)
		if validationErr != nil {
			return fmt.Errorf("%w: %v", ErrRelayQuotaReviewUnsafeRetry, validationErr)
		}

		var maxRevision int64
		if err := tx.Model(&model.RelayQuotaReservationReviewEvent{}).
			Where("reservation_id = ?", record.ReservationID).
			Select("COALESCE(MAX(revision), 0)").Scan(&maxRevision).Error; err != nil {
			return err
		}
		if maxRevision >= int64(math.MaxInt) {
			return errors.New("relay quota review revision overflow")
		}
		now := common.NowTimestamp()
		revision := int(maxRevision + 1)

		result := relayQuotaReviewSnapshotPredicate(tx, &record).
			Updates(map[string]any{
				"status":           targetStatus,
				"attempts":         0,
				"next_attempt_at":  now,
				"lease_owner":      "",
				"lease_expires_at": 0,
				"last_error":       "",
				"updated_at":       now,
				"completed_at":     0,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRelayQuotaReservationBusy
		}

		event := relayQuotaReviewEventFromRecord(eventID, revision, operatorUserID, targetStatus, now, &record)
		if err := tx.Create(&event).Error; err != nil {
			return err
		}

		record.Status = targetStatus
		record.Attempts = 0
		record.NextAttemptAt = now
		record.LeaseOwner = ""
		record.LeaseExpiresAt = 0
		record.LastError = ""
		record.UpdatedAt = now
		record.CompletedAt = 0
		item := relayQuotaReservationReviewItem(&record, nil, nil)
		outcome = &RelayQuotaReservationRetryResult{
			Reservation:  item,
			AuditEventID: event.EventID,
			Changed:      true,
		}
		return nil
	})
	if err == nil {
		return outcome, nil
	}

	// If a driver reports a post-COMMIT error, the unique event id proves that
	// this exact state change committed. If another concurrent identical retry
	// won, its matching event makes this request an idempotent replay.
	if verified, verifyErr := verifyRelayQuotaReviewRetry(reservationID, eventID); verifyErr == nil && verified != nil {
		return verified, nil
	}
	return nil, err
}

func normalizeRelayQuotaReviewFilter(filter RelayQuotaReservationReviewFilter) RelayQuotaReservationReviewFilter {
	if filter.Page == 0 {
		filter.Page = defaultRelayQuotaReviewPage
	}
	if filter.PageSize == 0 {
		filter.PageSize = defaultRelayQuotaReviewSize
	}
	return filter
}

func validateRelayQuotaReviewFilter(filter RelayQuotaReservationReviewFilter) error {
	if filter.Page < 1 || filter.Page > maxRelayQuotaReviewPage ||
		filter.PageSize < 1 || filter.PageSize > maxRelayQuotaReviewPageSize ||
		filter.UserID < 0 {
		return ErrRelayQuotaReviewQueryInvalid
	}
	if filter.Operation != "" && filter.Operation != model.RelayQuotaReservationOperationSettle &&
		filter.Operation != model.RelayQuotaReservationOperationRefund {
		return ErrRelayQuotaReviewQueryInvalid
	}
	if filter.ReservationID != "" && !validRelayQuotaReviewReservationID(filter.ReservationID) {
		return ErrRelayQuotaReviewQueryInvalid
	}
	return nil
}

func validRelayQuotaReviewReservationID(value string) bool {
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func manualReviewRetryTarget(record *model.RelayQuotaReservationRecord) (string, error) {
	if record == nil || record.Status != model.RelayQuotaReservationStatusManualReview {
		return "", ErrRelayQuotaReviewInvalidState
	}
	candidate := *record
	switch record.Operation {
	case model.RelayQuotaReservationOperationSettle:
		candidate.Status = model.RelayQuotaReservationStatusPendingSettlement
	case model.RelayQuotaReservationOperationRefund:
		if record.ChannelID != 0 {
			return "", errors.New("refund operation contains a settlement channel")
		}
		candidate.Status = model.RelayQuotaReservationStatusPendingRefund
	default:
		return "", errors.New("manual-review operation is unsupported")
	}
	if _, err := relayReservationFromRecord(&candidate); err != nil {
		return "", err
	}
	return candidate.Status, nil
}

func relayQuotaReviewSnapshotPredicate(tx *gorm.DB, record *model.RelayQuotaReservationRecord) *gorm.DB {
	return relayQuotaReviewSnapshotPredicateForStatus(
		tx, record, model.RelayQuotaReservationStatusManualReview,
	)
}

func relayQuotaReviewSnapshotPredicateForStatus(
	tx *gorm.DB,
	record *model.RelayQuotaReservationRecord,
	status string,
) *gorm.DB {
	return tx.Model(&model.RelayQuotaReservationRecord{}).
		Where("id = ? AND reservation_id = ? AND status = ? AND operation = ?", record.ID,
			record.ReservationID, status, record.Operation).
		Where("user_id = ? AND token_id = ? AND token_unlimited = ? AND trust_quota_bypassed = ? AND channel_id = ?",
			record.UserID, record.TokenID, record.TokenUnlimited, record.TrustQuotaBypassed, record.ChannelID).
		Where("funding_source = ? AND requested_quota = ? AND reserved_quota = ? AND token_reserved = ?",
			record.FundingSource, record.RequestedQuota, record.ReservedQuota, record.TokenReserved).
		Where("subscription_id = ? AND usage_epoch = ? AND actual_quota = ?",
			record.SubscriptionID, record.UsageEpoch, record.ActualQuota)
}

// AppendRelayQuotaReservationResolutionEventTx appends the immutable operator
// audit for a direct dispatched -> terminal resolution. It must be called from
// the same transaction as the Jimeng task, recovery operation, accounting
// counters, and reservation transition. The complete economic source snapshot
// is compared in the database before the append, and the operator cannot
// replace the amount or channel.
func AppendRelayQuotaReservationResolutionEventTx(
	tx *gorm.DB,
	source *model.RelayQuotaReservationRecord,
	operatorUserID int,
	resolution string,
	actualQuota int,
	providerChannelID int,
	eventID string,
) (*model.RelayQuotaReservationReviewEvent, error) {
	if tx == nil || source == nil || operatorUserID <= 0 || eventID == "" || len(eventID) > 64 {
		return nil, ErrRelayQuotaReviewQueryInvalid
	}
	action, targetStatus, expectedActual, err := relayQuotaReviewResolutionShape(source, resolution)
	if err != nil {
		return nil, err
	}
	if actualQuota != expectedActual {
		return nil, fmt.Errorf("%w: terminal quota does not match the immutable request", ErrRelayQuotaReviewUnsafeRetry)
	}
	if providerChannelID <= 0 {
		return nil, fmt.Errorf("%w: provider channel is missing", ErrRelayQuotaReviewUnsafeRetry)
	}

	var current model.RelayQuotaReservationRecord
	result := relayQuotaReviewSnapshotPredicateForStatus(
		subscriptionLockForUpdate(tx), source, model.RelayQuotaReservationStatusDispatched,
	).
		Where("dispatched_at = ? AND lease_owner = ? AND lease_expires_at = ?",
			source.DispatchedAt, "", int64(0)).
		Limit(1).Find(&current)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrRelayQuotaReservationBusy
	}

	// A resolution event without its terminal transition is impossible through
	// this API because both share a transaction. Refuse to build on such a row
	// rather than treating database corruption as an idempotent replay.
	var existingCount int64
	if err := tx.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ? AND action IN ?", source.ReservationID,
			[]string{RelayQuotaReviewActionResolveSettle, RelayQuotaReviewActionResolveRefund}).
		Count(&existingCount).Error; err != nil {
		return nil, err
	}
	if existingCount != 0 {
		return nil, fmt.Errorf("%w: a terminal resolution audit already exists", ErrRelayQuotaReviewUnsafeRetry)
	}

	var maxRevision int64
	if err := tx.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ?", source.ReservationID).
		Select("COALESCE(MAX(revision), 0)").Scan(&maxRevision).Error; err != nil {
		return nil, err
	}
	if maxRevision >= int64(math.MaxInt) {
		return nil, errors.New("relay quota review revision overflow")
	}
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return nil, err
	}
	event := model.RelayQuotaReservationReviewEvent{
		EventID: eventID, ReservationID: source.ReservationID, Revision: int(maxRevision + 1),
		OperatorUserID: operatorUserID, Action: action,
		FromStatus: model.RelayQuotaReservationStatusDispatched, ToStatus: targetStatus,
		Operation: resolution, UserID: source.UserID, TokenID: source.TokenID,
		TokenUnlimited: source.TokenUnlimited, TrustQuotaBypassed: source.TrustQuotaBypassed,
		ChannelID:     providerChannelID,
		FundingSource: source.FundingSource, RequestedQuota: source.RequestedQuota,
		ReservedQuota: source.ReservedQuota, TokenReserved: source.TokenReserved,
		SubscriptionID: source.SubscriptionID, UsageEpoch: source.UsageEpoch,
		ActualQuota: actualQuota, CreatedAt: now,
	}
	if err := tx.Create(&event).Error; err != nil {
		return nil, err
	}
	return &event, nil
}

// AppendRelayQuotaTaskPollRetryEventTx records an operator-authorized polling
// retry without changing accounting. A fixed-price task remains settled and a
// usage-metered task retains its dispatched hold. Callers must update the Task
// and TaskOperation in this same transaction. The event retains the previous
// polling horizon and attempt count because reopening a bounded recovery
// window intentionally resets both journal fields.
func AppendRelayQuotaTaskPollRetryEventTx(
	tx *gorm.DB,
	source *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
	operatorUserID int,
	eventID string,
	now int64,
) (*model.RelayQuotaReservationReviewEvent, error) {
	if tx == nil || source == nil || task == nil || operation == nil ||
		operatorUserID <= 0 || eventID == "" || len(eventID) > 64 || now <= 0 {
		return nil, ErrRelayQuotaReviewQueryInvalid
	}
	shapeErr := relayQuotaVideoReviewShape(source, task, operation)
	if model.IsKlingTaskOperationPlatform(operation.Platform) ||
		model.IsDoubaoVideoTaskOperationPlatform(operation.Platform) ||
		model.IsViduTaskOperationPlatform(operation.Platform) ||
		model.IsHailuoTaskOperationPlatform(operation.Platform) ||
		model.IsAliWanTaskOperationPlatform(operation.Platform) ||
		model.IsSunoTaskOperationPlatform(operation.Platform) ||
		model.IsMidjourneyTaskOperationPlatform(operation.Platform) ||
		model.IsVeoTaskOperationPlatform(operation.Platform) {
		shapeErr = relayQuotaAsyncReviewShape(source, task, operation)
	}
	if shapeErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrRelayQuotaReviewUnsafeRetry, shapeErr)
	}
	sourceStatus := source.Status
	if sourceStatus != model.RelayQuotaReservationStatusSettled &&
		sourceStatus != model.RelayQuotaReservationStatusDispatched {
		return nil, fmt.Errorf("%w: task polling accounting state is unsupported", ErrRelayQuotaReviewUnsafeRetry)
	}

	var current model.RelayQuotaReservationRecord
	result := relayQuotaReviewSnapshotPredicateForStatus(
		subscriptionLockForUpdate(tx), source, sourceStatus,
	).
		Where("dispatched_at = ? AND completed_at = ? AND lease_owner = ? AND lease_expires_at = ?",
			source.DispatchedAt, source.CompletedAt, "", int64(0)).
		Limit(1).Find(&current)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, fmt.Errorf("%w: polling retry accounting snapshot changed", ErrRelayQuotaReservationBusy)
	}
	var terminalReviewCount int64
	if err := tx.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ? AND action IN ?", source.ReservationID,
			[]string{RelayQuotaReviewActionResolveSettle, RelayQuotaReviewActionResolveRefund}).
		Count(&terminalReviewCount).Error; err != nil {
		return nil, err
	}
	if terminalReviewCount != 0 {
		return nil, fmt.Errorf("%w: terminal resolution audit already exists", ErrRelayQuotaReviewUnsafeRetry)
	}

	var maxRevision int64
	if err := tx.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ?", source.ReservationID).
		Select("COALESCE(MAX(revision), 0)").Scan(&maxRevision).Error; err != nil {
		return nil, err
	}
	if maxRevision >= int64(math.MaxInt) {
		return nil, errors.New("relay quota review revision overflow")
	}
	event := model.RelayQuotaReservationReviewEvent{
		EventID: eventID, ReservationID: source.ReservationID, Revision: int(maxRevision + 1),
		OperatorUserID:         operatorUserID,
		Action:                 model.RelayQuotaReservationReviewActionRetryTaskPoll,
		FromStatus:             sourceStatus,
		ToStatus:               sourceStatus,
		Operation:              source.Operation,
		TaskID:                 task.TaskID,
		TaskPlatform:           task.Platform,
		TaskFromStatus:         model.TaskStatusUnknown,
		TaskToStatus:           model.TaskStatusSubmitted,
		TaskOperationFromState: model.TaskOperationManualReview,
		TaskOperationToState:   model.TaskOperationSubmitted,
		TaskOperationCreatedAt: operation.CreatedAt,
		TaskOperationAttempts:  operation.Attempts,
		UserID:                 source.UserID,
		TokenID:                source.TokenID,
		TokenUnlimited:         source.TokenUnlimited,
		TrustQuotaBypassed:     source.TrustQuotaBypassed,
		ChannelID:              source.ChannelID,
		FundingSource:          source.FundingSource,
		RequestedQuota:         source.RequestedQuota,
		ReservedQuota:          source.ReservedQuota,
		TokenReserved:          source.TokenReserved,
		SubscriptionID:         source.SubscriptionID,
		UsageEpoch:             source.UsageEpoch,
		ActualQuota:            source.ActualQuota,
		CreatedAt:              now,
	}
	if err := tx.Create(&event).Error; err != nil {
		return nil, err
	}
	return &event, nil
}

// FindRelayQuotaTaskPollRetryEventTx returns the immutable proof for a prior
// polling retry. A settled task journal without this event is never treated as
// an idempotent operator replay.
func FindRelayQuotaTaskPollRetryEventTx(
	tx *gorm.DB,
	reservationID string,
) (*model.RelayQuotaReservationReviewEvent, bool, error) {
	if tx == nil || !validRelayQuotaReviewReservationID(reservationID) {
		return nil, false, ErrRelayQuotaReviewQueryInvalid
	}
	var event model.RelayQuotaReservationReviewEvent
	result := tx.Where("reservation_id = ? AND action = ?", reservationID,
		model.RelayQuotaReservationReviewActionRetryTaskPoll).
		Order("revision DESC").Limit(1).Find(&event)
	if result.Error != nil {
		return nil, false, result.Error
	}
	return &event, result.RowsAffected == 1, nil
}

// FindRelayQuotaReservationResolutionEventTx returns the one terminal review
// event for a requested disposition. The boolean is false when no such event
// exists; callers must treat a terminal accounting row without this audit as
// a conflict, not as a successful replay.
func FindRelayQuotaReservationResolutionEventTx(
	tx *gorm.DB,
	reservationID, resolution string,
) (*model.RelayQuotaReservationReviewEvent, bool, error) {
	if tx == nil || !validRelayQuotaReviewReservationID(reservationID) {
		return nil, false, ErrRelayQuotaReviewQueryInvalid
	}
	action, _, err := relayQuotaReviewResolutionAction(resolution)
	if err != nil {
		return nil, false, err
	}
	var event model.RelayQuotaReservationReviewEvent
	result := tx.Where("reservation_id = ? AND action = ?", reservationID, action).
		Order("revision DESC").Limit(1).Find(&event)
	if result.Error != nil {
		return nil, false, result.Error
	}
	return &event, result.RowsAffected == 1, nil
}

func relayQuotaReviewResolutionShape(
	source *model.RelayQuotaReservationRecord,
	resolution string,
) (action, targetStatus string, expectedActual int, err error) {
	if source == nil || source.Status != model.RelayQuotaReservationStatusDispatched ||
		source.Operation != model.RelayQuotaReservationOperationSettle ||
		source.ActualQuota != relayQuotaDispatchFallbackQuota(source) || source.ChannelID != 0 ||
		source.DispatchedAt <= 0 || source.LeaseOwner != "" || source.LeaseExpiresAt != 0 {
		return "", "", 0, fmt.Errorf("%w: reservation is not an unleased dispatched reservation", ErrRelayQuotaReviewUnsafeRetry)
	}
	if _, validationErr := relayReservationFromRecord(source); validationErr != nil {
		return "", "", 0, fmt.Errorf("%w: %v", ErrRelayQuotaReviewUnsafeRetry, validationErr)
	}
	action, targetStatus, err = relayQuotaReviewResolutionAction(resolution)
	if err != nil {
		return "", "", 0, err
	}
	if resolution == RelayQuotaReviewResolutionSettle {
		return action, targetStatus, source.RequestedQuota, nil
	}
	return action, targetStatus, 0, nil
}

func relayQuotaReviewResolutionAction(resolution string) (action, targetStatus string, err error) {
	switch resolution {
	case RelayQuotaReviewResolutionSettle:
		return RelayQuotaReviewActionResolveSettle, model.RelayQuotaReservationStatusSettled, nil
	case RelayQuotaReviewResolutionRefund:
		return RelayQuotaReviewActionResolveRefund, model.RelayQuotaReservationStatusRefunded, nil
	default:
		return "", "", fmt.Errorf("%w: unsupported terminal resolution", ErrRelayQuotaReviewQueryInvalid)
	}
}

func relayQuotaReviewEventFromRecord(eventID string, revision, operatorUserID int, targetStatus string, now int64, record *model.RelayQuotaReservationRecord) model.RelayQuotaReservationReviewEvent {
	return model.RelayQuotaReservationReviewEvent{
		EventID: eventID, ReservationID: record.ReservationID, Revision: revision,
		OperatorUserID: operatorUserID, Action: model.RelayQuotaReservationReviewActionRetry,
		FromStatus: model.RelayQuotaReservationStatusManualReview, ToStatus: targetStatus,
		Operation: record.Operation, UserID: record.UserID, TokenID: record.TokenID,
		TokenUnlimited: record.TokenUnlimited, TrustQuotaBypassed: record.TrustQuotaBypassed,
		ChannelID:     record.ChannelID,
		FundingSource: record.FundingSource, RequestedQuota: record.RequestedQuota,
		ReservedQuota: record.ReservedQuota, TokenReserved: record.TokenReserved,
		SubscriptionID: record.SubscriptionID, UsageEpoch: record.UsageEpoch,
		ActualQuota: record.ActualQuota, CreatedAt: now,
	}
}

func relayQuotaReviewIdempotentResultTx(tx *gorm.DB, record *model.RelayQuotaReservationRecord) (*RelayQuotaReservationRetryResult, error) {
	var event model.RelayQuotaReservationReviewEvent
	result := tx.Where("reservation_id = ? AND action = ?", record.ReservationID,
		model.RelayQuotaReservationReviewActionRetry).Order("revision DESC").Limit(1).Find(&event)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 || !relayQuotaReviewEventMatchesRecord(&event, record) {
		return nil, nil
	}
	item := relayQuotaReservationReviewItem(record, nil, nil)
	return &RelayQuotaReservationRetryResult{Reservation: item, AuditEventID: event.EventID, Changed: false}, nil
}

func relayQuotaReviewEventMatchesRecord(event *model.RelayQuotaReservationReviewEvent, record *model.RelayQuotaReservationRecord) bool {
	if event == nil || record == nil || event.Action != model.RelayQuotaReservationReviewActionRetry ||
		event.FromStatus != model.RelayQuotaReservationStatusManualReview || event.ReservationID != record.ReservationID ||
		event.Operation != record.Operation || event.UserID != record.UserID || event.TokenID != record.TokenID ||
		event.TokenUnlimited != record.TokenUnlimited || event.TrustQuotaBypassed != record.TrustQuotaBypassed ||
		event.ChannelID != record.ChannelID ||
		event.FundingSource != record.FundingSource || event.RequestedQuota != record.RequestedQuota ||
		event.ReservedQuota != record.ReservedQuota || event.TokenReserved != record.TokenReserved ||
		event.SubscriptionID != record.SubscriptionID || event.UsageEpoch != record.UsageEpoch ||
		event.ActualQuota != record.ActualQuota {
		return false
	}
	switch event.Operation {
	case model.RelayQuotaReservationOperationSettle:
		return event.ToStatus == model.RelayQuotaReservationStatusPendingSettlement &&
			(record.Status == event.ToStatus || record.Status == model.RelayQuotaReservationStatusSettled)
	case model.RelayQuotaReservationOperationRefund:
		return event.ToStatus == model.RelayQuotaReservationStatusPendingRefund &&
			(record.Status == event.ToStatus || record.Status == model.RelayQuotaReservationStatusRefunded)
	default:
		return false
	}
}

func verifyRelayQuotaReviewRetry(reservationID, eventID string) (*RelayQuotaReservationRetryResult, error) {
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
		return nil, err
	}
	if eventID != "" {
		var event model.RelayQuotaReservationReviewEvent
		if err := model.DB.Where("event_id = ?", eventID).First(&event).Error; err == nil {
			if relayQuotaReviewEventMatchesRecord(&event, &record) {
				item := relayQuotaReservationReviewItem(&record, nil, nil)
				return &RelayQuotaReservationRetryResult{Reservation: item, AuditEventID: event.EventID, Changed: true}, nil
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return relayQuotaReviewIdempotentResultTx(model.DB, &record)
}

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
	if common.UnmarshalJsonStr(strings.TrimPrefix(task.PrivateData, prefix), &privateData) != nil {
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
		return common.Unmarshal(raw, &pricing) == nil && pricing.Validate() == nil
	}
	var pricing struct {
		Plan                 ReferenceAsyncTaskBillingPlan `json:"plan"`
		Duration             int                           `json:"duration"`
		ResolutionMultiplier string                        `json:"resolution_multiplier"`
	}
	if common.Unmarshal(raw, &pricing) != nil || pricing.Plan.Validate() != nil ||
		(model.IsVeoTaskOperationPlatform(platform) && pricing.Duration != 4 && pricing.Duration != 6 && pricing.Duration != 8) ||
		(platform == model.TaskOperationPlatformAliWan && (pricing.Duration < 1 || pricing.Duration > 10)) ||
		pricing.ResolutionMultiplier == "" || len(pricing.ResolutionMultiplier) > 64 ||
		strings.TrimSpace(pricing.ResolutionMultiplier) != pricing.ResolutionMultiplier {
		return false
	}
	if !common.IsSafeDecimalLiteral(pricing.ResolutionMultiplier) {
		return false
	}
	multiplier, err := decimal.NewFromString(pricing.ResolutionMultiplier)
	maximum := decimal.NewFromInt(10)
	if platform == model.TaskOperationPlatformAliWan {
		maximum = decimal.NewFromInt(100)
	}
	return err == nil && multiplier.IsPositive() && multiplier.LessThanOrEqual(maximum)
}

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
	if common.UnmarshalJsonStr(strings.TrimPrefix(task.PrivateData,
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
