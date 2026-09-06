package commerce

import (
	"errors"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	stripepayments "github.com/tokenrouter/tokenrouter/internal/payments/stripe"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net/http"
	"strings"
	"time"
)

// SubscriptionStripePayRequest is the reference request body.
type SubscriptionStripePayRequest struct {
	PlanId int `json:"plan_id"`
}

// SubscriptionRequestStripePay creates a Stripe checkout session for a
// subscription plan (reference contract: compliance gate, plan/Stripe
// validation, per-user purchase cap, pending order with the sub_ref_ trade
// no, {message,data} response shapes).
func SubscriptionRequestStripePay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req SubscriptionStripePayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}

	plan, err := billingsvc.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if !plan.Enabled {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "套餐未启用"})
		return
	}
	if plan.StripePriceId == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "该套餐未配置 StripePriceId"})
		return
	}
	stripeSecret := env.GetEnv("STRIPE_SECRET_KEY", "")
	if !stripepayments.ValidSecret(stripeSecret) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Stripe 未配置或密钥无效"})
		return
	}
	if strings.TrimSpace(env.GetEnv("STRIPE_WEBHOOK_SECRET", "")) == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Stripe Webhook 未配置"})
		return
	}
	returnURL := paymentReturnPath("/wallet")
	if returnURL == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Stripe 回调地址配置无效"})
		return
	}

	userId := requestctx.GetUserId(c)
	user, err := userssvc.GetUserByID(userId)
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "用户不存在"})
		return
	}

	expectedCurrency, err := billingsvc.NormalizeStripeCurrency(plan.Currency)
	if err != nil || !billingsvc.StripeCurrencySupported(expectedCurrency) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "套餐币种配置无效"})
		return
	}
	expectedAmount, err := billingsvc.StripeMoneyToMinorUnitsForCurrency(strings.TrimSpace(plan.PriceAmount), expectedCurrency)
	if err != nil || expectedAmount <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "套餐价格配置无效"})
		return
	}
	referenceSuffix, err := cryptoutil.SecureRandomAlphanumeric(4)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	stripePrice, err := stripepayments.RetrievePrice(stripeSecret, plan.StripePriceId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "校验 Stripe Price 失败"})
		return
	}
	if err := stripepayments.ValidateSubscriptionPrice(stripePrice, plan.StripePriceId, expectedAmount, expectedCurrency); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}

	referenceId := billingsvc.NewSubscriptionStripeTradeNo(user.Id, time.Now().UnixMilli(), referenceSuffix)
	order, err := billingsvc.CreateBoundStripeSubscriptionOrder(userId, plan.Id, billingsvc.ValidatedStripePrice{
		ID: stripePrice.ID, AmountMinor: stripePrice.UnitAmount, Currency: string(stripePrice.Currency),
	}, referenceId)
	if err != nil {
		if errors.Is(err, billingsvc.ErrSubscriptionPurchaseLimit) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	payLink, err := genStripeSubscriptionLink(stripeSecret, order, user.StripeCustomer, user.Email, returnURL)
	if err != nil {
		if stripepayments.RequestDefinitelyRejected(err) {
			if statusErr := billingsvc.UpdatePendingSubscriptionOrderStatus(referenceId, billingsvc.PaymentProviderStripe, billingsvc.TopUpStatusFailed); statusErr != nil {
				logging.SysError("Stripe subscription checkout rejection status update failed trade_no=" + referenceId + ": " + statusErr.Error())
			}
		} else if errors.Is(err, billingsvc.ErrStripeCheckoutBindingMismatch) {
			if flagErr := billingsvc.FlagStripeSubscriptionReconciliation(referenceId, billingsvc.StripeReconciliationBindingMismatch); flagErr != nil {
				logging.SysError("Stripe subscription reconciliation update failed trade_no=" + referenceId + ": " + flagErr.Error())
			}
		} else {
			if flagErr := billingsvc.FlagStripeSubscriptionReconciliation(referenceId, billingsvc.StripeReconciliationCreationUnknown); flagErr != nil {
				logging.SysError("Stripe subscription reconciliation update failed trade_no=" + referenceId + ": " + flagErr.Error())
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"pay_link": payLink,
		},
	})
}

// genStripeSubscriptionLink opens a one-time payment Checkout session. Local
// entitlements have a fixed end date and this service does not implement
// Stripe renewal fulfillment, so recurring billing would be unsafe.
func genStripeSubscriptionLink(apiKey string, order *model.SubscriptionOrder, customerId string, email string, returnURL string) (string, error) {
	if !stripepayments.ValidSecret(apiKey) || order == nil || strings.TrimSpace(order.ProviderPriceId) == "" {
		return "", billingsvc.ErrSubscriptionOrderDataInvalid
	}
	referenceId := order.TradeNo
	customerEmail := ""
	if customerId == "" {
		customerEmail = email
	}
	snapshot := billingsvc.StripeCheckoutRequestSnapshot{
		Version: billingsvc.StripeCheckoutRequestSnapshotVersion,
		TradeNo: referenceId, OrderType: billingsvc.StripeOrderTypeSubscription, Mode: billingsvc.StripeCheckoutModeSubscription,
		AmountMinor: order.ProviderAmountMinor, Currency: order.ProviderCurrency, PriceID: order.ProviderPriceId,
		SuccessURL: returnURL, CancelURL: returnURL,
		CustomerID: customerId, CustomerEmail: customerEmail,
		IdempotencyKey: "subscription-checkout-" + referenceId,
	}
	if err := billingsvc.ConfigureStripeSubscriptionCheckoutRequest(referenceId, snapshot); err != nil {
		return "", err
	}
	params := stripepayments.CheckoutParamsFromSnapshot(snapshot)
	result, err := stripepayments.CreateCheckoutSession(apiKey, params)
	if err != nil {
		return "", err
	}
	if err := stripepayments.ValidateCreatedCheckoutSession(result, referenceId, billingsvc.StripeCheckoutModeSubscription,
		billingsvc.StripeOrderTypeSubscription, order.ProviderAmountMinor, order.ProviderCurrency, order.ProviderPriceId); err != nil {
		return "", err
	}
	if err := billingsvc.BindStripeSubscriptionSessionWithExpiry(referenceId, result.ID, result.ExpiresAt); err != nil {
		return "", err
	}
	return result.URL, nil
}
