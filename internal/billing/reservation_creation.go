package billing

import (
	"errors"
	"fmt"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
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
	if policy.trustQuota <= 0 || !quotamath.QuotaWithinBounds(policy.trustQuota) {
		policy.allowTrustQuotaBypass = false
		policy.trustQuota = 0
	}
	if policy.freeModel && quota != 0 {
		return nil, errors.New("free-model reservation requires zero quota")
	}

	reservationId, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, err
	}
	now := wallclock.NowTimestamp()
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
			return nil, false, userssvc.ErrUserNotFound
		}
		return nil, false, fmt.Errorf("load billing preference: %w", err)
	}
	settings := userssvc.ParseUserSettings(settingsUser.Setting)
	pref := userssvc.NormalizeBillingPreference(settings.BillingPreference)
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
	case userssvc.BillingPreferenceSubscriptionOnly:
		session, _, _, err := trySubscription()
		return session, false, err
	case userssvc.BillingPreferenceWalletOnly:
		return tryWallet()
	case userssvc.BillingPreferenceWalletFirst:
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
	if err := locking.SubscriptionLockForUpdate(tx).Select("id", "quota").First(&user, userId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, userssvc.ErrUserNotFound
		}
		return false, err
	}
	if !quotamath.QuotaWithinBounds(user.Quota) {
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
	if err := locking.SubscriptionLockForUpdate(tx).
		Select("id", "user_id", "remain_quota", "unlimited_quota").
		Where("id = ? AND user_id = ?", token.Id, userId).
		First(&liveToken).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	if !liveToken.UnlimitedQuota && !quotamath.QuotaWithinBounds(liveToken.RemainQuota) {
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
	if err := locking.SubscriptionLockForUpdate(tx).
		Select("id", "user_id", "remain_quota").
		Where("id = ? AND user_id = ?", tokenId, userId).First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTokenNotFound
		}
		return err
	}
	if !quotamath.QuotaWithinBounds(token.RemainQuota) {
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
