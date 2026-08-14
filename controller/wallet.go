package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// --- Redemption codes ---

// CreateRedemption creates a redemption code (admin).
func CreateRedemption(c *gin.Context) {
	var req struct {
		Name        string `json:"name" binding:"required"`
		Quota       int    `json:"quota" binding:"required"`
		ExpiredTime int64  `json:"expired_time"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	r, err := service.CreateRedemption(common.GetUserId(c), req.Name, req.Quota, req.ExpiredTime)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("创建失败"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(r))
}

// GetRedemptions lists redemption codes (admin).
func GetRedemptions(c *gin.Context) {
	c.JSON(http.StatusOK, dto.Ok(service.GetRedemptions(common.GetUserId(c), true)))
}

// Redeem redeems a code for the authenticated user.
func Redeem(c *gin.Context) {
	var req struct {
		Key string `json:"key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	quota, err := service.Redeem(common.GetUserId(c), req.Key)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"quota": quota}))
}

// --- Check-in ---

// CheckIn records a daily check-in.
func CheckIn(c *gin.Context) {
	reward, err := service.CheckIn(common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"reward": reward}))
}

// CheckInStatus reports whether the user has checked in today.
func CheckInStatus(c *gin.Context) {
	c.JSON(http.StatusOK, dto.Ok(gin.H{"checked_in": service.CheckInStatus(common.GetUserId(c))}))
}

// --- Wallet top-up ---

// TopUp creates a recharge order. A "balance" payment method completes
// immediately (offline-capable); external methods return a pending order that a
// webhook/notification later settles.
func TopUp(c *gin.Context) {
	var req struct {
		Amount        int    `json:"amount" binding:"required"`
		Money         float64 `json:"money"`
		PaymentMethod string `json:"payment_method" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	userId := common.GetUserId(c)
	order, err := service.CreateTopUp(userId, int64(req.Amount), req.Money, req.PaymentMethod, "balance")
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("创建订单失败"))
		return
	}
	if req.PaymentMethod == "balance" {
		if err := service.CompleteTopUp(userId, order.TradeNo, int64(req.Amount)); err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("充值失败"))
			return
		}
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"trade_no": order.TradeNo, "status": order.Status}))
}

// GetSelfTopUps lists the user's top-up orders.
func GetSelfTopUps(c *gin.Context) {
	var orders []model.TopUp
	model.DB.Where("user_id = ?", common.GetUserId(c)).Order("id desc").Find(&orders)
	c.JSON(http.StatusOK, dto.Ok(orders))
}
