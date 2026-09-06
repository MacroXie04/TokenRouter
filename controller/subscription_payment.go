package controller

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
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

	plan, err := service.GetSubscriptionPlanById(req.PlanId)
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
	stripeSecret := common.GetEnv("STRIPE_SECRET_KEY", "")
	if !validStripeSecret(stripeSecret) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Stripe 未配置或密钥无效"})
		return
	}
	if strings.TrimSpace(common.GetEnv("STRIPE_WEBHOOK_SECRET", "")) == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Stripe Webhook 未配置"})
		return
	}
	returnURL := paymentReturnPath("/wallet")
	if returnURL == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Stripe 回调地址配置无效"})
		return
	}

	userId := common.GetUserId(c)
	user, err := service.GetUserByID(userId)
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "用户不存在"})
		return
	}

	expectedCurrency, err := service.NormalizeStripeCurrency(plan.Currency)
	if err != nil || !service.StripeCurrencySupported(expectedCurrency) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "套餐币种配置无效"})
		return
	}
	expectedAmount, err := service.StripeMoneyToMinorUnitsForCurrency(strings.TrimSpace(plan.PriceAmount), expectedCurrency)
	if err != nil || expectedAmount <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "套餐价格配置无效"})
		return
	}
	referenceSuffix, err := common.SecureRandomAlphanumeric(4)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	stripePrice, err := retrieveStripePrice(stripeSecret, plan.StripePriceId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "校验 Stripe Price 失败"})
		return
	}
	if err := validateStripeSubscriptionPrice(stripePrice, plan.StripePriceId, expectedAmount, expectedCurrency); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}

	referenceId := service.NewSubscriptionStripeTradeNo(user.Id, time.Now().UnixMilli(), referenceSuffix)
	order, err := service.CreateBoundStripeSubscriptionOrder(userId, plan.Id, service.ValidatedStripePrice{
		ID: stripePrice.ID, AmountMinor: stripePrice.UnitAmount, Currency: string(stripePrice.Currency),
	}, referenceId)
	if err != nil {
		if errors.Is(err, service.ErrSubscriptionPurchaseLimit) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	payLink, err := genStripeSubscriptionLink(stripeSecret, order, user.StripeCustomer, user.Email, returnURL)
	if err != nil {
		if stripeRequestDefinitelyRejected(err) {
			if statusErr := service.UpdatePendingSubscriptionOrderStatus(referenceId, service.PaymentProviderStripe, service.TopUpStatusFailed); statusErr != nil {
				common.SysError("Stripe subscription checkout rejection status update failed trade_no=" + referenceId + ": " + statusErr.Error())
			}
		} else if errors.Is(err, service.ErrStripeCheckoutBindingMismatch) {
			if flagErr := service.FlagStripeSubscriptionReconciliation(referenceId, service.StripeReconciliationBindingMismatch); flagErr != nil {
				common.SysError("Stripe subscription reconciliation update failed trade_no=" + referenceId + ": " + flagErr.Error())
			}
		} else {
			if flagErr := service.FlagStripeSubscriptionReconciliation(referenceId, service.StripeReconciliationCreationUnknown); flagErr != nil {
				common.SysError("Stripe subscription reconciliation update failed trade_no=" + referenceId + ": " + flagErr.Error())
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
	if !validStripeSecret(apiKey) || order == nil || strings.TrimSpace(order.ProviderPriceId) == "" {
		return "", service.ErrSubscriptionOrderDataInvalid
	}
	referenceId := order.TradeNo
	customerEmail := ""
	if customerId == "" {
		customerEmail = email
	}
	snapshot := service.StripeCheckoutRequestSnapshot{
		Version: service.StripeCheckoutRequestSnapshotVersion,
		TradeNo: referenceId, OrderType: service.StripeOrderTypeSubscription, Mode: service.StripeCheckoutModeSubscription,
		AmountMinor: order.ProviderAmountMinor, Currency: order.ProviderCurrency, PriceID: order.ProviderPriceId,
		SuccessURL: returnURL, CancelURL: returnURL,
		CustomerID: customerId, CustomerEmail: customerEmail,
		IdempotencyKey: "subscription-checkout-" + referenceId,
	}
	if err := service.ConfigureStripeSubscriptionCheckoutRequest(referenceId, snapshot); err != nil {
		return "", err
	}
	params := stripeCheckoutParamsFromSnapshot(snapshot)
	result, err := createStripeCheckoutSession(apiKey, params)
	if err != nil {
		return "", err
	}
	if err := validateCreatedStripeCheckoutSession(result, referenceId, service.StripeCheckoutModeSubscription,
		service.StripeOrderTypeSubscription, order.ProviderAmountMinor, order.ProviderCurrency, order.ProviderPriceId); err != nil {
		return "", err
	}
	if err := service.BindStripeSubscriptionSessionWithExpiry(referenceId, result.ID, result.ExpiresAt); err != nil {
		return "", err
	}
	return result.URL, nil
}
