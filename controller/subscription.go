package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// ListSubscriptionPlans returns enabled plans (public).
func ListSubscriptionPlans(c *gin.Context) {
	c.JSON(http.StatusOK, dto.Ok(service.ListSubscriptionPlans()))
}

// CreateSubscriptionPlan creates a plan (admin).
func CreateSubscriptionPlan(c *gin.Context) {
	var p model.SubscriptionPlan
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := service.CreateSubscriptionPlan(&p); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(p))
}

// PurchaseSubscription purchases a plan for the authenticated user.
func PurchaseSubscription(c *gin.Context) {
	var req struct {
		PlanId int `json:"plan_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	sub, err := service.PurchaseSubscription(common.GetUserId(c), req.PlanId)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(sub))
}

// GetSelfSubscription returns the user's active subscription.
func GetSelfSubscription(c *gin.Context) {
	sub, err := service.GetActiveSubscription(common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusOK, dto.Ok(nil))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(sub))
}
