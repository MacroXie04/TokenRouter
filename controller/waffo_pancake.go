package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
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
		return common.ErrBodyTooLarge
	}
	body, err := common.ReadAllLimited(c.Request.Body, waffoPancakeControllerBodyLimit)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		if allowEmpty {
			return nil
		}
		return errWaffoPancakeRequestInvalid
	}
	if common.Unmarshal(body, target) != nil {
		return errWaffoPancakeRequestInvalid
	}
	return nil
}

func writeWaffoPancakeBindError(c *gin.Context, err error) {
	if errors.Is(err, common.ErrBodyTooLarge) {
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
	if request.Amount > common.MaxQuota {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围"})
		return
	}
	if request.Amount < config.MinTopUp {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", config.MinTopUp)})
		return
	}
	if _, err := service.NormalizeTopUpOrderAmount(request.Amount); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围或无法精确兑换"})
		return
	}
	user, err := service.GetUserByID(common.GetUserId(c))
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	money, err := service.GetWaffoPancakeTopupMoney(request.Amount, user.Group, config.UnitPrice)
	if err != nil || money <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	formatted, err := service.FormatWaffoPancakeAmount(money)
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
	if request.Amount > common.MaxQuota {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围"})
		return
	}
	if request.Amount < config.MinTopUp {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", config.MinTopUp)})
		return
	}
	amount, err := service.NormalizeTopUpOrderAmount(request.Amount)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量超出安全范围或无法精确兑换"})
		return
	}
	user, err := service.GetUserByID(common.GetUserId(c))
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "用户不存在"})
		return
	}
	money, err := service.GetWaffoPancakeTopupMoney(request.Amount, user.Group, config.UnitPrice)
	if err != nil || money < 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	providerAmount, err := service.FormatWaffoPancakeAmount(money)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值配置无效"})
		return
	}
	suffix, err := common.SecureRandomAlphanumeric(6)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	tradeNo := fmt.Sprintf("WAFFO_PANCAKE-%d-%d-%s", user.Id, time.Now().UnixMilli(), suffix)
	order, checkout, err := service.CreateBoundWaffoPancakeTopUp(user.Id, amount, request.Amount, providerAmount, config, tradeNo)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	session, err := service.CreateWaffoPancakeCheckoutSession(c.Request.Context(), config, checkout)
	if err != nil {
		if service.WaffoPancakeRequestDefinitelyRejected(err) {
			if statusErr := service.UpdatePendingTopUpStatus(order.TradeNo, service.PaymentProviderWaffoPancake, service.TopUpStatusFailed); statusErr != nil {
				common.SysError("Waffo Pancake rejection status update failed trade_no=" + order.TradeNo)
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if err := service.BindWaffoPancakeTopUpCheckout(order.TradeNo, session); err != nil {
		common.SysError("Waffo Pancake checkout binding failed trade_no=" + order.TradeNo)
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
		if errors.Is(err, common.ErrBodyTooLarge) {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
		} else {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		}
		return
	}
	plan, err := service.GetSubscriptionPlanById(request.PlanID)
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
	suffix, err := common.SecureRandomAlphanumeric(6)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	userID := common.GetUserId(c)
	tradeNo := fmt.Sprintf("WAFFO_PANCAKE_SUB-%d-%d-%s", userID, time.Now().UnixMilli(), suffix)
	order, checkout, err := service.CreateBoundWaffoPancakeSubscriptionOrder(userID, plan.Id, config, tradeNo)
	if err != nil {
		if errors.Is(err, service.ErrSubscriptionPurchaseLimit) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	session, err := service.CreateWaffoPancakeCheckoutSession(c.Request.Context(), config, checkout)
	if err != nil {
		if service.WaffoPancakeRequestDefinitelyRejected(err) {
			if statusErr := service.UpdatePendingSubscriptionOrderStatus(order.TradeNo, service.PaymentProviderWaffoPancake, service.TopUpStatusFailed); statusErr != nil {
				common.SysError("Waffo Pancake subscription rejection status update failed trade_no=" + order.TradeNo)
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if err := service.BindWaffoPancakeSubscriptionCheckout(order.TradeNo, session); err != nil {
		common.SysError("Waffo Pancake subscription checkout binding failed trade_no=" + order.TradeNo)
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	writeWaffoPancakeCheckout(c, order.TradeNo, session)
}

func writeWaffoPancakeCheckout(c *gin.Context, tradeNo string, session *service.WaffoPancakeCheckoutSession) {
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
	if !service.PaymentComplianceConfirmed() || !configured {
		c.String(http.StatusForbidden, "webhook disabled")
		return
	}
	expectedEnvironment := strings.TrimSpace(c.Param("env"))
	if expectedEnvironment != service.WaffoPancakeModeTest && expectedEnvironment != service.WaffoPancakeModeProd {
		c.String(http.StatusNotFound, "unknown env")
		return
	}
	if c.Request.ContentLength > waffoPancakeControllerBodyLimit {
		c.String(http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	body, err := common.ReadAllLimited(c.Request.Body, waffoPancakeControllerBodyLimit)
	if err != nil {
		if errors.Is(err, common.ErrBodyTooLarge) {
			c.String(http.StatusRequestEntityTooLarge, "request too large")
		} else {
			c.String(http.StatusBadRequest, "bad request")
		}
		return
	}
	event, err := service.VerifyConfiguredWaffoPancakeWebhook(body, c.GetHeader(waffoPancakeSignatureHeader), expectedEnvironment)
	if err != nil {
		c.String(http.StatusUnauthorized, "invalid signature")
		return
	}
	if event.Mode != expectedEnvironment || event.NormalizedEventType() != "order.completed" {
		c.String(http.StatusOK, "OK")
		return
	}
	settlement, err := service.WaffoPancakeSettlementFromEvent(event)
	if err != nil {
		c.String(http.StatusOK, "OK")
		return
	}
	if strings.HasPrefix(settlement.TradeNo, "WAFFO_PANCAKE_SUB-") {
		err = service.CompleteBoundWaffoPancakeSubscriptionOrder(settlement.TradeNo, string(body), settlement)
	} else {
		err = service.CompleteBoundWaffoPancakeTopUpOrder(settlement)
	}
	if err != nil {
		common.SysError("Waffo Pancake webhook settlement deferred trade_no=" + settlement.TradeNo)
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
	if err := service.SaveWaffoPancakeConfigWithPricing(
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
	result, err := service.CreateWaffoPancakePrimaryPair(c.Request.Context(), merchantID, privateKey, request.ReturnURL)
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
	catalog, err := service.ListWaffoPancakeCatalog(c.Request.Context(), merchantID, privateKey)
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
	productID, err := service.CreateWaffoPancakeProductForPlan(
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
	catalog, err := service.ListWaffoPancakeCatalog(c.Request.Context(), config.MerchantID, config.PrivateKey)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉取产品列表失败"})
		return
	}
	products := []service.WaffoPancakeCatalogProduct{}
	for _, store := range catalog.Stores {
		if store.ID == config.StoreID {
			products = store.OnetimeProducts
			break
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"store_id": config.StoreID, "products": products}})
}
