package controller

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	epay "github.com/Calcium-Ion/go-epay/epay"
	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/checkout/session"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

// paymentReturnPath builds an absolute URL on the server address (reference
// contract for payment return/cancel links).
func paymentReturnPath(suffix string) string {
	base := strings.TrimRight(setting.GetOption(setting.ServerAddressOption), "/")
	return base + suffix
}

// GetTopUpInfo returns the top-up page configuration: payment-method catalog
// (compliance-gated), gateway enable flags, minimums, amount presets and
// discounts (reference contract).
func GetTopUpInfo(c *gin.Context) {
	complianceConfirmed := service.PaymentComplianceConfirmed()
	payMethods := setting.GetPayMethods()
	if !complianceConfirmed {
		payMethods = []map[string]string{}
	}
	stripeEnabled := complianceConfirmed && setting.StripeConfigured()
	if stripeEnabled {
		hasStripe := false
		for _, method := range payMethods {
			if method["type"] == "stripe" {
				hasStripe = true
				break
			}
		}
		if !hasStripe {
			payMethods = append(payMethods, map[string]string{
				"name":      "Stripe",
				"type":      "stripe",
				"color":     "#635BFF",
				"min_topup": fmt.Sprintf("%d", setting.GetStripeMinTopUp()),
			})
		}
	}
	epayEnabled := complianceConfirmed && setting.EpayConfigured() && len(setting.GetPayMethods()) > 0
	ps := setting.GetPaymentSetting()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"enable_online_topup":              epayEnabled,
			"enable_stripe_topup":              stripeEnabled,
			"enable_creem_topup":               false,
			"enable_waffo_topup":               false,
			"enable_waffo_pancake_topup":       false,
			"enable_redemption":                complianceConfirmed,
			"payment_compliance_confirmed":     complianceConfirmed,
			"payment_compliance_terms_version": "v1",
			"waffo_pay_methods":                nil,
			"creem_products":                   nil,
			"pay_methods":                      payMethods,
			"min_topup":                        setting.GetMinTopUp(),
			"stripe_min_topup":                 setting.GetStripeMinTopUp(),
			"waffo_min_topup":                  0,
			"waffo_pancake_min_topup":          0,
			"amount_options":                   ps.AmountOptions,
			"discount":                         ps.AmountDiscount,
			"topup_link":                       paymentReturnPath("/topup"),
		},
	})
}

// getEpayClient builds the Epay client from the configured credentials, or
// nil when unconfigured.
func getEpayClient() *epay.Client {
	if !setting.EpayConfigured() {
		return nil
	}
	client, err := epay.NewClient(&epay.Config{
		PartnerID: setting.GetOption(setting.EpayIdOption),
		Key:       setting.GetOption(setting.EpayKeyOption),
	}, setting.GetOption(setting.PayAddressOption))
	if err != nil {
		return nil
	}
	return client
}

// RequestAmount converts a top-up amount into the payable money (reference
// contract: {message,data} shape with the money as a two-decimal string).
func RequestAmount(c *gin.Context) {
	var req struct {
		Amount int64 `json:"amount"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.Amount < setting.GetMinTopUp() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", setting.GetMinTopUp())})
		return
	}
	user, err := service.GetUserByID(common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney := service.GetTopupMoney(req.Amount, user.Group)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": service.FormatPayMoney(payMoney)})
}

// RequestEpay starts an Epay payment: validates the request, builds the
// signed payment URL, and records the pending order (reference contract).
func RequestEpay(c *gin.Context) {
	var req struct {
		Amount        int64  `json:"amount"`
		PaymentMethod string `json:"payment_method"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.Amount < setting.GetMinTopUp() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", setting.GetMinTopUp())})
		return
	}
	id := common.GetUserId(c)
	user, err := service.GetUserByID(id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney := service.GetTopupMoney(req.Amount, user.Group)
	if payMoney < 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	if !setting.ContainsPayMethod(req.PaymentMethod) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付方式不存在"})
		return
	}
	callBackAddress := setting.GetCallbackAddress()
	returnURL, _ := url.Parse(paymentReturnPath("/usage-logs"))
	notifyURL, _ := url.Parse(callBackAddress + "/api/user/epay/notify")
	tradeNo := fmt.Sprintf("USR%dNO%s%d", id, common.RandomAlphanumeric(6), time.Now().Unix())
	client := getEpayClient()
	if client == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "当前管理员未配置支付信息"})
		return
	}
	uri, params, err := client.Purchase(&epay.PurchaseArgs{
		Type:           req.PaymentMethod,
		ServiceTradeNo: tradeNo,
		Name:           fmt.Sprintf("TUC%d", req.Amount),
		Money:          service.FormatPayMoney(payMoney),
		Device:         epay.PC,
		NotifyUrl:      notifyURL,
		ReturnUrl:      returnURL,
	})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	amount := req.Amount
	if setting.GetQuotaDisplayType() == setting.QuotaDisplayTypeTokens {
		amount = int64(float64(amount) / common.QuotaPerUnit)
	}
	if _, err := service.CreateTopUpWithTradeNo(id, amount, payMoney, req.PaymentMethod, service.PaymentProviderEpay, tradeNo); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": params, "url": uri})
}

// EpayNotify handles the Epay gateway callback (POST form or GET query),
// verifies the signature and settles a successful payment idempotently
// (reference contract: the gateway expects a bare "success"/"fail" body).
func EpayNotify(c *gin.Context) {
	params := map[string]string{}
	if c.Request.Method == http.MethodPost {
		if err := c.Request.ParseForm(); err != nil {
			_, _ = c.Writer.Write([]byte("fail"))
			return
		}
		for key := range c.Request.PostForm {
			params[key] = c.Request.PostForm.Get(key)
		}
	} else {
		for key := range c.Request.URL.Query() {
			params[key] = c.Request.URL.Query().Get(key)
		}
	}
	if len(params) == 0 {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	client := getEpayClient()
	if client == nil {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	verifyInfo, err := client.Verify(params)
	if err != nil || !verifyInfo.VerifyStatus {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	if verifyInfo.TradeStatus == epay.StatusTradeSuccess {
		order, err := service.GetTopUpByTradeNo(verifyInfo.ServiceTradeNo)
		if err != nil {
			_, _ = c.Writer.Write([]byte("fail"))
			return
		}
		if order.PaymentMethod != verifyInfo.Type {
			_, _ = c.Writer.Write([]byte("fail"))
			return
		}
		// CompleteTopUp is idempotent: duplicate deliveries do not double-credit.
		if err := service.CompleteTopUp(order.UserId, order.TradeNo, order.Amount); err != nil {
			_, _ = c.Writer.Write([]byte("fail"))
			return
		}
	}
	_, _ = c.Writer.Write([]byte("success"))
}

// StripePayRequest is the Stripe top-up request (reference contract).
type StripePayRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method"`
	SuccessURL    string `json:"success_url,omitempty"`
	CancelURL     string `json:"cancel_url,omitempty"`
}

// getStripePayMoney converts a Stripe top-up amount into the payable money
// (display-type aware, group ratio and preset discount applied).
func getStripePayMoney(amount float64, group string) float64 {
	originalAmount := amount
	if setting.GetQuotaDisplayType() == setting.QuotaDisplayTypeTokens {
		amount = amount / common.QuotaPerUnit
	}
	ratio := service.GroupRatio(group)
	if ratio == 0 {
		ratio = 1
	}
	discount := 1.0
	if ds, ok := setting.GetPaymentSetting().AmountDiscount[int(originalAmount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}
	return amount * setting.GetStripeUnitPrice() * ratio * discount
}

// RequestStripeAmount converts a Stripe top-up amount into the payable money
// (reference contract).
func RequestStripeAmount(c *gin.Context) {
	var req StripePayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.Amount < setting.GetStripeMinTopUp() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", setting.GetStripeMinTopUp())})
		return
	}
	user, err := service.GetUserByID(common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney := getStripePayMoney(float64(req.Amount), user.Group)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": service.FormatPayMoney(payMoney)})
}

// RequestStripePay creates a Stripe checkout session and records the pending
// order (reference contract). The checkout session carries the metadata the
// Stripe webhook needs to settle the order.
func RequestStripePay(c *gin.Context) {
	var req StripePayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.PaymentMethod != "stripe" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "不支持的支付渠道"})
		return
	}
	if req.Amount < setting.GetStripeMinTopUp() {
		c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("充值数量不能小于 %d", setting.GetStripeMinTopUp()), "data": 10})
		return
	}
	if req.Amount > 10000 {
		c.JSON(http.StatusOK, gin.H{"message": "充值数量不能大于 10000", "data": 10})
		return
	}
	if req.SuccessURL != "" && common.ValidateRedirectURL(req.SuccessURL) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "支付成功重定向URL不在可信任域名列表中", "data": ""})
		return
	}
	if req.CancelURL != "" && common.ValidateRedirectURL(req.CancelURL) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "支付取消重定向URL不在可信任域名列表中", "data": ""})
		return
	}
	user, err := service.GetUserByID(common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户信息失败"})
		return
	}
	chargedMoney := float64(req.Amount) * service.GroupRatio(user.Group)
	if chargedMoney <= 0 {
		chargedMoney = float64(req.Amount)
	}
	reference := fmt.Sprintf("tokenrouter-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), common.RandomAlphanumeric(4))
	referenceID := "ref_" + sha1Hex(reference)
	payLink, err := genStripeLink(referenceID, user, req.Amount, req.SuccessURL, req.CancelURL)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if _, err := service.CreateTopUpWithTradeNo(user.Id, req.Amount, chargedMoney, "stripe", service.PaymentProviderStripe, referenceID); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"pay_link": payLink}})
}

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// genStripeLink creates the Stripe checkout session. The session metadata
// carries user_id/trade_no/quota so TokenRouter's Stripe webhook can settle
// the order.
func genStripeLink(referenceID string, user *model.User, amount int64, successURL, cancelURL string) (string, error) {
	apiKey := common.GetEnv("STRIPE_SECRET_KEY", "")
	if !strings.HasPrefix(apiKey, "sk_") && !strings.HasPrefix(apiKey, "rk_") {
		return "", fmt.Errorf("无效的Stripe API密钥")
	}
	stripe.Key = apiKey
	if successURL == "" {
		successURL = paymentReturnPath("/usage-logs")
	}
	if cancelURL == "" {
		cancelURL = paymentReturnPath("/wallet")
	}
	params := &stripe.CheckoutSessionParams{
		ClientReferenceID: stripe.String(referenceID),
		SuccessURL:        stripe.String(successURL),
		CancelURL:         stripe.String(cancelURL),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(setting.GetOption(setting.StripePriceIdOption)),
				Quantity: stripe.Int64(amount),
			},
		},
		Mode:                stripe.String(string(stripe.CheckoutSessionModePayment)),
		AllowPromotionCodes: stripe.Bool(setting.GetStripePromotionCodesEnabled()),
		Metadata: map[string]string{
			"user_id":  fmt.Sprintf("%d", user.Id),
			"trade_no": referenceID,
			"quota":    fmt.Sprintf("%d", amount),
		},
	}
	if user.StripeCustomer == "" {
		if user.Email != "" {
			params.CustomerEmail = stripe.String(user.Email)
		}
		params.CustomerCreation = stripe.String(string(stripe.CheckoutSessionCustomerCreationAlways))
	} else {
		params.Customer = stripe.String(user.StripeCustomer)
	}
	result, err := session.New(params)
	if err != nil {
		return "", err
	}
	return result.URL, nil
}
