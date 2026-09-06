package commerce

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
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
	waffoPancakeControllerBodyLimit = 64 << 10
	waffoPancakeSignatureHeader     = "X-Waffo-Signature"
)

var errWaffoPancakeRequestInvalid = errors.New("invalid Waffo Pancake request")

type waffoPancakePayRequest struct {
	Amount int64 `json:"amount"`
}

func bindWaffoPancakeJSON(c *gin.Context, target any, allowEmpty bool) error {
	if c == nil || c.Request == nil || c.Request.Body == nil || target == nil {
		return errWaffoPancakeRequestInvalid
	}
	if c.Request.ContentLength > waffoPancakeControllerBodyLimit {
		return httpx.ErrBodyTooLarge
	}
	body, err := httpx.ReadAllLimited(c.Request.Body, waffoPancakeControllerBodyLimit)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		if allowEmpty {
			return nil
		}
		return errWaffoPancakeRequestInvalid
	}
	if jsonutil.Unmarshal(body, target) != nil {
		return errWaffoPancakeRequestInvalid
	}
	return nil
}

func writeWaffoPancakeBindError(c *gin.Context, err error) {
	if errors.Is(err, httpx.ErrBodyTooLarge) {
		c.AbortWithStatus(http.StatusRequestEntityTooLarge)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
}

func checkedWaffoPancakeConfig() (setting.WaffoPancakeConfig, bool) {
	config, err := setting.GetWaffoPancakeConfigChecked()
	return config, err == nil && config.MerchantID != "" && config.PrivateKey != "" &&
		config.StoreID != "" && config.ProductID != ""
}

// RequestWaffoPancakeAmount calculates the exact two-decimal wallet checkout
// amount without creating any local or provider-side state.
func RequestWaffoPancakeAmount(c *gin.Context) {
	var request waffoPancakePayRequest
	if err := bindWaffoPancakeJSON(c, &request, false); err != nil {
		writeWaffoPancakeBindError(c, err)
		return
	}
	config, err := setting.GetWaffoPancakeConfigChecked()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	if request.Amount > quotamath.MaxQuota {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围"})
		return
	}
	if request.Amount < config.MinTopUp {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", config.MinTopUp)})
		return
	}
	if _, err := billingsvc.NormalizeTopUpOrderAmount(request.Amount); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围或无法精确兑换"})
		return
	}
	user, err := userssvc.GetUserByID(requestctx.GetUserId(c))
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	money, err := billingsvc.GetWaffoPancakeTopupMoney(request.Amount, user.Group, config.UnitPrice)
	if err != nil || money <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	formatted, err := billingsvc.FormatWaffoPancakeAmount(money)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": formatted})
}

// RequestWaffoPancakePay snapshots wallet value and provider economics before
// creating a signed hosted checkout. Ambiguous provider failures deliberately
// leave the immutable order pending for reconciliation.
func RequestWaffoPancakePay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	config, configured := checkedWaffoPancakeConfig()
	if !configured {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 配置不完整"})
		return
	}
	var request waffoPancakePayRequest
	if err := bindWaffoPancakeJSON(c, &request, false); err != nil {
		writeWaffoPancakeBindError(c, err)
		return
	}
	if request.Amount > quotamath.MaxQuota {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围"})
		return
	}
	if request.Amount < config.MinTopUp {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", config.MinTopUp)})
		return
	}
	amount, err := billingsvc.NormalizeTopUpOrderAmount(request.Amount)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围或无法精确兑换"})
		return
	}
	user, err := userssvc.GetUserByID(requestctx.GetUserId(c))
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "用户不存在"})
		return
	}
	money, err := billingsvc.GetWaffoPancakeTopupMoney(request.Amount, user.Group, config.UnitPrice)
	if err != nil || money < 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	providerAmount, err := billingsvc.FormatWaffoPancakeAmount(money)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	suffix, err := cryptoutil.SecureRandomAlphanumeric(6)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	tradeNo := fmt.Sprintf("WAFFO_PANCAKE-%d-%d-%s", user.Id, time.Now().UnixMilli(), suffix)
	order, checkout, err := billingsvc.CreateBoundWaffoPancakeTopUp(user.Id, amount, request.Amount, providerAmount, config, tradeNo)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	session, err := billingsvc.CreateWaffoPancakeCheckoutSession(c.Request.Context(), config, checkout)
	if err != nil {
		if billingsvc.WaffoPancakeRequestDefinitelyRejected(err) {
			if statusErr := billingsvc.UpdatePendingTopUpStatus(order.TradeNo, billingsvc.PaymentProviderWaffoPancake, billingsvc.TopUpStatusFailed); statusErr != nil {
				logging.SysError("Waffo Pancake rejection status update failed trade_no=" + order.TradeNo)
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if err := billingsvc.BindWaffoPancakeTopUpCheckout(order.TradeNo, session); err != nil {
		logging.SysError("Waffo Pancake checkout binding failed trade_no=" + order.TradeNo)
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	writeWaffoPancakeCheckout(c, order.TradeNo, session)
}

type subscriptionWaffoPancakePayRequest struct {
	PlanID int `json:"plan_id"`
}

// SubscriptionRequestWaffoPancakePay is the plan-specific counterpart of the
// wallet flow. Purchase capacity and entitlement details are reserved in the
// same transaction as the pending order.
func SubscriptionRequestWaffoPancakePay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	var request subscriptionWaffoPancakePayRequest
	if err := bindWaffoPancakeJSON(c, &request, false); err != nil || request.PlanID <= 0 {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
		} else {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		}
		return
	}
	plan, err := billingsvc.GetSubscriptionPlanById(request.PlanID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "套餐不存在"})
		return
	}
	if !plan.Enabled {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "套餐未启用"})
		return
	}
	if strings.TrimSpace(plan.WaffoPancakeProductId) == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "该套餐未配置 WaffoPancakeProductId"})
		return
	}
	config, err := setting.GetWaffoPancakeConfigChecked()
	if err != nil || config.MerchantID == "" || config.PrivateKey == "" || config.StoreID == "" || config.ProductID == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Waffo Pancake 未配置或密钥无效"})
		return
	}
	suffix, err := cryptoutil.SecureRandomAlphanumeric(6)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	userID := requestctx.GetUserId(c)
	tradeNo := fmt.Sprintf("WAFFO_PANCAKE_SUB-%d-%d-%s", userID, time.Now().UnixMilli(), suffix)
	order, checkout, err := billingsvc.CreateBoundWaffoPancakeSubscriptionOrder(userID, plan.Id, config, tradeNo)
	if err != nil {
		if errors.Is(err, billingsvc.ErrSubscriptionPurchaseLimit) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	session, err := billingsvc.CreateWaffoPancakeCheckoutSession(c.Request.Context(), config, checkout)
	if err != nil {
		if billingsvc.WaffoPancakeRequestDefinitelyRejected(err) {
			if statusErr := billingsvc.UpdatePendingSubscriptionOrderStatus(order.TradeNo, billingsvc.PaymentProviderWaffoPancake, billingsvc.TopUpStatusFailed); statusErr != nil {
				logging.SysError("Waffo Pancake subscription rejection status update failed trade_no=" + order.TradeNo)
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if err := billingsvc.BindWaffoPancakeSubscriptionCheckout(order.TradeNo, session); err != nil {
		logging.SysError("Waffo Pancake subscription checkout binding failed trade_no=" + order.TradeNo)
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	writeWaffoPancakeCheckout(c, order.TradeNo, session)
}

func writeWaffoPancakeCheckout(c *gin.Context, tradeNo string, session *billingsvc.WaffoPancakeCheckoutSession) {
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"checkout_url": session.CheckoutURL, "session_id": session.SessionID,
			"expires_at": session.ExpiresAt, "order_id": tradeNo,
			"token": session.Token, "token_expires_at": session.TokenExpiresAt,
		},
	})
}

// WaffoPancakeWebhook authenticates the raw bounded body, acknowledges signed
// irrelevant or misrouted events, and only settles exact immutable orders.
func WaffoPancakeWebhook(c *gin.Context) {
	_, configured := checkedWaffoPancakeConfig()
	if !billingsvc.PaymentComplianceConfirmed() || !configured {
		c.String(http.StatusForbidden, "webhook disabled")
		return
	}
	expectedEnvironment := strings.TrimSpace(c.Param("env"))
	if expectedEnvironment != billingsvc.WaffoPancakeModeTest && expectedEnvironment != billingsvc.WaffoPancakeModeProd {
		c.String(http.StatusNotFound, "unknown env")
		return
	}
	if c.Request.ContentLength > waffoPancakeControllerBodyLimit {
		c.String(http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	body, err := httpx.ReadAllLimited(c.Request.Body, waffoPancakeControllerBodyLimit)
	if err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			c.String(http.StatusRequestEntityTooLarge, "request too large")
		} else {
			c.String(http.StatusBadRequest, "bad request")
		}
		return
	}
	event, err := billingsvc.VerifyConfiguredWaffoPancakeWebhook(body, c.GetHeader(waffoPancakeSignatureHeader), expectedEnvironment)
	if err != nil {
		c.String(http.StatusUnauthorized, "invalid signature")
		return
	}
	if event.Mode != expectedEnvironment || event.NormalizedEventType() != "order.completed" {
		c.String(http.StatusOK, "OK")
		return
	}
	settlement, err := billingsvc.WaffoPancakeSettlementFromEvent(event)
	if err != nil {
		c.String(http.StatusOK, "OK")
		return
	}
	if strings.HasPrefix(settlement.TradeNo, "WAFFO_PANCAKE_SUB-") {
		err = billingsvc.CompleteBoundWaffoPancakeSubscriptionOrder(settlement.TradeNo, string(body), settlement)
	} else {
		err = billingsvc.CompleteBoundWaffoPancakeTopUpOrder(settlement)
	}
	if err != nil {
		logging.SysError("Waffo Pancake webhook settlement deferred trade_no=" + settlement.TradeNo)
		c.String(http.StatusInternalServerError, "retry")
		return
	}
	c.String(http.StatusOK, "OK")
}

type saveWaffoPancakeRequest struct {
	MerchantID string  `json:"merchant_id"`
	PrivateKey string  `json:"private_key"`
	ReturnURL  string  `json:"return_url"`
	StoreID    string  `json:"store_id"`
	ProductID  string  `json:"product_id"`
	UnitPrice  *string `json:"unit_price"`
	MinTopUp   *string `json:"min_top_up"`
}

type createWaffoPancakePairRequest struct {
	MerchantID string `json:"merchant_id"`
	PrivateKey string `json:"private_key"`
	ReturnURL  string `json:"return_url"`
}

func resolveWaffoPancakeAdminCredentials(bodyMerchantID, bodyPrivateKey string) (string, string) {
	merchantID := strings.TrimSpace(bodyMerchantID)
	privateKey := strings.TrimSpace(bodyPrivateKey)
	if merchantID == "" && privateKey == "" {
		return setting.GetOption(setting.WaffoPancakeMerchantIDOption), setting.GetOption(setting.WaffoPancakePrivateKeyOption)
	}
	return merchantID, privateKey
}

func SaveWaffoPancake(c *gin.Context) {
	var request saveWaffoPancakeRequest
	if err := bindWaffoPancakeJSON(c, &request, false); err != nil {
		writeWaffoPancakeBindError(c, err)
		return
	}
	if err := billingsvc.SaveWaffoPancakeConfigWithPricing(
		request.MerchantID, request.PrivateKey, request.ReturnURL, request.StoreID, request.ProductID,
		request.UnitPrice, request.MinTopUp,
	); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "保存配置失败"})
		return
	}
	config, err := setting.GetWaffoPancakeConfigChecked()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "保存配置失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"product_id": config.ProductID, "store_id": config.StoreID}})
}

func CreateWaffoPancakePair(c *gin.Context) {
	var request createWaffoPancakePairRequest
	if err := bindWaffoPancakeJSON(c, &request, true); err != nil {
		writeWaffoPancakeBindError(c, err)
		return
	}
	merchantID, privateKey := resolveWaffoPancakeAdminCredentials(request.MerchantID, request.PrivateKey)
	if merchantID == "" || privateKey == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 凭证未配置"})
		return
	}
	result, err := billingsvc.CreateWaffoPancakePrimaryPair(c.Request.Context(), merchantID, privateKey, request.ReturnURL)
	if err != nil {
		data := gin.H{}
		if result != nil && result.OrphanStore {
			data["store_id"] = result.StoreID
			data["store_name"] = result.StoreName
			data["orphan_store"] = true
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": data})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{
		"store_id": result.StoreID, "store_name": result.StoreName,
		"product_id": result.ProductID, "product_name": result.ProductName,
	}})
}

func ListWaffoPancakeCatalog(c *gin.Context) {
	merchantID, privateKey := resolveWaffoPancakeAdminCredentials(c.Query("merchant_id"), c.Query("private_key"))
	if merchantID == "" || privateKey == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 凭证未配置"})
		return
	}
	catalog, err := billingsvc.ListWaffoPancakeCatalog(c.Request.Context(), merchantID, privateKey)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉取目录失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": catalog})
}

type createWaffoPancakeSubscriptionProductRequest struct {
	Name   string `json:"name"`
	Amount string `json:"amount"`
}

func CreateWaffoPancakeSubscriptionProduct(c *gin.Context) {
	var request createWaffoPancakeSubscriptionProductRequest
	if err := bindWaffoPancakeJSON(c, &request, false); err != nil {
		writeWaffoPancakeBindError(c, err)
		return
	}
	if strings.TrimSpace(request.Name) == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "套餐名称不能为空"})
		return
	}
	if strings.TrimSpace(request.Amount) == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "套餐价格不能为空"})
		return
	}
	config, err := setting.GetWaffoPancakeConfigChecked()
	if err != nil || config.MerchantID == "" || config.PrivateKey == "" || config.StoreID == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 未完成配置，请先在支付设置中完成网关绑定"})
		return
	}
	productID, err := billingsvc.CreateWaffoPancakeProductForPlan(
		c.Request.Context(), config.MerchantID, config.PrivateKey, config.StoreID,
		request.Name, request.Amount, config.ReturnURL,
	)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建套餐产品失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{
		"product_id": productID, "product_name": strings.TrimSpace(request.Name), "store_id": config.StoreID,
	}})
}

func ListWaffoPancakeSubscriptionProductOptions(c *gin.Context) {
	config, err := setting.GetWaffoPancakeConfigChecked()
	if err != nil || config.MerchantID == "" || config.PrivateKey == "" || config.StoreID == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 未完成配置，请先在支付设置中完成网关绑定"})
		return
	}
	catalog, err := billingsvc.ListWaffoPancakeCatalog(c.Request.Context(), config.MerchantID, config.PrivateKey)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉取产品列表失败"})
		return
	}
	products := []billingsvc.WaffoPancakeCatalogProduct{}
	for _, store := range catalog.Stores {
		if store.ID == config.StoreID {
			products = store.OnetimeProducts
			break
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"store_id": config.StoreID, "products": products}})
}
