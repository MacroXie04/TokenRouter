package commerce

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// paymentReturnPath builds an absolute URL on the server address (reference
// contract for payment return/cancel links).
func paymentReturnPath(suffix string) string {
	base := strings.TrimRight(setting.GetOption(setting.ServerAddressOption), "/")
	if !validAbsoluteEpayURL(base) {
		return ""
	}
	result := base + suffix
	if !validAbsoluteEpayURLWithQuery(result, true) {
		return ""
	}
	return result
}

// GetTopUpInfo returns the top-up page configuration: payment-method catalog
// (compliance-gated), gateway enable flags, minimums, amount presets and
// discounts (reference contract).
func GetTopUpInfo(c *gin.Context) {
	complianceConfirmed := billingsvc.PaymentComplianceConfirmed()
	payMethods := setting.GetPayMethods()
	if !complianceConfirmed {
		payMethods = []map[string]string{}
	}
	stripeCurrency, stripeCurrencyErr := setting.GetStripeCurrencyChecked()
	stripeEnabled := complianceConfirmed && setting.StripeConfigured() &&
		stripeCurrencyErr == nil && billingsvc.StripeCurrencySupported(stripeCurrency) &&
		!setting.GetStripePromotionCodesEnabled() && paymentReturnPath("/usage-logs") != "" &&
		paymentReturnPath("/wallet") != ""
	if !stripeEnabled {
		// A configured catalog entry is still an advertisement. Remove Stripe
		// whenever the endpoint would reject it (missing compliance/credentials,
		// unsupported currency, or amount-changing promotion codes).
		filtered := make([]map[string]string, 0, len(payMethods))
		for _, method := range payMethods {
			if method["type"] != "stripe" {
				filtered = append(filtered, method)
			}
		}
		payMethods = filtered
	} else {
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
	_, epayCallbackErr := subscriptionEpayCallbackURL("/api/user/epay/notify")
	epayEnabled := complianceConfirmed && getEpayClient() != nil && len(setting.GetPayMethods()) > 0 &&
		epayCallbackErr == nil && paymentReturnPath("/usage-logs") != ""
	ps, creemEnabled, creemProducts := creemTopUpInfo(complianceConfirmed)
	waffoConfig, waffoConfigErr := setting.GetWaffoConfigChecked()
	_, _, waffoURLsOK := effectiveWaffoURLs(waffoConfig)
	waffoEnabled := complianceConfirmed && waffoConfigErr == nil && waffoConfig.Enabled &&
		waffoConfig.APIKey != "" && waffoConfig.PrivateKey != "" && waffoConfig.PublicKey != "" && waffoURLsOK
	waffoPancakeConfig, waffoPancakeConfigErr := setting.GetWaffoPancakeConfigChecked()
	waffoPancakeConfigured := waffoPancakeConfigErr == nil && waffoPancakeConfig.MerchantID != "" &&
		waffoPancakeConfig.PrivateKey != "" && waffoPancakeConfig.StoreID != "" && waffoPancakeConfig.ProductID != ""
	waffoPancakeEnabled := complianceConfirmed && waffoPancakeConfigured
	filteredPayMethods := make([]map[string]string, 0, len(payMethods)+2)
	var configuredWaffo map[string]string
	var configuredWaffoPancake map[string]string
	for _, method := range payMethods {
		switch method["type"] {
		case billingsvc.PaymentMethodWaffoPancake:
			if waffoPancakeEnabled {
				configuredWaffoPancake = method
			}
			continue
		case billingsvc.PaymentMethodWaffo:
			if waffoEnabled {
				configuredWaffo = method
			}
			continue
		}
		filteredPayMethods = append(filteredPayMethods, method)
	}
	payMethods = filteredPayMethods
	if waffoPancakeEnabled {
		if configuredWaffoPancake == nil {
			configuredWaffoPancake = map[string]string{
				"name": "Waffo Pancake", "type": billingsvc.PaymentMethodWaffoPancake,
				"color": "#F97316", "min_topup": strconv.FormatInt(waffoPancakeConfig.MinTopUp, 10),
			}
		}
		payMethods = append(payMethods, configuredWaffoPancake)
	}
	if waffoEnabled {
		if configuredWaffo == nil {
			configuredWaffo = map[string]string{
				"name": "Waffo (Global Payment)", "type": billingsvc.PaymentMethodWaffo,
				"color": "#3B82F6", "min_topup": strconv.FormatInt(waffoConfig.MinTopUp, 10),
			}
		}
		payMethods = append(payMethods, configuredWaffo)
	}
	var waffoPayMethods any
	waffoMinTopUp := int64(0)
	if waffoConfigErr == nil {
		waffoMinTopUp = waffoConfig.MinTopUp
		if waffoEnabled {
			waffoPayMethods = waffoConfig.PayMethods
		}
	}
	waffoPancakeMinTopUp := int64(0)
	if waffoPancakeConfigErr == nil {
		waffoPancakeMinTopUp = waffoPancakeConfig.MinTopUp
	}
	currencyDisplay := setting.GetCurrencyDisplaySetting()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"enable_online_topup":              epayEnabled,
			"enable_stripe_topup":              stripeEnabled,
			"enable_creem_topup":               creemEnabled,
			"enable_waffo_topup":               waffoEnabled,
			"enable_waffo_pancake_topup":       waffoPancakeEnabled,
			"enable_redemption":                complianceConfirmed,
			"payment_compliance_confirmed":     complianceConfirmed,
			"payment_compliance_terms_version": "v1",
			"waffo_pay_methods":                waffoPayMethods,
			"creem_products":                   creemProducts,
			"pay_methods":                      payMethods,
			"min_topup":                        setting.GetMinTopUp(),
			"stripe_min_topup":                 setting.GetStripeMinTopUp(),
			"waffo_min_topup":                  waffoMinTopUp,
			"waffo_pancake_min_topup":          waffoPancakeMinTopUp,
			"amount_options":                   ps.AmountOptions,
			"discount":                         ps.AmountDiscount,
			"topup_link":                       setting.GetTopUpLink(),
			"quota_display_type":               currencyDisplay.Type,
			"quota_per_unit":                   quotamath.QuotaPerUnit,
			"usd_exchange_rate":                currencyDisplay.USDExchangeRate,
			"currency_symbol":                  currencyDisplay.Symbol,
			"currency_exchange_rate":           currencyDisplay.CurrencyExchangeRate(),
		},
	})
}

// getEpayClient builds the Epay client from the configured credentials, or
// nil when unconfigured.
func getEpayClient() *epay.Client {
	if !setting.EpayConfigured() {
		return nil
	}
	partnerID := setting.GetOption(setting.EpayIdOption)
	key := setting.GetOption(setting.EpayKeyOption)
	baseURL := setting.GetOption(setting.PayAddressOption)
	if !validEpayCredential(partnerID, 255) || !validEpayCredential(key, 4096) ||
		!validAbsoluteEpayURL(baseURL) {
		return nil
	}
	client, err := epay.NewClient(&epay.Config{
		PartnerID: partnerID,
		Key:       key,
	}, baseURL)
	if err != nil {
		return nil
	}
	return client
}

func validEpayCredential(value string, maximumBytes int) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		boundedPaymentPublicText(value, maximumBytes) != ""
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
	if req.Amount > quotamath.MaxQuota {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围"})
		return
	}
	minimum, err := setting.GetMinTopUpChecked()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	if req.Amount < minimum {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", minimum)})
		return
	}
	if _, err := billingsvc.NormalizeTopUpOrderAmount(req.Amount); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围或无法精确兑换"})
		return
	}
	user, err := userssvc.GetUserByID(requestctx.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney, err := billingsvc.GetTopupMoney(req.Amount, user.Group)
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

// RequestEpay starts an Epay payment: validates the request, builds the
// signed payment URL, and records the pending order (reference contract).
func RequestEpay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	var req struct {
		Amount        int64  `json:"amount"`
		PaymentMethod string `json:"payment_method"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.Amount > quotamath.MaxQuota {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围"})
		return
	}
	minimum, err := setting.GetMinTopUpChecked()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	if req.Amount < minimum {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", minimum)})
		return
	}
	orderAmount, err := billingsvc.NormalizeTopUpOrderAmount(req.Amount)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围或无法精确兑换"})
		return
	}
	id := requestctx.GetUserId(c)
	user, err := userssvc.GetUserByID(id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney, err := billingsvc.GetTopupMoney(req.Amount, user.Group)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	if payMoney < 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	if !setting.ContainsPayMethod(req.PaymentMethod) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付方式不存在"})
		return
	}
	orderMoney, wireMoney, err := billingsvc.NormalizePayMoney(payMoney)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	client := getEpayClient()
	returnURLRaw := paymentReturnPath("/usage-logs")
	returnURL, returnURLErr := url.Parse(returnURLRaw)
	notifyURL, notifyURLErr := subscriptionEpayCallbackURL("/api/user/epay/notify")
	if client == nil || returnURLRaw == "" || returnURLErr != nil || notifyURLErr != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "当前管理员未配置支付信息"})
		return
	}
	tradeSuffix, err := cryptoutil.SecureRandomAlphanumeric(6)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	tradeNo := fmt.Sprintf("USR%dNO%s%d", id, tradeSuffix, time.Now().Unix())
	uri, params, err := client.Purchase(&epay.PurchaseArgs{
		Type:           req.PaymentMethod,
		ServiceTradeNo: tradeNo,
		Name:           fmt.Sprintf("TUC%d", req.Amount),
		Money:          wireMoney,
		Device:         epay.PC,
		NotifyUrl:      notifyURL,
		ReturnUrl:      returnURL,
	})
	if err != nil || !validAbsoluteEpayURL(uri) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if _, err := billingsvc.CreateTopUpWithTradeNo(id, orderAmount, orderMoney, req.PaymentMethod, billingsvc.PaymentProviderEpay, tradeNo); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": params, "url": uri})
}

// EpayNotify handles the Epay gateway callback (POST form or GET query),
// verifies the signature and settles a successful payment idempotently
// (reference contract: the gateway expects a bare "success"/"fail" body).
func EpayNotify(c *gin.Context) {
	// Compliance and the payment-method catalog gate new checkout creation, not
	// settlement. A pending order that was already issued must remain recoverable
	// after an operator disables the storefront. Signature verification, current
	// gateway credentials, and the immutable local order binding still apply.
	if getEpayClient() == nil {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	params, ok := boundedEpayCallbackParams(c)
	if !ok {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	verifyInfo, ok := verifyEpayCallback(params)
	if !ok {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	if verifyInfo.TradeStatus == epay.StatusTradeSuccess {
		order, err := billingsvc.GetTopUpByTradeNo(verifyInfo.ServiceTradeNo)
		if err != nil {
			_, _ = c.Writer.Write([]byte("fail"))
			return
		}
		if order.PaymentProvider != billingsvc.PaymentProviderEpay || order.PaymentMethod != verifyInfo.Type {
			_, _ = c.Writer.Write([]byte("fail"))
			return
		}
		if !billingsvc.TopUpMoneyMatches(order.Money, verifyInfo.Money) {
			_, _ = c.Writer.Write([]byte("fail"))
			return
		}
		// CompleteTopUp is idempotent: duplicate deliveries do not double-credit.
		if err := billingsvc.CompleteTopUp(order.UserId, order.TradeNo, order.Amount); err != nil {
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
func getStripePayMoney(amount int64, group string) (float64, error) {
	return billingsvc.GetStripeTopupMoney(amount, group)
}

// RequestStripeAmount converts a Stripe top-up amount into the payable money
// (reference contract).
func RequestStripeAmount(c *gin.Context) {
	var req StripePayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.Amount > quotamath.MaxQuota {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围"})
		return
	}
	minimum, err := setting.GetStripeMinTopUpChecked()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	if req.Amount < minimum {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", minimum)})
		return
	}
	if _, err := billingsvc.NormalizeTopUpOrderAmount(req.Amount); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围或无法精确兑换"})
		return
	}
	user, err := userssvc.GetUserByID(requestctx.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney, err := getStripePayMoney(req.Amount, user.Group)
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

// RequestStripePay creates a Stripe checkout session and records the pending
// order (reference contract). The checkout session carries the metadata the
// Stripe webhook needs to settle the order.
func RequestStripePay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	var req StripePayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.PaymentMethod != "stripe" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "不支持的支付渠道"})
		return
	}
	if req.Amount > quotamath.MaxQuota {
		c.JSON(http.StatusOK, gin.H{"message": "充值数量超出安全范围", "data": 10})
		return
	}
	minimum, err := setting.GetStripeMinTopUpChecked()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "充值配置无效", "data": 10})
		return
	}
	if req.Amount < minimum {
		c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("充值数量不能小于 %d", minimum), "data": 10})
		return
	}
	if setting.GetQuotaDisplayType() != setting.QuotaDisplayTypeTokens && req.Amount > 10000 {
		c.JSON(http.StatusOK, gin.H{"message": "充值数量不能大于 10000", "data": 10})
		return
	}
	orderAmount, err := billingsvc.NormalizeTopUpOrderAmount(req.Amount)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "充值数量超出安全范围或无法精确兑换", "data": 10})
		return
	}
	if req.SuccessURL != "" && httpx.ValidateRedirectURL(req.SuccessURL) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "支付成功重定向URL不在可信任域名列表中", "data": ""})
		return
	}
	if req.CancelURL != "" && httpx.ValidateRedirectURL(req.CancelURL) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "支付取消重定向URL不在可信任域名列表中", "data": ""})
		return
	}
	successURL := req.SuccessURL
	if successURL == "" {
		successURL = paymentReturnPath("/usage-logs")
	}
	cancelURL := req.CancelURL
	if cancelURL == "" {
		cancelURL = paymentReturnPath("/wallet")
	}
	if successURL == "" || cancelURL == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Stripe 回调地址配置无效"})
		return
	}
	user, err := userssvc.GetUserByID(requestctx.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户信息失败"})
		return
	}
	// Snapshot the exact amount and currency before creating any external
	// resource. Promotion codes are incompatible with exact amount binding,
	// because their final discount is not known until after Checkout starts.
	payMoney, err := getStripePayMoney(req.Amount, user.Group)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "充值配置无效", "data": 10})
		return
	}
	if payMoney <= 0.01 || setting.GetStripePromotionCodesEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "充值配置无效", "data": 10})
		return
	}
	providerCurrency, err := setting.GetStripeCurrencyChecked()
	if err != nil || !billingsvc.StripeCurrencySupported(providerCurrency) {
		c.JSON(http.StatusOK, gin.H{"message": "充值配置无效", "data": 10})
		return
	}
	stripeSecret := env.GetEnv("STRIPE_SECRET_KEY", "")
	if !validStripeSecret(stripeSecret) || strings.TrimSpace(env.GetEnv("STRIPE_WEBHOOK_SECRET", "")) == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Stripe 未配置或回调不可用"})
		return
	}
	referenceSuffix, err := cryptoutil.SecureRandomAlphanumeric(4)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	reference := fmt.Sprintf("tokenrouter-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), referenceSuffix)
	referenceID := "ref_" + sha1Hex(reference)
	order, err := billingsvc.CreateBoundStripeTopUpWithTradeNo(user.Id, orderAmount, payMoney, providerCurrency, referenceID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	payLink, err := genStripeLink(stripeSecret, order, user, successURL, cancelURL)
	if err != nil {
		if stripeRequestDefinitelyRejected(err) {
			if statusErr := billingsvc.UpdatePendingTopUpStatus(referenceID, billingsvc.PaymentProviderStripe, billingsvc.TopUpStatusFailed); statusErr != nil {
				logging.SysError(fmt.Sprintf("Stripe checkout rejection status update failed trade_no=%s: %v", referenceID, statusErr))
			}
		} else if errors.Is(err, billingsvc.ErrStripeCheckoutBindingMismatch) {
			if flagErr := billingsvc.FlagStripeTopUpReconciliation(referenceID, billingsvc.StripeReconciliationBindingMismatch); flagErr != nil {
				logging.SysError(fmt.Sprintf("Stripe reconciliation update failed trade_no=%s: %v", referenceID, flagErr))
			}
		} else {
			if flagErr := billingsvc.FlagStripeTopUpReconciliation(referenceID, billingsvc.StripeReconciliationCreationUnknown); flagErr != nil {
				logging.SysError(fmt.Sprintf("Stripe reconciliation update failed trade_no=%s: %v", referenceID, flagErr))
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
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
func genStripeLink(apiKey string, order *model.TopUp, user *model.User, successURL, cancelURL string) (string, error) {
	if !validStripeSecret(apiKey) || order == nil || user == nil || order.ProviderAmountMinor <= 0 {
		return "", fmt.Errorf("无效的Stripe API密钥")
	}
	referenceID := order.TradeNo
	if successURL == "" {
		successURL = paymentReturnPath("/usage-logs")
	}
	if cancelURL == "" {
		cancelURL = paymentReturnPath("/wallet")
	}
	customerEmail := ""
	if user.StripeCustomer == "" {
		customerEmail = user.Email
	}
	snapshot := billingsvc.StripeCheckoutRequestSnapshot{
		Version: billingsvc.StripeCheckoutRequestSnapshotVersion,
		TradeNo: referenceID, OrderType: billingsvc.StripeOrderTypeWallet, Mode: billingsvc.StripeCheckoutModePayment,
		AmountMinor: order.ProviderAmountMinor, Currency: order.ProviderCurrency,
		SuccessURL: successURL, CancelURL: cancelURL,
		CustomerID: user.StripeCustomer, CustomerEmail: customerEmail,
		ProductName: "TokenRouter wallet top-up", UserID: user.Id, WalletAmount: order.Amount,
		IdempotencyKey: "wallet-checkout-" + referenceID,
	}
	if err := billingsvc.ConfigureStripeTopUpCheckoutRequest(referenceID, snapshot); err != nil {
		return "", err
	}
	params := stripeCheckoutParamsFromSnapshot(snapshot)
	result, err := createStripeCheckoutSession(apiKey, params)
	if err != nil {
		return "", err
	}
	if err := validateCreatedStripeCheckoutSession(result, referenceID, billingsvc.StripeCheckoutModePayment,
		billingsvc.StripeOrderTypeWallet, order.ProviderAmountMinor, order.ProviderCurrency, ""); err != nil {
		return "", err
	}
	if err := billingsvc.BindStripeTopUpSessionWithExpiry(referenceID, result.ID, result.ExpiresAt); err != nil {
		return "", err
	}
	return result.URL, nil
}
