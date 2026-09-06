package billing

import (
	"context"
	"errors"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"sync"
)

// RelayQuotaReservation pairs a user funding reservation with the matching
// limited-token reservation. Settle reconciles both in one database
// transaction; Refund releases both holds in one database transaction. Every
// failed operation remains durable and retryable.
type RelayQuotaReservation struct {
	mu             sync.Mutex
	reservationId  string
	funding        *FundingSession
	tokenId        int
	tokenReserved  int
	tokenUnlimited bool
	// trustQuotaBypassed is copied from the durable ledger. It is true only for
	// the ordinary wallet-funded path that deliberately created zero holds.
	trustQuotaBypassed bool
	channelId          int
	quota              int
	dispatched         bool
	settled            bool
	refunded           bool
	// transact is nil in production. Tests may inject a runner that reports an
	// error after committing to exercise ambiguous-commit recovery.
	transact fundingTransactionRunner
}

// RelayQuotaReservationCreation is the immutable accounting snapshot exposed
// to a caller that persists request-specific state in the reservation's
// creation transaction.
type RelayQuotaReservationCreation struct {
	ReservationID      string
	Funding            FundingReservation
	TokenID            int
	TokenReserved      int
	TokenUnlimited     bool
	TrustQuotaBypassed bool
}

// RelayQuotaReservationPersistence runs inside the primary-database
// transaction that owns the associated accounting transition.
type RelayQuotaReservationPersistence func(tx *gorm.DB) error

// RelayQuotaReservationCreationPersistence runs after both quota holds and
// the durable reservation row have been written, but before their transaction
// commits. Returning an error rolls every one of those writes back.
type RelayQuotaReservationCreationPersistence func(tx *gorm.DB, creation RelayQuotaReservationCreation) error

// NewRelayQuotaReservation reserves quota before contacting an upstream.
func NewRelayQuotaReservation(userId int, token *model.Token, quota int) (*RelayQuotaReservation, error) {
	return createDurableRelayQuotaReservation(userId, token, quota)
}

// NewOrdinaryRelayQuotaReservation applies the reference trust-threshold
// policy to ordinary synchronous relays. Realtime and asynchronous task
// callers intentionally continue to use NewRelayQuotaReservation so their
// full holds are never bypassed.
func NewOrdinaryRelayQuotaReservation(userId int, token *model.Token, quota int) (*RelayQuotaReservation, error) {
	trustQuota, enabled := RelayTrustQuota()
	return createDurableRelayQuotaReservationWithPolicy(
		userId, token, quota,
		relayQuotaReservationPolicy{allowTrustQuotaBypass: enabled, trustQuota: trustQuota},
	)
}

// NewOrdinaryRelayQuotaReservationWithFreeModel preserves the ordinary trust
// policy while allowing a caller with a captured zero-effective-price policy
// to create an explicit durable zero-hold reservation.
func NewOrdinaryRelayQuotaReservationWithFreeModel(
	userId int,
	token *model.Token,
	quota int,
	freeModel bool,
) (*RelayQuotaReservation, error) {
	trustQuota, enabled := RelayTrustQuota()
	return createDurableRelayQuotaReservationWithPolicy(
		userId, token, quota,
		relayQuotaReservationPolicy{
			allowTrustQuotaBypass: enabled,
			trustQuota:            trustQuota,
			freeModel:             freeModel,
		},
	)
}

// NewRelayQuotaReservationWithFreeModel creates a full-hold reservation for
// ordinary paid work, or an explicitly marked durable zero hold when the
// request's captured pricing policy identifies a free model.
func NewRelayQuotaReservationWithFreeModel(
	userId int,
	token *model.Token,
	quota int,
	freeModel bool,
) (*RelayQuotaReservation, error) {
	return createDurableRelayQuotaReservationWithPolicy(
		userId, token, quota, relayQuotaReservationPolicy{freeModel: freeModel},
	)
}

// NewRelayQuotaReservationWithPersistence creates both quota holds, the
// durable accounting record, and caller-owned request state in one commit.
func NewRelayQuotaReservationWithPersistence(
	userId int,
	token *model.Token,
	quota int,
	persist RelayQuotaReservationCreationPersistence,
) (*RelayQuotaReservation, error) {
	return createDurableRelayQuotaReservationWithPersistence(userId, token, quota, persist)
}

// NewRelayQuotaReservationWithFreeModelAndPersistence atomically persists an
// async task alongside either its normal full holds or an explicit durable
// free-model marker.
func NewRelayQuotaReservationWithFreeModelAndPersistence(
	userId int,
	token *model.Token,
	quota int,
	freeModel bool,
	persist RelayQuotaReservationCreationPersistence,
) (*RelayQuotaReservation, error) {
	return createDurableRelayQuotaReservationWithTransactionAndPersistence(
		userId, token, quota, relayQuotaReservationPolicy{freeModel: freeModel},
		func(fn func(tx *gorm.DB) error) error { return model.DB.Transaction(fn) },
		persist,
	)
}

// MarkDispatched durably records that an upstream may have accepted work. It
// must complete before the first network dispatch so recovery never mistakes
// a crash-window request for an untouched reservation that is safe to refund.
func (r *RelayQuotaReservation) MarkDispatched() error {
	return r.MarkDispatchedWithPersistence(nil)
}

// MarkDispatchedWithPersistence atomically moves the accounting reservation
// into its at-risk state and writes request-specific dispatch metadata before
// the caller is allowed to contact the upstream.
func (r *RelayQuotaReservation) MarkDispatchedWithPersistence(persist RelayQuotaReservationPersistence) error {
	if r == nil {
		return errors.New("quota reservation is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refunded {
		return errors.New("quota reservation was refunded")
	}
	if r.settled {
		return errors.New("quota reservation was settled")
	}
	if err := markDurableRelayQuotaReservationDispatched(r, persist); err != nil {
		return err
	}
	r.dispatched = true
	return nil
}

// Settle commits authoritative usage exactly once. On failure both
// reservations remain held, so a caller never refunds already-consumed
// upstream work accidentally.
func (r *RelayQuotaReservation) Settle(actual int) error {
	return r.SettleWithChannel(actual, 0)
}

// SettleWithChannel commits authoritative usage to the user, token, funding
// source, and selected upstream channel in one retryable transaction.
func (r *RelayQuotaReservation) SettleWithChannel(actual, channelId int) error {
	return r.SettleWithChannelAndPersistence(actual, channelId, nil)
}

// SettleWithChannelAndPersistence atomically settles all accounting counters
// and caller-owned accepted-request state. It deliberately transitions
// directly from dispatched to settled, leaving no generic-reconciler window
// in which accounting could commit without the caller's durable state.
func (r *RelayQuotaReservation) SettleWithChannelAndPersistence(
	actual, channelId int,
	persist RelayQuotaReservationPersistence,
) error {
	if r == nil {
		return errors.New("quota reservation is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refunded {
		return errors.New("quota reservation was refunded")
	}
	if r.settled && persist == nil {
		return nil
	}
	if persist == nil {
		if err := settleDurableRelayQuotaReservation(r, actual, channelId); err != nil {
			return err
		}
	} else if err := settleDurableRelayQuotaReservationWithPersistence(r, actual, channelId, persist); err != nil {
		return err
	}
	r.channelId = channelId
	r.settled = true
	return nil
}

// Refund releases an unused reservation exactly once. Funding and token
// accounting are reconciled atomically with the durable terminal marker; a
// failure leaves the operation pending for a safe retry.
func (r *RelayQuotaReservation) Refund() error {
	return r.RefundWithPersistence(nil)
}

// RefundWithPersistence atomically releases both holds and persists the
// caller's terminal pre-dispatch state.
func (r *RelayQuotaReservation) RefundWithPersistence(persist RelayQuotaReservationPersistence) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if (r.settled || r.refunded) && persist == nil {
		return nil
	}
	if r.settled {
		return errors.New("quota reservation was settled")
	}
	if persist == nil {
		if err := refundDurableRelayQuotaReservation(r); err != nil {
			return err
		}
	} else if err := refundDurableRelayQuotaReservationWithPersistence(r, persist); err != nil {
		return err
	}
	r.refunded = true
	return nil
}

// ReverseSettledRelayQuotaReservationWithPersistence restores the spend of an
// already-settled asynchronous operation after the provider authoritatively
// reports terminal failure. Usage/request/channel counters remain historical
// facts; only the user's available funding and limited-token balance are
// restored. The caller's task transition and refund audit share the same
// transaction, and replay is idempotent.
func ReverseSettledRelayQuotaReservationWithPersistence(
	reservationID string,
	persist RelayQuotaReservationPersistence,
) error {
	return reverseSettledRelayQuotaReservationWithPersistence(reservationID, persist)
}

// RestoreRelayQuotaReservation reconstructs a durable reservation from the
// primary database. It is used by another process after the submitting
// process has exited; no process-local state is required.
func RestoreRelayQuotaReservation(reservationID string) (*RelayQuotaReservation, error) {
	if reservationID == "" {
		return nil, errors.New("reservation id is empty")
	}
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
		return nil, err
	}
	return relayReservationFromRecord(&record)
}

// Quota is the request's pre-dispatch estimate. It is also the conservative
// dispatch fallback when an explicitly trusted request produced no usage.
func (r *RelayQuotaReservation) Quota() int {
	if r == nil {
		return 0
	}
	return r.quota
}

// BillingLogFields returns funding metadata after successful settlement.
func (r *RelayQuotaReservation) BillingLogFields() map[string]any {
	if r == nil || r.funding == nil {
		return map[string]any{}
	}
	fields := r.funding.BillingLogFields()
	if r.reservationId != "" {
		fields["relay_reservation_id"] = r.reservationId
	}
	if r.trustQuotaBypassed {
		fields["trust_quota_bypassed"] = true
	}
	return fields
}

// ReservationID returns the server-generated durable accounting operation ID.
func (r *RelayQuotaReservation) ReservationID() string {
	if r == nil {
		return ""
	}
	return r.reservationId
}

func (r *RelayQuotaReservation) transactionRunner() fundingTransactionRunner {
	return r.transactionRunnerContext(context.Background())
}

func (r *RelayQuotaReservation) transactionRunnerContext(ctx context.Context) fundingTransactionRunner {
	if r != nil && r.transact != nil {
		return r.transact
	}
	return func(fn func(tx *gorm.DB) error) error { return model.DB.WithContext(ctx).Transaction(fn) }
}
