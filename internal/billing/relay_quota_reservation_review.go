package billing

import (
	"errors"
	"fmt"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
)

var (
	ErrRelayQuotaReviewQueryInvalid = errors.New("invalid relay quota review query")
	ErrRelayQuotaReviewNotFound     = errors.New("relay quota reservation review record not found")
	ErrRelayQuotaReviewInvalidState = errors.New("relay quota reservation is not awaiting manual review")
	ErrRelayQuotaReviewUnsafeRetry  = errors.New("relay quota reservation cannot be retried safely")
)

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
