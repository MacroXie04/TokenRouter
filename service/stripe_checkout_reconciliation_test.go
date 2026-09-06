package service

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
)

func installStripeCheckoutResolver(t *testing.T, resolver StripeCheckoutResolver) {
	t.Helper()
	previous := registeredStripeCheckoutResolver()
	RegisterStripeCheckoutResolver(resolver)
	t.Cleanup(func() { RegisterStripeCheckoutResolver(previous) })
}

func walletCheckoutSnapshot(order *model.TopUp, user *model.User) StripeCheckoutRequestSnapshot {
	return StripeCheckoutRequestSnapshot{
		Version: StripeCheckoutRequestSnapshotVersion,
		TradeNo: order.TradeNo, OrderType: StripeOrderTypeWallet, Mode: StripeCheckoutModePayment,
		AmountMinor: order.ProviderAmountMinor, Currency: order.ProviderCurrency,
		SuccessURL: "https://example.com/success", CancelURL: "https://example.com/cancel",
		CustomerID: user.StripeCustomer, ProductName: "TokenRouter wallet top-up",
		UserID: user.Id, WalletAmount: order.Amount,
		IdempotencyKey: "wallet-checkout-" + order.TradeNo,
	}
}

func subscriptionCheckoutSnapshot(order *model.SubscriptionOrder) StripeCheckoutRequestSnapshot {
	return StripeCheckoutRequestSnapshot{
		Version: StripeCheckoutRequestSnapshotVersion,
		TradeNo: order.TradeNo, OrderType: StripeOrderTypeSubscription, Mode: StripeCheckoutModeSubscription,
		AmountMinor: order.ProviderAmountMinor, Currency: order.ProviderCurrency, PriceID: order.ProviderPriceId,
		SuccessURL: "https://example.com/wallet", CancelURL: "https://example.com/wallet",
		IdempotencyKey: "subscription-checkout-" + order.TradeNo,
	}
}

func checkoutResolution(snapshot StripeCheckoutRequestSnapshot, sessionID, status, paymentStatus string, expiresAt int64) *StripeCheckoutResolution {
	return &StripeCheckoutResolution{
		SessionID: sessionID, ClientReferenceID: snapshot.TradeNo,
		OrderType: snapshot.OrderType, Mode: snapshot.Mode,
		AmountMinor: snapshot.AmountMinor, Currency: snapshot.Currency, PriceID: snapshot.PriceID,
		Status: status, PaymentStatus: paymentStatus, ExpiresAt: expiresAt,
	}
}

func TestStripeCheckoutReconciliationFulfillsAmbiguousWalletCreateExactlyOnce(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-wallet", 0, "default")
	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 2, 1, "USD", "ref_reconcile_wallet_paid")
	require.NoError(t, err)
	snapshot := walletCheckoutSnapshot(order, user)
	require.NoError(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot))

	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	var calls atomic.Int32
	installStripeCheckoutResolver(t, func(_ context.Context, got StripeCheckoutRequestSnapshot, existing string) (*StripeCheckoutResolution, error) {
		calls.Add(1)
		assert.Equal(t, snapshot, got)
		assert.Empty(t, existing, "ambiguous create must be replayed with its original idempotency key")
		return checkoutResolution(got, "cs_reconcile_wallet_paid", "complete", "paid", now+3600), nil
	})

	require.NoError(t, ReconcileStripeCheckoutOrders(context.Background()))
	require.NoError(t, ReconcileStripeCheckoutOrders(context.Background()))
	assert.Equal(t, int32(1), calls.Load())

	var stored model.TopUp
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, stored.Status)
	assert.Equal(t, now+3600, stored.ProviderExpiresAt)
	assert.Zero(t, stored.ReconciliationNextAt)
	assert.Empty(t, stored.ReconciliationLeaseOwner)
	var storedUser model.User
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, int(order.CreditQuota), storedUser.Quota)
}

func TestStripeCheckoutReconciliationExpiresSubscriptionAndReleasesCapacity(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-sub-expired", 0, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.PriceAmount = "4.00"
		plan.Currency = "USD"
		plan.StripePriceId = "price_reconcile_expired"
		plan.MaxPurchasePerUser = 1
	})
	order, err := CreateBoundStripeSubscriptionOrder(user.Id, plan.Id, ValidatedStripePrice{
		ID: plan.StripePriceId, AmountMinor: 400, Currency: "USD",
	}, "sub_ref_reconcile_expired")
	require.NoError(t, err)
	snapshot := subscriptionCheckoutSnapshot(order)
	require.NoError(t, ConfigureStripeSubscriptionCheckoutRequest(order.TradeNo, snapshot))
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	installStripeCheckoutResolver(t, func(_ context.Context, got StripeCheckoutRequestSnapshot, existing string) (*StripeCheckoutResolution, error) {
		assert.Empty(t, existing)
		return checkoutResolution(got, "cs_reconcile_sub_expired", "expired", "unpaid", now-1), nil
	})

	require.NoError(t, ReconcileStripeCheckoutOrders(context.Background()))
	var stored model.SubscriptionOrder
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusExpired, stored.Status)
	assert.False(t, stored.CapacityReserved)
	assert.Zero(t, stored.ReconciliationNextAt)
	used, err := countSubscriptionCapacityUsedTx(model.DB, user.Id, plan.Id)
	require.NoError(t, err)
	assert.Zero(t, used)
}

func TestStripeCheckoutReconciliationFulfillsSubscriptionFromImmutableEntitlement(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-sub-paid", 0, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.Title = "Immutable paid plan"
		plan.PriceAmount = "4.00"
		plan.Currency = "USD"
		plan.StripePriceId = "price_reconcile_paid"
		plan.TotalAmount = 4321
		plan.QuotaResetPeriod = SubscriptionResetWeekly
	})
	order, err := CreateBoundStripeSubscriptionOrder(user.Id, plan.Id, ValidatedStripePrice{
		ID: plan.StripePriceId, AmountMinor: 400, Currency: "USD",
	}, "sub_ref_reconcile_paid")
	require.NoError(t, err)
	snapshot := subscriptionCheckoutSnapshot(order)
	require.NoError(t, ConfigureStripeSubscriptionCheckoutRequest(order.TradeNo, snapshot))
	require.NoError(t, model.DB.Delete(&model.SubscriptionPlan{}, plan.Id).Error)
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	installStripeCheckoutResolver(t, func(_ context.Context, got StripeCheckoutRequestSnapshot, existing string) (*StripeCheckoutResolution, error) {
		assert.Empty(t, existing)
		return checkoutResolution(got, "cs_reconcile_sub_paid", "complete", "paid", now+3600), nil
	})

	require.NoError(t, ReconcileStripeCheckoutOrders(context.Background()))
	var stored model.SubscriptionOrder
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, stored.Status)
	assert.False(t, stored.CapacityReserved)
	assert.Equal(t, now+3600, stored.ProviderExpiresAt)
	var subscription model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ? AND plan_id = ?", user.Id, plan.Id).First(&subscription).Error)
	assert.Equal(t, int64(4321), subscription.AmountTotal)
	assert.Equal(t, SubscriptionResetWeekly, subscription.QuotaResetPeriodSnapshot)
}

func TestStripeCheckoutReconciliationKeepsOpenAndCompleteUnpaidPending(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-unpaid", 0, "default")
	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 3, 1.5, "USD", "ref_reconcile_unpaid")
	require.NoError(t, err)
	snapshot := walletCheckoutSnapshot(order, user)
	require.NoError(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot))
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	var calls atomic.Int32
	installStripeCheckoutResolver(t, func(_ context.Context, got StripeCheckoutRequestSnapshot, existing string) (*StripeCheckoutResolution, error) {
		call := calls.Add(1)
		if call == 1 {
			assert.Empty(t, existing)
			return checkoutResolution(got, "cs_reconcile_unpaid", "open", "unpaid", now+3600), nil
		}
		assert.Equal(t, "cs_reconcile_unpaid", existing, "a bound order must retrieve instead of recreating Checkout")
		return checkoutResolution(got, existing, "complete", "unpaid", now+3600), nil
	})

	require.NoError(t, ReconcileStripeCheckoutOrders(context.Background()))
	var stored model.TopUp
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
	assert.Equal(t, StripeReconciliationCheckoutPending, stored.ReconciliationState)
	assert.Zero(t, stored.ReconciliationAttempts)
	require.NotNil(t, stored.ProviderSessionId)
	assert.Equal(t, "cs_reconcile_unpaid", *stored.ProviderSessionId)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("id = ?", order.Id).Update("reconciliation_next_at", 0).Error)
	require.NoError(t, ReconcileStripeCheckoutOrders(context.Background()))
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
	assert.Equal(t, int32(2), calls.Load())
	assert.Zero(t, storedUserQuota(t, user.Id))
}

func TestStripeCheckoutHealthyOpenPollsDoNotExhaustRecovery(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-long-open", 0, "default")
	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 2, 1, "USD", "ref_reconcile_long_open")
	require.NoError(t, err)
	snapshot := walletCheckoutSnapshot(order, user)
	require.NoError(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot))
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)

	var calls atomic.Int32
	installStripeCheckoutResolver(t, func(_ context.Context, got StripeCheckoutRequestSnapshot, existing string) (*StripeCheckoutResolution, error) {
		call := calls.Add(1)
		sessionID := existing
		if sessionID == "" {
			sessionID = "cs_reconcile_long_open"
		}
		if call <= int32(stripeCheckoutReconciliationMaxAttempts+1) {
			return checkoutResolution(got, sessionID, "open", "unpaid", now+24*3600), nil
		}
		return checkoutResolution(got, sessionID, "complete", "paid", now+24*3600), nil
	})

	for poll := 0; poll < stripeCheckoutReconciliationMaxAttempts+1; poll++ {
		if poll > 0 {
			require.NoError(t, model.DB.Model(&model.TopUp{}).Where("id = ?", order.Id).
				Update("reconciliation_next_at", 0).Error)
		}
		require.NoError(t, ReconcileStripeCheckoutOrders(context.Background()))
		var pending model.TopUp
		require.NoError(t, model.DB.First(&pending, order.Id).Error)
		assert.Equal(t, TopUpStatusPending, pending.Status)
		assert.Equal(t, StripeReconciliationCheckoutPending, pending.ReconciliationState)
		assert.Zero(t, pending.ReconciliationAttempts, "healthy polls must reset the crash/failure budget")
	}

	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("id = ?", order.Id).
		Update("reconciliation_next_at", 0).Error)
	require.NoError(t, ReconcileStripeCheckoutOrders(context.Background()))
	assert.Equal(t, int32(stripeCheckoutReconciliationMaxAttempts+2), calls.Load())
	var completed model.TopUp
	require.NoError(t, model.DB.First(&completed, order.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, completed.Status)
	assert.EqualValues(t, order.CreditQuota, storedUserQuota(t, user.Id))
	require.NoError(t, ReconcileStripeCheckoutOrders(context.Background()))
	assert.Equal(t, int32(stripeCheckoutReconciliationMaxAttempts+2), calls.Load())
	assert.EqualValues(t, order.CreditQuota, storedUserQuota(t, user.Id), "paid settlement must remain exactly once")
}

func storedUserQuota(t *testing.T, userID int) int {
	t.Helper()
	var user model.User
	require.NoError(t, model.DB.Select("quota").First(&user, userID).Error)
	return user.Quota
}

func TestStripeCheckoutRequestSnapshotIsImmutable(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-immutable", 0, "default")
	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 1, 1, "USD", "ref_reconcile_immutable")
	require.NoError(t, err)
	snapshot := walletCheckoutSnapshot(order, user)
	require.NoError(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot))
	var before model.TopUp
	require.NoError(t, model.DB.First(&before, order.Id).Error)

	changed := snapshot
	changed.SuccessURL = "https://attacker.invalid/success"
	require.ErrorIs(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, changed), ErrStripeCheckoutBindingMismatch)
	var after model.TopUp
	require.NoError(t, model.DB.First(&after, order.Id).Error)
	assert.Equal(t, before.CheckoutRequest, after.CheckoutRequest)
	assert.Equal(t, before.CheckoutFingerprint, after.CheckoutFingerprint)
}

func TestStripeCheckoutReconciliationLeaseAllowsOnlyOneProviderCall(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-one-lease", 0, "default")
	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 1, 1, "USD", "ref_reconcile_one_lease")
	require.NoError(t, err)
	snapshot := walletCheckoutSnapshot(order, user)
	require.NoError(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot))
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	installStripeCheckoutResolver(t, func(_ context.Context, got StripeCheckoutRequestSnapshot, _ string) (*StripeCheckoutResolution, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return checkoutResolution(got, "cs_reconcile_one_lease", "open", "unpaid", now+3600), nil
	})
	first := make(chan error, 1)
	go func() { first <- ReconcileStripeCheckoutOrders(context.Background()) }()
	<-started
	require.NoError(t, ReconcileStripeCheckoutOrders(context.Background()))
	assert.Equal(t, int32(1), calls.Load())
	close(release)
	require.NoError(t, <-first)
}

func TestStripeCheckoutReconciliationStaleLeaseCannotCredit(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-stale", 0, "default")
	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 1, 1, "USD", "ref_reconcile_stale")
	require.NoError(t, err)
	snapshot := walletCheckoutSnapshot(order, user)
	require.NoError(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot))
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	started := make(chan struct{})
	release := make(chan struct{})
	installStripeCheckoutResolver(t, func(_ context.Context, got StripeCheckoutRequestSnapshot, _ string) (*StripeCheckoutResolution, error) {
		close(started)
		<-release
		return checkoutResolution(got, "cs_reconcile_stale", "complete", "paid", now+3600), nil
	})
	done := make(chan error, 1)
	go func() { done <- ReconcileStripeCheckoutOrders(context.Background()) }()
	<-started
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("id = ?", order.Id).Updates(map[string]any{
		"reconciliation_lease_owner": "new-owner", "reconciliation_lease_expires_at": now + 3600,
	}).Error)
	close(release)
	err = <-done
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrStripeCheckoutReconciliationLeaseLost))
	assert.Zero(t, storedUserQuota(t, user.Id))
	var stored model.TopUp
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
}

func TestStripeCheckoutCorruptSnapshotMovesToManualReview(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-corrupt", 0, "default")
	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 1, 1, "USD", "ref_reconcile_corrupt")
	require.NoError(t, err)
	snapshot := walletCheckoutSnapshot(order, user)
	require.NoError(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot))
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("id = ?", order.Id).
		Update("checkout_request", `{\"tampered\":true}`).Error)
	var calls atomic.Int32
	installStripeCheckoutResolver(t, func(context.Context, StripeCheckoutRequestSnapshot, string) (*StripeCheckoutResolution, error) {
		calls.Add(1)
		return nil, assert.AnError
	})
	require.Error(t, ReconcileStripeCheckoutOrders(context.Background()))
	assert.Zero(t, calls.Load())
	var stored model.TopUp
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, StripeReconciliationProviderReview, stored.ReconciliationState)
	assert.Zero(t, stored.ReconciliationNextAt)
	assert.Equal(t, TopUpStatusPending, stored.Status)
	assert.Contains(t, stored.ReconciliationDetail, ErrStripeCheckoutBindingMismatch.Error())
}

func TestStripeCheckoutManualReviewIsNotReclaimedByLaterSweep(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-stable-review", 0, "default")
	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 1, 1, "USD", "ref_reconcile_stable_review")
	require.NoError(t, err)
	snapshot := walletCheckoutSnapshot(order, user)
	require.NoError(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot))
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	var calls atomic.Int32
	installStripeCheckoutResolver(t, func(_ context.Context, got StripeCheckoutRequestSnapshot, _ string) (*StripeCheckoutResolution, error) {
		calls.Add(1)
		resolved := checkoutResolution(got, "cs_reconcile_stable_review", "complete", "paid", now+3600)
		resolved.ClientReferenceID = "ref_other_order"
		return resolved, nil
	})

	require.Error(t, ReconcileStripeCheckoutOrders(context.Background()))
	assert.Equal(t, int32(1), calls.Load())
	var first model.TopUp
	require.NoError(t, model.DB.First(&first, order.Id).Error)
	assert.Equal(t, StripeReconciliationProviderReview, first.ReconciliationState)
	assert.Zero(t, first.ReconciliationNextAt)
	// Even a legacy/manual row already at the attempt ceiling must remain
	// immutable; the exhausted-lease sweep must not overwrite its diagnosis.
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("id = ?", order.Id).
		Update("reconciliation_attempts", stripeCheckoutReconciliationMaxAttempts).Error)
	require.NoError(t, model.DB.First(&first, order.Id).Error)
	firstDetail := first.ReconciliationDetail

	require.NoError(t, ReconcileStripeCheckoutOrders(context.Background()))
	assert.Equal(t, int32(1), calls.Load(), "manual-review orders must not call the provider again")
	var second model.TopUp
	require.NoError(t, model.DB.First(&second, order.Id).Error)
	assert.Equal(t, first.ReconciliationAttempts, second.ReconciliationAttempts)
	assert.Equal(t, StripeReconciliationProviderReview, second.ReconciliationState)
	assert.Equal(t, firstDetail, second.ReconciliationDetail)
}

func TestStripeCheckoutProviderErrorDetailIsRedactedBeforePersistence(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-redacted-error", 0, "default")
	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 1, 1, "USD", "ref_reconcile_redacted_error")
	require.NoError(t, err)
	snapshot := walletCheckoutSnapshot(order, user)
	require.NoError(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot))
	installStripeCheckoutResolver(t, func(context.Context, StripeCheckoutRequestSnapshot, string) (*StripeCheckoutResolution, error) {
		return nil, errors.New("provider rejected secret=sk_live_do_not_persist")
	})

	require.Error(t, ReconcileStripeCheckoutOrders(context.Background()))
	var stored model.TopUp
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, "provider reconciliation failed", stored.ReconciliationDetail)
	assert.NotContains(t, stored.ReconciliationDetail, "sk_live_do_not_persist")
}

func TestStripeCheckoutSnapshotRejectsUnsafeCustomerBinding(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-customer", 0, "default")
	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 1, 1, "USD", "ref_reconcile_customer")
	require.NoError(t, err)
	snapshot := walletCheckoutSnapshot(order, user)
	snapshot.CustomerID = "customer-attacker"
	require.ErrorIs(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot), ErrStripeCheckoutBindingMismatch)
	snapshot.CustomerID = "cus_safe"
	snapshot.CustomerEmail = "conflicting@example.com"
	require.ErrorIs(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot), ErrStripeCheckoutBindingMismatch)
}

func TestStripeCheckoutSnapshotRejectsUnsafeRedirectURLs(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-redirect", 0, "default")
	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 1, 1, "USD", "ref_reconcile_redirect")
	require.NoError(t, err)
	base := walletCheckoutSnapshot(order, user)

	unsafe := map[string]string{
		"empty":             "",
		"surrounding space": " https://example.com/success",
		"opaque":            "https:example.com/success",
		"missing host":      "https:///success",
		"credentials":       "https://user:secret@example.com/success",
		"remote plaintext":  "http://example.com/success",
		"backslash":         `https://example.com\@evil.test/success`,
		"control":           "https://example.com/success\nnext",
		"bidi":              "https://example.com/\u202esuccess",
		"unicode host":      "https://exämple.test/success",
		"empty host label":  "https://example..test/success",
		"oversized":         "https://example.com/" + strings.Repeat("a", maxStripeCheckoutRedirectURLBytes),
	}
	for name, redirectURL := range unsafe {
		t.Run(name+" success", func(t *testing.T) {
			snapshot := base
			snapshot.SuccessURL = redirectURL
			require.ErrorIs(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot), ErrStripeCheckoutBindingMismatch)
		})
		t.Run(name+" cancel", func(t *testing.T) {
			snapshot := base
			snapshot.CancelURL = redirectURL
			require.ErrorIs(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot), ErrStripeCheckoutBindingMismatch)
		})
	}

	base.SuccessURL = "http://127.0.0.1:3000/success?checkout=1"
	base.CancelURL = "http://app.localhost:3000/cancel#topup"
	require.NoError(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, base))
}

func TestStripeCheckoutReconciliationRejectsChangedWalletOwnerAndCustomer(t *testing.T) {
	t.Run("wallet owner changed after snapshot", func(t *testing.T) {
		initSubDB(t)
		owner := subUser(t, "stripe-reconcile-original-owner", 0, "default")
		other := subUser(t, "stripe-reconcile-changed-owner", 0, "default")
		order, err := CreateBoundStripeTopUpWithTradeNo(owner.Id, 1, 1, "USD", "ref_reconcile_owner_changed")
		require.NoError(t, err)
		snapshot := walletCheckoutSnapshot(order, owner)
		require.NoError(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot))
		require.NoError(t, model.DB.Model(&model.TopUp{}).Where("id = ?", order.Id).Update("user_id", other.Id).Error)
		now, err := model.DatabaseUnixTimestamp(model.DB)
		require.NoError(t, err)
		installStripeCheckoutResolver(t, func(_ context.Context, got StripeCheckoutRequestSnapshot, _ string) (*StripeCheckoutResolution, error) {
			return checkoutResolution(got, "cs_reconcile_owner_changed", "complete", "paid", now+3600), nil
		})

		require.Error(t, ReconcileStripeCheckoutOrders(context.Background()))
		assert.Zero(t, storedUserQuota(t, owner.Id))
		assert.Zero(t, storedUserQuota(t, other.Id))
	})

	t.Run("provider customer differs from immutable customer", func(t *testing.T) {
		initSubDB(t)
		user := subUser(t, "stripe-reconcile-customer-mismatch", 0, "default")
		user.StripeCustomer = "cus_expected"
		require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).
			Update("stripe_customer", user.StripeCustomer).Error)
		order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 1, 1, "USD", "ref_reconcile_customer_mismatch")
		require.NoError(t, err)
		snapshot := walletCheckoutSnapshot(order, user)
		require.NoError(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot))
		now, err := model.DatabaseUnixTimestamp(model.DB)
		require.NoError(t, err)
		installStripeCheckoutResolver(t, func(_ context.Context, got StripeCheckoutRequestSnapshot, _ string) (*StripeCheckoutResolution, error) {
			resolved := checkoutResolution(got, "cs_reconcile_customer_mismatch", "complete", "paid", now+3600)
			resolved.CustomerID = "cus_other"
			return resolved, nil
		})

		require.Error(t, ReconcileStripeCheckoutOrders(context.Background()))
		assert.Zero(t, storedUserQuota(t, user.Id))
		var stored model.TopUp
		require.NoError(t, model.DB.First(&stored, order.Id).Error)
		assert.Equal(t, StripeReconciliationProviderReview, stored.ReconciliationState)
	})
}

func TestStripeCheckoutReconciliationExhaustedClaimCannotStarve(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reconcile-exhausted", 0, "default")
	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 1, 1, "USD", "ref_reconcile_exhausted")
	require.NoError(t, err)
	snapshot := walletCheckoutSnapshot(order, user)
	require.NoError(t, ConfigureStripeTopUpCheckoutRequest(order.TradeNo, snapshot))
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("id = ?", order.Id).Updates(map[string]any{
		"reconciliation_attempts":         stripeCheckoutReconciliationMaxAttempts,
		"reconciliation_lease_owner":      "crashed-final-worker",
		"reconciliation_lease_expires_at": 1,
	}).Error)
	var calls atomic.Int32
	installStripeCheckoutResolver(t, func(context.Context, StripeCheckoutRequestSnapshot, string) (*StripeCheckoutResolution, error) {
		calls.Add(1)
		return nil, assert.AnError
	})

	require.NoError(t, ReconcileStripeCheckoutOrders(context.Background()))
	assert.Zero(t, calls.Load())
	var stored model.TopUp
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
	assert.Equal(t, StripeReconciliationProviderReview, stored.ReconciliationState)
	assert.Contains(t, stored.ReconciliationDetail, "attempt limit")
	assert.Empty(t, stored.ReconciliationLeaseOwner)
	assert.Zero(t, stored.ReconciliationNextAt)
}

func TestStripeRefundAndDisputePolicyOnlyMarksManualReview(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reversal-review", 777, "default")
	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 1, 1, "USD", "ref_reversal_review")
	require.NoError(t, err)
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("id = ?", order.Id).Updates(map[string]any{
		"status": TopUpStatusSuccess, "complete_time": now,
	}).Error)

	require.NoError(t, FlagStripePaymentReversalReview(order.TradeNo, "evt_refund_review", "charge.refunded", now))
	// Stripe retries are idempotent: the same provider event produces the same
	// immutable audit payload and never creates a second financial mutation.
	require.NoError(t, FlagStripePaymentReversalReview(order.TradeNo, "evt_refund_review", "charge.refunded", now))
	var stored model.TopUp
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, StripeReconciliationProviderReview, stored.ReconciliationState)
	assert.Contains(t, stored.ReconciliationDetail, "automatic entitlement reversal is disabled")
	assert.Equal(t, 777, storedUserQuota(t, user.Id), "refund policy must not guess at already-consumed wallet value")
	var auditCount int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("content LIKE ?", "%evt_refund_review%").Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)
}

func TestUnmatchedStripeDisputeCreatesDurableManualCorrelationAudit(t *testing.T) {
	initSubDB(t)
	now, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	require.NoError(t, RecordUnmatchedStripePaymentReversalReview("evt_unmatched_dispute", "charge.dispute.created", now))
	require.NoError(t, RecordUnmatchedStripePaymentReversalReview("evt_unmatched_dispute", "charge.dispute.created", now))
	var auditCount int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("content LIKE ?", "%evt_unmatched_dispute%").Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)
}
