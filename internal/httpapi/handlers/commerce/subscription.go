package commerce

import (
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/dto"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net/http"
)

// ListSubscriptionPlans returns enabled plans for the user plans view. Until
// the operator confirms payment compliance the list is empty rather than an
// error, so the storefront simply shows nothing.
func ListSubscriptionPlans(c *gin.Context) {
	if !billingsvc.PaymentComplianceConfirmed() {
		c.JSON(http.StatusOK, dto.Ok([]SubscriptionPlanDTO{}))
		return
	}
	plans, err := billingsvc.ListSubscriptionPlans()
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("获取订阅套餐失败"))
		return
	}
	result := make([]SubscriptionPlanDTO, 0, len(plans))
	for _, p := range plans {
		result = append(result, SubscriptionPlanDTO{Plan: p})
	}
	c.JSON(http.StatusOK, dto.Ok(result))
}

// CreateSubscriptionPlan creates a plan (admin).
func CreateSubscriptionPlan(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	var p model.SubscriptionPlan
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := billingsvc.CreateSubscriptionPlan(&p); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(p))
}

// SubscriptionRequestBalancePay purchases a plan for the authenticated user by
// deducting wallet balance.
func SubscriptionRequestBalancePay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	var req struct {
		PlanId int `json:"plan_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := billingsvc.PurchaseSubscriptionWithBalance(requestctx.GetUserId(c), req.PlanId); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(nil))
}

// GetSelfSubscription returns the user's billing preference plus their active
// and historical subscriptions. List loading errors degrade to empty lists so
// the view always renders.
func GetSelfSubscription(c *gin.Context) {
	userId := requestctx.GetUserId(c)
	active, err := billingsvc.GetAllActiveUserSubscriptions(userId)
	if err != nil {
		active = []billingsvc.SubscriptionSummary{}
	}
	all, err := billingsvc.GetAllUserSubscriptions(userId)
	if err != nil {
		all = []billingsvc.SubscriptionSummary{}
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{
		"billing_preference": userssvc.GetUserBillingPreference(userId),
		"subscriptions":      active,
		"all_subscriptions":  all,
	}))
}

// UpdateSubscriptionPreference stores the user's billing preference. Unknown
// values normalize to the default rather than erroring.
func UpdateSubscriptionPreference(c *gin.Context) {
	var req struct {
		BillingPreference string `json:"billing_preference"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	pref, err := userssvc.UpdateUserBillingPreference(requestctx.GetUserId(c), req.BillingPreference)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"billing_preference": pref}))
}
