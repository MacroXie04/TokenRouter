package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/checkout/session"
	"github.com/stripe/stripe-go/v81/price"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/service"
)

func init() {
	service.RegisterStripeCheckoutResolver(resolveStripeCheckoutSnapshot)
}

func validStripeSecret(secret string) bool {
	if secret != strings.TrimSpace(secret) {
		return false
	}
	for _, prefix := range []string{"sk_test_", "sk_live_", "rk_test_", "rk_live_"} {
		if strings.HasPrefix(secret, prefix) && len(secret) > len(prefix) {
			return true
		}
	}
	return false
}

func createStripeCheckoutSession(secret string, params *stripe.CheckoutSessionParams) (*stripe.CheckoutSession, error) {
	client := session.Client{B: stripe.GetBackend(stripe.APIBackend), Key: secret}
	return client.New(params)
}

func retrieveStripeCheckoutSession(secret, sessionID string, params *stripe.CheckoutSessionParams) (*stripe.CheckoutSession, error) {
	client := session.Client{B: stripe.GetBackend(stripe.APIBackend), Key: secret}
	return client.Get(sessionID, params)
}

// stripeCheckoutParamsFromSnapshot is shared by the interactive request and
// the background reconciler. This keeps an ambiguous create retry byte-for-
// byte equivalent at Stripe's parameter layer and reuses the original
// idempotency key.
func stripeCheckoutParamsFromSnapshot(snapshot service.StripeCheckoutRequestSnapshot) *stripe.CheckoutSessionParams {
	params := &stripe.CheckoutSessionParams{
		ClientReferenceID: stripe.String(snapshot.TradeNo),
		SuccessURL:        stripe.String(snapshot.SuccessURL),
		CancelURL:         stripe.String(snapshot.CancelURL),
		Mode:              stripe.String(snapshot.Mode),
	}
	if snapshot.OrderType == service.StripeOrderTypeWallet {
		params.LineItems = []*stripe.CheckoutSessionLineItemParams{{
			PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
				Currency:   stripe.String(strings.ToLower(snapshot.Currency)),
				UnitAmount: stripe.Int64(snapshot.AmountMinor),
				ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
					Name: stripe.String(snapshot.ProductName),
				},
			},
			Quantity: stripe.Int64(1),
		}}
		params.AllowPromotionCodes = stripe.Bool(false)
		params.Metadata = map[string]string{
			"order_type": service.StripeOrderTypeWallet,
			"user_id":    fmt.Sprintf("%d", snapshot.UserID),
			"trade_no":   snapshot.TradeNo,
			"quota":      fmt.Sprintf("%d", snapshot.WalletAmount),
		}
		params.PaymentIntentData = &stripe.CheckoutSessionPaymentIntentDataParams{Metadata: params.Metadata}
		if snapshot.CustomerID == "" {
			if snapshot.CustomerEmail != "" {
				params.CustomerEmail = stripe.String(snapshot.CustomerEmail)
			}
			params.CustomerCreation = stripe.String(string(stripe.CheckoutSessionCustomerCreationAlways))
		} else {
			params.Customer = stripe.String(snapshot.CustomerID)
		}
	} else {
		params.LineItems = []*stripe.CheckoutSessionLineItemParams{{
			Price: stripe.String(snapshot.PriceID), Quantity: stripe.Int64(1),
		}}
		params.Metadata = map[string]string{
			"order_type": service.StripeOrderTypeSubscription,
			"trade_no":   snapshot.TradeNo,
			"price_id":   snapshot.PriceID,
		}
		params.PaymentIntentData = &stripe.CheckoutSessionPaymentIntentDataParams{Metadata: params.Metadata}
		if snapshot.CustomerID == "" {
			if snapshot.CustomerEmail != "" {
				params.CustomerEmail = stripe.String(snapshot.CustomerEmail)
			}
		} else {
			params.Customer = stripe.String(snapshot.CustomerID)
		}
	}
	params.SetIdempotencyKey(snapshot.IdempotencyKey)
	return params
}

func resolveStripeCheckoutSnapshot(ctx context.Context, snapshot service.StripeCheckoutRequestSnapshot, existingSessionID string) (*service.StripeCheckoutResolution, error) {
	secret := common.GetEnv("STRIPE_SECRET_KEY", "")
	if !validStripeSecret(secret) {
		return nil, errors.New("Stripe reconciliation is not configured")
	}
	params := stripeCheckoutParamsFromSnapshot(snapshot)
	params.Context = ctx
	var result *stripe.CheckoutSession
	var err error
	existingSessionID = strings.TrimSpace(existingSessionID)
	if existingSessionID == "" {
		result, err = createStripeCheckoutSession(secret, params)
	} else {
		if !strings.HasPrefix(existingSessionID, "cs_") {
			return nil, service.ErrStripeCheckoutBindingMismatch
		}
		result, err = retrieveStripeCheckoutSession(secret, existingSessionID, &stripe.CheckoutSessionParams{Params: stripe.Params{Context: ctx}})
	}
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, service.ErrStripeCheckoutBindingMismatch
	}
	customerID := ""
	if result.Customer != nil {
		customerID = result.Customer.ID
	}
	return &service.StripeCheckoutResolution{
		SessionID: result.ID, ClientReferenceID: result.ClientReferenceID,
		OrderType: result.Metadata["order_type"], Mode: string(result.Mode),
		AmountMinor: result.AmountTotal, Currency: string(result.Currency), PriceID: result.Metadata["price_id"],
		Status: string(result.Status), PaymentStatus: string(result.PaymentStatus), CustomerID: customerID,
		ExpiresAt: result.ExpiresAt,
	}, nil
}

func retrieveStripePrice(secret, priceID string) (*stripe.Price, error) {
	client := price.Client{B: stripe.GetBackend(stripe.APIBackend), Key: secret}
	return client.Get(priceID, nil)
}

func validateCreatedStripeCheckoutSession(result *stripe.CheckoutSession, referenceID, expectedMode, expectedOrderType string, expectedAmount int64, expectedCurrency, expectedPriceID string) error {
	if result == nil || !strings.HasPrefix(strings.TrimSpace(result.ID), "cs_") || strings.TrimSpace(result.URL) == "" ||
		result.ClientReferenceID != referenceID || string(result.Mode) != expectedMode ||
		result.AmountTotal != expectedAmount || result.AmountTotal <= 0 ||
		result.Metadata["order_type"] != expectedOrderType || result.Metadata["trade_no"] != referenceID {
		return service.ErrStripeCheckoutBindingMismatch
	}
	if expectedPriceID != "" && result.Metadata["price_id"] != expectedPriceID {
		return service.ErrStripeCheckoutBindingMismatch
	}
	currency, err := service.NormalizeStripeCurrency(string(result.Currency))
	if err != nil || currency != expectedCurrency {
		return service.ErrStripeCheckoutBindingMismatch
	}
	return nil
}

func validateStripeSubscriptionPrice(price *stripe.Price, expectedID string, expectedAmount int64, expectedCurrency string) error {
	if price == nil || price.ID != expectedID || !price.Active || price.Deleted ||
		price.Type != stripe.PriceTypeOneTime || price.Recurring != nil ||
		price.BillingScheme != stripe.PriceBillingSchemePerUnit ||
		price.CustomUnitAmount != nil || price.TransformQuantity != nil ||
		price.UnitAmount <= 0 || price.UnitAmount != expectedAmount ||
		(price.UnitAmountDecimal != 0 && price.UnitAmountDecimal != float64(price.UnitAmount)) {
		return fmt.Errorf("Stripe Price 与套餐配置不一致")
	}
	currency, err := service.NormalizeStripeCurrency(string(price.Currency))
	if err != nil || currency != expectedCurrency {
		return fmt.Errorf("Stripe Price 与套餐配置不一致")
	}
	return nil
}

// stripeRequestDefinitelyRejected returns true only when Stripe supplied a
// non-retryable 4xx response. Transport failures, 5xx responses, rate limits
// and ambiguous/malformed success responses leave the local order pending.
func stripeRequestDefinitelyRejected(err error) bool {
	var stripeErr *stripe.Error
	if !errors.As(err, &stripeErr) {
		return false
	}
	status := stripeErr.HTTPStatusCode
	return status >= 400 && status < 500 && status != 408 && status != 409 && status != 425 && status != 429
}
