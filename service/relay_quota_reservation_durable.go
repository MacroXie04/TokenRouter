package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

var (
	ErrRelayQuotaReservationBusy = errors.New("relay quota reservation reconciliation is leased")
	ErrRelayQuotaManualReview    = errors.New("relay quota reservation requires manual review")
	ErrRelayQuotaOperation       = errors.New("relay quota reservation operation conflict")
)

const (
	defaultRelayReservationHoldSeconds  = int64(24 * time.Hour / time.Second)
	defaultRelayReservationLeaseSeconds = int64(30 * time.Second / time.Second)
	defaultRelayReservationMaxAttempts  = 8
	defaultRelayReservationBatchSize    = 100
	maxRelayReservationBackoffSeconds   = int64(5 * time.Minute / time.Second)
	maxRelayReservationHoldSeconds      = int64(30 * 24 * time.Hour / time.Second)
	maxRelayReservationLeaseSeconds     = int64(10 * time.Minute / time.Second)
	maxRelayReservationAttempts         = 100
	maxRelayReservationErrorBytes       = 4096
)

type relayQuotaReservationPolicy struct {
	allowTrustQuotaBypass bool
	trustQuota            int
	freeModel             bool
}

func createDurableRelayQuotaReservation(userId int, token *model.Token, quota int) (*RelayQuotaReservation, error) {
	return createDurableRelayQuotaReservationWithTransactionAndPersistence(
		userId,
		token,
		quota,
		relayQuotaReservationPolicy{},
		func(fn func(tx *gorm.DB) error) error { return model.DB.Transaction(fn) },
		nil,
	)
}

func createDurableRelayQuotaReservationWithPolicy(
	userId int,
	token *model.Token,
	quota int,
	policy relayQuotaReservationPolicy,
) (*RelayQuotaReservation, error) {
	return createDurableRelayQuotaReservationWithTransactionAndPersistence(
		userId,
		token,
		quota,
		policy,
		func(fn func(tx *gorm.DB) error) error { return model.DB.Transaction(fn) },
		nil,
	)
}

func createDurableRelayQuotaReservationWithPersistence(
	userId int,
	token *model.Token,
	quota int,
	persist RelayQuotaReservationCreationPersistence,
) (*RelayQuotaReservation, error) {
	return createDurableRelayQuotaReservationWithTransactionAndPersistence(
		userId, token, quota,
		relayQuotaReservationPolicy{},
		func(fn func(tx *gorm.DB) error) error { return model.DB.Transaction(fn) },
		persist,
	)
}

func createDurableRelayQuotaReservationWithTransaction(
	userId int,
	token *model.Token,
	quota int,
	transact fundingTransactionRunner,
) (*RelayQuotaReservation, error) {
	return createDurableRelayQuotaReservationWithTransactionAndPersistence(
		userId, token, quota, relayQuotaReservationPolicy{}, transact, nil,
	)
}

func createDurableRelayQuotaReservationWithTransactionAndPersistence(
	userId int,
	token *model.Token,
	quota int,
	policy relayQuotaReservationPolicy,
	transact fundingTransactionRunner,
	persist RelayQuotaReservationCreationPersistence,
) (*RelayQuotaReservation, error) {
	if userId <= 0 {
		return nil, errors.New("invalid reservation user")
	}
	if token == nil || token.UserId != userId || token.Id < 0 ||
		(token.Id == 0 && !token.UnlimitedQuota) {
		return nil, errors.New("invalid reservation token")
	}
	if err := validateQuotaAmount(quota); err != nil {
		return nil, fmt.Errorf("reservation quota: %w", err)
	}
	if model.DB == nil {
		return nil, errors.New("reservation database is nil")
	}
	if transact == nil {
		return nil, errors.New("reservation transaction runner is nil")
	}
	if policy.trustQuota <= 0 || !common.QuotaWithinBounds(policy.trustQuota) {
		policy.allowTrustQuotaBypass = false
		policy.trustQuota = 0
	}
	if policy.freeModel && quota != 0 {
		return nil, errors.New("free-model reservation requires zero quota")
	}

	reservationId, err := common.SecureRandomUUID()
	if err != nil {
		return nil, err
	}
	now := common.NowTimestamp()
	r := &RelayQuotaReservation{reservationId: reservationId, quota: quota}
	err = transact(func(tx *gorm.DB) error {
		funding, trustQuotaBypassed, err := reserveRelayFundingTx(
			tx, reservationId, userId, token, quota, now, policy,
		)
		if err != nil {
			return err
		}
		r.funding = funding
		r.tokenId = token.Id
		r.trustQuotaBypassed = trustQuotaBypassed
		liveToken, err := loadRelayReservationTokenTx(tx, userId, token)
		if err != nil {
			return err
		}
		r.tokenUnlimited = liveToken.UnlimitedQuota

		if !trustQuotaBypassed && !liveToken.UnlimitedQuota {
			if err := reserveRelayTokenTx(tx, userId, token.Id, quota); err != nil {
				return err
			}
			r.tokenReserved = quota
		}

		record := &model.RelayQuotaReservationRecord{
			ReservationID:      reservationId,
			UserID:             userId,
			TokenID:            r.tokenId,
			TokenUnlimited:     r.tokenUnlimited,
			TrustQuotaBypassed: trustQuotaBypassed,
			FundingSource:      funding.source,
			RequestedQuota:     quota,
			ReservedQuota:      funding.reserved,
			TokenReserved:      r.tokenReserved,
			SubscriptionID:     funding.subscriptionId,
			UsageEpoch:         funding.usageEpoch,
			Status:             model.RelayQuotaReservationStatusHeld,
			ExpiresAt:          now + relayReservationHoldSeconds(),
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		if err := tx.Create(record).Error; err != nil {
			return err
		}
		if persist == nil {
			return nil
		}
		return persist(tx, RelayQuotaReservationCreation{
			ReservationID: reservationId,
			Funding: FundingReservation{
				Source: funding.source, Reserved: funding.reserved, RequestId: funding.requestId,
				SubscriptionId: funding.subscriptionId,
				UsageEpoch:     funding.usageEpoch,
			},
			TokenID: r.tokenId, TokenReserved: r.tokenReserved, TokenUnlimited: r.tokenUnlimited,
			TrustQuotaBypassed: trustQuotaBypassed,
		})
	})
	if err != nil {
		// A driver may report an error after COMMIT reached the database. The
		// server-generated id lets us prove that this exact reservation exists
		// instead of returning an error and abandoning committed accounting state
		// or holds.
		var record model.RelayQuotaReservationRecord
		verification := model.DB.Where("reservation_id = ?", reservationId).Limit(1).Find(&record)
		verifyErr := verification.Error
		if verifyErr == nil && verification.RowsAffected == 1 &&
			relayQuotaReservationMatchesCreation(&record, userId, quota, now, r) {
			recovered, restoreErr := relayReservationFromRecord(&record)
			if restoreErr == nil {
				return recovered, nil
			}
			verifyErr = restoreErr
		} else if verifyErr == nil && verification.RowsAffected == 1 {
			verifyErr = errors.New("reservation id is owned by a different durable record")
		}
		return nil, errors.Join(err, wrapRelayReservationError("verify durable reservation creation", verifyErr))
	}
	return r, nil
}

func relayQuotaReservationMatchesCreation(
	record *model.RelayQuotaReservationRecord,
	userId int,
	quota int,
	createdAt int64,
	reservation *RelayQuotaReservation,
) bool {
	if record == nil || reservation == nil || reservation.funding == nil ||
		record.UserID != userId || record.RequestedQuota != quota ||
		record.CreatedAt != createdAt || record.Status != model.RelayQuotaReservationStatusHeld ||
		record.FundingSource != reservation.funding.source || record.ReservedQuota != reservation.funding.reserved ||
		record.SubscriptionID != reservation.funding.subscriptionId || record.UsageEpoch != reservation.funding.usageEpoch ||
		record.TrustQuotaBypassed != reservation.trustQuotaBypassed {
		return false
	}
	return record.TokenID == reservation.tokenId && record.TokenReserved == reservation.tokenReserved &&
		record.TokenUnlimited == reservation.tokenUnlimited
}

func reserveRelayFundingTx(
	tx *gorm.DB,
	reservationId string,
	userId int,
	token *model.Token,
	quota int,
	now int64,
	policy relayQuotaReservationPolicy,
) (*FundingSession, bool, error) {
	var settingsUser model.User
	if err := tx.Select("id", "setting").Where("id = ?", userId).First(&settingsUser).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrUserNotFound
		}
		return nil, false, fmt.Errorf("load billing preference: %w", err)
	}
	settings := parseUserSettings(settingsUser.Setting)
	pref := NormalizeBillingPreference(settings.BillingPreference)
	if policy.freeModel {
		return &FundingSession{
			userId: userId, source: BillingSourceFreeModel,
			rawPreference: settings.BillingPreference,
		}, false, nil
	}

	tryWallet := func() (*FundingSession, bool, error) {
		trustQuotaBypassed, err := reserveRelayWalletTx(tx, userId, token, quota, policy)
		if err != nil {
			return nil, false, err
		}
		reserved := quota
		if trustQuotaBypassed {
			reserved = 0
		}
		return &FundingSession{
			userId: userId, source: BillingSourceWallet,
			rawPreference: settings.BillingPreference, reserved: reserved,
		}, trustQuotaBypassed, nil
	}
	trySubscription := func() (*FundingSession, bool, bool, error) {
		amount := quota
		if amount <= 0 {
			amount = 1
		}
		return reserveRelaySubscriptionTx(
			tx, reservationId, userId, amount, settings.BillingPreference, now,
		)
	}

	switch pref {
	case BillingPreferenceSubscriptionOnly:
		session, _, _, err := trySubscription()
		return session, false, err
	case BillingPreferenceWalletOnly:
		return tryWallet()
	case BillingPreferenceWalletFirst:
		session, bypassed, err := tryWallet()
		if err == nil || !errors.Is(err, ErrInsufficientQuota) {
			return session, bypassed, err
		}
		session, _, _, err = trySubscription()
		return session, false, err
	default:
		session, hasActive, allowWalletOverflow, err := trySubscription()
		if err == nil {
			return session, false, nil
		}
		if !hasActive {
			return tryWallet()
		}
		if IsSubscriptionFundingErr(err) && allowWalletOverflow {
			return tryWallet()
		}
		return nil, false, err
	}
}

func reserveRelayWalletTx(
	tx *gorm.DB,
	userId int,
	token *model.Token,
	quota int,
	policy relayQuotaReservationPolicy,
) (bool, error) {
	var user model.User
	if err := subscriptionLockForUpdate(tx).Select("id", "quota").First(&user, userId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, ErrUserNotFound
		}
		return false, err
	}
	if !common.QuotaWithinBounds(user.Quota) {
		return false, ErrUserQuotaOverflow
	}
	if user.Quota < quota {
		return false, ErrInsufficientQuota
	}
	if policy.allowTrustQuotaBypass && user.Quota > policy.trustQuota {
		liveToken, err := loadRelayReservationTokenTx(tx, userId, token)
		if err != nil {
			return false, err
		}
		if liveToken.UnlimitedQuota || liveToken.RemainQuota > policy.trustQuota {
			return true, nil
		}
	}
	if quota == 0 {
		return false, nil
	}
	result := tx.Model(&model.User{}).Where("id = ? AND quota = ?", userId, user.Quota).
		UpdateColumn("quota", user.Quota-quota)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, ErrInsufficientQuota
	}
	return false, nil
}

func loadRelayReservationTokenTx(tx *gorm.DB, userId int, token *model.Token) (*model.Token, error) {
	if token == nil || token.UserId != userId || token.Id < 0 ||
		(token.Id == 0 && !token.UnlimitedQuota) {
		return nil, errors.New("invalid reservation token")
	}
	if token.Id == 0 {
		copy := *token
		return &copy, nil
	}
	var liveToken model.Token
	if err := subscriptionLockForUpdate(tx).
		Select("id", "user_id", "remain_quota", "unlimited_quota").
		Where("id = ? AND user_id = ?", token.Id, userId).
		First(&liveToken).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	if !liveToken.UnlimitedQuota && !common.QuotaWithinBounds(liveToken.RemainQuota) {
		return nil, ErrTokenQuotaOverflow
	}
	return &liveToken, nil
}

func reserveRelaySubscriptionTx(
	tx *gorm.DB,
	reservationId string,
	userId, amount int,
	rawPreference string,
	now int64,
) (*FundingSession, bool, bool, error) {
	var active []model.UserSubscription
	if err := tx.Select("id", "allow_wallet_overflow").
		Where("user_id = ? AND status = ? AND end_time > ?", userId, SubscriptionStatusActive, now).
		Find(&active).Error; err != nil {
		return nil, false, false, err
	}
	if len(active) == 0 {
		return nil, false, true, errors.New("no active subscription")
	}
	allowWalletOverflow := true
	for index := range active {
		if !active[index].AllowWalletOverflow {
			allowWalletOverflow = false
		}
	}
	result, err := preConsumeUserSubscriptionTx(tx, reservationId, userId, int64(amount))
	if err != nil {
		return nil, true, allowWalletOverflow, err
	}
	session := &FundingSession{
		userId: userId, requestId: reservationId,
		source: BillingSourceSubscription, rawPreference: rawPreference,
		reserved: amount, subscriptionId: result.UserSubscriptionId,
		usageEpoch: result.UsageEpoch,
		subTotal:   result.AmountTotal, subUsedAfter: result.AmountUsedAfter,
	}
	// Plan fields decorate logs only. Reservation accounting remains valid if
	// the plan is concurrently removed after pre-consume.
	var subscription model.UserSubscription
	if err := tx.Select("plan_id").Where("id = ?", result.UserSubscriptionId).First(&subscription).Error; err == nil {
		if plan, planErr := getSubscriptionPlanByIdTx(tx, subscription.PlanId); planErr == nil {
			session.subPlanId = subscription.PlanId
			session.subPlanTitle = plan.Title
		}
	}
	return session, true, allowWalletOverflow, nil
}

func reserveRelayTokenTx(tx *gorm.DB, userId, tokenId, quota int) error {
	var token model.Token
	if err := subscriptionLockForUpdate(tx).
		Select("id", "user_id", "remain_quota").
		Where("id = ? AND user_id = ?", tokenId, userId).First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTokenNotFound
		}
		return err
	}
	if !common.QuotaWithinBounds(token.RemainQuota) {
		return ErrTokenQuotaOverflow
	}
	if token.RemainQuota < quota {
		return ErrInsufficientTokenQuota
	}
	if quota == 0 {
		return nil
	}
	result := tx.Model(&model.Token{}).
		Where("id = ? AND user_id = ? AND remain_quota = ?", tokenId, userId, token.RemainQuota).
		UpdateColumn("remain_quota", token.RemainQuota-quota)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrInsufficientTokenQuota
	}
	return nil
}

func markDurableRelayQuotaReservationDispatched(
	reservation *RelayQuotaReservation,
	persist RelayQuotaReservationPersistence,
) error {
	if reservation == nil || reservation.reservationId == "" {
		return errors.New("invalid durable relay quota reservation")
	}
	now := common.NowTimestamp()
	alreadyDispatched := false
	err := reservation.transactionRunner()(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := subscriptionLockForUpdate(tx).
			Where("reservation_id = ?", reservation.reservationId).First(&record).Error; err != nil {
			return err
		}
		// Zero holds are valid only when the immutable trust decision is explicit.
		// Validate the durable tuple before permitting the first network dispatch,
		// so a missing/corrupt marker cannot be inferred from zero balances later.
		if _, validationErr := relayReservationFromRecord(&record); validationErr != nil {
			return validationErr
		}
		switch record.Status {
		case model.RelayQuotaReservationStatusDispatched:
			alreadyDispatched = true
			if persist != nil {
				return persist(tx)
			}
			return nil
		case model.RelayQuotaReservationStatusHeld:
			fallbackQuota := relayQuotaDispatchFallbackQuota(&record)
			result := tx.Model(&model.RelayQuotaReservationRecord{}).
				Where("id = ? AND status = ?", record.ID, model.RelayQuotaReservationStatusHeld).
				Updates(map[string]any{
					"status":          model.RelayQuotaReservationStatusDispatched,
					"operation":       model.RelayQuotaReservationOperationSettle,
					"actual_quota":    fallbackQuota,
					"dispatched_at":   now,
					"expires_at":      now + relayReservationHoldSeconds(),
					"updated_at":      now,
					"last_error":      "",
					"next_attempt_at": 0,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrRelayQuotaReservationBusy
			}
			if persist != nil {
				return persist(tx)
			}
			return nil
		case model.RelayQuotaReservationStatusManualReview:
			return fmt.Errorf("%w: %s", ErrRelayQuotaManualReview, record.LastError)
		default:
			return fmt.Errorf("%w: reservation is %s", ErrRelayQuotaOperation, record.Status)
		}
	})
	if err == nil {
		return nil
	}
	if alreadyDispatched {
		return err
	}
	// Resolve the same post-COMMIT ambiguity as settlement/refund. Only the
	// dispatched state proves this transition; an unrelated terminal state is
	// deliberately not treated as success.
	var record model.RelayQuotaReservationRecord
	verifyErr := model.DB.Where("reservation_id = ?", reservation.reservationId).First(&record).Error
	if verifyErr == nil && record.Status == model.RelayQuotaReservationStatusDispatched &&
		record.Operation == model.RelayQuotaReservationOperationSettle &&
		record.ActualQuota == relayQuotaDispatchFallbackQuota(&record) && record.DispatchedAt > 0 {
		if _, validationErr := relayReservationFromRecord(&record); validationErr == nil {
			return nil
		} else {
			verifyErr = validationErr
		}
	}
	return errors.Join(err, wrapRelayReservationError("verify durable dispatch marker", verifyErr))
}

func settleDurableRelayQuotaReservation(reservation *RelayQuotaReservation, actual, channelId int) error {
	if reservation == nil || reservation.funding == nil || reservation.reservationId == "" {
		return errors.New("invalid durable relay quota reservation")
	}
	if err := validateQuotaAmount(actual); err != nil {
		return fmt.Errorf("actual quota: %w", err)
	}
	if channelId < 0 {
		return errors.New("invalid settlement channel")
	}
	if err := prepareRelayQuotaOperation(
		reservation.reservationId,
		model.RelayQuotaReservationOperationSettle,
		actual,
		channelId,
	); err != nil {
		return err
	}
	return reconcileRelayQuotaSettlement(reservation, false)
}

// settleDurableRelayQuotaReservationWithPersistence is the direct atomic path
// for asynchronous request types whose accepted-task marker must commit with
// accounting. Unlike the generic retry path, it never exposes a
// pending_settlement row that another reconciler could finish without the
// caller-owned persistence callback.
func settleDurableRelayQuotaReservationWithPersistence(
	reservation *RelayQuotaReservation,
	actual, channelId int,
	persist RelayQuotaReservationPersistence,
) error {
	if reservation == nil || reservation.reservationId == "" || persist == nil {
		return errors.New("invalid durable relay settlement persistence")
	}
	if err := validateQuotaAmount(actual); err != nil {
		return fmt.Errorf("actual quota: %w", err)
	}
	if channelId < 0 {
		return errors.New("invalid settlement channel")
	}
	now := common.NowTimestamp()
	alreadySettled := false
	err := reservation.transactionRunner()(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := subscriptionLockForUpdate(tx).
			Where("reservation_id = ?", reservation.reservationId).First(&record).Error; err != nil {
			return err
		}
		if record.Status == model.RelayQuotaReservationStatusSettled {
			alreadySettled = true
			if record.Operation != model.RelayQuotaReservationOperationSettle ||
				record.ActualQuota != actual || record.ChannelID != channelId {
				return ErrRelayQuotaOperation
			}
			return persist(tx)
		}
		if record.Status != model.RelayQuotaReservationStatusDispatched {
			return fmt.Errorf("%w: reservation is %s", ErrRelayQuotaOperation, record.Status)
		}
		durable, validationErr := relayReservationFromRecord(&record)
		if validationErr != nil {
			return validationErr
		}
		inlineTransaction := func(fn func(tx *gorm.DB) error) error { return fn(tx) }
		return durable.funding.commitReservedUsageWithTransaction(
			actual,
			record.TokenID,
			record.TokenReserved,
			record.TokenUnlimited,
			channelId,
			func(tx *gorm.DB) (bool, error) {
				if err := persist(tx); err != nil {
					return false, err
				}
				result := tx.Model(&model.RelayQuotaReservationRecord{}).
					Where("id = ? AND status = ?", record.ID, model.RelayQuotaReservationStatusDispatched).
					Updates(map[string]any{
						"status":       model.RelayQuotaReservationStatusSettled,
						"operation":    model.RelayQuotaReservationOperationSettle,
						"actual_quota": actual, "channel_id": channelId,
						"completed_at": now, "updated_at": now,
						"lease_owner": "", "lease_expires_at": 0,
						"next_attempt_at": 0, "last_error": "",
					})
				if result.Error != nil {
					return false, result.Error
				}
				if result.RowsAffected != 1 {
					return false, ErrRelayQuotaReservationBusy
				}
				return false, nil
			},
			false,
			inlineTransaction,
		)
	})
	if err == nil {
		markFundingSessionSettled(reservation.funding, actual)
		return nil
	}
	if alreadySettled {
		return err
	}
	// A transaction driver can report an error after COMMIT. The terminal
	// accounting row proves that the persistence callback committed too.
	var record model.RelayQuotaReservationRecord
	verifyErr := model.DB.Where("reservation_id = ?", reservation.reservationId).First(&record).Error
	if verifyErr == nil && record.Status == model.RelayQuotaReservationStatusSettled &&
		record.Operation == model.RelayQuotaReservationOperationSettle &&
		record.ActualQuota == actual && record.ChannelID == channelId {
		markFundingSessionSettled(reservation.funding, actual)
		return nil
	}
	return errors.Join(err, wrapRelayReservationError("verify atomic durable settlement", verifyErr))
}

func refundDurableRelayQuotaReservation(reservation *RelayQuotaReservation) error {
	if reservation == nil || reservation.funding == nil || reservation.reservationId == "" {
		return errors.New("invalid durable relay quota reservation")
	}
	if err := prepareRelayQuotaOperation(
		reservation.reservationId,
		model.RelayQuotaReservationOperationRefund,
		0,
		0,
	); err != nil {
		return err
	}
	return reconcileRelayQuotaRefund(reservation, false)
}

func refundDurableRelayQuotaReservationWithPersistence(
	reservation *RelayQuotaReservation,
	persist RelayQuotaReservationPersistence,
) error {
	if reservation == nil || reservation.reservationId == "" || persist == nil {
		return errors.New("invalid durable relay refund persistence")
	}
	now := common.NowTimestamp()
	alreadyRefunded := false
	err := reservation.transactionRunner()(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := subscriptionLockForUpdate(tx).
			Where("reservation_id = ?", reservation.reservationId).First(&record).Error; err != nil {
			return err
		}
		if record.Status == model.RelayQuotaReservationStatusRefunded {
			alreadyRefunded = true
			return persist(tx)
		}
		if record.Status != model.RelayQuotaReservationStatusHeld &&
			record.Status != model.RelayQuotaReservationStatusDispatched {
			return fmt.Errorf("%w: reservation is %s", ErrRelayQuotaOperation, record.Status)
		}
		if _, validationErr := relayReservationFromRecord(&record); validationErr != nil {
			return validationErr
		}
		if err := refundRelayFundingTx(tx, &record); err != nil {
			return err
		}
		if err := refundRelayTokenTx(tx, &record); err != nil {
			return err
		}
		if err := persist(tx); err != nil {
			return err
		}
		result := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("id = ? AND status = ?", record.ID, record.Status).
			Updates(map[string]any{
				"status":       model.RelayQuotaReservationStatusRefunded,
				"operation":    model.RelayQuotaReservationOperationRefund,
				"actual_quota": 0, "channel_id": 0,
				"completed_at": now, "updated_at": now,
				"lease_owner": "", "lease_expires_at": 0,
				"next_attempt_at": 0, "last_error": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err == nil {
		markFundingSessionRefunded(reservation.funding)
		return nil
	}
	if alreadyRefunded {
		return err
	}
	var record model.RelayQuotaReservationRecord
	verifyErr := model.DB.Where("reservation_id = ?", reservation.reservationId).First(&record).Error
	if verifyErr == nil && record.Status == model.RelayQuotaReservationStatusRefunded &&
		record.Operation == model.RelayQuotaReservationOperationRefund {
		markFundingSessionRefunded(reservation.funding)
		return nil
	}
	return errors.Join(err, wrapRelayReservationError("verify atomic durable refund", verifyErr))
}

func reverseSettledRelayQuotaReservationWithPersistence(
	reservationID string,
	persist RelayQuotaReservationPersistence,
) error {
	if strings.TrimSpace(reservationID) == "" || persist == nil {
		return errors.New("invalid durable relay reversal persistence")
	}
	now := common.NowTimestamp()
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := subscriptionLockForUpdate(tx).
			Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
			return err
		}
		if record.Status == model.RelayQuotaReservationStatusReversed {
			if record.Operation != model.RelayQuotaReservationOperationReverse {
				return ErrRelayQuotaOperation
			}
			return persist(tx)
		}
		if record.Status != model.RelayQuotaReservationStatusSettled ||
			record.Operation != model.RelayQuotaReservationOperationSettle {
			return fmt.Errorf("%w: reservation is %s", ErrRelayQuotaOperation, record.Status)
		}
		if _, validationErr := relayReservationFromRecord(&record); validationErr != nil {
			return validationErr
		}
		if err := reverseSettledRelayFundingTx(tx, &record, now); err != nil {
			return err
		}
		if err := reverseSettledRelayTokenTx(tx, &record); err != nil {
			return err
		}
		if err := persist(tx); err != nil {
			return err
		}
		result := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("id = ? AND status = ? AND operation = ?", record.ID,
				model.RelayQuotaReservationStatusSettled, model.RelayQuotaReservationOperationSettle).
			Updates(map[string]any{
				"status":       model.RelayQuotaReservationStatusReversed,
				"operation":    model.RelayQuotaReservationOperationReverse,
				"completed_at": now, "updated_at": now,
				"lease_owner": "", "lease_expires_at": 0,
				"next_attempt_at": 0, "last_error": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err == nil {
		return nil
	}
	var record model.RelayQuotaReservationRecord
	verifyErr := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error
	if verifyErr == nil && record.Status == model.RelayQuotaReservationStatusReversed &&
		record.Operation == model.RelayQuotaReservationOperationReverse {
		return nil
	}
	return errors.Join(err, wrapRelayReservationError("verify atomic durable reversal", verifyErr))
}

func prepareRelayQuotaOperation(reservationId, operation string, actual, channelId int) error {
	if reservationId == "" {
		return errors.New("reservation id is empty")
	}
	now := common.NowTimestamp()
	pendingStatus := model.RelayQuotaReservationStatusPendingRefund
	terminalStatus := model.RelayQuotaReservationStatusRefunded
	if operation == model.RelayQuotaReservationOperationSettle {
		pendingStatus = model.RelayQuotaReservationStatusPendingSettlement
		terminalStatus = model.RelayQuotaReservationStatusSettled
	} else if operation != model.RelayQuotaReservationOperationRefund {
		return fmt.Errorf("%w: unsupported operation %q", ErrRelayQuotaOperation, operation)
	}

	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := subscriptionLockForUpdate(tx).
			Where("reservation_id = ?", reservationId).First(&record).Error; err != nil {
			return err
		}
		switch record.Status {
		case terminalStatus:
			return nil
		case model.RelayQuotaReservationStatusHeld, model.RelayQuotaReservationStatusDispatched:
			updates := map[string]any{
				"status": pendingStatus, "operation": operation, "actual_quota": actual,
				"channel_id":      channelId,
				"next_attempt_at": now, "updated_at": now, "last_error": "",
			}
			result := tx.Model(&model.RelayQuotaReservationRecord{}).
				Where("id = ? AND status = ?", record.ID, record.Status).
				Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrRelayQuotaReservationBusy
			}
			return nil
		case pendingStatus:
			if record.Operation != operation ||
				(operation == model.RelayQuotaReservationOperationSettle &&
					(record.ActualQuota != actual || record.ChannelID != channelId)) {
				return fmt.Errorf("%w: existing=%s/%d/channel-%d requested=%s/%d/channel-%d",
					ErrRelayQuotaOperation, record.Operation, record.ActualQuota, record.ChannelID,
					operation, actual, channelId)
			}
			return nil
		case model.RelayQuotaReservationStatusManualReview:
			return fmt.Errorf("%w: %s", ErrRelayQuotaManualReview, record.LastError)
		default:
			return fmt.Errorf("%w: reservation is %s", ErrRelayQuotaOperation, record.Status)
		}
	})
	if err == nil {
		return nil
	}
	var record model.RelayQuotaReservationRecord
	verifyErr := model.DB.Where("reservation_id = ?", reservationId).First(&record).Error
	if verifyErr == nil {
		if record.Status == terminalStatus ||
			(record.Status == pendingStatus && record.Operation == operation &&
				(operation != model.RelayQuotaReservationOperationSettle ||
					(record.ActualQuota == actual && record.ChannelID == channelId))) {
			return nil
		}
	}
	return errors.Join(err, wrapRelayReservationError("verify durable operation intent", verifyErr))
}

func relayReservationFromRecord(record *model.RelayQuotaReservationRecord) (*RelayQuotaReservation, error) {
	if record == nil || record.ReservationID == "" || record.UserID <= 0 {
		return nil, errors.New("invalid relay quota reservation record")
	}
	if record.UsageEpoch < 0 {
		return nil, errors.New("invalid durable subscription usage epoch")
	}
	if err := validateQuotaAmount(record.RequestedQuota); err != nil {
		return nil, err
	}
	if err := validateQuotaAmount(record.ReservedQuota); err != nil {
		return nil, err
	}
	if err := validateQuotaAmount(record.TokenReserved); err != nil {
		return nil, err
	}
	if record.TokenID < 0 || (record.TokenReserved > 0 && record.TokenID <= 0) ||
		(record.TokenID == 0 && (record.TokenReserved != 0 || !record.TokenUnlimited)) ||
		(record.TokenUnlimited && record.TokenReserved != 0) {
		return nil, errors.New("invalid durable token reservation")
	}
	expectedTokenReserved := record.RequestedQuota
	if record.TokenUnlimited || record.TrustQuotaBypassed || record.FundingSource == BillingSourceFreeModel {
		expectedTokenReserved = 0
	}
	if record.TokenReserved != expectedTokenReserved {
		return nil, errors.New("invalid durable token reservation")
	}
	switch record.FundingSource {
	case BillingSourceWallet:
		expectedReserved := record.RequestedQuota
		if record.TrustQuotaBypassed {
			expectedReserved = 0
		}
		if record.SubscriptionID != 0 || record.UsageEpoch != 0 || record.ReservedQuota != expectedReserved {
			return nil, errors.New("invalid durable wallet reservation")
		}
	case BillingSourceSubscription:
		if record.TrustQuotaBypassed {
			return nil, errors.New("invalid durable subscription trust bypass")
		}
		expectedReserved := record.RequestedQuota
		if expectedReserved == 0 {
			expectedReserved = 1
		}
		if record.SubscriptionID <= 0 || record.ReservedQuota != expectedReserved {
			return nil, errors.New("invalid durable subscription reservation")
		}
	case BillingSourceFreeModel:
		if record.TrustQuotaBypassed || record.RequestedQuota != 0 || record.ReservedQuota != 0 ||
			record.TokenReserved != 0 || record.SubscriptionID != 0 || record.UsageEpoch != 0 {
			return nil, errors.New("invalid durable free-model reservation")
		}
	default:
		return nil, fmt.Errorf("unsupported durable funding source %q", record.FundingSource)
	}
	switch record.Status {
	case model.RelayQuotaReservationStatusHeld:
		if record.Operation != "" || record.DispatchedAt != 0 {
			return nil, errors.New("invalid durable undispatched reservation")
		}
	case model.RelayQuotaReservationStatusDispatched:
		if record.Operation != model.RelayQuotaReservationOperationSettle ||
			record.ActualQuota != relayQuotaDispatchFallbackQuota(record) || record.DispatchedAt <= 0 {
			return nil, errors.New("invalid durable dispatched reservation")
		}
	case model.RelayQuotaReservationStatusPendingSettlement, model.RelayQuotaReservationStatusSettled:
		if record.Operation != model.RelayQuotaReservationOperationSettle {
			return nil, errors.New("invalid durable settlement operation")
		}
		if err := validateQuotaAmount(record.ActualQuota); err != nil {
			return nil, fmt.Errorf("stored actual quota: %w", err)
		}
		if record.ChannelID < 0 {
			return nil, errors.New("invalid durable settlement channel")
		}
	case model.RelayQuotaReservationStatusPendingRefund, model.RelayQuotaReservationStatusRefunded:
		if record.Operation != model.RelayQuotaReservationOperationRefund || record.ActualQuota != 0 {
			return nil, errors.New("invalid durable refund operation")
		}
	case model.RelayQuotaReservationStatusReversed:
		if record.Operation != model.RelayQuotaReservationOperationReverse {
			return nil, errors.New("invalid durable reversal operation")
		}
		if err := validateQuotaAmount(record.ActualQuota); err != nil {
			return nil, fmt.Errorf("stored reversed quota: %w", err)
		}
		if record.ChannelID < 0 {
			return nil, errors.New("invalid durable reversal channel")
		}
	case model.RelayQuotaReservationStatusManualReview:
		// Manual-review rows deliberately preserve the operation and accounting
		// fields that failed validation so an operator can diagnose them.
	default:
		return nil, fmt.Errorf("unsupported durable reservation status %q", record.Status)
	}
	funding := &FundingSession{
		userId: record.UserID, requestId: record.ReservationID,
		source: record.FundingSource, reserved: record.ReservedQuota,
		subscriptionId: record.SubscriptionID,
		usageEpoch:     record.UsageEpoch,
	}
	return &RelayQuotaReservation{
		reservationId:      record.ReservationID,
		funding:            funding,
		tokenId:            record.TokenID,
		tokenReserved:      record.TokenReserved,
		tokenUnlimited:     record.TokenUnlimited,
		trustQuotaBypassed: record.TrustQuotaBypassed,
		channelId:          record.ChannelID,
		quota:              record.RequestedQuota,
		dispatched:         record.Status == model.RelayQuotaReservationStatusDispatched,
		settled: record.Status == model.RelayQuotaReservationStatusSettled ||
			record.Status == model.RelayQuotaReservationStatusReversed,
		refunded: record.Status == model.RelayQuotaReservationStatusRefunded,
	}, nil
}

func relayQuotaDispatchFallbackQuota(record *model.RelayQuotaReservationRecord) int {
	if record != nil && record.TrustQuotaBypassed {
		return record.RequestedQuota
	}
	if record == nil {
		return 0
	}
	return record.ReservedQuota
}

func reconcileRelayQuotaSettlement(reservation *RelayQuotaReservation, worker bool) error {
	return reconcileRelayQuotaSettlementAt(reservation, worker, common.NowTimestamp())
}

func reconcileRelayQuotaSettlementAt(reservation *RelayQuotaReservation, worker bool, now int64) error {
	return reconcileRelayQuotaSettlementAtContext(context.Background(), reservation, worker, now)
}

func reconcileRelayQuotaSettlementAtContext(ctx context.Context, reservation *RelayQuotaReservation, worker bool, now int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ownerID, err := common.SecureRandomUUID()
	if err != nil {
		return err
	}
	owner := "relay-settle-" + ownerID
	record, claimed, err := claimRelayQuotaReservationContext(
		ctx,
		reservation.reservationId,
		model.RelayQuotaReservationStatusPendingSettlement,
		owner,
		now,
		worker,
	)
	if err != nil {
		return err
	}
	if !claimed {
		if record.Status == model.RelayQuotaReservationStatusSettled {
			markFundingSessionSettled(reservation.funding, record.ActualQuota)
			return nil
		}
		if worker {
			return nil
		}
		return ErrRelayQuotaReservationBusy
	}
	durable, validationErr := relayReservationFromRecord(record)
	if validationErr != nil {
		return markInvalidRelayQuotaReservationForReviewContext(ctx, record, validationErr, now)
	}
	actual := record.ActualQuota
	err = durable.funding.commitReservedUsageWithTransaction(
		actual,
		record.TokenID,
		record.TokenReserved,
		record.TokenUnlimited,
		record.ChannelID,
		func(tx *gorm.DB) (bool, error) {
			return markRelayQuotaReservationTerminalTx(
				tx, record.ReservationID, owner,
				model.RelayQuotaReservationStatusPendingSettlement,
				model.RelayQuotaReservationStatusSettled,
				actual,
			)
		},
		false,
		reservation.transactionRunnerContext(ctx),
	)
	if err == nil {
		markFundingSessionSettled(reservation.funding, actual)
		return nil
	}
	terminal, verifyErr := relayQuotaReservationHasStatusContext(ctx, record.ReservationID, model.RelayQuotaReservationStatusSettled)
	if verifyErr == nil && terminal {
		markFundingSessionSettled(reservation.funding, actual)
		return nil
	}
	return recordRelayQuotaReconciliationFailureContext(ctx, record.ReservationID, owner, err, verifyErr)
}

func reconcileRelayQuotaRefund(reservation *RelayQuotaReservation, worker bool) error {
	return reconcileRelayQuotaRefundAt(reservation, worker, common.NowTimestamp())
}

func reconcileRelayQuotaRefundAt(reservation *RelayQuotaReservation, worker bool, now int64) error {
	return reconcileRelayQuotaRefundAtContext(context.Background(), reservation, worker, now)
}

func reconcileRelayQuotaRefundAtContext(ctx context.Context, reservation *RelayQuotaReservation, worker bool, now int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ownerID, err := common.SecureRandomUUID()
	if err != nil {
		return err
	}
	owner := "relay-refund-" + ownerID
	record, claimed, err := claimRelayQuotaReservationContext(
		ctx,
		reservation.reservationId,
		model.RelayQuotaReservationStatusPendingRefund,
		owner,
		now,
		worker,
	)
	if err != nil {
		return err
	}
	if !claimed {
		if record.Status == model.RelayQuotaReservationStatusRefunded {
			markFundingSessionRefunded(reservation.funding)
			return nil
		}
		if worker {
			return nil
		}
		return ErrRelayQuotaReservationBusy
	}
	if _, validationErr := relayReservationFromRecord(record); validationErr != nil {
		return markInvalidRelayQuotaReservationForReviewContext(ctx, record, validationErr, now)
	}

	err = reservation.transactionRunnerContext(ctx)(func(tx *gorm.DB) error {
		if err := refundRelayFundingTx(tx, record); err != nil {
			return err
		}
		if err := refundRelayTokenTx(tx, record); err != nil {
			return err
		}
		_, err := markRelayQuotaReservationTerminalTx(
			tx, record.ReservationID, owner,
			model.RelayQuotaReservationStatusPendingRefund,
			model.RelayQuotaReservationStatusRefunded,
			0,
		)
		return err
	})
	if err == nil {
		markFundingSessionRefunded(reservation.funding)
		return nil
	}
	terminal, verifyErr := relayQuotaReservationHasStatusContext(ctx, record.ReservationID, model.RelayQuotaReservationStatusRefunded)
	if verifyErr == nil && terminal {
		markFundingSessionRefunded(reservation.funding)
		return nil
	}
	return recordRelayQuotaReconciliationFailureContext(ctx, record.ReservationID, owner, err, verifyErr)
}

func claimRelayQuotaReservation(
	reservationId, pendingStatus, owner string,
	now int64,
	dueOnly bool,
) (*model.RelayQuotaReservationRecord, bool, error) {
	return claimRelayQuotaReservationContext(context.Background(), reservationId, pendingStatus, owner, now, dueOnly)
}

func claimRelayQuotaReservationContext(
	ctx context.Context,
	reservationId, pendingStatus, owner string,
	now int64,
	dueOnly bool,
) (*model.RelayQuotaReservationRecord, bool, error) {
	leaseUntil := now + relayReservationLeaseSeconds()
	db := model.DB.WithContext(ctx)
	query := db.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ? AND status = ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			reservationId, pendingStatus, now).
		Where("attempts >= 0 AND attempts < ?", relayReservationMaxAttempts())
	if dueOnly {
		query = query.Where("next_attempt_at <= ?", now)
	}
	result := query.
		Updates(map[string]any{
			"lease_owner": owner, "lease_expires_at": leaseUntil, "updated_at": now,
			"attempts": gorm.Expr("attempts + ?", 1),
		})
	if result.Error != nil {
		return nil, false, result.Error
	}
	var record model.RelayQuotaReservationRecord
	if err := db.Where("reservation_id = ?", reservationId).First(&record).Error; err != nil {
		return nil, false, err
	}
	return &record, result.RowsAffected == 1 && record.LeaseOwner == owner, nil
}

func markRelayQuotaReservationTerminalTx(
	tx *gorm.DB,
	reservationId, owner, pendingStatus, terminalStatus string,
	actual int,
) (bool, error) {
	var record model.RelayQuotaReservationRecord
	if err := subscriptionLockForUpdate(tx).
		Where("reservation_id = ?", reservationId).First(&record).Error; err != nil {
		return false, err
	}
	if record.Status == terminalStatus {
		return true, nil
	}
	if record.Status != pendingStatus || record.LeaseOwner != owner {
		return false, ErrRelayQuotaReservationBusy
	}
	if pendingStatus == model.RelayQuotaReservationStatusPendingSettlement && record.ActualQuota != actual {
		return false, ErrRelayQuotaOperation
	}
	now := common.NowTimestamp()
	result := tx.Model(&model.RelayQuotaReservationRecord{}).
		Where("id = ? AND status = ? AND lease_owner = ?", record.ID, pendingStatus, owner).
		Updates(map[string]any{
			"status": terminalStatus, "completed_at": now, "updated_at": now,
			"lease_owner": "", "lease_expires_at": 0, "next_attempt_at": 0, "last_error": "",
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, ErrRelayQuotaReservationBusy
	}
	return false, nil
}

func refundRelayFundingTx(tx *gorm.DB, record *model.RelayQuotaReservationRecord) error {
	if err := validateQuotaAmount(record.ReservedQuota); err != nil {
		return fmt.Errorf("stored funding reservation: %w", err)
	}
	switch record.FundingSource {
	case BillingSourceWallet:
		var user model.User
		if err := subscriptionLockForUpdate(tx).Select("id", "quota").First(&user, record.UserID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrUserNotFound
			}
			return err
		}
		newQuota, ok := common.AddQuotaWithinBounds(user.Quota, record.ReservedQuota)
		if !ok {
			return ErrUserQuotaOverflow
		}
		if newQuota == user.Quota {
			return nil
		}
		result := tx.Model(&model.User{}).Where("id = ? AND quota = ?", record.UserID, user.Quota).
			UpdateColumn("quota", newQuota)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrUserNotFound
		}
		return nil
	case BillingSourceSubscription:
		return refundRelaySubscriptionTx(tx, record)
	case BillingSourceFreeModel:
		if record.ReservedQuota != 0 {
			return errors.New("invalid free-model funding reservation")
		}
		return nil
	default:
		return fmt.Errorf("unsupported funding source %q", record.FundingSource)
	}
}

func refundRelaySubscriptionTx(tx *gorm.DB, record *model.RelayQuotaReservationRecord) error {
	var ledger model.SubscriptionPreConsumeRecord
	ledgerQuery := subscriptionLockForUpdate(tx).
		Where("request_id = ?", record.ReservationID).Limit(1).Find(&ledger)
	if ledgerQuery.Error != nil {
		return ledgerQuery.Error
	}
	ledgerPresent := ledgerQuery.RowsAffected == 1
	if ledgerPresent {
		if ledger.UserId != record.UserID || ledger.UserSubscriptionId != record.SubscriptionID ||
			ledger.PreConsumed != int64(record.ReservedQuota) || ledger.UsageEpoch != record.UsageEpoch {
			return errors.New("subscription reservation ledger does not match durable reservation")
		}
		if ledger.Status == SubscriptionPreConsumeStatusRefunded {
			return nil
		}
		if ledger.Status != SubscriptionPreConsumeStatusConsumed {
			return fmt.Errorf("unsupported subscription reservation status %q", ledger.Status)
		}
	}
	var subscription model.UserSubscription
	if err := subscriptionLockForUpdate(tx).
		Where("id = ? AND user_id = ?", record.SubscriptionID, record.UserID).
		First(&subscription).Error; err != nil {
		return err
	}
	epochMatches := subscription.UsageEpoch == record.UsageEpoch
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return err
	}
	if epochMatches {
		used, ok := boundedSubscriptionQuota(subscription.AmountUsed)
		if !ok {
			return ErrSubscriptionQuotaOverflow
		}
		newUsed := used
		if record.ReservedQuota >= used {
			newUsed = 0
		} else {
			newUsed = used - record.ReservedQuota
		}
		if newUsed != used {
			result := tx.Model(&model.UserSubscription{}).
				Where("id = ? AND user_id = ? AND amount_used = ? AND usage_epoch = ?",
					subscription.Id, record.UserID, subscription.AmountUsed, record.UsageEpoch).
				Updates(map[string]any{"amount_used": int64(newUsed), "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("subscription changed during refund")
			}
		}
	}
	if ledgerPresent {
		result := tx.Model(&model.SubscriptionPreConsumeRecord{}).
			Where("id = ? AND status = ? AND usage_epoch = ?", ledger.Id, SubscriptionPreConsumeStatusConsumed, record.UsageEpoch).
			Updates(map[string]any{"status": SubscriptionPreConsumeStatusRefunded, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("subscription reservation ledger changed during refund")
		}
	}
	return nil
}

func refundRelayTokenTx(tx *gorm.DB, record *model.RelayQuotaReservationRecord) error {
	if record.TokenReserved == 0 {
		return nil
	}
	if record.TokenID <= 0 {
		return ErrTokenNotFound
	}
	if err := validateQuotaAmount(record.TokenReserved); err != nil {
		return err
	}
	var token model.Token
	if err := subscriptionLockForUpdate(tx.Unscoped()).
		Select("id", "user_id", "remain_quota").
		Where("id = ? AND user_id = ?", record.TokenID, record.UserID).
		First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTokenNotFound
		}
		return err
	}
	newRemain, ok := common.AddQuotaWithinBounds(token.RemainQuota, record.TokenReserved)
	if !ok {
		return ErrTokenQuotaOverflow
	}
	result := tx.Unscoped().Model(&model.Token{}).
		Where("id = ? AND user_id = ? AND remain_quota = ?", record.TokenID, record.UserID, token.RemainQuota).
		UpdateColumn("remain_quota", newRemain)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTokenNotFound
	}
	return nil
}

func reverseSettledRelayFundingTx(tx *gorm.DB, record *model.RelayQuotaReservationRecord, now int64) error {
	if record == nil {
		return errors.New("settled relay reservation is nil")
	}
	if err := validateQuotaAmount(record.ActualQuota); err != nil {
		return fmt.Errorf("stored settled quota: %w", err)
	}
	if record.ActualQuota == 0 {
		return nil
	}
	switch record.FundingSource {
	case BillingSourceWallet:
		var user model.User
		if err := subscriptionLockForUpdate(tx).Select("id", "quota").
			Where("id = ?", record.UserID).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrUserNotFound
			}
			return err
		}
		newQuota, ok := common.AddQuotaWithinBounds(user.Quota, record.ActualQuota)
		if !ok {
			return ErrUserQuotaOverflow
		}
		result := tx.Model(&model.User{}).Where("id = ? AND quota = ?", record.UserID, user.Quota).
			UpdateColumn("quota", newQuota)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrUserNotFound
		}
		return nil
	case BillingSourceSubscription:
		return postConsumeUserSubscriptionDeltaEpochTx(
			tx, record.SubscriptionID, -int64(record.ActualQuota), now, &record.UsageEpoch,
		)
	default:
		return fmt.Errorf("unsupported funding source %q", record.FundingSource)
	}
}

func reverseSettledRelayTokenTx(tx *gorm.DB, record *model.RelayQuotaReservationRecord) error {
	if record == nil || record.TokenUnlimited || record.TokenID == 0 || record.ActualQuota == 0 {
		return nil
	}
	if record.TokenID < 0 {
		return ErrTokenNotFound
	}
	var token model.Token
	if err := subscriptionLockForUpdate(tx.Unscoped()).
		Select("id", "user_id", "remain_quota").
		Where("id = ? AND user_id = ?", record.TokenID, record.UserID).
		First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTokenNotFound
		}
		return err
	}
	newRemain, ok := common.AddQuotaWithinBounds(token.RemainQuota, record.ActualQuota)
	if !ok {
		return ErrTokenQuotaOverflow
	}
	result := tx.Unscoped().Model(&model.Token{}).
		Where("id = ? AND user_id = ? AND remain_quota = ?", record.TokenID, record.UserID, token.RemainQuota).
		UpdateColumn("remain_quota", newRemain)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTokenNotFound
	}
	return nil
}

func relayQuotaReservationHasStatus(reservationId, status string) (bool, error) {
	return relayQuotaReservationHasStatusContext(context.Background(), reservationId, status)
}

func relayQuotaReservationHasStatusContext(ctx context.Context, reservationId, status string) (bool, error) {
	var count int64
	err := model.DB.WithContext(ctx).Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ? AND status = ?", reservationId, status).Count(&count).Error
	return count == 1, err
}

func recordRelayQuotaReconciliationFailure(reservationId, owner string, operationErr, verifyErr error) error {
	return recordRelayQuotaReconciliationFailureContext(context.Background(), reservationId, owner, operationErr, verifyErr)
}

func recordRelayQuotaReconciliationFailureContext(ctx context.Context, reservationId, owner string, operationErr, verifyErr error) error {
	combined := errors.Join(operationErr, wrapRelayReservationError("verify terminal marker", verifyErr))
	if combined == nil {
		return nil
	}
	now := common.NowTimestamp()
	var record model.RelayQuotaReservationRecord
	db := model.DB.WithContext(ctx)
	loadErr := db.Where("reservation_id = ?", reservationId).First(&record).Error
	if loadErr != nil {
		result := errors.Join(combined, fmt.Errorf("load failed reservation: %w", loadErr))
		common.SysError("relay quota reconciliation failed: " + result.Error())
		return result
	}
	maxAttempts := relayReservationMaxAttempts()
	attempts := record.Attempts
	if attempts < 1 || attempts > maxAttempts {
		attempts = maxAttempts
	}
	status := record.Status
	nextAttempt := now + relayReservationBackoffSeconds(attempts)
	if attempts >= maxAttempts {
		status = model.RelayQuotaReservationStatusManualReview
		nextAttempt = 0
	}
	persistedError := truncateRelayReservationError(combined.Error())
	updates := map[string]any{
		"status": status, "attempts": attempts, "last_error": persistedError,
		"next_attempt_at": nextAttempt, "lease_owner": "", "lease_expires_at": 0,
		"updated_at": now,
	}
	if status == model.RelayQuotaReservationStatusManualReview {
		updates["completed_at"] = now
	}
	result := db.Model(&model.RelayQuotaReservationRecord{}).
		Where("id = ? AND status = ? AND lease_owner = ?", record.ID, record.Status, owner).
		Updates(updates)
	if result.Error != nil {
		combined = errors.Join(combined, fmt.Errorf("record reconciliation failure: %w", result.Error))
	} else if result.RowsAffected != 1 {
		combined = errors.Join(combined, errors.New("record reconciliation failure: lease lost"))
	}
	common.SysError(fmt.Sprintf(
		"relay quota reservation %s reconciliation failed (attempt=%d status=%s): %s",
		reservationId, attempts, status, persistedError,
	))
	return combined
}

func wrapRelayReservationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func markFundingSessionSettled(funding *FundingSession, actual int) {
	if funding == nil {
		return
	}
	funding.mu.Lock()
	defer funding.mu.Unlock()
	funding.settled = true
	if funding.source == BillingSourceSubscription && validateQuotaAmount(actual) == nil && validateQuotaAmount(funding.reserved) == nil {
		funding.postDelta = quotaDeltaByComparison(actual, funding.reserved)
	}
}

func markFundingSessionRefunded(funding *FundingSession) {
	if funding == nil {
		return
	}
	funding.mu.Lock()
	funding.refunded = true
	funding.mu.Unlock()
}

// ReconcileRelayQuotaReservations retries due durable operations and expires
// unused reservations. It is safe for concurrent nodes: every operation is
// claimed with a short lease and accounting commits with its terminal marker.
func ReconcileRelayQuotaReservations() error {
	return ReconcileRelayQuotaReservationsContext(context.Background())
}

// ReconcileRelayQuotaReservationsContext is the cancellable maintenance form.
// Every scan/update and production accounting transaction inherits ctx, and
// cancellation is checked between independently leased records.
func ReconcileRelayQuotaReservationsContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("relay quota reconciliation context is nil")
	}
	db := model.DB.WithContext(ctx)
	now, err := model.DatabaseUnixTimestamp(db)
	if err != nil {
		return err
	}
	return reconcileRelayQuotaReservationsAtContext(ctx, now, defaultRelayReservationBatchSize)
}

func reconcileRelayQuotaReservationsAt(now int64, limit int) error {
	return reconcileRelayQuotaReservationsAtContext(context.Background(), now, limit)
}

func reconcileRelayQuotaReservationsAtContext(ctx context.Context, now int64, limit int) error {
	if ctx == nil {
		return errors.New("relay quota reconciliation context is nil")
	}
	if limit <= 0 {
		limit = defaultRelayReservationBatchSize
	}
	if err := expireHeldRelayQuotaReservationsContext(ctx, now); err != nil {
		return err
	}
	if err := markExhaustedRelayQuotaReservationsContext(ctx, now); err != nil {
		return err
	}
	var records []model.RelayQuotaReservationRecord
	if err := model.DB.WithContext(ctx).Where(
		"status IN ? AND next_attempt_at <= ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
		[]string{
			model.RelayQuotaReservationStatusPendingSettlement,
			model.RelayQuotaReservationStatusPendingRefund,
		},
		now,
		now,
	).Order("next_attempt_at asc, id asc").Limit(limit).Find(&records).Error; err != nil {
		return err
	}
	errs := make([]error, 0)
	for index := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		record := records[index]
		reservation, err := relayReservationFromRecord(&record)
		if err != nil {
			err = markInvalidRelayQuotaReservationForReviewContext(ctx, &record, err, now)
		} else {
			switch record.Status {
			case model.RelayQuotaReservationStatusPendingSettlement:
				err = reconcileRelayQuotaSettlementAtContext(ctx, reservation, true, now)
			case model.RelayQuotaReservationStatusPendingRefund:
				err = reconcileRelayQuotaRefundAtContext(ctx, reservation, true, now)
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("reconcile reservation %s: %w", record.ReservationID, err))
		}
	}
	return errors.Join(errs...)
}

func markExhaustedRelayQuotaReservations(now int64) error {
	return markExhaustedRelayQuotaReservationsContext(context.Background(), now)
}

func markExhaustedRelayQuotaReservationsContext(ctx context.Context, now int64) error {
	maxAttempts := relayReservationMaxAttempts()
	reason := "automatic relay quota reconciliation attempt limit reached; reservation state and any holds retained for manual review"
	result := model.DB.WithContext(ctx).Model(&model.RelayQuotaReservationRecord{}).
		Where("status IN ? AND (attempts < 0 OR attempts >= ?) AND (lease_expires_at = 0 OR lease_expires_at <= ?)", []string{
			model.RelayQuotaReservationStatusPendingSettlement,
			model.RelayQuotaReservationStatusPendingRefund,
		}, maxAttempts, now).
		Updates(map[string]any{
			"status":          model.RelayQuotaReservationStatusManualReview,
			"next_attempt_at": 0, "lease_owner": "", "lease_expires_at": 0,
			"last_error": reason, "updated_at": now, "completed_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		common.SysError(fmt.Sprintf(
			"moved %d exhausted relay quota reservations to manual review",
			result.RowsAffected,
		))
	}
	return nil
}

func markInvalidRelayQuotaReservationForReview(
	record *model.RelayQuotaReservationRecord,
	validationErr error,
	now int64,
) error {
	return markInvalidRelayQuotaReservationForReviewContext(context.Background(), record, validationErr, now)
}

func markInvalidRelayQuotaReservationForReviewContext(
	ctx context.Context,
	record *model.RelayQuotaReservationRecord,
	validationErr error,
	now int64,
) error {
	message := truncateRelayReservationError("invalid durable reservation: " + validationErr.Error())
	result := model.DB.WithContext(ctx).Model(&model.RelayQuotaReservationRecord{}).
		Where("id = ? AND status = ?", record.ID, record.Status).
		Updates(map[string]any{
			"status":     model.RelayQuotaReservationStatusManualReview,
			"last_error": message, "next_attempt_at": 0,
			"lease_owner": "", "lease_expires_at": 0, "updated_at": now, "completed_at": now,
		})
	if result.Error != nil {
		return errors.Join(validationErr, result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.Join(validationErr, ErrRelayQuotaReservationBusy)
	}
	common.SysError(fmt.Sprintf("relay quota reservation %s requires manual review: %s", record.ReservationID, message))
	return validationErr
}

func expireHeldRelayQuotaReservations(now int64) error {
	return expireHeldRelayQuotaReservationsContext(context.Background(), now)
}

func expireHeldRelayQuotaReservationsContext(ctx context.Context, now int64) error {
	refundReason := "reservation expired before upstream dispatch; queued for automatic refund"
	heldQuery := model.DB.WithContext(ctx).Model(&model.RelayQuotaReservationRecord{}).
		Where("status = ? AND expires_at > 0 AND expires_at <= ?", model.RelayQuotaReservationStatusHeld, now)
	heldQuery = excludeActiveJimengReservations(heldQuery, now)
	heldQuery = excludeActiveTaskReservations(heldQuery, now)
	refundResult := heldQuery.
		Updates(map[string]any{
			"status":          model.RelayQuotaReservationStatusPendingRefund,
			"operation":       model.RelayQuotaReservationOperationRefund,
			"actual_quota":    0,
			"next_attempt_at": now,
			"last_error":      refundReason,
			"updated_at":      now,
		})
	var errs []error
	if refundResult.Error != nil {
		errs = append(errs, fmt.Errorf("expire undispatched relay reservations: %w", refundResult.Error))
	}
	if refundResult.RowsAffected > 0 {
		common.SysError(fmt.Sprintf("queued %d expired undispatched relay quota reservations for refund", refundResult.RowsAffected))
	}

	// A dispatched request may have been accepted or partially streamed before
	// the process died. Its authoritative usage is unknowable, so automatic
	// refund would create free work and automatic settlement could charge a
	// still-live long request. Retain the reservation state and any holds, and
	// make the condition explicit for an operator. ActualQuota already contains
	// the conservative dispatch fallback: the hold for normal rows or the
	// requested estimate for an explicitly trusted zero-hold row.
	manualReason := "dispatched reservation expired without authoritative usage; accounting state and any holds retained for manual upstream audit and settlement or refund"
	dispatchedQuery := model.DB.WithContext(ctx).Model(&model.RelayQuotaReservationRecord{}).
		Where("status = ? AND expires_at > 0 AND expires_at <= ?", model.RelayQuotaReservationStatusDispatched, now)
	dispatchedQuery = excludeActiveJimengReservations(dispatchedQuery, now)
	dispatchedQuery = excludeActiveTaskReservations(dispatchedQuery, now)
	manualResult := dispatchedQuery.
		Updates(map[string]any{
			"status":           model.RelayQuotaReservationStatusManualReview,
			"operation":        model.RelayQuotaReservationOperationSettle,
			"next_attempt_at":  0,
			"lease_owner":      "",
			"lease_expires_at": 0,
			"last_error":       manualReason,
			"updated_at":       now,
			"completed_at":     now,
		})
	if manualResult.Error != nil {
		errs = append(errs, fmt.Errorf("expire dispatched relay reservations: %w", manualResult.Error))
	}
	if manualResult.RowsAffected > 0 {
		common.SysError(fmt.Sprintf(
			"moved %d expired dispatched relay quota reservations to manual review with accounting state retained",
			manualResult.RowsAffected,
		))
	}
	return errors.Join(errs...)
}

func excludeActiveJimengReservations(query *gorm.DB, now int64) *gorm.DB {
	if query == nil || !query.Migrator().HasTable(&model.JimengTaskOperation{}) {
		return query
	}
	retentionDays := common.GetEnvInt("JIMENG_RECOVERY_RETENTION_DAYS", 7)
	if retentionDays < 1 || retentionDays > 90 {
		retentionDays = 7
	}
	manualReviewCutoff := now - int64(retentionDays*24*60*60)
	return query.Where(
		"NOT EXISTS (SELECT 1 FROM jimeng_task_operations WHERE jimeng_task_operations.reservation_id = relay_quota_reservations.reservation_id AND (jimeng_task_operations.state IN ? OR (jimeng_task_operations.state = ? AND jimeng_task_operations.completed_at > ?)))",
		[]string{
			model.JimengTaskOperationPrepared,
			model.JimengTaskOperationDispatching,
			model.JimengTaskOperationSubmitted,
		},
		model.JimengTaskOperationManualReview,
		manualReviewCutoff,
	)
}

func excludeActiveTaskReservations(query *gorm.DB, now int64) *gorm.DB {
	if query == nil || !query.Migrator().HasTable(&model.TaskOperation{}) {
		return query
	}
	retentionDays := common.GetEnvInt("TASK_RECOVERY_RETENTION_DAYS", 7)
	if retentionDays < 1 || retentionDays > 90 {
		retentionDays = 7
	}
	manualReviewCutoff := now - int64(retentionDays*24*60*60)
	return query.Where(
		"NOT EXISTS (SELECT 1 FROM task_operations WHERE task_operations.reservation_id = relay_quota_reservations.reservation_id AND (task_operations.state IN ? OR (task_operations.state = ? AND task_operations.completed_at > ?)))",
		[]string{
			model.TaskOperationPrepared,
			model.TaskOperationDispatching,
			model.TaskOperationSubmitted,
		},
		model.TaskOperationManualReview,
		manualReviewCutoff,
	)
}

func truncateRelayReservationError(message string) string {
	message = strings.ToValidUTF8(message, "�")
	if len(message) <= maxRelayReservationErrorBytes {
		return message
	}
	const suffix = "... [truncated]"
	cut := maxRelayReservationErrorBytes - len(suffix)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + suffix
}

func relayReservationHoldSeconds() int64 {
	seconds := int64(common.GetEnvInt("RELAY_RESERVATION_HOLD_SECONDS", int(defaultRelayReservationHoldSeconds)))
	if seconds < 60 || seconds > maxRelayReservationHoldSeconds {
		return defaultRelayReservationHoldSeconds
	}
	return seconds
}

func relayReservationLeaseSeconds() int64 {
	seconds := int64(common.GetEnvInt("RELAY_RESERVATION_LEASE_SECONDS", int(defaultRelayReservationLeaseSeconds)))
	if seconds < 1 || seconds > maxRelayReservationLeaseSeconds {
		return defaultRelayReservationLeaseSeconds
	}
	return seconds
}

func relayReservationMaxAttempts() int {
	attempts := common.GetEnvInt("RELAY_RESERVATION_MAX_ATTEMPTS", defaultRelayReservationMaxAttempts)
	if attempts < 1 || attempts > maxRelayReservationAttempts {
		return defaultRelayReservationMaxAttempts
	}
	return attempts
}

func relayReservationBackoffSeconds(attempt int) int64 {
	if attempt < 1 {
		return 1
	}
	shift := attempt - 1
	if shift > 8 {
		shift = 8
	}
	seconds := int64(1) << shift
	if seconds > maxRelayReservationBackoffSeconds {
		return maxRelayReservationBackoffSeconds
	}
	return seconds
}
