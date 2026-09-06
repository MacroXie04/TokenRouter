package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	"gorm.io/gorm"
	"net"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	StripeCheckoutRequestSnapshotVersion = 1
	StripeReconciliationCheckoutPending  = "checkout_pending"
	StripeReconciliationProviderReview   = "provider_manual_review"

	stripeCheckoutReconciliationBatchSize    = 100
	stripeCheckoutReconciliationLeaseSeconds = int64(60)
	stripeCheckoutReconciliationBaseDelay    = int64(60)
	stripeCheckoutReconciliationMaxDelay     = int64(3600)
	stripeCheckoutReconciliationMaxAttempts  = 100
	maxStripeCheckoutSnapshotBytes           = 16 << 10
	maxStripeCheckoutRedirectURLBytes        = 2048
)

var ErrStripeCheckoutReconciliationLeaseLost = errors.New("Stripe Checkout reconciliation lease lost")

var stripeReversalReviewEventTypes = map[string]struct{}{
	"charge.refunded":        {},
	"charge.dispute.created": {},
	"charge.dispute.updated": {},
	"charge.dispute.closed":  {},
}

// RecordUnmatchedStripePaymentReversalReview durably records a signed Stripe
// reversal that did not carry TokenRouter's trade number. Disputes do not
// reliably inherit Checkout metadata, so dropping such an event would create
// an invisible financial exception.
func RecordUnmatchedStripePaymentReversalReview(eventID, eventType string, eventCreated int64) error {
	eventID = strings.TrimSpace(eventID)
	eventType = strings.TrimSpace(eventType)
	if _, ok := stripeReversalReviewEventTypes[eventType]; !ok ||
		!strings.HasPrefix(eventID, "evt_") || len(eventID) > 255 || eventCreated <= 0 {
		return ErrStripeCheckoutBindingMismatch
	}
	auditEventID := ""
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		content := "Stripe " + eventType + " requires manual correlation (event_id=" + eventID + ")"
		var err error
		auditEventID, err = enqueuePaymentSystemLogTx(tx, "stripe_reversal_unmatched", eventID, 0, content, eventCreated)
		return err
	})
	if err != nil {
		return err
	}
	if err := DeliverAuditLogOutboxEvent(auditEventID); err != nil {
		logging.SysError("unmatched Stripe reversal audit delivery deferred event_id=" + eventID + ": " + err.Error())
	}
	return nil
}

// StripeCheckoutRequestSnapshot is the credential-free request contract
// persisted before Stripe is contacted. Reconciliation recreates the exact
// request with the same idempotency key, so an ambiguous create cannot produce
// a second Checkout Session with different financial parameters.
type StripeCheckoutRequestSnapshot struct {
	Version        int    `json:"version"`
	TradeNo        string `json:"trade_no"`
	OrderType      string `json:"order_type"`
	Mode           string `json:"mode"`
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
	PriceID        string `json:"price_id,omitempty"`
	SuccessURL     string `json:"success_url"`
	CancelURL      string `json:"cancel_url"`
	CustomerID     string `json:"customer_id,omitempty"`
	CustomerEmail  string `json:"customer_email,omitempty"`
	ProductName    string `json:"product_name,omitempty"`
	UserID         int    `json:"user_id,omitempty"`
	WalletAmount   int64  `json:"wallet_amount,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

// StripeCheckoutResolution is the audited subset of a Checkout Session needed
// for exact local reconciliation.
type StripeCheckoutResolution struct {
	SessionID         string
	ClientReferenceID string
	OrderType         string
	Mode              string
	AmountMinor       int64
	Currency          string
	PriceID           string
	Status            string
	PaymentStatus     string
	CustomerID        string
	ExpiresAt         int64
}

// StripeCheckoutResolver either retrieves existingSessionID or, when it is
// empty, repeats the immutable create request with the snapshot's original
// idempotency key. The existing id is deliberately separate from the immutable
// request document because it is provider response state.
type StripeCheckoutResolver func(context.Context, StripeCheckoutRequestSnapshot, string) (*StripeCheckoutResolution, error)

var stripeCheckoutResolverRegistry struct {
	sync.RWMutex
	resolver StripeCheckoutResolver
}

// RegisterStripeCheckoutResolver installs the provider adapter used by the
// service background job. Passing nil disables provider reconciliation.
func RegisterStripeCheckoutResolver(resolver StripeCheckoutResolver) {
	stripeCheckoutResolverRegistry.Lock()
	stripeCheckoutResolverRegistry.resolver = resolver
	stripeCheckoutResolverRegistry.Unlock()
}

func registeredStripeCheckoutResolver() StripeCheckoutResolver {
	stripeCheckoutResolverRegistry.RLock()
	defer stripeCheckoutResolverRegistry.RUnlock()
	return stripeCheckoutResolverRegistry.resolver
}

// FlagStripePaymentReversalReview records Stripe refunds and disputes without
// guessing how much local wallet or subscription value remains recoverable.
// The paid entitlement is intentionally left unchanged; a root operator must
// review and resolve it. The state marker and immutable audit event commit
// atomically so a reversal can never be silently acknowledged.
func FlagStripePaymentReversalReview(tradeNo, eventID, eventType string, eventCreated int64) error {
	tradeNo = strings.TrimSpace(tradeNo)
	eventID = strings.TrimSpace(eventID)
	eventType = strings.TrimSpace(eventType)
	if _, ok := stripeReversalReviewEventTypes[eventType]; !ok ||
		!strings.HasPrefix(eventID, "evt_") || len(eventID) > 255 || len(tradeNo) > 255 || eventCreated <= 0 {
		return ErrStripeCheckoutBindingMismatch
	}
	detail := "Stripe " + eventType + " requires manual review; automatic entitlement reversal is disabled"
	auditEventID := ""
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		userID := 0
		switch {
		case strings.HasPrefix(tradeNo, "sub_ref_"):
			var order model.SubscriptionOrder
			if err := locking.SubscriptionLockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrSubscriptionOrderNotFound
				}
				return err
			}
			if order.PaymentProvider != PaymentProviderStripe || order.Status != TopUpStatusSuccess {
				return ErrSubscriptionOrderStatusInvalid
			}
			userID = order.UserId
			if order.ReconciliationState != StripeReconciliationProviderReview || order.ReconciliationDetail != detail {
				result := tx.Model(&model.SubscriptionOrder{}).
					Where("id = ? AND status = ? AND payment_provider = ?", order.Id, TopUpStatusSuccess, PaymentProviderStripe).
					Updates(map[string]any{
						"reconciliation_state":  StripeReconciliationProviderReview,
						"reconciliation_detail": detail,
					})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return ErrSubscriptionOrderStatusInvalid
				}
			}
		case strings.HasPrefix(tradeNo, "ref_"):
			var order model.TopUp
			if err := locking.SubscriptionLockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrTopUpNotFound
				}
				return err
			}
			if order.PaymentProvider != PaymentProviderStripe || order.Status != TopUpStatusSuccess {
				return ErrTopUpStatusInvalid
			}
			userID = order.UserId
			if order.ReconciliationState != StripeReconciliationProviderReview || order.ReconciliationDetail != detail {
				result := tx.Model(&model.TopUp{}).
					Where("id = ? AND status = ? AND payment_provider = ?", order.Id, TopUpStatusSuccess, PaymentProviderStripe).
					Updates(map[string]any{
						"reconciliation_state":  StripeReconciliationProviderReview,
						"reconciliation_detail": detail,
					})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return ErrTopUpStatusInvalid
				}
			}
		default:
			return ErrStripeCheckoutBindingMismatch
		}
		var err error
		auditEventID, err = enqueuePaymentSystemLogTx(tx, "stripe_reversal", tradeNo+":"+eventID, userID,
			detail+" (event_id="+eventID+", trade_no="+tradeNo+")", eventCreated)
		return err
	})
	if err != nil {
		return err
	}
	if err := DeliverAuditLogOutboxEvent(auditEventID); err != nil {
		logging.SysError("Stripe reversal audit delivery deferred trade_no=" + tradeNo + ": " + err.Error())
	}
	return nil
}

func normalizeStripeCheckoutSnapshot(snapshot *StripeCheckoutRequestSnapshot) error {
	if snapshot == nil {
		return ErrStripeCheckoutBindingMismatch
	}
	snapshot.TradeNo = strings.TrimSpace(snapshot.TradeNo)
	snapshot.OrderType = strings.TrimSpace(snapshot.OrderType)
	snapshot.Mode = strings.TrimSpace(snapshot.Mode)
	snapshot.PriceID = strings.TrimSpace(snapshot.PriceID)
	snapshot.CustomerID = strings.TrimSpace(snapshot.CustomerID)
	snapshot.CustomerEmail = strings.TrimSpace(snapshot.CustomerEmail)
	snapshot.ProductName = strings.TrimSpace(snapshot.ProductName)
	snapshot.IdempotencyKey = strings.TrimSpace(snapshot.IdempotencyKey)
	currency, err := NormalizeStripeCurrency(snapshot.Currency)
	if err != nil || !StripeCurrencySupported(currency) {
		return ErrStripePaymentMismatch
	}
	snapshot.Currency = currency
	if snapshot.Version != StripeCheckoutRequestSnapshotVersion || snapshot.TradeNo == "" ||
		snapshot.Mode != StripeCheckoutModePayment || snapshot.AmountMinor <= 0 ||
		!validStripeCheckoutRedirectURL(snapshot.SuccessURL) || !validStripeCheckoutRedirectURL(snapshot.CancelURL) ||
		snapshot.IdempotencyKey == "" ||
		len(snapshot.TradeNo) > 255 || len(snapshot.IdempotencyKey) > 255 || len(snapshot.PriceID) > 255 ||
		len(snapshot.CustomerID) > 128 || len(snapshot.CustomerEmail) > 255 || len(snapshot.ProductName) > 255 ||
		(snapshot.CustomerID != "" && (!strings.HasPrefix(snapshot.CustomerID, "cus_") || snapshot.CustomerEmail != "")) {
		return ErrStripeCheckoutBindingMismatch
	}
	switch snapshot.OrderType {
	case StripeOrderTypeWallet:
		if !strings.HasPrefix(snapshot.TradeNo, "ref_") || strings.HasPrefix(snapshot.TradeNo, "sub_ref_") ||
			snapshot.PriceID != "" || snapshot.ProductName == "" || snapshot.UserID <= 0 || snapshot.WalletAmount <= 0 ||
			snapshot.IdempotencyKey != "wallet-checkout-"+snapshot.TradeNo {
			return ErrStripeCheckoutBindingMismatch
		}
	case StripeOrderTypeSubscription:
		if !strings.HasPrefix(snapshot.TradeNo, "sub_ref_") || snapshot.PriceID == "" || snapshot.UserID != 0 ||
			snapshot.WalletAmount != 0 || snapshot.ProductName != "" ||
			snapshot.IdempotencyKey != "subscription-checkout-"+snapshot.TradeNo {
			return ErrStripeCheckoutBindingMismatch
		}
	default:
		return ErrStripeCheckoutBindingMismatch
	}
	return nil
}

// validStripeCheckoutRedirectURL applies a provider-independent safety floor
// to the immutable redirect stored with a checkout request. Trust-list checks
// for user-supplied URLs remain at the HTTP boundary; this validation also
// protects service callers and reconciliation of persisted snapshots.
func validStripeCheckoutRedirectURL(rawURL string) bool {
	if rawURL == "" || rawURL != strings.TrimSpace(rawURL) ||
		len(rawURL) > maxStripeCheckoutRedirectURLBytes || !utf8.ValidString(rawURL) ||
		strings.Contains(rawURL, `\`) {
		return false
	}
	for _, character := range rawURL {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) || character == 0x061c ||
			character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil || !parsedURL.IsAbs() || parsedURL.Opaque != "" || parsedURL.User != nil ||
		parsedURL.Host == "" || parsedURL.Hostname() == "" {
		return false
	}
	scheme := strings.ToLower(parsedURL.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsedURL.Hostname()), ".")
	if !validStripeCheckoutRedirectHostname(hostname) {
		return false
	}
	if scheme == "https" {
		return true
	}
	address := net.ParseIP(hostname)
	return hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") ||
		(address != nil && address.IsLoopback())
}

func validStripeCheckoutRedirectHostname(hostname string) bool {
	if hostname == "" || len(hostname) > 253 {
		return false
	}
	if net.ParseIP(hostname) != nil {
		return true
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func marshalStripeCheckoutSnapshot(snapshot StripeCheckoutRequestSnapshot) (string, string, error) {
	if err := normalizeStripeCheckoutSnapshot(&snapshot); err != nil {
		return "", "", err
	}
	raw, err := jsonutil.Marshal(snapshot)
	if err != nil || len(raw) == 0 || len(raw) > maxStripeCheckoutSnapshotBytes {
		return "", "", ErrStripeCheckoutBindingMismatch
	}
	digest := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(digest[:]), nil
}

func unmarshalStripeCheckoutSnapshot(raw, expectedFingerprint string) (StripeCheckoutRequestSnapshot, error) {
	var snapshot StripeCheckoutRequestSnapshot
	if strings.TrimSpace(raw) == "" || len(raw) > maxStripeCheckoutSnapshotBytes ||
		jsonutil.Unmarshal([]byte(raw), &snapshot) != nil {
		return snapshot, ErrStripeCheckoutBindingMismatch
	}
	canonical, fingerprint, err := marshalStripeCheckoutSnapshot(snapshot)
	if err != nil || canonical != raw || fingerprint != expectedFingerprint {
		return snapshot, ErrStripeCheckoutBindingMismatch
	}
	return snapshot, nil
}

func ConfigureStripeTopUpCheckoutRequest(tradeNo string, snapshot StripeCheckoutRequestSnapshot) error {
	snapshot.TradeNo = strings.TrimSpace(tradeNo)
	snapshot.OrderType = StripeOrderTypeWallet
	snapshot.Mode = StripeCheckoutModePayment
	raw, fingerprint, err := marshalStripeCheckoutSnapshot(snapshot)
	if err != nil {
		return err
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var order model.TopUp
		if err := locking.SubscriptionLockForUpdate(tx).Where("trade_no = ?", snapshot.TradeNo).First(&order).Error; err != nil {
			return err
		}
		if order.Status != TopUpStatusPending || order.ProviderBindingVersion != StripeCheckoutBindingVersion ||
			order.ProviderAmountMinor != snapshot.AmountMinor || order.ProviderCurrency != snapshot.Currency ||
			order.ProviderOrderType != snapshot.OrderType || order.ProviderMode != snapshot.Mode ||
			order.UserId != snapshot.UserID || order.Amount != snapshot.WalletAmount {
			return ErrStripeCheckoutBindingMismatch
		}
		if order.CheckoutFingerprint != "" {
			if order.CheckoutFingerprint == fingerprint && order.CheckoutRequest == raw &&
				order.ProviderCreateIdempotencyKey == snapshot.IdempotencyKey {
				return nil
			}
			return ErrStripeCheckoutBindingMismatch
		}
		result := tx.Model(&model.TopUp{}).
			Where("id = ? AND status = ? AND checkout_fingerprint = ''", order.Id, TopUpStatusPending).
			Updates(map[string]any{
				"checkout_request": raw, "checkout_fingerprint": fingerprint,
				"provider_create_idempotency_key": snapshot.IdempotencyKey,
				"reconciliation_next_at":          now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrStripeCheckoutBindingMismatch
		}
		return nil
	})
}

func ConfigureStripeSubscriptionCheckoutRequest(tradeNo string, snapshot StripeCheckoutRequestSnapshot) error {
	snapshot.TradeNo = strings.TrimSpace(tradeNo)
	snapshot.OrderType = StripeOrderTypeSubscription
	snapshot.Mode = StripeCheckoutModeSubscription
	raw, fingerprint, err := marshalStripeCheckoutSnapshot(snapshot)
	if err != nil {
		return err
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var order model.SubscriptionOrder
		if err := locking.SubscriptionLockForUpdate(tx).Where("trade_no = ?", snapshot.TradeNo).First(&order).Error; err != nil {
			return err
		}
		if order.Status != TopUpStatusPending || !order.CapacityReserved ||
			order.ProviderBindingVersion != StripeSubscriptionCheckoutBindingVersion ||
			order.ProviderAmountMinor != snapshot.AmountMinor || order.ProviderCurrency != snapshot.Currency ||
			order.ProviderOrderType != snapshot.OrderType || order.ProviderMode != snapshot.Mode ||
			order.ProviderPriceId != snapshot.PriceID {
			return ErrStripeCheckoutBindingMismatch
		}
		if order.CheckoutFingerprint != "" {
			if order.CheckoutFingerprint == fingerprint && order.CheckoutRequest == raw &&
				order.ProviderCreateIdempotencyKey == snapshot.IdempotencyKey {
				return nil
			}
			return ErrStripeCheckoutBindingMismatch
		}
		result := tx.Model(&model.SubscriptionOrder{}).
			Where("id = ? AND status = ? AND checkout_fingerprint = ''", order.Id, TopUpStatusPending).
			Updates(map[string]any{
				"checkout_request": raw, "checkout_fingerprint": fingerprint,
				"provider_create_idempotency_key": snapshot.IdempotencyKey,
				"reconciliation_next_at":          now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrStripeCheckoutBindingMismatch
		}
		return nil
	})
}

func stripeCheckoutReconciliationBackoff(attempt int) int64 {
	if attempt < 1 {
		attempt = 1
	}
	delay := stripeCheckoutReconciliationBaseDelay
	for i := 1; i < attempt && delay < stripeCheckoutReconciliationMaxDelay; i++ {
		delay *= 2
		if delay > stripeCheckoutReconciliationMaxDelay {
			delay = stripeCheckoutReconciliationMaxDelay
		}
	}
	return delay
}

func validateStripeCheckoutResolution(snapshot StripeCheckoutRequestSnapshot, result *StripeCheckoutResolution) error {
	if result == nil {
		return ErrStripeCheckoutBindingMismatch
	}
	sessionID := strings.TrimSpace(result.SessionID)
	customerID := strings.TrimSpace(result.CustomerID)
	if result.SessionID != sessionID || !strings.HasPrefix(sessionID, "cs_") || len(sessionID) > 255 ||
		result.ClientReferenceID != snapshot.TradeNo || result.OrderType != snapshot.OrderType ||
		result.Mode != snapshot.Mode || result.AmountMinor != snapshot.AmountMinor ||
		strings.TrimSpace(result.PriceID) != snapshot.PriceID || result.ExpiresAt <= 0 ||
		result.CustomerID != customerID || len(customerID) > 128 ||
		(customerID != "" && !strings.HasPrefix(customerID, "cus_")) ||
		(snapshot.CustomerID != "" && customerID != snapshot.CustomerID) {
		return ErrStripeCheckoutBindingMismatch
	}
	currency, err := NormalizeStripeCurrency(result.Currency)
	if err != nil || currency != snapshot.Currency {
		return ErrStripeCheckoutBindingMismatch
	}
	switch result.Status {
	case "open", "complete", "expired":
	default:
		return ErrStripeCheckoutBindingMismatch
	}
	switch result.PaymentStatus {
	case "paid", "unpaid":
	default:
		return ErrStripeCheckoutBindingMismatch
	}
	if result.PaymentStatus == "paid" && result.Status != "complete" {
		return ErrStripeCheckoutBindingMismatch
	}
	return nil
}

type claimedStripeCheckout struct {
	orderType   string
	id          int
	tradeNo     string
	owner       string
	attempt     int
	raw         string
	fingerprint string
	sessionID   string
}

func claimStripeTopUpReconciliation(ctx context.Context, id int) (*claimedStripeCheckout, bool, error) {
	ownerID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, false, err
	}
	owner := "stripe-wallet-" + ownerID
	var order model.TopUp
	claimed := false
	err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&model.TopUp{}).
			Where(`id = ? AND status = ? AND checkout_fingerprint <> '' AND reconciliation_next_at <= ?
					AND (reconciliation_lease_expires_at = 0 OR reconciliation_lease_expires_at <= ?)
					AND reconciliation_state <> ?
					AND reconciliation_attempts >= 0 AND reconciliation_attempts < ?`,
				id, TopUpStatusPending, now, now, StripeReconciliationProviderReview, stripeCheckoutReconciliationMaxAttempts).
			Updates(map[string]any{
				"reconciliation_lease_owner": owner, "reconciliation_lease_expires_at": now + stripeCheckoutReconciliationLeaseSeconds,
				"reconciliation_attempts": gorm.Expr("reconciliation_attempts + ?", 1),
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return result.Error
		}
		if err := tx.First(&order, id).Error; err != nil {
			return err
		}
		claimed = order.ReconciliationLeaseOwner == owner
		return nil
	})
	if err != nil || !claimed {
		return nil, claimed, err
	}
	sessionID := ""
	if order.ProviderSessionId != nil {
		sessionID = strings.TrimSpace(*order.ProviderSessionId)
	}
	return &claimedStripeCheckout{orderType: StripeOrderTypeWallet, id: id, tradeNo: order.TradeNo,
		owner: owner, attempt: order.ReconciliationAttempts, raw: order.CheckoutRequest,
		fingerprint: order.CheckoutFingerprint, sessionID: sessionID}, true, nil
}

func claimStripeSubscriptionReconciliation(ctx context.Context, id int) (*claimedStripeCheckout, bool, error) {
	ownerID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, false, err
	}
	owner := "stripe-subscription-" + ownerID
	var order model.SubscriptionOrder
	claimed := false
	err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&model.SubscriptionOrder{}).
			Where(`id = ? AND status = ? AND checkout_fingerprint <> '' AND reconciliation_next_at <= ?
					AND (reconciliation_lease_expires_at = 0 OR reconciliation_lease_expires_at <= ?)
					AND reconciliation_state <> ?
					AND reconciliation_attempts >= 0 AND reconciliation_attempts < ?`,
				id, TopUpStatusPending, now, now, StripeReconciliationProviderReview, stripeCheckoutReconciliationMaxAttempts).
			Updates(map[string]any{
				"reconciliation_lease_owner": owner, "reconciliation_lease_expires_at": now + stripeCheckoutReconciliationLeaseSeconds,
				"reconciliation_attempts": gorm.Expr("reconciliation_attempts + ?", 1),
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return result.Error
		}
		if err := tx.First(&order, id).Error; err != nil {
			return err
		}
		claimed = order.ReconciliationLeaseOwner == owner
		return nil
	})
	if err != nil || !claimed {
		return nil, claimed, err
	}
	sessionID := ""
	if order.ProviderSessionId != nil {
		sessionID = strings.TrimSpace(*order.ProviderSessionId)
	}
	return &claimedStripeCheckout{orderType: StripeOrderTypeSubscription, id: id, tradeNo: order.TradeNo,
		owner: owner, attempt: order.ReconciliationAttempts, raw: order.CheckoutRequest,
		fingerprint: order.CheckoutFingerprint, sessionID: sessionID}, true, nil
}

func recordStripeCheckoutReconciliationFailure(claim *claimedStripeCheckout, now int64, reconciliationErr error) error {
	if claim == nil {
		return reconciliationErr
	}
	state := StripeReconciliationCreationUnknown
	if errors.Is(reconciliationErr, ErrStripeCheckoutBindingMismatch) {
		state = StripeReconciliationProviderReview
	}
	next := now + stripeCheckoutReconciliationBackoff(claim.attempt)
	if claim.attempt >= stripeCheckoutReconciliationMaxAttempts || state == StripeReconciliationProviderReview {
		state = StripeReconciliationProviderReview
		next = 0
	}
	detail := stripeCheckoutReconciliationDetail(reconciliationErr)
	updates := map[string]any{
		"reconciliation_state": state, "reconciliation_detail": detail,
		"reconciliation_next_at": next, "reconciliation_lease_owner": "", "reconciliation_lease_expires_at": 0,
	}
	var result *gorm.DB
	if claim.orderType == StripeOrderTypeSubscription {
		result = model.DB.Model(&model.SubscriptionOrder{}).
			Where("id = ? AND status = ? AND reconciliation_lease_owner = ?", claim.id, TopUpStatusPending, claim.owner).
			Updates(updates)
	} else {
		result = model.DB.Model(&model.TopUp{}).
			Where("id = ? AND status = ? AND reconciliation_lease_owner = ?", claim.id, TopUpStatusPending, claim.owner).
			Updates(updates)
	}
	if result.Error != nil {
		return errors.Join(reconciliationErr, result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.Join(reconciliationErr, ErrStripeCheckoutReconciliationLeaseLost)
	}
	return reconciliationErr
}

func truncatePaymentReconciliationDetail(detail string) string {
	const maxBytes = 2048
	if len(detail) <= maxBytes {
		return detail
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(detail[cut]) {
		cut--
	}
	return detail[:cut]
}

// stripeCheckoutReconciliationDetail never persists arbitrary provider or
// driver error text. Those errors may contain request metadata or other
// customer-specific details; the returned error still reaches protected
// runtime logs for diagnosis, while durable order rows retain only a bounded
// public classification.
func stripeCheckoutReconciliationDetail(err error) string {
	detail := "provider reconciliation failed"
	switch {
	case errors.Is(err, ErrStripeCheckoutBindingMismatch):
		detail = ErrStripeCheckoutBindingMismatch.Error()
	case errors.Is(err, ErrStripePaymentMismatch):
		detail = ErrStripePaymentMismatch.Error()
	case errors.Is(err, ErrStripeLegacyOrderRequiresReview):
		detail = ErrStripeLegacyOrderRequiresReview.Error()
	case errors.Is(err, ErrTopUpLegacyCreditRequiresReview):
		detail = ErrTopUpLegacyCreditRequiresReview.Error()
	case errors.Is(err, ErrStripeCheckoutReconciliationLeaseLost):
		detail = ErrStripeCheckoutReconciliationLeaseLost.Error()
	}
	return truncatePaymentReconciliationDetail(detail)
}

func reconcileClaimedStripeCheckout(ctx context.Context, resolver StripeCheckoutResolver, claim *claimedStripeCheckout) error {
	snapshot, err := unmarshalStripeCheckoutSnapshot(claim.raw, claim.fingerprint)
	if err != nil || snapshot.TradeNo != claim.tradeNo || snapshot.OrderType != claim.orderType {
		if err == nil {
			err = ErrStripeCheckoutBindingMismatch
		}
		now, clockErr := model.DatabaseUnixTimestamp(model.DB)
		if clockErr != nil {
			return errors.Join(err, clockErr)
		}
		return recordStripeCheckoutReconciliationFailure(claim, now, err)
	}
	result, resolveErr := resolver(ctx, snapshot, claim.sessionID)
	now, clockErr := model.DatabaseUnixTimestamp(model.DB)
	if clockErr != nil {
		return errors.Join(resolveErr, clockErr)
	}
	if resolveErr != nil {
		return recordStripeCheckoutReconciliationFailure(claim, now, resolveErr)
	}
	if err := validateStripeCheckoutResolution(snapshot, result); err != nil {
		return recordStripeCheckoutReconciliationFailure(claim, now, err)
	}
	binding := &stripeCheckoutBinding{
		sessionID: strings.TrimSpace(result.SessionID), mode: result.Mode, orderType: result.OrderType,
		priceID: strings.TrimSpace(result.PriceID), leaseOwner: claim.owner,
		checkoutRequest: claim.raw, checkoutFingerprint: claim.fingerprint,
		providerExpiresAt: result.ExpiresAt, snapshotUserID: snapshot.UserID,
		snapshotWalletAmount: snapshot.WalletAmount,
	}
	settlement := &stripeSettlement{
		amountMinor: result.AmountMinor, currency: snapshot.Currency, customerID: strings.TrimSpace(result.CustomerID),
	}
	if result.PaymentStatus == "paid" {
		if claim.orderType == StripeOrderTypeSubscription {
			err = completeSubscriptionOrder(claim.tradeNo, "", PaymentProviderStripe, "", settlement, binding, nil, nil, nil)
		} else {
			var order model.TopUp
			if lookupErr := model.DB.Select("user_id", "amount").First(&order, claim.id).Error; lookupErr != nil {
				err = lookupErr
			} else {
				err = completeTopUp(order.UserId, claim.tradeNo, order.Amount, settlement, binding, nil)
			}
		}
		if err != nil {
			return recordStripeCheckoutReconciliationFailure(claim, now, err)
		}
		return nil
	}
	if result.Status == "expired" {
		if err := terminalizeReconciledStripeCheckout(claim, snapshot, result, TopUpStatusExpired, now); err != nil {
			return recordStripeCheckoutReconciliationFailure(claim, now, err)
		}
		return nil
	}
	if err := retainPendingReconciledStripeCheckout(claim, snapshot, result, now); err != nil {
		return recordStripeCheckoutReconciliationFailure(claim, now, err)
	}
	return nil
}

func terminalizeReconciledStripeCheckout(claim *claimedStripeCheckout, snapshot StripeCheckoutRequestSnapshot, resolved *StripeCheckoutResolution, status string, now int64) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if claim.orderType == StripeOrderTypeSubscription {
			var order model.SubscriptionOrder
			if err := locking.SubscriptionLockForUpdate(tx).First(&order, claim.id).Error; err != nil {
				return err
			}
			if err := validateStripeSubscriptionReconciliationLease(&order, claim, snapshot, resolved, now); err != nil {
				return err
			}
			result := tx.Model(&model.SubscriptionOrder{}).
				Where("id = ? AND status = ? AND reconciliation_lease_owner = ?", claim.id, TopUpStatusPending, claim.owner).
				Updates(map[string]any{
					"status": status, "complete_time": now, "capacity_reserved": false,
					"provider_session_id": resolved.SessionID, "provider_expires_at": resolved.ExpiresAt,
					"reconciliation_state": "", "reconciliation_detail": "", "reconciliation_next_at": 0,
					"reconciliation_lease_owner": "", "reconciliation_lease_expires_at": 0,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrStripeCheckoutReconciliationLeaseLost
			}
			return nil
		}
		var order model.TopUp
		if err := locking.SubscriptionLockForUpdate(tx).First(&order, claim.id).Error; err != nil {
			return err
		}
		if err := validateStripeTopUpReconciliationLease(&order, claim, snapshot, resolved, now); err != nil {
			return err
		}
		result := tx.Model(&model.TopUp{}).
			Where("id = ? AND status = ? AND reconciliation_lease_owner = ?", claim.id, TopUpStatusPending, claim.owner).
			Updates(map[string]any{
				"status": status, "complete_time": now,
				"provider_session_id": resolved.SessionID, "provider_expires_at": resolved.ExpiresAt,
				"reconciliation_state": "", "reconciliation_detail": "", "reconciliation_next_at": 0,
				"reconciliation_lease_owner": "", "reconciliation_lease_expires_at": 0,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrStripeCheckoutReconciliationLeaseLost
		}
		return nil
	})
}

func retainPendingReconciledStripeCheckout(claim *claimedStripeCheckout, snapshot StripeCheckoutRequestSnapshot, resolved *StripeCheckoutResolution, now int64) error {
	next := now + stripeCheckoutReconciliationBaseDelay
	if resolved.ExpiresAt > 0 && next > resolved.ExpiresAt {
		next = resolved.ExpiresAt
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"provider_session_id": resolved.SessionID, "provider_expires_at": resolved.ExpiresAt,
			"reconciliation_state": StripeReconciliationCheckoutPending, "reconciliation_detail": "",
			"reconciliation_next_at": next, "reconciliation_lease_owner": "", "reconciliation_lease_expires_at": 0,
			// Claiming increments attempts before the provider call so a crash consumes
			// the bounded recovery budget. A fully validated open/unpaid response is
			// healthy progress, so reset the consecutive-failure budget only while the
			// same owner still holds the lease.
			"reconciliation_attempts": 0,
		}
		if claim.orderType == StripeOrderTypeSubscription {
			var order model.SubscriptionOrder
			if err := locking.SubscriptionLockForUpdate(tx).First(&order, claim.id).Error; err != nil {
				return err
			}
			if err := validateStripeSubscriptionReconciliationLease(&order, claim, snapshot, resolved, now); err != nil {
				return err
			}
			result := tx.Model(&model.SubscriptionOrder{}).
				Where("id = ? AND status = ? AND reconciliation_lease_owner = ?", claim.id, TopUpStatusPending, claim.owner).
				Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrStripeCheckoutReconciliationLeaseLost
			}
			return nil
		}
		var order model.TopUp
		if err := locking.SubscriptionLockForUpdate(tx).First(&order, claim.id).Error; err != nil {
			return err
		}
		if err := validateStripeTopUpReconciliationLease(&order, claim, snapshot, resolved, now); err != nil {
			return err
		}
		result := tx.Model(&model.TopUp{}).
			Where("id = ? AND status = ? AND reconciliation_lease_owner = ?", claim.id, TopUpStatusPending, claim.owner).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrStripeCheckoutReconciliationLeaseLost
		}
		return nil
	})
}

func validateStripeTopUpReconciliationLease(order *model.TopUp, claim *claimedStripeCheckout, snapshot StripeCheckoutRequestSnapshot, resolved *StripeCheckoutResolution, now int64) error {
	if order == nil || claim == nil || resolved == nil || order.Status != TopUpStatusPending ||
		order.ReconciliationLeaseOwner != claim.owner || order.ReconciliationLeaseExpiresAt < now ||
		order.CheckoutFingerprint != claim.fingerprint || order.CheckoutRequest != claim.raw ||
		order.TradeNo != snapshot.TradeNo || order.ProviderAmountMinor != snapshot.AmountMinor ||
		order.ProviderCurrency != snapshot.Currency || order.ProviderOrderType != snapshot.OrderType ||
		order.ProviderMode != snapshot.Mode || order.UserId != snapshot.UserID || order.Amount != snapshot.WalletAmount ||
		(order.ProviderSessionId != nil && *order.ProviderSessionId != resolved.SessionID) ||
		(order.ProviderExpiresAt > 0 && resolved.ExpiresAt > 0 && order.ProviderExpiresAt != resolved.ExpiresAt) {
		return ErrStripeCheckoutReconciliationLeaseLost
	}
	return nil
}

func validateStripeSubscriptionReconciliationLease(order *model.SubscriptionOrder, claim *claimedStripeCheckout, snapshot StripeCheckoutRequestSnapshot, resolved *StripeCheckoutResolution, now int64) error {
	if order == nil || claim == nil || resolved == nil || order.Status != TopUpStatusPending || !order.CapacityReserved ||
		order.ReconciliationLeaseOwner != claim.owner || order.ReconciliationLeaseExpiresAt < now ||
		order.CheckoutFingerprint != claim.fingerprint || order.CheckoutRequest != claim.raw ||
		order.TradeNo != snapshot.TradeNo || order.ProviderAmountMinor != snapshot.AmountMinor ||
		order.ProviderCurrency != snapshot.Currency || order.ProviderOrderType != snapshot.OrderType ||
		order.ProviderMode != snapshot.Mode || order.ProviderPriceId != snapshot.PriceID ||
		(order.ProviderSessionId != nil && *order.ProviderSessionId != resolved.SessionID) ||
		(order.ProviderExpiresAt > 0 && resolved.ExpiresAt > 0 && order.ProviderExpiresAt != resolved.ExpiresAt) {
		return ErrStripeCheckoutReconciliationLeaseLost
	}
	return nil
}

// markExhaustedStripeCheckoutReconciliations closes the crash window after a
// worker acquired the final permitted lease but exited before recording its
// outcome. Without this sweep, attempts == max would be excluded from every
// later claim and the order (and subscription capacity) would remain pending
// forever.
func markExhaustedStripeCheckoutReconciliations(ctx context.Context, now int64) error {
	updates := map[string]any{
		"reconciliation_state":            StripeReconciliationProviderReview,
		"reconciliation_detail":           "provider reconciliation attempt limit reached",
		"reconciliation_next_at":          0,
		"reconciliation_lease_owner":      "",
		"reconciliation_lease_expires_at": 0,
	}
	where := `status = ? AND checkout_fingerprint <> '' AND reconciliation_attempts >= ?
		AND (reconciliation_lease_expires_at = 0 OR reconciliation_lease_expires_at <= ?)
		AND reconciliation_state <> ?`
	if err := model.DB.WithContext(ctx).Model(&model.TopUp{}).
		Where(where, TopUpStatusPending, stripeCheckoutReconciliationMaxAttempts, now, StripeReconciliationProviderReview).
		Updates(updates).Error; err != nil {
		return err
	}
	return model.DB.WithContext(ctx).Model(&model.SubscriptionOrder{}).
		Where(where, TopUpStatusPending, stripeCheckoutReconciliationMaxAttempts, now, StripeReconciliationProviderReview).
		Updates(updates).Error
}

// ReconcileStripeCheckoutOrders runs one bounded pass for both wallet and
// subscription orders. Provider calls occur only after an owner-fenced lease
// is committed and never while a database transaction is held.
func ReconcileStripeCheckoutOrders(ctx context.Context) error {
	resolver := registeredStripeCheckoutResolver()
	if resolver == nil || model.DB == nil {
		return nil
	}
	now, err := model.DatabaseUnixTimestamp(model.DB.WithContext(ctx))
	if err != nil {
		return err
	}
	if err := markExhaustedStripeCheckoutReconciliations(ctx, now); err != nil {
		return err
	}
	var walletIDs []int
	if err := model.DB.WithContext(ctx).Model(&model.TopUp{}).
		Where(`status = ? AND checkout_fingerprint <> '' AND reconciliation_next_at <= ?
			AND (reconciliation_lease_expires_at = 0 OR reconciliation_lease_expires_at <= ?)
			AND reconciliation_state <> ? AND reconciliation_attempts < ?`, TopUpStatusPending, now, now,
			StripeReconciliationProviderReview, stripeCheckoutReconciliationMaxAttempts).
		Order("reconciliation_next_at asc, id asc").Limit(stripeCheckoutReconciliationBatchSize).Pluck("id", &walletIDs).Error; err != nil {
		return err
	}
	var subscriptionIDs []int
	if err := model.DB.WithContext(ctx).Model(&model.SubscriptionOrder{}).
		Where(`status = ? AND checkout_fingerprint <> '' AND reconciliation_next_at <= ?
			AND (reconciliation_lease_expires_at = 0 OR reconciliation_lease_expires_at <= ?)
			AND reconciliation_state <> ? AND reconciliation_attempts < ?`, TopUpStatusPending, now, now,
			StripeReconciliationProviderReview, stripeCheckoutReconciliationMaxAttempts).
		Order("reconciliation_next_at asc, id asc").Limit(stripeCheckoutReconciliationBatchSize).Pluck("id", &subscriptionIDs).Error; err != nil {
		return err
	}
	errs := make([]error, 0)
	for _, id := range walletIDs {
		claim, claimed, claimErr := claimStripeTopUpReconciliation(ctx, id)
		if claimErr != nil {
			errs = append(errs, claimErr)
			continue
		}
		if claimed {
			if err := reconcileClaimedStripeCheckout(ctx, resolver, claim); err != nil {
				errs = append(errs, fmt.Errorf("reconcile Stripe wallet order %d: %w", id, err))
			}
		}
	}
	for _, id := range subscriptionIDs {
		claim, claimed, claimErr := claimStripeSubscriptionReconciliation(ctx, id)
		if claimErr != nil {
			errs = append(errs, claimErr)
			continue
		}
		if claimed {
			if err := reconcileClaimedStripeCheckout(ctx, resolver, claim); err != nil {
				errs = append(errs, fmt.Errorf("reconcile Stripe subscription order %d: %w", id, err))
			}
		}
	}
	return errors.Join(errs...)
}
