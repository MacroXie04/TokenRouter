package commerce

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxSubscriptionEpayCallbackParams = 32
	maxSubscriptionEpayCallbackKey    = 64
	maxSubscriptionEpayCallbackValue  = 2048
	maxSubscriptionEpayCallbackBytes  = 16 << 10
)

// SubscriptionEpayPayRequest is the reference subscription EPay request body.
type SubscriptionEpayPayRequest struct {
	PlanId        int    `json:"plan_id"`
	PaymentMethod string `json:"payment_method"`
}

// SubscriptionRequestEpay creates an amount-bound, capacity-reserving local
// order and then returns the EPay library's signed checkout parameters.
func SubscriptionRequestEpay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req SubscriptionEpayPayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}
	if !setting.ContainsPayMethod(req.PaymentMethod) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "支付方式不存在"})
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
	price, err := billingsvc.ParseSubscriptionPlanPrice(plan.PriceAmount)
	if err != nil || price < 0.01 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "套餐金额过低"})
		return
	}
	client := getEpayClient()
	if client == nil || !validAbsoluteEpayURL(setting.GetOption(setting.PayAddressOption)) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "当前管理员未配置支付信息"})
		return
	}
	returnURL, err := subscriptionEpayCallbackURL("/api/subscription/epay/return")
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "回调地址配置错误"})
		return
	}
	notifyURL, err := subscriptionEpayCallbackURL("/api/subscription/epay/notify")
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "回调地址配置错误"})
		return
	}
	suffix, err := cryptoutil.SecureRandomAlphanumeric(6)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	userID := requestctx.GetUserId(c)
	tradeNo := billingsvc.NewSubscriptionEpayTradeNo(userID, time.Now().Unix(), suffix)
	order, err := billingsvc.CreateBoundEpaySubscriptionOrder(userID, plan.Id, req.PaymentMethod, tradeNo)
	if err != nil {
		if errors.Is(err, billingsvc.ErrSubscriptionPurchaseLimit) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	uri, params, err := client.Purchase(&epay.PurchaseArgs{
		Type:           req.PaymentMethod,
		ServiceTradeNo: order.TradeNo,
		Name:           fmt.Sprintf("SUB:%s", plan.Title),
		Money:          billingsvc.FormatPayMoney(order.Money),
		Device:         epay.PC,
		NotifyUrl:      notifyURL,
		ReturnUrl:      returnURL,
	})
	if err != nil || !validAbsoluteEpayURL(uri) {
		if statusErr := billingsvc.ExpireSubscriptionOrder(order.TradeNo, billingsvc.PaymentProviderEpay); statusErr != nil {
			logging.SysError("EPay subscription checkout failure status update failed trade_no=" + order.TradeNo + ": " + statusErr.Error())
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": params, "url": uri})
}

func validAbsoluteEpayURL(raw string) bool {
	return validAbsoluteEpayURLWithQuery(raw, false)
}

func validAbsoluteEpayURLWithQuery(raw string, allowQuery bool) bool {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.Contains(raw, `\`) ||
		boundedPaymentPublicText(raw, maxPaymentServerURLBytes) == "" {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Fragment != "" || !allowQuery && parsed.RawQuery != "" || parsed.Opaque != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	address := net.ParseIP(host)
	return host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		address != nil && address.IsLoopback()
}

func subscriptionEpayCallbackURL(suffix string) (*url.URL, error) {
	base := strings.TrimRight(strings.TrimSpace(setting.GetCallbackAddress()), "/")
	raw := base + suffix
	if !validAbsoluteEpayURL(raw) {
		return nil, errors.New("invalid EPay callback URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.RawQuery != "" {
		return nil, errors.New("invalid EPay callback URL")
	}
	return parsed, nil
}

func subscriptionEpayCallbacksEnabled() bool {
	// Compliance and payment-method settings gate creation of new checkouts.
	// They must not strand an already-issued, signed order if an operator later
	// changes the storefront configuration; callback verification and the
	// immutable local order binding remain the settlement authority.
	return getEpayClient() != nil
}

func boundedEpayCallbackParams(c *gin.Context) (map[string]string, bool) {
	var values url.Values
	switch c.Request.Method {
	case http.MethodPost:
		if err := c.Request.ParseForm(); err != nil {
			return nil, false
		}
		values = c.Request.PostForm
	case http.MethodGet:
		values = c.Request.URL.Query()
	default:
		return nil, false
	}
	if len(values) == 0 || len(values) > maxSubscriptionEpayCallbackParams {
		return nil, false
	}
	params := make(map[string]string, len(values))
	total := 0
	for key, candidates := range values {
		if key == "" || len(key) > maxSubscriptionEpayCallbackKey || len(candidates) != 1 ||
			len(candidates[0]) > maxSubscriptionEpayCallbackValue {
			return nil, false
		}
		total += len(key) + len(candidates[0])
		if total > maxSubscriptionEpayCallbackBytes {
			return nil, false
		}
		params[key] = candidates[0]
	}
	return params, true
}

func verifyEpayCallback(params map[string]string) (*epay.VerifyRes, bool) {
	for _, key := range []string{"pid", "trade_no", "out_trade_no", "type", "money", "trade_status", "sign"} {
		if params[key] == "" {
			return nil, false
		}
	}
	if len(params["pid"]) > 255 || len(params["trade_no"]) > 255 || len(params["out_trade_no"]) > 255 ||
		len(params["type"]) > 50 || len(params["money"]) > 64 || len(params["trade_status"]) > 32 ||
		len(params["sign"]) != 32 {
		return nil, false
	}
	if params["pid"] != setting.GetOption(setting.EpayIdOption) {
		return nil, false
	}
	if signType := params["sign_type"]; signType != "" && signType != "MD5" {
		return nil, false
	}
	key := setting.GetOption(setting.EpayKeyOption)
	provided := params["sign"]
	toSign := make(map[string]string, len(params))
	for name, value := range params {
		toSign[name] = value
	}
	expected := epay.GenerateParams(toSign, key)["sign"]
	if len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		return nil, false
	}
	client := getEpayClient()
	if client == nil {
		return nil, false
	}
	forVerify := make(map[string]string, len(params))
	for name, value := range params {
		forVerify[name] = value
	}
	info, err := client.Verify(forVerify)
	if err != nil || info == nil || !info.VerifyStatus {
		return nil, false
	}
	return info, true
}

func subscriptionEpayProviderPayload(info *epay.VerifyRes) (string, bool) {
	encoded, err := jsonutil.Marshal(info)
	if err != nil || len(encoded) > maxSubscriptionEpayCallbackBytes {
		return "", false
	}
	return string(encoded), true
}

// SubscriptionEpayNotify verifies a successful signed gateway callback and
// responds with the bare acknowledgement required by EPay.
func SubscriptionEpayNotify(c *gin.Context) {
	if !subscriptionEpayCallbacksEnabled() {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	params, ok := boundedEpayCallbackParams(c)
	if !ok {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	info, ok := verifyEpayCallback(params)
	if !ok || info.TradeStatus != epay.StatusTradeSuccess {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	payload, ok := subscriptionEpayProviderPayload(info)
	if !ok || billingsvc.CompleteBoundEpaySubscriptionOrder(info.ServiceTradeNo, payload, info.Type, info.Money) != nil {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	_, _ = c.Writer.Write([]byte("success"))
}

// SubscriptionEpayReturn verifies the browser return and redirects to the
// reference wallet result state. A successful return uses the same atomic,
// idempotent fulfillment path as the asynchronous notification.
func SubscriptionEpayReturn(c *gin.Context) {
	failURL := paymentReturnPath("/wallet?pay=fail")
	if !subscriptionEpayCallbacksEnabled() {
		c.Redirect(http.StatusFound, failURL)
		return
	}
	params, ok := boundedEpayCallbackParams(c)
	if !ok {
		c.Redirect(http.StatusFound, failURL)
		return
	}
	info, ok := verifyEpayCallback(params)
	if !ok {
		c.Redirect(http.StatusFound, failURL)
		return
	}
	if info.TradeStatus != epay.StatusTradeSuccess {
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=pending"))
		return
	}
	payload, ok := subscriptionEpayProviderPayload(info)
	if !ok || billingsvc.CompleteBoundEpaySubscriptionOrder(info.ServiceTradeNo, payload, info.Type, info.Money) != nil {
		c.Redirect(http.StatusFound, failURL)
		return
	}
	c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=success"))
}
