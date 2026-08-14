package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Funding sources for a relay request.
const (
	BillingSourceWallet       = "wallet"
	BillingSourceSubscription = "subscription"
)

// FundingSession funds one relay request's quota reservation from either the
// user's wallet balance or an active subscription, per the user's billing
// preference, and carries the state needed to settle or refund it exactly
// once.
type FundingSession struct {
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
	SubscriptionId int
}

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
	settings, err := loadUserSettings(userId)
	if err != nil {
		settings = UserSettings{}
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
		s := &FundingSession{
			userId:        userId,
			requestId:     common.GenerateUUID(),
			source:        BillingSourceSubscription,
			rawPreference: settings.BillingPreference,
			reserved:      int(subConsume),
		}
		res, err := PreConsumeUserSubscription(s.requestId, userId, subConsume)
		if err != nil {
			return nil, err
		}
		s.subscriptionId = res.UserSubscriptionId
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
	if s == nil || s.settled || s.refunded {
		return nil
	}
	s.settled = true
	if s.source == BillingSourceSubscription {
		delta := int64(actual) - int64(s.reserved)
		if delta != 0 {
			if err := PostConsumeUserSubscriptionDelta(s.subscriptionId, delta); err != nil {
				// The response is already sent; the reservation stands and the
				// discrepancy is only logged.
				common.SysError("subscription settle failed for user " + common.Int2Str(s.userId) + ": " + err.Error())
			} else {
				s.postDelta = delta
			}
		}
		return RecordUserUsage(s.userId, actual)
	}
	return SettleUserQuota(s.userId, s.reserved, actual)
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
	if s == nil {
		return errors.New("funding session is nil")
	}
	if actual < 0 {
		return errors.New("actual quota must not be negative")
	}
	if s.refunded {
		return errors.New("funding session was refunded")
	}
	if s.settled {
		return nil
	}

	delta := int64(actual) - int64(s.reserved)
	err := model.DB.Transaction(func(tx *gorm.DB) error {
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
			if delta != 0 {
				if err := postConsumeUserSubscriptionDeltaTx(tx, s.subscriptionId, delta, common.NowTimestamp()); err != nil {
					if !(delta < 0 && errors.Is(err, gorm.ErrRecordNotFound)) {
						return err
					}
				}
			}
		case BillingSourceWallet:
			if delta > 0 {
				result := tx.Model(&model.User{}).
					Where("id = ? AND quota >= ?", s.userId, delta).
					UpdateColumn("quota", gormExpr("quota - ?", delta))
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected == 0 {
					return ErrInsufficientQuota
				}
			} else if delta < 0 {
				result := tx.Model(&model.User{}).Where("id = ?", s.userId).
					UpdateColumn("quota", gormExpr("quota + ?", -delta))
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected == 0 {
					return fmt.Errorf("user %d not found", s.userId)
				}
			}
		default:
			return fmt.Errorf("unsupported funding source %q", s.source)
		}

		userResult := tx.Model(&model.User{}).Where("id = ?", s.userId).Updates(map[string]any{
			"used_quota":    gormExpr("used_quota + ?", actual),
			"request_count": gormExpr("request_count + ?", 1),
		})
		if userResult.Error != nil {
			return userResult.Error
		}
		if userResult.RowsAffected == 0 {
			return fmt.Errorf("user %d not found", s.userId)
		}
		if tokenId > 0 && actual > 0 {
			tokenResult := tx.Unscoped().Model(&model.Token{}).
				Where("id = ? AND user_id = ?", tokenId, s.userId).
				UpdateColumn("used_quota", gormExpr("used_quota + ?", actual))
			if tokenResult.Error != nil {
				return tokenResult.Error
			}
			if tokenResult.RowsAffected == 0 {
				return ErrTokenNotFound
			}
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

// Refund returns the reservation after a failed request. Idempotent at the
// session level; the wallet refund is a non-idempotent quota increment and is
// never retried, while the subscription refund is ledger-idempotent and
// retries on transient failures.
func (s *FundingSession) Refund() {
	if s == nil || s.settled || s.refunded {
		return
	}
	s.refunded = true
	if s.source == BillingSourceSubscription {
		if s.reserved > 0 {
			if err := refundWithRetry(func() error {
				return RefundSubscriptionPreConsume(s.requestId)
			}); err != nil {
				common.SysError("subscription refund failed for user " + common.Int2Str(s.userId) + ": " + err.Error())
			}
		}
		return
	}
	if s.reserved > 0 {
		if err := RefundUserQuota(s.userId, s.reserved); err != nil {
			common.SysError("wallet refund failed for user " + common.Int2Str(s.userId) + ": " + err.Error())
		}
	}
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
	if s.source != "" {
		fields["billing_source"] = s.source
	}
	// The raw stored preference (not normalized), only when the user set one.
	if s.rawPreference != "" {
		fields["billing_preference"] = s.rawPreference
	}
	if s.source != BillingSourceSubscription {
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
	consumed := preConsumed + s.postDelta
	if consumed < 0 {
		consumed = 0
	}
	usedFinal := s.subUsedAfter + s.postDelta
	if usedFinal < 0 {
		usedFinal = 0
	}
	if s.subTotal > 0 {
		remain := s.subTotal - usedFinal
		if remain < 0 {
			remain = 0
		}
		fields["subscription_total"] = s.subTotal
		fields["subscription_used"] = usedFinal
		fields["subscription_remain"] = remain
	}
	if consumed > 0 {
		fields["subscription_consumed"] = consumed
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
	return FundingReservation{
		Source: s.source, Reserved: s.reserved, SubscriptionId: s.subscriptionId,
	}
}

// RestoreFundingSession reconstructs an unsettled reservation so a later
// owner-scoped task fetch can finish accounting that failed after acceptance.
func RestoreFundingSession(userId int, reservation FundingReservation) (*FundingSession, error) {
	if userId <= 0 {
		return nil, errors.New("invalid funding user")
	}
	if reservation.Reserved < 0 {
		return nil, errors.New("invalid reserved quota")
	}
	switch reservation.Source {
	case BillingSourceWallet:
		reservation.SubscriptionId = 0
	case BillingSourceSubscription:
		if reservation.SubscriptionId <= 0 {
			return nil, errors.New("invalid funding subscription")
		}
	default:
		return nil, fmt.Errorf("unsupported funding source %q", reservation.Source)
	}
	return &FundingSession{
		userId: userId, source: reservation.Source, reserved: reservation.Reserved,
		subscriptionId: reservation.SubscriptionId,
	}, nil
}

// Source returns the funding source that paid for the request.
func (s *FundingSession) Source() string {
	if s == nil {
		return ""
	}
	return s.source
}
