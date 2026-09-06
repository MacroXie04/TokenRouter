package commerce

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	waffopayments "github.com/tokenrouter/tokenrouter/internal/payments/waffo"
	"github.com/tokenrouter/tokenrouter/internal/platform/buildinfo"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net/http"
	"strings"
	"time"
)

const (
	waffoPayRequestMaxBodyBytes = 64 << 10
	waffoWebhookMaxBodyBytes    = 64 << 10
	waffoWebhookMaxEventType    = 128
)

type WaffoPayRequest struct {
	Amount         int64  `json:"amount"`
	PayMethodIndex *int   `json:"pay_method_index"`
	PayMethodType  string `json:"pay_method_type"`
	PayMethodName  string `json:"pay_method_name"`
}

func RequestWaffoAmount(c *gin.Context) {
	var req WaffoPayRequest
	if !bindWaffoPayRequest(c, &req) {
		return
	}
	config, err := setting.GetWaffoConfigChecked()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	if req.Amount > quotamath.MaxQuota {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围"})
		return
	}
	if req.Amount < config.MinTopUp {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", config.MinTopUp)})
		return
	}
	if _, err := billingsvc.NormalizeTopUpOrderAmount(req.Amount); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围或无法精确兑换"})
		return
	}
	user, err := userssvc.GetUserByID(requestctx.GetUserId(c))
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney, err := billingsvc.GetWaffoTopupMoney(req.Amount, user.Group, config.UnitPrice)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": billingsvc.FormatPayMoney(payMoney)})
}

// RequestWaffoPay persists the full economic/request snapshot before calling
// the injectable Waffo client. Ambiguous transport outcomes remain pending so
// a later signed webhook can safely claim the provider order identifier.
func RequestWaffoPay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	var req WaffoPayRequest
	if !bindWaffoPayRequest(c, &req) {
		return
	}
	config, err := setting.GetWaffoConfigChecked()
	if err != nil || !config.Enabled || config.APIKey == "" || config.PrivateKey == "" || config.PublicKey == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo 未配置或回调不可用"})
		return
	}
	if req.Amount > quotamath.MaxQuota {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围"})
		return
	}
	if req.Amount < config.MinTopUp {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", config.MinTopUp)})
		return
	}
	amount, err := billingsvc.NormalizeTopUpOrderAmount(req.Amount)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围或无法精确兑换"})
		return
	}
	payMethodType, payMethodName, ok := resolveWaffoPayMethod(req, config.PayMethods)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "不支持的支付方式"})
		return
	}
	user, err := userssvc.GetUserByID(requestctx.GetUserId(c))
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "用户不存在"})
		return
	}
	payMoney, err := billingsvc.GetWaffoTopupMoney(req.Amount, user.Group, config.UnitPrice)
	if err != nil || payMoney < 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	orderAmount, err := billingsvc.FormatWaffoAmount(payMoney, config.Currency)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	notifyURL, returnURL, urlsOK := effectiveWaffoURLs(config)
	if !urlsOK {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo 回调地址配置无效"})
		return
	}
	suffix, err := cryptoutil.SecureRandomAlphanumeric(6)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	tradeNo := fmt.Sprintf("WAFFO-%d-%d-%s", user.Id, time.Now().UnixMilli(), suffix)
	appName := strings.TrimSpace(setting.GetSiteName())
	if appName == "" {
		appName = buildinfo.ProductName
	}
	order, checkout, err := billingsvc.CreateBoundWaffoTopUp(billingsvc.WaffoCheckoutSpec{
		UserID: user.Id, Amount: amount, RequestedAmount: req.Amount,
		OrderAmount: orderAmount, Currency: config.Currency, MerchantID: config.MerchantID,
		NotifyURL: notifyURL, ReturnURL: returnURL, AppName: appName,
		PayMethodType: payMethodType, PayMethodName: payMethodName,
		RequestedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), Sandbox: config.Sandbox,
	}, tradeNo)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	providerResult, err := waffopayments.CurrentOrderClient().CreateOrder(c.Request.Context(), config, checkout)
	if err != nil {
		if waffopayments.CreateDefinitelyRejected(err) {
			if statusErr := billingsvc.UpdatePendingTopUpStatus(order.TradeNo, billingsvc.PaymentProviderWaffo, billingsvc.TopUpStatusFailed); statusErr != nil {
				logging.SysError("Waffo rejection status update failed trade_no=" + order.TradeNo)
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if err := billingsvc.BindWaffoTopUpCheckout(order.TradeNo, billingsvc.WaffoCreateBinding{
		PaymentRequestID: providerResult.PaymentRequestID,
		MerchantOrderID:  providerResult.MerchantOrderID,
		AcquiringOrderID: providerResult.AcquiringOrderID,
	}); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data":    gin.H{"payment_url": providerResult.PaymentURL, "order_id": order.TradeNo},
	})
}

func effectiveWaffoURLs(config setting.WaffoConfig) (string, string, bool) {
	notifyURL := config.NotifyURL
	if notifyURL == "" {
		notifyURL = strings.TrimRight(setting.GetCallbackAddress(), "/") + "/api/waffo/webhook"
	}
	returnURL := config.ReturnURL
	if returnURL == "" {
		returnURL = paymentReturnPath("/wallet?show_history=true")
	}
	return notifyURL, returnURL, setting.ValidWaffoCallbackURL(notifyURL) && setting.ValidWaffoCallbackURL(returnURL)
}

type waffoWebhookEvent struct {
	EventType string                    `json:"eventType"`
	Result    *waffoPaymentNotification `json:"result,omitempty"`
}

type waffoPaymentNotification struct {
	PaymentRequestID string                       `json:"paymentRequestId,omitempty"`
	MerchantOrderID  string                       `json:"merchantOrderId,omitempty"`
	AcquiringOrderID string                       `json:"acquiringOrderId,omitempty"`
	OrderStatus      string                       `json:"orderStatus,omitempty"`
	OrderCurrency    string                       `json:"orderCurrency,omitempty"`
	OrderAmount      string                       `json:"orderAmount,omitempty"`
	MerchantInfo     billingsvc.WaffoMerchantInfo `json:"merchantInfo,omitempty"`
	UserInfo         billingsvc.WaffoUserInfo     `json:"userInfo,omitempty"`
	PaymentInfo      billingsvc.WaffoPaymentInfo  `json:"paymentInfo,omitempty"`
}

func WaffoWebhook(c *gin.Context) {
	config, err := setting.GetWaffoConfigChecked()
	if err != nil || !billingsvc.PaymentComplianceConfirmed() || !config.Enabled ||
		config.APIKey == "" || config.PrivateKey == "" || config.PublicKey == "" {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	body, err := httpx.ReadAllLimited(c.Request.Body, waffoWebhookMaxBodyBytes)
	if err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
		} else {
			c.AbortWithStatus(http.StatusBadRequest)
		}
		return
	}
	codec := waffopayments.CurrentSignatureCodec()
	if !codec.Verify(body, c.GetHeader(waffopayments.SignatureHeader), config.PublicKey) {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	var event waffoWebhookEvent
	if jsonutil.Unmarshal(body, &event) != nil || event.EventType == "" || len(event.EventType) > waffoWebhookMaxEventType {
		writeWaffoWebhookResponse(c, codec, config, false)
		return
	}
	if event.EventType != "PAYMENT_NOTIFICATION" {
		writeWaffoWebhookResponse(c, codec, config, true)
		return
	}
	settlement, status, ok := waffoSettlementFromEvent(event.Result, config)
	if !ok {
		writeWaffoWebhookResponse(c, codec, config, false)
		return
	}
	switch status {
	case "PAY_SUCCESS":
		err = billingsvc.CompleteBoundWaffoTopUpOrder(settlement)
	case "ORDER_CLOSE":
		err = billingsvc.CloseBoundWaffoTopUpOrder(settlement)
	case "PAY_IN_PROGRESS", "AUTHORIZATION_REQUIRED", "AUTHED_WAITING_CAPTURE", "CAPTURE_IN_PROGRESS":
		writeWaffoWebhookResponse(c, codec, config, true)
		return
	default:
		writeWaffoWebhookResponse(c, codec, config, false)
		return
	}
	if err != nil {
		logging.SysError("Waffo webhook settlement deferred trade_no=" + settlement.MerchantOrderID)
		writeWaffoWebhookResponse(c, codec, config, false)
		return
	}
	writeWaffoWebhookResponse(c, codec, config, true)
}

func waffoSettlementFromEvent(result *waffoPaymentNotification, config setting.WaffoConfig) (billingsvc.WaffoSettlement, string, bool) {
	if result == nil || len(result.OrderStatus) > 64 {
		return billingsvc.WaffoSettlement{}, "", false
	}
	environment := billingsvc.WaffoEnvironmentProduction
	if config.Sandbox {
		environment = billingsvc.WaffoEnvironmentSandbox
	}
	settlement := billingsvc.WaffoSettlement{
		PaymentRequestID: result.PaymentRequestID, MerchantOrderID: result.MerchantOrderID,
		AcquiringOrderID: result.AcquiringOrderID, OrderAmount: result.OrderAmount,
		OrderCurrency: result.OrderCurrency, MerchantID: result.MerchantInfo.MerchantID,
		UserID: result.UserInfo.UserID, ProductName: result.PaymentInfo.ProductName,
		Environment: environment,
	}
	if !billingsvc.ValidWaffoCreateIdentifier(settlement.PaymentRequestID) ||
		!billingsvc.ValidWaffoCreateIdentifier(settlement.MerchantOrderID) ||
		!billingsvc.ValidWaffoCreateIdentifier(settlement.AcquiringOrderID) ||
		len(settlement.OrderAmount) > 64 || len(settlement.OrderCurrency) > 8 ||
		len(settlement.MerchantID) > 255 || len(settlement.UserID) > 255 || len(settlement.ProductName) > 128 {
		return billingsvc.WaffoSettlement{}, "", false
	}
	return settlement, result.OrderStatus, true
}

func writeWaffoWebhookResponse(c *gin.Context, codec waffopayments.SignatureCodec, config setting.WaffoConfig, success bool) {
	body := []byte(`{"message":"failed"}`)
	if success {
		body = []byte(`{"message":"success"}`)
	}
	signature, err := codec.Sign(body, config.PrivateKey)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.Header(waffopayments.SignatureHeader, signature)
	c.Data(http.StatusOK, "application/json", body)
}

func bindWaffoPayRequest(c *gin.Context, target *WaffoPayRequest) bool {
	if c.Request.ContentLength > waffoPayRequestMaxBodyBytes {
		c.AbortWithStatus(http.StatusRequestEntityTooLarge)
		return false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, waffoPayRequestMaxBodyBytes)
	if err := c.ShouldBindJSON(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
		} else {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		}
		return false
	}
	return true
}

func resolveWaffoPayMethod(req WaffoPayRequest, methods []setting.WaffoPayMethod) (string, string, bool) {
	if req.PayMethodIndex != nil {
		index := *req.PayMethodIndex
		if index < 0 || index >= len(methods) {
			return "", "", false
		}
		return methods[index].PayMethodType, methods[index].PayMethodName, true
	}
	if req.PayMethodType == "" {
		if req.PayMethodName != "" {
			return "", "", false
		}
		return "", "", true
	}
	for _, method := range methods {
		if method.PayMethodType == req.PayMethodType && method.PayMethodName == req.PayMethodName {
			return method.PayMethodType, method.PayMethodName, true
		}
	}
	return "", "", false
}
