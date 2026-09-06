package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

func writeAdminSubscriptionError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrSubscriptionTargetForbidden) {
		c.JSON(http.StatusForbidden, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
}

// SubscriptionPlanDTO wraps a plan for list responses.
type SubscriptionPlanDTO struct {
	Plan model.SubscriptionPlan `json:"plan"`
}

type adminUpsertSubscriptionPlanRequest struct {
	Plan model.SubscriptionPlan `json:"plan"`
}

type adminResetSubscriptionRequest struct {
	PlanId           int   `json:"plan_id"`
	AdvanceResetTime *bool `json:"advance_reset_time"`
}

// requirePaymentCompliance aborts payment-adjacent admin operations until the
// operator confirms the compliance statement.
func requirePaymentCompliance(c *gin.Context) bool {
	if !service.PaymentComplianceConfirmed() {
		c.JSON(http.StatusBadRequest, dto.Fail(service.ErrPaymentComplianceRequired.Error()))
		return false
	}
	return true
}

// AdminListSubscriptionPlans lists every plan, enabled or not.
func AdminListSubscriptionPlans(c *gin.Context) {
	plans, err := service.AdminListSubscriptionPlans()
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	result := make([]SubscriptionPlanDTO, 0, len(plans))
	for _, p := range plans {
		result = append(result, SubscriptionPlanDTO{Plan: p})
	}
	c.JSON(http.StatusOK, dto.Ok(result))
}

// AdminCreateSubscriptionPlan creates a plan from a {"plan": {...}} payload.
func AdminCreateSubscriptionPlan(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	var req adminUpsertSubscriptionPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := service.AdminCreateSubscriptionPlan(&req.Plan); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(req.Plan))
}

// AdminUpdateSubscriptionPlan fully updates a plan by path id.
func AdminUpdateSubscriptionPlan(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的ID"))
		return
	}
	var req adminUpsertSubscriptionPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := service.AdminUpdateSubscriptionPlan(id, &req.Plan); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(nil))
}

// AdminUpdateSubscriptionPlanStatus toggles a plan's enabled flag.
func AdminUpdateSubscriptionPlanStatus(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的ID"))
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := service.AdminUpdateSubscriptionPlanStatus(id, *req.Enabled); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(nil))
}

// AdminBindSubscription grants a plan to a user without payment.
func AdminBindSubscription(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	var req struct {
		UserId int `json:"user_id"`
		PlanId int `json:"plan_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.UserId <= 0 || req.PlanId <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	msg, err := service.AdminBindSubscriptionAuthorized(req.UserId, req.PlanId, common.GetRole(c))
	if err != nil {
		writeAdminSubscriptionError(c, err)
		return
	}
	if msg != "" {
		c.JSON(http.StatusOK, dto.Ok(gin.H{"message": msg}))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(nil))
}

// AdminListUserSubscriptions lists all subscriptions of a user (any status).
func AdminListUserSubscriptions(c *gin.Context) {
	userId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的用户ID"))
		return
	}
	subs, err := service.AdminListUserSubscriptionsAuthorized(userId, common.GetRole(c))
	if err != nil {
		writeAdminSubscriptionError(c, err)
		return
	}
	c.JSON(http.StatusOK, dto.Ok(subs))
}

// AdminCreateUserSubscription creates a subscription for the path user from a
// plan (no payment); same semantics as bind.
func AdminCreateUserSubscription(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	userId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的用户ID"))
		return
	}
	var req struct {
		PlanId int `json:"plan_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	msg, err := service.AdminBindSubscriptionAuthorized(userId, req.PlanId, common.GetRole(c))
	if err != nil {
		writeAdminSubscriptionError(c, err)
		return
	}
	if msg != "" {
		c.JSON(http.StatusOK, dto.Ok(gin.H{"message": msg}))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(nil))
}

func resolveAdvanceResetTime(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

// recordSubscriptionResetUserLogs writes a manage log for every affected user.
func recordSubscriptionResetUserLogs(result *service.SubscriptionResetResult) {
	if result == nil || result.ResetCount == 0 {
		return
	}
	content := fmt.Sprintf("管理员重置订阅套餐 %s（ID: %d）额度", result.PlanTitle, result.PlanId)
	for _, userId := range result.AffectedUserIds {
		service.RecordSystemLog(userId, service.LogTypeManage, content)
	}
}

// AdminResetUserSubscriptionsByPlan resets one user's active subscriptions of
// a plan.
func AdminResetUserSubscriptionsByPlan(c *gin.Context) {
	userId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的用户ID"))
		return
	}
	var req adminResetSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	result, err := service.AdminResetUserSubscriptionsByPlanAuthorized(
		userId, req.PlanId, resolveAdvanceResetTime(req.AdvanceResetTime), common.GetRole(c),
	)
	if err != nil {
		writeAdminSubscriptionError(c, err)
		return
	}
	recordSubscriptionResetUserLogs(result)
	c.JSON(http.StatusOK, dto.Ok(result))
}

// AdminResetPlanSubscriptions resets every active subscription of the plan.
func AdminResetPlanSubscriptions(c *gin.Context) {
	planId, _ := strconv.Atoi(c.Param("id"))
	if planId <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的ID"))
		return
	}
	var req adminResetSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	result, err := service.AdminResetPlanSubscriptionsAuthorized(
		planId, resolveAdvanceResetTime(req.AdvanceResetTime), common.GetRole(c),
	)
	if err != nil {
		writeAdminSubscriptionError(c, err)
		return
	}
	recordSubscriptionResetUserLogs(result)
	common.SysLog(fmt.Sprintf("admin reset subscription plan %d quota: reset_count=%d user_count=%d advance_reset_time=%t",
		result.PlanId, result.ResetCount, result.UserCount, result.AdvanceResetTime))
	c.JSON(http.StatusOK, dto.Ok(result))
}

// AdminInvalidateUserSubscription cancels a user subscription immediately.
func AdminInvalidateUserSubscription(c *gin.Context) {
	subId, _ := strconv.Atoi(c.Param("id"))
	if subId <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的订阅ID"))
		return
	}
	msg, err := service.AdminInvalidateUserSubscriptionAuthorized(subId, common.GetRole(c))
	if err != nil {
		writeAdminSubscriptionError(c, err)
		return
	}
	if msg != "" {
		c.JSON(http.StatusOK, dto.Ok(gin.H{"message": msg}))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(nil))
}

// AdminDeleteUserSubscription hard-deletes a user subscription.
func AdminDeleteUserSubscription(c *gin.Context) {
	subId, _ := strconv.Atoi(c.Param("id"))
	if subId <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的订阅ID"))
		return
	}
	msg, err := service.AdminDeleteUserSubscriptionAuthorized(subId, common.GetRole(c))
	if err != nil {
		writeAdminSubscriptionError(c, err)
		return
	}
	if msg != "" {
		c.JSON(http.StatusOK, dto.Ok(gin.H{"message": msg}))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(nil))
}

// ResolveLegacySubscriptionEntitlementReview applies a root-attested immutable
// reset snapshot to an orphaned legacy subscription in review state.
func ResolveLegacySubscriptionEntitlementReview(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	subscriptionID, _ := strconv.Atoi(c.Param("id"))
	if subscriptionID <= 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的订阅ID"))
		return
	}
	var resolution service.LegacySubscriptionEntitlementResolution
	if err := c.ShouldBindJSON(&resolution); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := service.ResolveLegacySubscriptionEntitlementReview(common.GetUserId(c), subscriptionID, resolution); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(nil))
}
