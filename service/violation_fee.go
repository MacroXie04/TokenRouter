package service

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/setting"
)

var (
	ErrViolationFeeReservationState  = errors.New("violation fee requires a refunded relay reservation")
	ErrViolationFeeOperation         = errors.New("violation fee operation conflict")
	ErrViolationFeeAuditMissing      = errors.New("violation fee audit record is missing")
	ErrViolationFeeAuditPersistence  = errors.New("violation fee audit persistence failed")
	ErrViolationFeeSubscriptionEpoch = errors.New("violation fee subscription epoch changed")
)

const (
	violationFeeTransactionAttempts = 4
	violationFeeFailureInsufficient = "insufficient_quota"
	violationFeeFailureToken        = "insufficient_token_quota"
	violationFeeFailureEpoch        = "subscription_epoch_changed"
	violationFeeFailureOverflow     = "accounting_overflow"
	violationFeeFailureMissing      = "accounting_subject_missing"
	violationFeeFailureAudit        = "audit_persistence_failed"
	violationFeeFailureConflict     = "accounting_conflict"
	violationFeeFailureTransaction  = "transaction_failed"
)

// GrokViolationFeeInput contains only bounded accounting metadata. Provider
// bodies and user content must never enter this structure or its audit event.
type GrokViolationFeeInput struct {
	ReservationID string
	ChannelID     int
	ModelName     string
	Group         string
	RequestID     string
	StatusCode    int
	UseTime       int
	IsStream      bool
	GroupRatio    float64
}

type GrokViolationFeeResult struct {
	Charged        bool
	AlreadyCharged bool
	Quota          int
	AuditEventID   string
}

type grokViolationFeePlan struct {
	quota        int
	amount       string
	groupRatio   string
	auditEventID string
}

// CalculateGrokViolationFeeQuota applies the validated USD-equivalent policy,
// immutable QuotaPerUnit scale, and effective user/routing group ratio. It
// rounds half away from zero like the reference and rejects saturation.
func CalculateGrokViolationFeeQuota(policy setting.GrokSetting, groupRatio float64) (int, error) {
	if math.IsNaN(groupRatio) || math.IsInf(groupRatio, 0) || groupRatio < 0 {
		return 0, errors.New("violation fee group ratio must be finite and non-negative")
	}
	amount := policy.ViolationDeductionAmountDecimal()
	if amount.IsNegative() {
		return 0, errors.New("violation fee amount must be non-negative")
	}
	if amount.IsZero() || groupRatio == 0 {
		return 0, nil
	}
	quotaDecimal := amount.
		Mul(decimal.NewFromInt(int64(common.QuotaPerUnit))).
		Mul(decimal.NewFromFloat(groupRatio)).
		Round(0)
	quota, err := common.QuotaFromDecimalStrict(quotaDecimal)
	if err != nil {
		return 0, fmt.Errorf("violation fee quota: %w", err)
	}
	if quota < 0 {
		return 0, errors.New("violation fee quota must be non-negative")
	}
	return quota, nil
}

// ChargeGrokViolationFee atomically charges one post-refund fee and enqueues
// its immutable consume audit. ReservationID is the idempotency key: retries,
// concurrent callers, and ambiguous commit results cannot charge twice.
func ChargeGrokViolationFee(input GrokViolationFeeInput) (GrokViolationFeeResult, error) {
	policy := setting.GetGrokSetting()
	if !policy.ViolationDeductionEnabled {
		return GrokViolationFeeResult{}, nil
	}
	quota, err := CalculateGrokViolationFeeQuota(policy, input.GroupRatio)
	if err != nil {
		return GrokViolationFeeResult{}, err
	}
	if quota == 0 {
		return GrokViolationFeeResult{}, nil
	}
	if err := validateGrokViolationFeeInput(input); err != nil {
		return GrokViolationFeeResult{}, err
	}
	plan := grokViolationFeePlan{
		quota:        quota,
		amount:       policy.ViolationDeductionAmountDecimal().String(),
		groupRatio:   decimal.NewFromFloat(input.GroupRatio).String(),
		auditEventID: "violation_fee_" + common.SHA256Hex(input.ReservationID)[:48],
	}
	return chargeGrokViolationFeeWithRunner(input, plan, defaultViolationFeeTransactionRunner)
}

func validateGrokViolationFeeInput(input GrokViolationFeeInput) error {
	if err := validateViolationFeeAuditField("reservation id", input.ReservationID, 64, false); err != nil {
		return errors.New("violation fee reservation id is invalid")
	}
	if input.ChannelID <= 0 {
		return errors.New("violation fee channel id is invalid")
	}
	for name, field := range map[string]struct {
		value string
		limit int
	}{
		"model":      {input.ModelName, 255},
		"group":      {input.Group, 64},
		"request id": {input.RequestID, 64},
	} {
		if err := validateViolationFeeAuditField(name, field.value, field.limit, true); err != nil {
			return err
		}
	}
	if input.StatusCode < 100 || input.StatusCode > 599 {
		return errors.New("violation fee status code is invalid")
	}
	if input.UseTime < 0 || int64(input.UseTime) > common.MaxQuota {
		return errors.New("violation fee use time is invalid")
	}
	return nil
}

// validateViolationFeeAuditField is deliberately stricter than a generic UTF-8
// check because these values are copied into security/financial audit records.
// Surrounding whitespace, controls, and Unicode format characters (including
// bidi overrides and zero-width controls) are rejected rather than normalized.
func validateViolationFeeAuditField(name, value string, limit int, allowEmpty bool) error {
	if (!allowEmpty && value == "") || len(value) > limit || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf("violation fee %s is invalid", name)
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.Is(unicode.Cf, current) {
			return fmt.Errorf("violation fee %s is invalid", name)
		}
	}
	return nil
}

func defaultViolationFeeTransactionRunner(fn func(tx *gorm.DB) error) error {
	if model.DB == nil {
		return errors.New("violation fee database is nil")
	}
	return model.DB.Transaction(fn)
}

func chargeGrokViolationFeeWithRunner(
	input GrokViolationFeeInput,
	plan grokViolationFeePlan,
	runner fundingTransactionRunner,
) (GrokViolationFeeResult, error) {
	if runner == nil {
		return GrokViolationFeeResult{}, errors.New("violation fee transaction runner is nil")
	}
	prepared, err := prepareGrokViolationFeeIntent(input, plan)
	if err != nil {
		return GrokViolationFeeResult{}, err
	}
	if prepared.Charged {
		deliverGrokViolationFeeAudit(input.ReservationID, prepared)
		return prepared, nil
	}
	var result GrokViolationFeeResult
	var transactionErr error
	for attempt := 0; attempt < violationFeeTransactionAttempts; attempt++ {
		result = GrokViolationFeeResult{}
		transactionErr = runner(func(tx *gorm.DB) error {
			return chargeGrokViolationFeeTx(tx, input, plan, &result)
		})
		if transactionErr == nil {
			break
		}
		verified, verifyErr := verifyGrokViolationFeeCharge(input, plan)
		if verifyErr == nil && verified.Charged {
			result = verified
			transactionErr = nil
			break
		}
		if !isRetryableViolationFeeTransactionError(transactionErr) || attempt == violationFeeTransactionAttempts-1 {
			if verifyErr != nil && !errors.Is(verifyErr, gorm.ErrRecordNotFound) {
				transactionErr = errors.Join(transactionErr, fmt.Errorf("verify violation fee transaction: %w", verifyErr))
			}
			break
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	if transactionErr != nil {
		failureCode := classifyGrokViolationFeeFailure(transactionErr)
		if reviewErr := persistGrokViolationFeeReview(input, plan, failureCode); reviewErr != nil {
			common.SysError(fmt.Sprintf(
				"violation fee review persistence failed reservation_id=%s failure_code=%s: %v",
				input.ReservationID, failureCode, reviewErr,
			))
			transactionErr = errors.Join(transactionErr, fmt.Errorf("persist violation fee review: %w", reviewErr))
		}
		// A concurrent caller may have committed the fee between our last
		// verification and the review transition. Prefer the proven committed
		// result over reporting a false accounting failure.
		if verified, verifyErr := verifyGrokViolationFeeCharge(input, plan); verifyErr == nil && verified.Charged {
			deliverGrokViolationFeeAudit(input.ReservationID, verified)
			return verified, nil
		}
		return GrokViolationFeeResult{}, transactionErr
	}
	if result.Charged {
		deliverGrokViolationFeeAudit(input.ReservationID, result)
	}
	return result, nil
}

func deliverGrokViolationFeeAudit(reservationID string, result GrokViolationFeeResult) {
	if !result.Charged || result.AuditEventID == "" {
		return
	}
	if err := DeliverAuditLogOutboxEvent(result.AuditEventID); err != nil {
		// The charge and full audit payload already share a primary-database
		// commit. Surface degraded delivery to operators while leaving the row
		// pending for the durable outbox worker.
		common.SysError(fmt.Sprintf(
			"violation fee audit delivery deferred reservation_id=%s event_id=%s: %v",
			reservationID, result.AuditEventID, err,
		))
	}
}

// prepareGrokViolationFeeIntent commits the immutable fee inputs before any
// balance mutation. A crash, insufficient balance, or later transaction/audit
// failure therefore leaves a row that is included in the operator review API.
func prepareGrokViolationFeeIntent(input GrokViolationFeeInput, plan grokViolationFeePlan) (GrokViolationFeeResult, error) {
	var result GrokViolationFeeResult
	var transactionErr error
	for attempt := 0; attempt < violationFeeTransactionAttempts; attempt++ {
		result = GrokViolationFeeResult{}
		transactionErr = defaultViolationFeeTransactionRunner(func(tx *gorm.DB) error {
			return prepareGrokViolationFeeIntentTx(tx, input, plan, &result)
		})
		if transactionErr == nil {
			return result, nil
		}
		verified, verifyErr := verifyGrokViolationFeeIntent(input, plan)
		if verifyErr == nil {
			return verified, nil
		}
		if !isRetryableViolationFeeTransactionError(transactionErr) || attempt == violationFeeTransactionAttempts-1 {
			if !errors.Is(verifyErr, gorm.ErrRecordNotFound) {
				transactionErr = errors.Join(transactionErr, fmt.Errorf("verify violation fee intent: %w", verifyErr))
			}
			return GrokViolationFeeResult{}, transactionErr
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return GrokViolationFeeResult{}, transactionErr
}

func prepareGrokViolationFeeIntentTx(
	tx *gorm.DB,
	input GrokViolationFeeInput,
	plan grokViolationFeePlan,
	result *GrokViolationFeeResult,
) error {
	if tx == nil || result == nil {
		return errors.New("violation fee intent transaction is invalid")
	}
	var reservation model.RelayQuotaReservationRecord
	if err := subscriptionLockForUpdate(tx).
		Where("reservation_id = ?", input.ReservationID).First(&reservation).Error; err != nil {
		return err
	}
	if !emptyGrokViolationFeeIntent(&reservation) {
		if err := validateStoredGrokViolationFeeIntent(&reservation, input, plan); err != nil {
			return err
		}
		if reservation.ViolationFeeStatus == model.RelayQuotaViolationFeeStatusCharged {
			if err := requireViolationFeeAuditTx(tx, plan.auditEventID); err != nil {
				return err
			}
			*result = GrokViolationFeeResult{
				Charged: true, AlreadyCharged: true, Quota: plan.quota, AuditEventID: plan.auditEventID,
			}
		}
		return nil
	}
	if err := validateGrokViolationFeeReservation(&reservation); err != nil {
		return err
	}
	if _, err := loadGrokViolationFeeChannelTx(tx, input.ChannelID); err != nil {
		return err
	}
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return err
	}
	intent := tx.Model(&model.RelayQuotaReservationRecord{}).
		Where("id = ? AND violation_fee_status = ? AND violation_fee_code = ? AND violation_fee_charged_at = ?",
			reservation.ID, "", "", 0).
		Updates(map[string]any{
			"violation_fee_status":         model.RelayQuotaViolationFeeStatusPending,
			"violation_fee_failure_code":   "",
			"violation_fee_attempts":       0,
			"violation_fee_code":           relaycommon.GrokViolationErrorCode,
			"violation_fee_quota":          plan.quota,
			"violation_fee_amount":         plan.amount,
			"violation_fee_group_ratio":    plan.groupRatio,
			"violation_fee_channel_id":     input.ChannelID,
			"violation_fee_audit_event_id": plan.auditEventID,
			"violation_fee_model_name":     input.ModelName,
			"violation_fee_group":          input.Group,
			"violation_fee_request_id":     input.RequestID,
			"violation_fee_status_code":    input.StatusCode,
			"violation_fee_use_time":       input.UseTime,
			"violation_fee_is_stream":      input.IsStream,
			"violation_fee_updated_at":     now,
			"updated_at":                   now,
		})
	if intent.Error != nil {
		return intent.Error
	}
	if intent.RowsAffected != 1 {
		return ErrViolationFeeOperation
	}
	return nil
}

func chargeGrokViolationFeeTx(
	tx *gorm.DB,
	input GrokViolationFeeInput,
	plan grokViolationFeePlan,
	result *GrokViolationFeeResult,
) error {
	if tx == nil || result == nil {
		return errors.New("violation fee transaction is invalid")
	}
	var reservation model.RelayQuotaReservationRecord
	if err := subscriptionLockForUpdate(tx).
		Where("reservation_id = ?", input.ReservationID).First(&reservation).Error; err != nil {
		return err
	}
	if err := validateStoredGrokViolationFeeIntent(&reservation, input, plan); err != nil {
		return err
	}
	if reservation.ViolationFeeStatus == model.RelayQuotaViolationFeeStatusCharged {
		if err := requireViolationFeeAuditTx(tx, plan.auditEventID); err != nil {
			return err
		}
		*result = GrokViolationFeeResult{
			Charged: true, AlreadyCharged: true, Quota: plan.quota, AuditEventID: plan.auditEventID,
		}
		return nil
	}
	if err := validateGrokViolationFeeReservation(&reservation); err != nil {
		return err
	}

	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return err
	}
	if reservation.FundingSource == BillingSourceSubscription {
		if err := chargeViolationFeeSubscriptionTx(tx, &reservation, plan.quota, now); err != nil {
			return err
		}
	} else if reservation.FundingSource != BillingSourceWallet && reservation.FundingSource != BillingSourceFreeModel {
		return fmt.Errorf("%w: unsupported funding source %q", ErrViolationFeeOperation, reservation.FundingSource)
	}

	username, err := chargeViolationFeeUserTx(tx, &reservation, plan.quota)
	if err != nil {
		return err
	}
	tokenName, err := chargeViolationFeeTokenTx(tx, &reservation, plan.quota)
	if err != nil {
		return err
	}
	channelName, err := chargeViolationFeeChannelTx(tx, input.ChannelID, plan.quota)
	if err != nil {
		return err
	}

	other := map[string]any{
		"violation_fee":               true,
		"violation_fee_code":          relaycommon.GrokViolationErrorCode,
		"violation_fee_audit_version": 1,
		"fee_quota":                   plan.quota,
		"base_amount":                 plan.amount,
		"group_ratio":                 plan.groupRatio,
		"status_code":                 input.StatusCode,
		"relay_reservation_id":        reservation.ReservationID,
		"billing_source":              reservation.FundingSource,
	}
	if reservation.FundingSource == BillingSourceSubscription {
		other["subscription_id"] = reservation.SubscriptionID
		other["subscription_usage_epoch"] = reservation.UsageEpoch
	}
	otherJSON, err := common.Marshal(other)
	if err != nil {
		return fmt.Errorf("marshal violation fee audit: %w", err)
	}
	auditEntry := &model.Log{
		AuditEventId: &plan.auditEventID,
		UserId:       reservation.UserID, Type: LogTypeConsume,
		Content: "Violation fee charged", Username: username, TokenName: tokenName,
		ModelName: input.ModelName, Quota: plan.quota, UseTime: input.UseTime,
		IsStream: input.IsStream, ChannelId: input.ChannelID, ChannelName: channelName,
		TokenId: reservation.TokenID, Group: input.Group, RequestId: input.RequestID,
		Other: string(otherJSON), CreatedAt: now,
	}
	if err := EnqueueAuditLogTx(tx, auditEntry); err != nil {
		return errors.Join(ErrViolationFeeAuditPersistence, err)
	}
	nextAttempts, attemptsOK := common.AddQuotaWithinBounds(reservation.ViolationFeeAttempts, 1)
	if !attemptsOK {
		return ErrViolationFeeOperation
	}
	marker := tx.Model(&model.RelayQuotaReservationRecord{}).
		Where("id = ? AND status = ? AND operation = ? AND violation_fee_charged_at = ? AND violation_fee_code = ? AND violation_fee_status IN ?",
			reservation.ID, model.RelayQuotaReservationStatusRefunded,
			model.RelayQuotaReservationOperationRefund, 0, relaycommon.GrokViolationErrorCode,
			[]string{model.RelayQuotaViolationFeeStatusPending, model.RelayQuotaViolationFeeStatusManualReview}).
		Updates(map[string]any{
			"violation_fee_status":       model.RelayQuotaViolationFeeStatusCharged,
			"violation_fee_failure_code": "",
			"violation_fee_attempts":     nextAttempts,
			"violation_fee_updated_at":   now,
			"violation_fee_charged_at":   now,
			"updated_at":                 now,
		})
	if marker.Error != nil {
		return marker.Error
	}
	if marker.RowsAffected != 1 {
		return ErrViolationFeeOperation
	}
	*result = GrokViolationFeeResult{Charged: true, Quota: plan.quota, AuditEventID: plan.auditEventID}
	return nil
}

func chargeViolationFeeSubscriptionTx(tx *gorm.DB, reservation *model.RelayQuotaReservationRecord, quota int, now int64) error {
	if reservation.SubscriptionID <= 0 {
		return fmt.Errorf("%w: subscription id is missing", ErrViolationFeeOperation)
	}
	var subscription model.UserSubscription
	if err := subscriptionLockForUpdate(tx).
		Where("id = ? AND user_id = ?", reservation.SubscriptionID, reservation.UserID).
		First(&subscription).Error; err != nil {
		return err
	}
	if subscription.UsageEpoch != reservation.UsageEpoch {
		return ErrViolationFeeSubscriptionEpoch
	}
	used, usedOK := boundedSubscriptionQuota(subscription.AmountUsed)
	total, totalOK := boundedSubscriptionQuota(subscription.AmountTotal)
	if !usedOK || !totalOK || total > 0 && used > total {
		return ErrSubscriptionQuotaOverflow
	}
	newUsed, ok := common.AddQuotaWithinBounds(used, quota)
	if !ok {
		return ErrSubscriptionQuotaOverflow
	}
	if total > 0 && newUsed > total {
		return ErrInsufficientQuota
	}
	update := tx.Model(&model.UserSubscription{}).
		Where("id = ? AND user_id = ? AND amount_used = ? AND amount_total = ? AND usage_epoch = ?",
			subscription.Id, reservation.UserID, subscription.AmountUsed, subscription.AmountTotal, reservation.UsageEpoch).
		Updates(map[string]any{"amount_used": int64(newUsed), "updated_at": now})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return ErrViolationFeeOperation
	}
	return nil
}

func chargeViolationFeeUserTx(tx *gorm.DB, reservation *model.RelayQuotaReservationRecord, quota int) (string, error) {
	var user model.User
	if err := subscriptionLockForUpdate(tx).
		Select("id", "username", "quota", "used_quota", "request_count").
		First(&user, reservation.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrUserNotFound
		}
		return "", err
	}
	newUsed, usedOK := common.AddQuotaWithinBounds(user.UsedQuota, quota)
	newCount, countOK := common.AddQuotaWithinBounds(user.RequestCount, 1)
	if !usedOK || !countOK {
		return "", ErrUserUsageOverflow
	}
	updates := map[string]any{"used_quota": newUsed, "request_count": newCount}
	query := tx.Model(&model.User{}).
		Where("id = ? AND used_quota = ? AND request_count = ?", user.Id, user.UsedQuota, user.RequestCount)
	if reservation.FundingSource == BillingSourceWallet || reservation.FundingSource == BillingSourceFreeModel {
		if !common.QuotaWithinBounds(user.Quota) {
			return "", ErrUserQuotaOverflow
		}
		if user.Quota < quota {
			return "", ErrInsufficientQuota
		}
		updates["quota"] = user.Quota - quota
		query = query.Where("quota = ?", user.Quota)
	}
	update := query.Updates(updates)
	if update.Error != nil {
		return "", update.Error
	}
	if update.RowsAffected != 1 {
		return "", ErrViolationFeeOperation
	}
	return user.Username, nil
}

func chargeViolationFeeTokenTx(tx *gorm.DB, reservation *model.RelayQuotaReservationRecord, quota int) (string, error) {
	if reservation.TokenID == 0 {
		if !reservation.TokenUnlimited || reservation.TokenReserved != 0 {
			return "", ErrViolationFeeOperation
		}
		return "", nil
	}
	var token model.Token
	if err := subscriptionLockForUpdate(tx.Unscoped()).
		Select("id", "user_id", "name", "remain_quota", "used_quota", "unlimited_quota").
		Where("id = ? AND user_id = ?", reservation.TokenID, reservation.UserID).
		First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrTokenNotFound
		}
		return "", err
	}
	newUsed, ok := common.AddQuotaWithinBounds(token.UsedQuota, quota)
	if !ok {
		return "", ErrTokenQuotaOverflow
	}
	updates := map[string]any{"used_quota": newUsed}
	query := tx.Unscoped().Model(&model.Token{}).
		Where("id = ? AND user_id = ? AND used_quota = ?", token.Id, reservation.UserID, token.UsedQuota)
	if !reservation.TokenUnlimited {
		if !common.QuotaWithinBounds(token.RemainQuota) {
			return "", ErrTokenQuotaOverflow
		}
		if token.RemainQuota < quota {
			return "", ErrInsufficientTokenQuota
		}
		updates["remain_quota"] = token.RemainQuota - quota
		query = query.Where("remain_quota = ?", token.RemainQuota)
	}
	update := query.Updates(updates)
	if update.Error != nil {
		return "", update.Error
	}
	if update.RowsAffected != 1 {
		return "", ErrViolationFeeOperation
	}
	return token.Name, nil
}

func chargeViolationFeeChannelTx(tx *gorm.DB, channelID, quota int) (string, error) {
	channel, err := loadGrokViolationFeeChannelTx(tx, channelID)
	if err != nil {
		return "", err
	}
	if channel.UsedQuota < 0 || int64(quota) > math.MaxInt64-channel.UsedQuota {
		return "", ErrChannelUsageOverflow
	}
	update := tx.Model(&model.Channel{}).
		Where("id = ? AND used_quota = ?", channel.Id, channel.UsedQuota).
		UpdateColumn("used_quota", channel.UsedQuota+int64(quota))
	if update.Error != nil {
		return "", update.Error
	}
	if update.RowsAffected != 1 {
		return "", ErrViolationFeeOperation
	}
	if channel.Name != "" {
		return channel.Name, nil
	}
	return "", nil
}

func loadGrokViolationFeeChannelTx(tx *gorm.DB, channelID int) (*model.Channel, error) {
	var channel model.Channel
	if err := subscriptionLockForUpdate(tx).
		Select("id", "name", "type", "used_quota").First(&channel, channelID).Error; err != nil {
		return nil, err
	}
	if channel.Type != int(constant.ChannelTypeXai) {
		return nil, fmt.Errorf("%w: violation fee channel is not xAI", ErrViolationFeeOperation)
	}
	return &channel, nil
}

func emptyGrokViolationFeeIntent(record *model.RelayQuotaReservationRecord) bool {
	return record != nil && record.ViolationFeeStatus == "" && record.ViolationFeeFailureCode == "" &&
		record.ViolationFeeAttempts == 0 && record.ViolationFeeCode == "" && record.ViolationFeeQuota == 0 &&
		record.ViolationFeeAmount == "" && record.ViolationFeeGroupRatio == "" &&
		record.ViolationFeeChannelID == 0 && record.ViolationFeeAuditEventID == "" &&
		record.ViolationFeeModelName == "" && record.ViolationFeeGroup == "" &&
		record.ViolationFeeRequestID == "" && record.ViolationFeeStatusCode == 0 &&
		record.ViolationFeeUseTime == 0 && !record.ViolationFeeIsStream &&
		record.ViolationFeeUpdatedAt == 0 && record.ViolationFeeChargedAt == 0
}

func validateGrokViolationFeeReservation(record *model.RelayQuotaReservationRecord) error {
	if record == nil || record.Status != model.RelayQuotaReservationStatusRefunded ||
		record.Operation != model.RelayQuotaReservationOperationRefund || record.ActualQuota != 0 {
		return ErrViolationFeeReservationState
	}
	if record.UserID <= 0 || record.ChannelID < 0 || record.TokenID < 0 ||
		record.TokenReserved < 0 || record.UsageEpoch < 0 {
		return ErrViolationFeeOperation
	}
	return nil
}

func validateStoredGrokViolationFeeIntent(
	record *model.RelayQuotaReservationRecord,
	input GrokViolationFeeInput,
	plan grokViolationFeePlan,
) error {
	if record == nil || record.ViolationFeeCode != relaycommon.GrokViolationErrorCode ||
		record.ViolationFeeQuota != plan.quota || record.ViolationFeeAmount != plan.amount ||
		record.ViolationFeeGroupRatio != plan.groupRatio || record.ViolationFeeChannelID != input.ChannelID ||
		record.ViolationFeeAuditEventID != plan.auditEventID || record.ViolationFeeModelName != input.ModelName ||
		record.ViolationFeeGroup != input.Group || record.ViolationFeeRequestID != input.RequestID ||
		record.ViolationFeeStatusCode != input.StatusCode || record.ViolationFeeUseTime != input.UseTime ||
		record.ViolationFeeIsStream != input.IsStream || record.ViolationFeeUpdatedAt <= 0 ||
		record.ViolationFeeAttempts < 0 || int64(record.ViolationFeeAttempts) > common.MaxQuota {
		return ErrViolationFeeOperation
	}
	switch record.ViolationFeeStatus {
	case model.RelayQuotaViolationFeeStatusPending:
		if record.ViolationFeeFailureCode != "" || record.ViolationFeeChargedAt != 0 ||
			record.ViolationFeeAttempts != 0 {
			return ErrViolationFeeOperation
		}
	case model.RelayQuotaViolationFeeStatusManualReview:
		if record.ViolationFeeAttempts <= 0 || record.ViolationFeeChargedAt != 0 ||
			!validGrokViolationFeeFailureCode(record.ViolationFeeFailureCode) {
			return ErrViolationFeeOperation
		}
	case model.RelayQuotaViolationFeeStatusCharged:
		if record.ViolationFeeFailureCode != "" || record.ViolationFeeChargedAt <= 0 ||
			record.ViolationFeeAttempts <= 0 {
			return ErrViolationFeeOperation
		}
	default:
		return ErrViolationFeeOperation
	}
	return validateGrokViolationFeeReservation(record)
}

func classifyGrokViolationFeeFailure(err error) string {
	switch {
	case errors.Is(err, ErrInsufficientTokenQuota):
		return violationFeeFailureToken
	case errors.Is(err, ErrInsufficientQuota):
		return violationFeeFailureInsufficient
	case errors.Is(err, ErrViolationFeeSubscriptionEpoch):
		return violationFeeFailureEpoch
	case errors.Is(err, ErrUserQuotaOverflow), errors.Is(err, ErrTokenQuotaOverflow),
		errors.Is(err, ErrSubscriptionQuotaOverflow), errors.Is(err, ErrChannelUsageOverflow):
		return violationFeeFailureOverflow
	case errors.Is(err, ErrUserNotFound), errors.Is(err, ErrTokenNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		return violationFeeFailureMissing
	case errors.Is(err, ErrViolationFeeAuditPersistence), errors.Is(err, ErrViolationFeeAuditMissing):
		return violationFeeFailureAudit
	case errors.Is(err, ErrViolationFeeOperation), errors.Is(err, ErrViolationFeeReservationState):
		return violationFeeFailureConflict
	default:
		return violationFeeFailureTransaction
	}
}

func validGrokViolationFeeFailureCode(code string) bool {
	switch code {
	case violationFeeFailureInsufficient, violationFeeFailureToken, violationFeeFailureEpoch,
		violationFeeFailureOverflow, violationFeeFailureMissing, violationFeeFailureAudit,
		violationFeeFailureConflict, violationFeeFailureTransaction:
		return true
	default:
		return false
	}
}

// persistGrokViolationFeeReview is deliberately separate from the failed fee
// transaction. It records only a bounded machine code and immutable fee plan,
// so raw database/provider errors can never be copied into operator output.
func persistGrokViolationFeeReview(input GrokViolationFeeInput, plan grokViolationFeePlan, failureCode string) error {
	if !validGrokViolationFeeFailureCode(failureCode) {
		failureCode = violationFeeFailureTransaction
	}
	return defaultViolationFeeTransactionRunner(func(tx *gorm.DB) error {
		var reservation model.RelayQuotaReservationRecord
		if err := subscriptionLockForUpdate(tx).
			Where("reservation_id = ?", input.ReservationID).First(&reservation).Error; err != nil {
			return err
		}
		if err := validateStoredGrokViolationFeeIntent(&reservation, input, plan); err != nil {
			return err
		}
		if reservation.ViolationFeeStatus == model.RelayQuotaViolationFeeStatusCharged {
			return requireViolationFeeAuditTx(tx, plan.auditEventID)
		}
		nextAttempts, ok := common.AddQuotaWithinBounds(reservation.ViolationFeeAttempts, 1)
		if !ok {
			return ErrViolationFeeOperation
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		update := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("id = ? AND violation_fee_status = ? AND violation_fee_attempts = ? AND violation_fee_charged_at = ?",
				reservation.ID, reservation.ViolationFeeStatus, reservation.ViolationFeeAttempts, 0).
			Updates(map[string]any{
				"violation_fee_status":       model.RelayQuotaViolationFeeStatusManualReview,
				"violation_fee_failure_code": failureCode,
				"violation_fee_attempts":     nextAttempts,
				"violation_fee_updated_at":   now,
				"updated_at":                 now,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrViolationFeeOperation
		}
		return nil
	})
}

// retryGrokViolationFeeReviewLocked executes the root-only recovery action.
// The caller holds relayQuotaReviewRetryMu. Current hot policy is deliberately
// not consulted: recovery must use the already committed immutable intent.
func retryGrokViolationFeeReviewLocked(
	source *model.RelayQuotaReservationRecord,
	operatorUserID int,
) (*RelayQuotaReservationRetryResult, error) {
	if source == nil || operatorUserID <= 0 {
		return nil, ErrRelayQuotaReviewQueryInvalid
	}
	input, plan, err := grokViolationFeeIntentFromRecord(source)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRelayQuotaReviewUnsafeRetry, err)
	}
	eventID, err := common.SecureRandomUUID()
	if err != nil {
		return nil, err
	}

	var outcome *RelayQuotaReservationRetryResult
	var chargeResult GrokViolationFeeResult
	var transactionErr error
	for attempt := 0; attempt < violationFeeTransactionAttempts; attempt++ {
		outcome = nil
		chargeResult = GrokViolationFeeResult{}
		transactionErr = defaultViolationFeeTransactionRunner(func(tx *gorm.DB) error {
			var current model.RelayQuotaReservationRecord
			if err := subscriptionLockForUpdate(tx).
				Where("reservation_id = ?", source.ReservationID).First(&current).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrRelayQuotaReviewNotFound
				}
				return err
			}
			if _, _, err := grokViolationFeeIntentFromRecord(&current); err != nil {
				return fmt.Errorf("%w: %v", ErrRelayQuotaReviewUnsafeRetry, err)
			}
			if current.ViolationFeeStatus == model.RelayQuotaViolationFeeStatusCharged {
				event, found, err := latestGrokViolationFeeRetryEventTx(tx, &current)
				if err != nil {
					return err
				}
				if !found {
					return ErrRelayQuotaReviewInvalidState
				}
				outcome = &RelayQuotaReservationRetryResult{
					Reservation:  relayQuotaReservationReviewItem(&current, nil, nil),
					AuditEventID: event.EventID,
					Changed:      false,
				}
				return nil
			}
			if current.ViolationFeeStatus != model.RelayQuotaViolationFeeStatusPending &&
				current.ViolationFeeStatus != model.RelayQuotaViolationFeeStatusManualReview {
				return ErrRelayQuotaReviewInvalidState
			}
			if _, err := loadGrokViolationFeeChannelTx(tx, input.ChannelID); err != nil {
				return fmt.Errorf("%w: selected channel is no longer a valid xAI channel", ErrRelayQuotaReviewUnsafeRetry)
			}
			fromStatus := current.ViolationFeeStatus
			if err := chargeGrokViolationFeeTx(tx, input, plan, &chargeResult); err != nil {
				return err
			}
			if chargeResult.AlreadyCharged {
				return ErrViolationFeeOperation
			}
			if err := tx.Where("id = ?", current.ID).First(&current).Error; err != nil {
				return err
			}
			if err := appendGrokViolationFeeRetryEventTx(
				tx, &current, operatorUserID, eventID, fromStatus, "",
			); err != nil {
				return err
			}
			outcome = &RelayQuotaReservationRetryResult{
				Reservation:  relayQuotaReservationReviewItem(&current, nil, nil),
				AuditEventID: eventID,
				Changed:      true,
			}
			return nil
		})
		if transactionErr == nil {
			if chargeResult.Charged {
				deliverGrokViolationFeeAudit(source.ReservationID, chargeResult)
			}
			return outcome, nil
		}
		if verified, verifyErr := verifyGrokViolationFeeOperatorRetry(source.ReservationID, eventID); verifyErr == nil && verified != nil {
			if verified.Reservation.ViolationFeeStatus == model.RelayQuotaViolationFeeStatusCharged {
				deliverGrokViolationFeeAudit(source.ReservationID, GrokViolationFeeResult{
					Charged: true, AlreadyCharged: true, Quota: plan.quota, AuditEventID: plan.auditEventID,
				})
			}
			return verified, nil
		}
		if errors.Is(transactionErr, ErrRelayQuotaReviewUnsafeRetry) ||
			errors.Is(transactionErr, ErrRelayQuotaReviewInvalidState) ||
			errors.Is(transactionErr, ErrRelayQuotaReviewNotFound) {
			return nil, transactionErr
		}
		if !isRetryableViolationFeeTransactionError(transactionErr) || attempt == violationFeeTransactionAttempts-1 {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}

	failureCode := classifyGrokViolationFeeFailure(transactionErr)
	manual, reviewErr := persistGrokViolationFeeOperatorReview(
		input, plan, failureCode, operatorUserID, eventID,
	)
	if reviewErr != nil {
		return nil, errors.Join(transactionErr, fmt.Errorf("persist operator violation fee review: %w", reviewErr))
	}
	return manual, nil
}

func grokViolationFeeIntentFromRecord(
	record *model.RelayQuotaReservationRecord,
) (GrokViolationFeeInput, grokViolationFeePlan, error) {
	if record == nil || record.ViolationFeeQuota <= 0 || !common.QuotaWithinBounds(record.ViolationFeeQuota) {
		return GrokViolationFeeInput{}, grokViolationFeePlan{}, ErrViolationFeeOperation
	}
	if record.ViolationFeeAmount == "" || len(record.ViolationFeeAmount) > 64 ||
		record.ViolationFeeGroupRatio == "" || len(record.ViolationFeeGroupRatio) > 64 ||
		strings.TrimSpace(record.ViolationFeeAmount) != record.ViolationFeeAmount ||
		strings.TrimSpace(record.ViolationFeeGroupRatio) != record.ViolationFeeGroupRatio ||
		!common.IsSafeDecimalLiteral(record.ViolationFeeAmount) ||
		!common.IsSafeDecimalLiteral(record.ViolationFeeGroupRatio) {
		return GrokViolationFeeInput{}, grokViolationFeePlan{}, ErrViolationFeeOperation
	}
	amountFloat, amountFloatErr := strconv.ParseFloat(record.ViolationFeeAmount, 64)
	ratioFloat, ratioFloatErr := strconv.ParseFloat(record.ViolationFeeGroupRatio, 64)
	if amountFloatErr != nil || ratioFloatErr != nil || math.IsNaN(amountFloat) || math.IsInf(amountFloat, 0) ||
		math.IsNaN(ratioFloat) || math.IsInf(ratioFloat, 0) || amountFloat <= 0 || ratioFloat <= 0 ||
		amountFloat > float64(common.MaxQuota)/float64(common.QuotaPerUnit) ||
		amountFloat*float64(common.QuotaPerUnit) > float64(common.MaxQuota)/ratioFloat {
		return GrokViolationFeeInput{}, grokViolationFeePlan{}, ErrViolationFeeOperation
	}
	amount, amountErr := decimal.NewFromString(record.ViolationFeeAmount)
	ratio, ratioErr := decimal.NewFromString(record.ViolationFeeGroupRatio)
	if amountErr != nil || ratioErr != nil || amount.String() != record.ViolationFeeAmount ||
		ratio.String() != record.ViolationFeeGroupRatio || amount.LessThanOrEqual(decimal.Zero) ||
		ratio.LessThanOrEqual(decimal.Zero) {
		return GrokViolationFeeInput{}, grokViolationFeePlan{}, ErrViolationFeeOperation
	}
	recomputed, err := common.QuotaFromDecimalStrict(amount.
		Mul(decimal.NewFromInt(int64(common.QuotaPerUnit))).Mul(ratio).Round(0))
	if err != nil || recomputed != record.ViolationFeeQuota {
		return GrokViolationFeeInput{}, grokViolationFeePlan{}, ErrViolationFeeOperation
	}
	expectedAuditID := "violation_fee_" + common.SHA256Hex(record.ReservationID)[:48]
	if record.ViolationFeeAuditEventID != expectedAuditID {
		return GrokViolationFeeInput{}, grokViolationFeePlan{}, ErrViolationFeeOperation
	}
	input := GrokViolationFeeInput{
		ReservationID: record.ReservationID,
		ChannelID:     record.ViolationFeeChannelID,
		ModelName:     record.ViolationFeeModelName,
		Group:         record.ViolationFeeGroup,
		RequestID:     record.ViolationFeeRequestID,
		StatusCode:    record.ViolationFeeStatusCode,
		UseTime:       record.ViolationFeeUseTime,
		IsStream:      record.ViolationFeeIsStream,
		GroupRatio:    ratioFloat,
	}
	if err := validateGrokViolationFeeInput(input); err != nil {
		return GrokViolationFeeInput{}, grokViolationFeePlan{}, err
	}
	plan := grokViolationFeePlan{
		quota: record.ViolationFeeQuota, amount: record.ViolationFeeAmount,
		groupRatio: record.ViolationFeeGroupRatio, auditEventID: record.ViolationFeeAuditEventID,
	}
	if err := validateStoredGrokViolationFeeIntent(record, input, plan); err != nil {
		return GrokViolationFeeInput{}, grokViolationFeePlan{}, err
	}
	return input, plan, nil
}

func persistGrokViolationFeeOperatorReview(
	input GrokViolationFeeInput,
	plan grokViolationFeePlan,
	failureCode string,
	operatorUserID int,
	eventID string,
) (*RelayQuotaReservationRetryResult, error) {
	if operatorUserID <= 0 || eventID == "" || len(eventID) > 64 {
		return nil, ErrRelayQuotaReviewQueryInvalid
	}
	if !validGrokViolationFeeFailureCode(failureCode) {
		failureCode = violationFeeFailureTransaction
	}
	var outcome *RelayQuotaReservationRetryResult
	run := func() error {
		return defaultViolationFeeTransactionRunner(func(tx *gorm.DB) error {
			var reservation model.RelayQuotaReservationRecord
			if err := subscriptionLockForUpdate(tx).
				Where("reservation_id = ?", input.ReservationID).First(&reservation).Error; err != nil {
				return err
			}
			if err := validateStoredGrokViolationFeeIntent(&reservation, input, plan); err != nil {
				return err
			}
			if reservation.ViolationFeeStatus == model.RelayQuotaViolationFeeStatusCharged {
				event, found, err := latestGrokViolationFeeRetryEventTx(tx, &reservation)
				if err != nil {
					return err
				}
				if !found {
					return ErrRelayQuotaReviewInvalidState
				}
				outcome = &RelayQuotaReservationRetryResult{
					Reservation:  relayQuotaReservationReviewItem(&reservation, nil, nil),
					AuditEventID: event.EventID,
					Changed:      false,
				}
				return nil
			}
			fromStatus := reservation.ViolationFeeStatus
			nextAttempts, ok := common.AddQuotaWithinBounds(reservation.ViolationFeeAttempts, 1)
			if !ok {
				return ErrViolationFeeOperation
			}
			now, err := model.DatabaseUnixTimestamp(tx)
			if err != nil {
				return err
			}
			update := tx.Model(&model.RelayQuotaReservationRecord{}).
				Where("id = ? AND violation_fee_status = ? AND violation_fee_attempts = ? AND violation_fee_charged_at = ?",
					reservation.ID, reservation.ViolationFeeStatus, reservation.ViolationFeeAttempts, 0).
				Updates(map[string]any{
					"violation_fee_status":       model.RelayQuotaViolationFeeStatusManualReview,
					"violation_fee_failure_code": failureCode,
					"violation_fee_attempts":     nextAttempts,
					"violation_fee_updated_at":   now,
					"updated_at":                 now,
				})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return ErrViolationFeeOperation
			}
			reservation.ViolationFeeStatus = model.RelayQuotaViolationFeeStatusManualReview
			reservation.ViolationFeeFailureCode = failureCode
			reservation.ViolationFeeAttempts = nextAttempts
			reservation.ViolationFeeUpdatedAt = now
			reservation.UpdatedAt = now
			if err := appendGrokViolationFeeRetryEventTx(
				tx, &reservation, operatorUserID, eventID, fromStatus, failureCode,
			); err != nil {
				return err
			}
			outcome = &RelayQuotaReservationRetryResult{
				Reservation:  relayQuotaReservationReviewItem(&reservation, nil, nil),
				AuditEventID: eventID,
				Changed:      true,
			}
			return nil
		})
	}
	var transactionErr error
	for attempt := 0; attempt < violationFeeTransactionAttempts; attempt++ {
		outcome = nil
		transactionErr = run()
		if transactionErr == nil {
			return outcome, nil
		}
		if verified, verifyErr := verifyGrokViolationFeeOperatorRetry(input.ReservationID, eventID); verifyErr == nil && verified != nil {
			return verified, nil
		}
		if !isRetryableViolationFeeTransactionError(transactionErr) || attempt == violationFeeTransactionAttempts-1 {
			return nil, transactionErr
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return nil, transactionErr
}

func appendGrokViolationFeeRetryEventTx(
	tx *gorm.DB,
	record *model.RelayQuotaReservationRecord,
	operatorUserID int,
	eventID string,
	fromStatus string,
	failureCode string,
) error {
	if tx == nil || record == nil || operatorUserID <= 0 || eventID == "" || len(eventID) > 64 ||
		(fromStatus != model.RelayQuotaViolationFeeStatusPending &&
			fromStatus != model.RelayQuotaViolationFeeStatusManualReview) {
		return ErrRelayQuotaReviewQueryInvalid
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
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return err
	}
	event := model.RelayQuotaReservationReviewEvent{
		EventID: eventID, ReservationID: record.ReservationID, Revision: int(maxRevision + 1),
		OperatorUserID: operatorUserID, Action: model.RelayQuotaReservationReviewActionRetryFee,
		FromStatus: record.Status, ToStatus: record.Status, Operation: record.Operation,
		UserID: record.UserID, TokenID: record.TokenID, TokenUnlimited: record.TokenUnlimited,
		TrustQuotaBypassed: record.TrustQuotaBypassed, ChannelID: record.ChannelID,
		FundingSource: record.FundingSource, RequestedQuota: record.RequestedQuota,
		ReservedQuota: record.ReservedQuota, TokenReserved: record.TokenReserved,
		SubscriptionID: record.SubscriptionID, UsageEpoch: record.UsageEpoch, ActualQuota: record.ActualQuota,
		ViolationFeeCode: record.ViolationFeeCode, ViolationFeeQuota: record.ViolationFeeQuota,
		ViolationFeeAmount: record.ViolationFeeAmount, ViolationFeeGroupRatio: record.ViolationFeeGroupRatio,
		ViolationFeeChannelID:    record.ViolationFeeChannelID,
		ViolationFeeAuditEventID: record.ViolationFeeAuditEventID,
		ViolationFeeFromStatus:   fromStatus, ViolationFeeToStatus: record.ViolationFeeStatus,
		ViolationFeeFailureCode: failureCode, ViolationFeeAttempts: record.ViolationFeeAttempts,
		CreatedAt: now,
	}
	return tx.Create(&event).Error
}

func latestGrokViolationFeeRetryEventTx(
	tx *gorm.DB,
	record *model.RelayQuotaReservationRecord,
) (*model.RelayQuotaReservationReviewEvent, bool, error) {
	var event model.RelayQuotaReservationReviewEvent
	result := tx.Where("reservation_id = ? AND action = ?", record.ReservationID,
		model.RelayQuotaReservationReviewActionRetryFee).
		Order("revision DESC").Limit(1).Find(&event)
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected != 1 || !grokViolationFeeRetryEventMatches(&event, record) {
		return nil, false, nil
	}
	return &event, true, nil
}

func grokViolationFeeRetryEventMatches(
	event *model.RelayQuotaReservationReviewEvent,
	record *model.RelayQuotaReservationRecord,
) bool {
	if event == nil || record == nil || event.Action != model.RelayQuotaReservationReviewActionRetryFee ||
		event.EventID == "" || len(event.EventID) > 64 || event.Revision <= 0 || event.OperatorUserID <= 0 ||
		(event.ViolationFeeFromStatus != model.RelayQuotaViolationFeeStatusPending &&
			event.ViolationFeeFromStatus != model.RelayQuotaViolationFeeStatusManualReview) ||
		event.ReservationID != record.ReservationID || event.FromStatus != record.Status ||
		event.ToStatus != record.Status || event.Operation != record.Operation ||
		event.UserID != record.UserID || event.TokenID != record.TokenID ||
		event.TokenUnlimited != record.TokenUnlimited || event.TrustQuotaBypassed != record.TrustQuotaBypassed ||
		event.ChannelID != record.ChannelID || event.FundingSource != record.FundingSource ||
		event.RequestedQuota != record.RequestedQuota || event.ReservedQuota != record.ReservedQuota ||
		event.TokenReserved != record.TokenReserved || event.SubscriptionID != record.SubscriptionID ||
		event.UsageEpoch != record.UsageEpoch || event.ActualQuota != record.ActualQuota ||
		event.ViolationFeeCode != record.ViolationFeeCode || event.ViolationFeeQuota != record.ViolationFeeQuota ||
		event.ViolationFeeAmount != record.ViolationFeeAmount ||
		event.ViolationFeeGroupRatio != record.ViolationFeeGroupRatio ||
		event.ViolationFeeChannelID != record.ViolationFeeChannelID ||
		event.ViolationFeeAuditEventID != record.ViolationFeeAuditEventID ||
		event.ViolationFeeToStatus != record.ViolationFeeStatus || event.ViolationFeeAttempts > record.ViolationFeeAttempts {
		return false
	}
	if event.ViolationFeeToStatus == model.RelayQuotaViolationFeeStatusCharged {
		return event.ViolationFeeFailureCode == "" && record.ViolationFeeFailureCode == "" &&
			event.ViolationFeeAttempts == record.ViolationFeeAttempts
	}
	return event.ViolationFeeToStatus == model.RelayQuotaViolationFeeStatusManualReview &&
		validGrokViolationFeeFailureCode(event.ViolationFeeFailureCode)
}

func verifyGrokViolationFeeOperatorRetry(reservationID, eventID string) (*RelayQuotaReservationRetryResult, error) {
	if model.DB == nil {
		return nil, errors.New("violation fee database is nil")
	}
	var event model.RelayQuotaReservationReviewEvent
	if err := model.DB.Where("event_id = ? AND reservation_id = ? AND action = ?", eventID, reservationID,
		model.RelayQuotaReservationReviewActionRetryFee).First(&event).Error; err != nil {
		return nil, err
	}
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
		return nil, err
	}
	if !grokViolationFeeRetryEventMatches(&event, &record) {
		return nil, ErrViolationFeeOperation
	}
	return &RelayQuotaReservationRetryResult{
		Reservation:  relayQuotaReservationReviewItem(&record, nil, nil),
		AuditEventID: event.EventID,
		Changed:      true,
	}, nil
}

func requireViolationFeeAuditTx(tx *gorm.DB, eventID string) error {
	var count int64
	if err := tx.Model(&model.AuditLogOutbox{}).Where("event_id = ?", eventID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrViolationFeeAuditMissing
	}
	return nil
}

func verifyGrokViolationFeeIntent(input GrokViolationFeeInput, plan grokViolationFeePlan) (GrokViolationFeeResult, error) {
	if model.DB == nil {
		return GrokViolationFeeResult{}, errors.New("violation fee database is nil")
	}
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", input.ReservationID).First(&record).Error; err != nil {
		return GrokViolationFeeResult{}, err
	}
	if emptyGrokViolationFeeIntent(&record) {
		return GrokViolationFeeResult{}, gorm.ErrRecordNotFound
	}
	if err := validateStoredGrokViolationFeeIntent(&record, input, plan); err != nil {
		return GrokViolationFeeResult{}, err
	}
	if record.ViolationFeeStatus != model.RelayQuotaViolationFeeStatusCharged {
		return GrokViolationFeeResult{}, nil
	}
	if err := requireViolationFeeAuditTx(model.DB, plan.auditEventID); err != nil {
		return GrokViolationFeeResult{}, err
	}
	return GrokViolationFeeResult{
		Charged: true, AlreadyCharged: true, Quota: plan.quota, AuditEventID: plan.auditEventID,
	}, nil
}

func verifyGrokViolationFeeCharge(input GrokViolationFeeInput, plan grokViolationFeePlan) (GrokViolationFeeResult, error) {
	result, err := verifyGrokViolationFeeIntent(input, plan)
	if err != nil {
		return GrokViolationFeeResult{}, err
	}
	if !result.Charged {
		return GrokViolationFeeResult{}, gorm.ErrRecordNotFound
	}
	return result, nil
}

func isRetryableViolationFeeTransactionError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "deadlock") ||
		strings.Contains(message, "serialization failure") ||
		strings.Contains(message, "could not serialize")
}
