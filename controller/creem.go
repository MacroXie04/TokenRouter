package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

const (
	CreemSignatureHeader     = "creem-signature"
	creemWebhookMaxBodyBytes = 64 << 10
	creemWebhookMaxString    = 2048
	creemWebhookMaxEventType = 128
)

// VerifyCreemSignature authenticates the exact raw webhook bytes with the
// reference HMAC-SHA256/lowercase-hex contract.
func VerifyCreemSignature(payload []byte, signature, secret string) bool {
	if secret == "" || len(signature) != sha256.Size*2 {
		return false
	}
	for i := range len(signature) {
		char := signature[i]
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	provided, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hmac.Equal(provided, mac.Sum(nil))
}

type CreemPayRequest struct {
	ProductID     string `json:"product_id"`
	PaymentMethod string `json:"payment_method"`
}

// RequestCreemPay creates a durable wallet order and immutable checkout
// snapshot before contacting Creem through the bounded injectable client.
func RequestCreemPay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	var req CreemPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.PaymentMethod != service.PaymentMethodCreem {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "不支持的支付渠道"})
		return
	}
	if req.ProductID == "" || req.ProductID != strings.TrimSpace(req.ProductID) || len(req.ProductID) > 255 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "请选择产品"})
		return
	}
	config, err := setting.GetCreemConfigChecked()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "产品配置错误"})
		return
	}
	if config.APIKey == "" || config.WebhookSecret == "" || len(config.Products) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Creem 未配置或回调不可用"})
		return
	}
	product, ok := config.FindCreemProduct(req.ProductID)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "产品不存在"})
		return
	}
	userID := common.GetUserId(c)
	suffix, err := common.SecureRandomAlphanumeric(4)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	reference := fmt.Sprintf("creem-api-ref-%d-%d-%s", userID, time.Now().UnixMilli(), suffix)
	tradeNo := "ref_" + sha1Hex(reference)
	order, checkoutRequest, err := service.CreateBoundCreemTopUpWithTradeNo(userID, service.CreemProductEconomics{
		ProductID: product.ProductID,
		Name:      product.Name,
		PriceText: product.PriceText,
		Currency:  product.Currency,
		Quota:     product.Quota,
	}, tradeNo)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	checkout, err := createCreemCheckout(c.Request.Context(), config, checkoutRequest)
	if err != nil {
		if creemRequestDefinitelyRejected(err) {
			if statusErr := service.UpdatePendingTopUpStatus(order.TradeNo, service.PaymentProviderCreem, service.TopUpStatusFailed); statusErr != nil {
				common.SysError("Creem wallet rejection status update failed trade_no=" + order.TradeNo + ": " + statusErr.Error())
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if err := service.BindCreemTopUpCheckout(order.TradeNo, checkout.ID); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data":    gin.H{"checkout_url": checkout.CheckoutURL, "order_id": order.TradeNo},
	})
}

type SubscriptionCreemPayRequest struct {
	PlanID int `json:"plan_id"`
}

// SubscriptionRequestCreemPay creates a capacity-reserving order with an
// immutable entitlement/economic snapshot before contacting Creem.
func SubscriptionRequestCreemPay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	var req SubscriptionCreemPayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanID <= 0 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	plan, err := service.GetSubscriptionPlanById(req.PlanID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if !plan.Enabled {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "套餐未启用"})
		return
	}
	if strings.TrimSpace(plan.CreemProductId) == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "该套餐未配置 CreemProductId"})
		return
	}
	config, err := setting.GetCreemConfigChecked()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Creem 配置无效"})
		return
	}
	if config.WebhookSecret == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Creem Webhook 未配置"})
		return
	}
	if config.APIKey == "" || len(config.Products) == 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Creem 未配置"})
		return
	}
	userID := common.GetUserId(c)
	if user, userErr := service.GetUserByID(userID); userErr != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "用户不存在"})
		return
	}
	suffix, err := common.SecureRandomAlphanumeric(6)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	reference := fmt.Sprintf("sub-creem-ref-%d-%d-%s", userID, time.Now().UnixMilli(), suffix)
	tradeNo := "sub_ref_" + sha1Hex(reference)
	order, checkoutRequest, err := service.CreateBoundCreemSubscriptionOrder(userID, plan.Id, tradeNo)
	if err != nil {
		if errors.Is(err, service.ErrSubscriptionPurchaseLimit) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	checkout, err := createCreemCheckout(c.Request.Context(), config, checkoutRequest)
	if err != nil {
		if creemRequestDefinitelyRejected(err) {
			if statusErr := service.UpdatePendingSubscriptionOrderStatus(order.TradeNo, service.PaymentProviderCreem, service.TopUpStatusFailed); statusErr != nil {
				common.SysError("Creem subscription rejection status update failed trade_no=" + order.TradeNo + ": " + statusErr.Error())
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if err := service.BindCreemSubscriptionCheckout(order.TradeNo, checkout.ID); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data":    gin.H{"checkout_url": checkout.CheckoutURL, "order_id": order.TradeNo},
	})
}

type CreemWebhookEvent struct {
	ID        string `json:"id"`
	EventType string `json:"eventType"`
	CreatedAt int64  `json:"created_at"`
	Object    struct {
		ID        string `json:"id"`
		RequestID string `json:"request_id"`
		Order     struct {
			ID         string `json:"id"`
			Product    string `json:"product"`
			AmountPaid int64  `json:"amount_paid"`
			Currency   string `json:"currency"`
			Status     string `json:"status"`
			Type       string `json:"type"`
		} `json:"order"`
		Product struct {
			ID string `json:"id"`
		} `json:"product"`
		Metadata map[string]string `json:"metadata"`
	} `json:"object"`
}

// CreemWebhook verifies the exact raw body, ignores non-paid/non-completion
// events, and settles only the checkout/economics bound before any local
// wallet or subscription value is created.
func CreemWebhook(c *gin.Context) {
	config, err := setting.GetCreemConfigChecked()
	if err != nil || !service.PaymentComplianceConfirmed() || config.APIKey == "" ||
		config.WebhookSecret == "" || len(config.Products) == 0 {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	body, err := common.ReadAllLimited(c.Request.Body, creemWebhookMaxBodyBytes)
	if err != nil {
		if errors.Is(err, common.ErrBodyTooLarge) {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
		} else {
			c.AbortWithStatus(http.StatusBadRequest)
		}
		return
	}
	if !VerifyCreemSignature(body, c.GetHeader(CreemSignatureHeader), config.WebhookSecret) {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	var event CreemWebhookEvent
	if common.Unmarshal(body, &event) != nil || len(event.EventType) > creemWebhookMaxEventType || len(event.ID) > creemWebhookMaxString {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if event.EventType != "checkout.completed" || event.Object.Order.Status != "paid" {
		c.Status(http.StatusOK)
		return
	}
	if !validPaidCreemEvent(&event) {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	settlement := service.CreemSettlement{
		CheckoutID:        event.Object.ID,
		ProviderOrderID:   event.Object.Order.ID,
		ReferenceID:       event.Object.RequestID,
		ProductID:         event.Object.Product.ID,
		AmountPaid:        event.Object.Order.AmountPaid,
		Currency:          event.Object.Order.Currency,
		OrderType:         event.Object.Order.Type,
		MetadataReference: event.Object.Metadata["reference_id"],
		MetadataQuota:     event.Object.Metadata["quota"],
	}
	switch {
	case strings.HasPrefix(settlement.ReferenceID, "sub_ref_"):
		err = service.CompleteBoundCreemSubscriptionOrder(settlement.ReferenceID, string(body), settlement)
	case strings.HasPrefix(settlement.ReferenceID, "ref_"):
		if settlement.OrderType != "onetime" {
			c.Status(http.StatusOK)
			return
		}
		err = service.CompleteBoundCreemTopUpOrder(settlement.ReferenceID, settlement)
	default:
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if err != nil {
		if creemWebhookRequestInvalid(err) {
			c.AbortWithStatus(http.StatusBadRequest)
		} else {
			common.SysError("Creem webhook settlement failed trade_no=" + settlement.ReferenceID + ": " + err.Error())
			c.AbortWithStatus(http.StatusInternalServerError)
		}
		return
	}
	c.Status(http.StatusOK)
}

func validPaidCreemEvent(event *CreemWebhookEvent) bool {
	if event == nil || !service.ValidCreemCheckoutID(event.ID) || !service.ValidCreemCheckoutID(event.Object.ID) ||
		!service.ValidCreemCheckoutID(event.Object.Order.ID) || event.Object.RequestID == "" ||
		event.Object.RequestID != strings.TrimSpace(event.Object.RequestID) || len(event.Object.RequestID) > 255 ||
		event.Object.Order.Product == "" || event.Object.Order.Product != event.Object.Product.ID ||
		len(event.Object.Product.ID) > 255 || event.Object.Order.AmountPaid <= 0 ||
		len(event.Object.Order.Currency) != 3 || event.Object.Order.Type == "" || len(event.Object.Order.Type) > 32 ||
		event.Object.Metadata["reference_id"] != event.Object.RequestID || len(event.Object.Metadata) > 32 {
		return false
	}
	for key, value := range event.Object.Metadata {
		if key == "" || len(key) > 64 || len(value) > creemWebhookMaxString || strings.ContainsRune(value, '\x00') {
			return false
		}
	}
	if quota := event.Object.Metadata["quota"]; quota == "" || len(quota) > 32 {
		return false
	} else if _, err := strconv.ParseInt(quota, 10, 64); err != nil {
		return false
	}
	return true
}

func creemWebhookRequestInvalid(err error) bool {
	return errors.Is(err, service.ErrTopUpNotFound) ||
		errors.Is(err, service.ErrSubscriptionOrderNotFound) ||
		errors.Is(err, service.ErrCreemPaymentMismatch) ||
		errors.Is(err, service.ErrCreemCheckoutBindingMismatch)
}

func creemTopUpInfo(complianceConfirmed bool) (setting.PaymentSetting, bool, string) {
	products := "[]"
	config, err := setting.GetCreemConfigChecked()
	if err != nil {
		return setting.GetPaymentSetting(), false, products
	}
	products = config.ProductsRaw
	enabled := complianceConfirmed && config.APIKey != "" && config.WebhookSecret != "" && len(config.Products) > 0
	return setting.GetPaymentSetting(), enabled, products
}
