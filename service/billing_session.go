package service

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Funding sources for a relay request.
const (
	BillingSourceWallet       = "wallet"
	BillingSourceSubscription = "subscription"
	// BillingSourceFreeModel is the durable marker for a deliberately
	// un-funded zero-price request. It is distinct from a wallet reservation so
	// recovery cannot mistake a missing hold for a valid free-model decision.
	BillingSourceFreeModel = "free_model"
)

// ErrSubscriptionQuotaOverflow reports corrupt or out-of-domain subscription
// counters that cannot be reconciled without risking integer wraparound.
var ErrSubscriptionQuotaOverflow = errors.New("subscription quota overflow")

// ErrChannelUsageOverflow reports a corrupt or saturated channel lifetime
// usage counter. Channel usage is committed in the same transaction as user
// and token accounting so a successful settlement cannot omit provider cost.
var ErrChannelUsageOverflow = errors.New("channel usage overflow")

// FundingSession funds one relay request's quota reservation from either the
// user's wallet balance or an active subscription, per the user's billing
// preference, and carries the state needed to settle or refund it exactly
// once.
type FundingSession struct {
	mu     sync.Mutex
	userId int
	// requestId keys the subscription pre-consume ledger. It is always
	// generated server-side: the inbound X-Request-Id header is
	// client-controlled, and honoring it would let clients replay an id to
	// turn the ledger's idempotency into free requests.
	requestId     string
	source        string
	rawPreference string
	reserved      int // wallet reservation or subscription pre-consume amount
	settled       bool
	refunded      bool

	subscriptionId int
	usageEpoch     int64
	subPlanId      int
	subPlanTitle   string
	subTotal       int64
	subUsedAfter   int64
	postDelta      int64
}

// FundingReservation is the durable minimum needed to resume settlement after
// a provider accepted work but the original accounting transaction failed.
type FundingReservation struct {
	Source         string
	Reserved       int
	RequestId      string
	SubscriptionId int
	UsageEpoch     int64
}

type fundingTransactionRunner func(func(tx *gorm.DB) error) error

// IsSubscriptionFundingErr classifies subscription pre-consume failures
// (missing or exhausted subscription) so callers can map them to the
// subscription-specific relay error. The reference contract classifies by
// message content.
func IsSubscriptionFundingErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no active subscription") ||
		strings.Contains(msg, "subscription quota insufficient")
}

// NewFundingSession reserves quota for a request from the funding source the
// user's billing preference selects, falling back between subscription and
// wallet where the preference allows it.
func NewFundingSession(userId int, reserved int) (*FundingSession, error) {
	if err := validateQuotaAmount(reserved); err != nil {
		return nil, fmt.Errorf("funding reservation: %w", err)
	}
	settings, err := loadUserSettings(userId)
	if err != nil {
		return nil, fmt.Errorf("load billing preference: %w", err)
	}
	pref := NormalizeBillingPreference(settings.BillingPreference)

	tryWallet := func() (*FundingSession, error) {
		if err := PreConsumeUserQuota(userId, reserved); err != nil {
			return nil, err
		}
		return &FundingSession{
			userId:        userId,
			source:        BillingSourceWallet,
			rawPreference: settings.BillingPreference,
			reserved:      reserved,
		}, nil
	}

	trySubscription := func() (*FundingSession, error) {
		// The ledger requires amount > 0 to create the idempotency record and
		// lock the subscription, so zero-cost estimates still reserve 1.
		subConsume := int64(reserved)
		if subConsume <= 0 {
			subConsume = 1
		}
		requestID, err := common.SecureRandomUUID()
		if err != nil {
			return nil, err
		}
		s := &FundingSession{
			userId:        userId,
			requestId:     requestID,
			source:        BillingSourceSubscription,
			rawPreference: settings.BillingPreference,
			reserved:      int(subConsume),
		}
		res, err := PreConsumeUserSubscription(s.requestId, userId, subConsume)
		if err != nil {
			return nil, err
		}
		s.subscriptionId = res.UserSubscriptionId
		s.usageEpoch = res.UsageEpoch
		s.subTotal = res.AmountTotal
		s.subUsedAfter = res.AmountUsedAfter
		// Plan info is log decoration only; both fields resolve or neither.
		var sub model.UserSubscription
		if err := model.DB.Where("id = ?", res.UserSubscriptionId).First(&sub).Error; err == nil {
			if plan, planErr := GetSubscriptionPlan(sub.PlanId); planErr == nil {
				s.subPlanId = sub.PlanId
				s.subPlanTitle = plan.Title
			}
		}
		return s, nil
	}

	switch pref {
	case BillingPreferenceSubscriptionOnly:
		return trySubscription()
	case BillingPreferenceWalletOnly:
		return tryWallet()
	case BillingPreferenceWalletFirst:
		session, err := tryWallet()
		if err != nil {
			if errors.Is(err, ErrInsufficientQuota) {
				return trySubscription()
			}
			return nil, err
		}
		return session, nil
	default: // subscription_first
		hasSub, err := HasActiveUserSubscription(userId)
		if err != nil {
			return nil, err
		}
		if !hasSub {
			return tryWallet()
		}
		session, err := trySubscription()
		if err != nil {
			if IsSubscriptionFundingErr(err) {
				// Fall back to the wallet only when every active subscription
				// permits wallet overflow.
				allowOverflow, overflowErr := UserActiveSubscriptionsAllowWalletOverflow(userId)
				if overflowErr != nil {
					return nil, overflowErr
				}
				if allowOverflow {
					return tryWallet()
				}
			}
			return nil, err
		}
		return session, nil
	}
}

// Settle commits the actual usage against the funding source and records the
// user's lifetime usage counters. Idempotent; a refunded session never
// settles.
func (s *FundingSession) Settle(actual int) error {
	if s == nil {
		return nil
	}
	return s.commitReservedUsage(actual, 0, 0, false, 0, nil, true)
}

// CommitAcceptedPerCall atomically persists an accepted asynchronous task and
// settles its exact per-call funding and token reservations. The persistence
// callback must return alreadyCommitted when its durable task marker proves a
// prior transaction committed; this makes a retry safe after an ambiguous
// database commit result.
func (s *FundingSession) CommitAcceptedPerCall(
	actual, tokenId int,
	persist func(tx *gorm.DB) (alreadyCommitted bool, err error),
) error {
	return s.CommitAcceptedPerCallWithAccounting(actual, tokenId, false, 0, persist)
}

// CommitAcceptedPerCallWithAccounting is the full asynchronous-task
// settlement primitive. Unlimited tokens still accrue used_quota, while their
// remain_quota is untouched; channel usage commits atomically with the task,
// user, token, and funding records.
func (s *FundingSession) CommitAcceptedPerCallWithAccounting(
	actual, tokenId int,
	tokenUnlimited bool,
	channelId int,
	persist func(tx *gorm.DB) (alreadyCommitted bool, err error),
) error {
	tokenReserved := actual
	if tokenUnlimited {
		tokenReserved = 0
	}
	return s.commitReservedUsage(
		actual, tokenId, tokenReserved, tokenUnlimited, channelId, persist, false,
	)
}

// CommitReservedUsage settles a user funding reservation together with a
// possibly different token reservation. It is intended for bounded streaming
// sessions, where an estimate is reserved before the upstream connection and
// authoritative usage becomes available later.
func (s *FundingSession) CommitReservedUsage(actual, tokenId, tokenReserved int) error {
	return s.CommitReservedUsageWithAccounting(actual, tokenId, tokenReserved, false, 0)
}

// CommitReservedUsageWithAccounting settles a streaming reservation and its
// provider accounting as one durable operation.
func (s *FundingSession) CommitReservedUsageWithAccounting(
	actual, tokenId, tokenReserved int,
	tokenUnlimited bool,
	channelId int,
) error {
	return s.commitReservedUsage(
		actual, tokenId, tokenReserved, tokenUnlimited, channelId, nil, false,
	)
}

func (s *FundingSession) commitReservedUsage(
	actual, tokenId, tokenReserved int,
	tokenUnlimited bool,
	channelId int,
	persist func(tx *gorm.DB) (alreadyCommitted bool, err error),
	terminalStateIsNoop bool,
) error {
	return s.commitReservedUsageWithTransaction(
		actual, tokenId, tokenReserved, tokenUnlimited, channelId, persist, terminalStateIsNoop,
		func(fn func(tx *gorm.DB) error) error { return model.DB.Transaction(fn) },
	)
}

func (s *FundingSession) commitReservedUsageWithTransaction(
	actual, tokenId, tokenReserved int,
	tokenUnlimited bool,
	channelId int,
	persist func(tx *gorm.DB) (alreadyCommitted bool, err error),
	terminalStateIsNoop bool,
	transact fundingTransactionRunner,
) error {
	if s == nil {
		return errors.New("funding session is nil")
	}
	if transact == nil {
		return errors.New("funding transaction runner is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if terminalStateIsNoop && (s.refunded || s.settled) {
		return nil
	}
	if err := validateQuotaAmount(actual); err != nil {
		return fmt.Errorf("actual quota: %w", err)
	}
	if err := validateQuotaAmount(tokenReserved); err != nil {
		return fmt.Errorf("token reservation: %w", err)
	}
	if err := validateQuotaAmount(s.reserved); err != nil {
		return fmt.Errorf("funding reservation: %w", err)
	}
	if tokenReserved > 0 && tokenId <= 0 {
		return errors.New("token reservation requires a token")
	}
	// Dashboard Playground requests intentionally use a synthetic unlimited
	// token with ID zero. Persistent API tokens always carry a positive ID and
	// are accounted below; the synthetic token has no token row to update.
	if tokenUnlimited && tokenReserved != 0 {
		return errors.New("unlimited token accounting requires an unreserved token")
	}
	if channelId < 0 {
		return errors.New("invalid settlement channel")
	}
	if s.refunded {
		if terminalStateIsNoop {
			return nil
		}
		return errors.New("funding session was refunded")
	}
	if s.settled {
		return nil
	}

	delta := quotaDeltaByComparison(actual, s.reserved)
	err := transact(func(tx *gorm.DB) error {
		alreadyCommitted := false
		if persist != nil {
			var err error
			alreadyCommitted, err = persist(tx)
			if err != nil {
				return err
			}
		}
		if alreadyCommitted {
			return nil
		}

		switch s.source {
		case BillingSourceSubscription:
			now, clockErr := model.DatabaseUnixTimestamp(tx)
			if clockErr != nil {
				return clockErr
			}
			terminalStatus, ledgerErr := settleSubscriptionPreConsumeTx(
				tx, s.requestId, s.userId, s.subscriptionId, s.reserved, s.usageEpoch, now,
			)
			if ledgerErr != nil {
				return ledgerErr
			}
			switch terminalStatus {
			case SubscriptionPreConsumeStatusSettled:
				return nil
			case SubscriptionPreConsumeStatusRefunded:
				if terminalStateIsNoop {
					return nil
				}
				return errors.New("funding session was refunded")
			}
			if delta != 0 {
				if err := settleFundingSubscriptionDeltaTx(tx, s.userId, s.subscriptionId, s.usageEpoch, delta, now); err != nil {
					if !(delta < 0 && errors.Is(err, gorm.ErrRecordNotFound)) {
						return err
					}
				}
			}
		case BillingSourceWallet:
		case BillingSourceFreeModel:
			if s.reserved != 0 || actual != 0 || tokenReserved != 0 {
				return errors.New("free-model funding requires zero accounting")
			}
		default:
			return fmt.Errorf("unsupported funding source %q", s.source)
		}

		var user model.User
		if err := subscriptionLockForUpdate(tx).
			Select("id", "quota", "used_quota", "request_count").First(&user, s.userId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrUserNotFound
			}
			return err
		}
		newUsedQuota, usageOK := common.AddQuotaWithinBounds(user.UsedQuota, actual)
		newRequestCount, countOK := common.AddQuotaWithinBounds(user.RequestCount, 1)
		if !usageOK || !countOK {
			return ErrUserUsageOverflow
		}
		userUpdates := map[string]any{
			"used_quota":    newUsedQuota,
			"request_count": newRequestCount,
		}
		userQuery := tx.Model(&model.User{}).
			Where("id = ? AND used_quota = ? AND request_count = ?", s.userId, user.UsedQuota, user.RequestCount)
		if s.source == BillingSourceWallet {
			if !common.QuotaWithinBounds(user.Quota) {
				return ErrUserQuotaOverflow
			}
			newQuota := user.Quota
			switch {
			case actual > s.reserved:
				extraCharge := actual - s.reserved
				if user.Quota < extraCharge {
					return ErrInsufficientQuota
				}
				newQuota = user.Quota - extraCharge
			case s.reserved > actual:
				refund := s.reserved - actual
				var ok bool
				newQuota, ok = common.AddQuotaWithinBounds(user.Quota, refund)
				if !ok {
					return ErrUserQuotaOverflow
				}
			}
			userUpdates["quota"] = newQuota
			userQuery = userQuery.Where("quota = ?", user.Quota)
		}
		userResult := userQuery.Updates(userUpdates)
		if userResult.Error != nil {
			return userResult.Error
		}
		if userResult.RowsAffected == 0 {
			return ErrUserUsageOverflow
		}
		if tokenId > 0 && (actual > 0 || tokenReserved > 0) {
			var token model.Token
			if err := subscriptionLockForUpdate(tx.Unscoped()).
				Select("id", "user_id", "remain_quota", "used_quota", "unlimited_quota").
				Where("id = ? AND user_id = ?", tokenId, s.userId).First(&token).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrTokenNotFound
				}
				return err
			}
			newTokenUsed, usedOK := common.AddQuotaWithinBounds(token.UsedQuota, actual)
			if !usedOK || (!tokenUnlimited && !common.QuotaWithinBounds(token.RemainQuota)) {
				return ErrTokenQuotaOverflow
			}
			newRemain := token.RemainQuota
			tokenQuery := tx.Unscoped().Model(&model.Token{}).
				Where("id = ? AND user_id = ? AND used_quota = ?",
					tokenId, s.userId, token.UsedQuota)
			updates := map[string]any{
				"used_quota": newTokenUsed,
			}
			if !tokenUnlimited {
				tokenQuery = tokenQuery.Where("remain_quota = ?", token.RemainQuota)
				switch {
				case actual > tokenReserved:
					extraCharge := actual - tokenReserved
					if token.RemainQuota < extraCharge {
						return ErrInsufficientTokenQuota
					}
					newRemain = token.RemainQuota - extraCharge
				case tokenReserved > actual:
					refund := tokenReserved - actual
					var ok bool
					newRemain, ok = common.AddQuotaWithinBounds(token.RemainQuota, refund)
					if !ok {
						return ErrTokenQuotaOverflow
					}
				}
				updates["remain_quota"] = newRemain
			}
			tokenResult := tokenQuery.Updates(updates)
			if tokenResult.Error != nil {
				return tokenResult.Error
			}
			if tokenResult.RowsAffected == 0 {
				if actual > tokenReserved {
					return ErrInsufficientTokenQuota
				}
				return ErrTokenQuotaOverflow
			}
		}
		if err := incrementChannelUsedQuotaTx(tx, channelId, actual); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.settled = true
	if s.source == BillingSourceSubscription {
		s.postDelta = delta
	}
	return nil
}

func incrementChannelUsedQuotaTx(tx *gorm.DB, channelId, actual int) error {
	if channelId == 0 {
		return nil
	}
	if channelId < 0 {
		return errors.New("invalid settlement channel")
	}
	var channel model.Channel
	if err := subscriptionLockForUpdate(tx).
		Select("id", "used_quota").First(&channel, channelId).Error; err != nil {
		// A channel can be removed after an upstream request was accepted but
		// before its durable settlement is replayed.  There is no channel row
		// left to account against in that case, and blocking the user/token
		// settlement would strand a real charge indefinitely.
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if channel.UsedQuota < 0 || int64(actual) > math.MaxInt64-channel.UsedQuota {
		return ErrChannelUsageOverflow
	}
	newUsed := channel.UsedQuota + int64(actual)
	if newUsed == channel.UsedQuota {
		return nil
	}
	result := tx.Model(&model.Channel{}).
		Where("id = ? AND used_quota = ?", channelId, channel.UsedQuota).
		UpdateColumn("used_quota", newUsed)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("channel usage changed during settlement")
	}
	return nil
}

// quotaDeltaByComparison avoids subtracting two signed values until their
// order is known. The inputs have already passed validateQuotaAmount, so the
// magnitude always fits both the machine int and the persisted quota domain.
func quotaDeltaByComparison(actual, reserved int) int64 {
	switch {
	case actual > reserved:
		return int64(actual - reserved)
	case reserved > actual:
		return -int64(reserved - actual)
	default:
		return 0
	}
}

func boundedSubscriptionQuota(value int64) (int, bool) {
	if value < 0 || value > common.MaxQuota {
		return 0, false
	}
	converted := int(value)
	return converted, common.QuotaWithinBounds(converted)
}

// applySubscriptionQuotaDelta applies a signed adjustment without ever
// negating MinInt64 or adding two potentially corrupt persisted values.
func applySubscriptionQuotaDelta(value, delta int64) (int64, bool) {
	quota, ok := boundedSubscriptionQuota(value)
	if !ok {
		return 0, false
	}
	switch {
	case delta > 0:
		if delta > common.MaxQuota {
			return 0, false
		}
		updated, ok := common.AddQuotaWithinBounds(quota, int(delta))
		return int64(updated), ok
	case delta < 0:
		if delta < -common.MaxQuota {
			return 0, false
		}
		refund := int(-delta)
		if refund >= quota {
			return 0, true
		}
		return int64(quota - refund), true
	default:
		return int64(quota), true
	}
}

// settleFundingSubscriptionDeltaTx is the funding-session-specific checked
// form of subscription reconciliation. The legacy shared helper accepts the
// full int64 range and adds delta directly; a corrupt MaxInt counter can wrap
// before the total is enforced. Funding reservations are quota-domain values,
// so this path validates the persisted snapshot and writes a compare-and-swap
// result without database-side arithmetic.
func settleFundingSubscriptionDeltaTx(tx *gorm.DB, userId, subscriptionId int, expectedEpoch, delta int64, now int64) error {
	var subscription model.UserSubscription
	if err := subscriptionLockForUpdate(tx).
		Where("id = ? AND user_id = ?", subscriptionId, userId).
		First(&subscription).Error; err != nil {
		return err
	}
	if subscription.UsageEpoch != expectedEpoch {
		return nil
	}
	used, usedOK := boundedSubscriptionQuota(subscription.AmountUsed)
	total, totalOK := boundedSubscriptionQuota(subscription.AmountTotal)
	if !usedOK || !totalOK {
		return fmt.Errorf("%w: subscription=%d used=%d total=%d",
			ErrSubscriptionQuotaOverflow, subscriptionId, subscription.AmountUsed, subscription.AmountTotal)
	}

	newUsed := used
	switch {
	case delta > 0:
		if delta > common.MaxQuota {
			return ErrSubscriptionQuotaOverflow
		}
		var ok bool
		newUsed, ok = common.AddQuotaWithinBounds(used, int(delta))
		if !ok {
			return ErrSubscriptionQuotaOverflow
		}
	case delta < 0:
		if delta < -common.MaxQuota {
			return ErrSubscriptionQuotaOverflow
		}
		refund := int(-delta)
		if refund >= used {
			newUsed = 0
		} else {
			newUsed = used - refund
		}
	}
	if total > 0 && newUsed > total {
		return fmt.Errorf("subscription used exceeds total, used=%d total=%d", newUsed, total)
	}
	if newUsed == used {
		return nil
	}
	result := tx.Model(&model.UserSubscription{}).
		Where("id = ? AND user_id = ? AND amount_used = ? AND amount_total = ? AND usage_epoch = ?",
			subscriptionId, userId, subscription.AmountUsed, subscription.AmountTotal, expectedEpoch).
		Updates(map[string]any{"amount_used": int64(newUsed), "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("subscription %d changed during settlement", subscriptionId)
	}
	return nil
}

// Refund returns the reservation after a failed request. Idempotent at the
// session level; the wallet refund is a non-idempotent quota increment and is
// never retried, while the subscription refund is ledger-idempotent and
// retries on transient failures.
func (s *FundingSession) Refund() {
	if err := s.RefundChecked(); err != nil {
		common.SysError("funding refund failed for user " + common.Int2Str(s.userId) + ": " + err.Error())
	}
}

// RefundChecked returns the reservation and reports failures to callers that
// must make cleanup outcomes explicit. Like Refund, it is attempted at most
// once because a wallet increment is not safely replayable after an ambiguous
// database result.
func (s *FundingSession) RefundChecked() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled || s.refunded {
		return nil
	}
	s.refunded = true
	if s.source == BillingSourceSubscription {
		if s.reserved > 0 {
			if err := refundWithRetry(func() error {
				return RefundSubscriptionPreConsume(s.requestId)
			}); err != nil {
				return err
			}
		}
		return nil
	}
	if s.reserved > 0 {
		return RefundUserQuota(s.userId, s.reserved)
	}
	return nil
}

// refundWithRetry retries a refund a few times with backoff. Only safe for
// ledger-idempotent refunds (never the wallet increment).
func refundWithRetry(fn func() error) error {
	const maxAttempts = 3
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if i < maxAttempts-1 {
			time.Sleep(time.Duration(200*(i+1)) * time.Millisecond)
		}
	}
	return lastErr
}

// BillingLogFields returns the billing keys recorded in the consume log's
// `other` payload, matching the reference key set.
func (s *FundingSession) BillingLogFields() map[string]any {
	fields := map[string]any{}
	if s == nil {
		return fields
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.source != "" {
		fields["billing_source"] = s.source
	}
	// The raw stored preference (not normalized), only when the user set one.
	if s.rawPreference != "" {
		fields["billing_preference"] = s.rawPreference
	}
	if s.source != BillingSourceSubscription {
		if s.source == BillingSourceFreeModel {
			fields["free_model"] = true
		}
		return fields
	}
	if s.subscriptionId != 0 {
		fields["subscription_id"] = s.subscriptionId
	}
	preConsumed := int64(s.reserved)
	if preConsumed > 0 {
		fields["subscription_pre_consumed"] = preConsumed
	}
	if s.postDelta != 0 {
		fields["subscription_post_delta"] = s.postDelta
	}
	if s.subPlanId != 0 {
		fields["subscription_plan_id"] = s.subPlanId
	}
	if s.subPlanTitle != "" {
		fields["subscription_plan_title"] = s.subPlanTitle
	}
	consumed, consumedOK := applySubscriptionQuotaDelta(preConsumed, s.postDelta)
	if s.subTotal > 0 {
		total, totalOK := boundedSubscriptionQuota(s.subTotal)
		usedFinal, usedOK := applySubscriptionQuotaDelta(s.subUsedAfter, s.postDelta)
		if totalOK && usedOK {
			remain := int64(total) - usedFinal
			if remain < 0 {
				remain = 0
			}
			fields["subscription_total"] = int64(total)
			fields["subscription_used"] = usedFinal
			fields["subscription_remain"] = remain
		} else {
			common.SysError(fmt.Sprintf(
				"subscription billing log quota overflow for user %d: used=%d total=%d delta=%d",
				s.userId, s.subUsedAfter, s.subTotal, s.postDelta,
			))
		}
	}
	if consumedOK && consumed > 0 {
		fields["subscription_consumed"] = consumed
	} else if !consumedOK {
		common.SysError(fmt.Sprintf(
			"subscription billing log consumed quota overflow for user %d: reserved=%d delta=%d",
			s.userId, s.reserved, s.postDelta,
		))
	}
	// Wallet quota is untouched when the subscription funds the request.
	fields["wallet_quota_deducted"] = 0
	return fields
}

// Reservation returns the durable accounting coordinates for this reservation.
func (s *FundingSession) Reservation() FundingReservation {
	if s == nil {
		return FundingReservation{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return FundingReservation{
		Source: s.source, Reserved: s.reserved, RequestId: s.requestId,
		SubscriptionId: s.subscriptionId, UsageEpoch: s.usageEpoch,
	}
}

// RestoreFundingSession reconstructs an unsettled reservation so a later
// owner-scoped task fetch can finish accounting that failed after acceptance.
func RestoreFundingSession(userId int, reservation FundingReservation) (*FundingSession, error) {
	if userId <= 0 {
		return nil, errors.New("invalid funding user")
	}
	if err := validateQuotaAmount(reservation.Reserved); err != nil {
		return nil, fmt.Errorf("invalid reserved quota: %w", err)
	}
	switch reservation.Source {
	case BillingSourceWallet:
		reservation.RequestId = ""
		reservation.SubscriptionId = 0
		reservation.UsageEpoch = 0
	case BillingSourceFreeModel:
		if reservation.Reserved != 0 || reservation.RequestId != "" ||
			reservation.SubscriptionId != 0 || reservation.UsageEpoch != 0 {
			return nil, errors.New("invalid free-model funding reservation")
		}
	case BillingSourceSubscription:
		reservation.RequestId = strings.TrimSpace(reservation.RequestId)
		if len(reservation.RequestId) > 64 {
			return nil, errors.New("invalid funding request id")
		}
		if reservation.SubscriptionId <= 0 || reservation.UsageEpoch < 0 {
			return nil, errors.New("invalid funding subscription")
		}
	default:
		return nil, fmt.Errorf("unsupported funding source %q", reservation.Source)
	}
	return &FundingSession{
		userId: userId, requestId: reservation.RequestId,
		source: reservation.Source, reserved: reservation.Reserved,
		subscriptionId: reservation.SubscriptionId, usageEpoch: reservation.UsageEpoch,
	}, nil
}

// Source returns the funding source that paid for the request.
func (s *FundingSession) Source() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.source
}
