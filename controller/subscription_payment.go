package controller

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/checkout/session"

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
	if !strings.HasPrefix(stripeSecret, "sk_") && !strings.HasPrefix(stripeSecret, "rk_") {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Stripe 未配置或密钥无效"})
		return
	}
	if common.GetEnv("STRIPE_WEBHOOK_SECRET", "") == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Stripe Webhook 未配置"})
		return
	}

	userId := common.GetUserId(c)
	user, err := service.GetUserByID(userId)
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "用户不存在"})
		return
	}

	if plan.MaxPurchasePerUser > 0 {
		count, err := service.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "已达到该套餐购买上限"})
			return
		}
	}

	referenceId := service.NewSubscriptionStripeTradeNo(user.Id, time.Now().UnixMilli(), common.RandomAlphanumeric(4))
	payLink, err := genStripeSubscriptionLink(referenceId, user.StripeCustomer, user.Email, plan.StripePriceId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	price, err := service.ParseSubscriptionPlanPrice(plan.PriceAmount)
	if err != nil || price < 0 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	order := model.SubscriptionOrder{
		UserId:          userId,
		PlanId:          plan.Id,
		Money:           price,
		TradeNo:         referenceId,
		PaymentMethod:   service.PaymentMethodStripe,
		PaymentProvider: service.PaymentProviderStripe,
		CreateTime:      time.Now().Unix(),
		Status:          service.TopUpStatusPending,
	}
	if err := model.DB.Create(&order).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"pay_link": payLink,
		},
	})
}

// genStripeSubscriptionLink opens a subscription-mode checkout session
// (reference: ClientReferenceID as the order reference, price with quantity
// 1, success/cancel back to the wallet page, customer email/creation or
// existing Stripe customer).
func genStripeSubscriptionLink(referenceId string, customerId string, email string, priceId string) (string, error) {
	params := &stripe.CheckoutSessionParams{
		ClientReferenceID: stripe.String(referenceId),
		SuccessURL:        stripe.String(paymentReturnPath("/wallet")),
		CancelURL:         stripe.String(paymentReturnPath("/wallet")),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(priceId),
				Quantity: stripe.Int64(1),
			},
		},
		Mode: stripe.String(string(stripe.CheckoutSessionModeSubscription)),
	}
	if customerId == "" {
		if email != "" {
			params.CustomerEmail = stripe.String(email)
		}
		params.CustomerCreation = stripe.String(string(stripe.CheckoutSessionCustomerCreationAlways))
	} else {
		params.Customer = stripe.String(customerId)
	}
	result, err := session.New(params)
	if err != nil {
		return "", err
	}
	return result.URL, nil
}

