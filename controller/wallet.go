package controller

import (
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/service"
)

// --- Redemption codes ---

// CreateRedemption generates a batch of redemption codes (reference
// contract): compliance-gated, name 1-20 runes, count 1-100, future expiry
// only, one row per code with a UUID key, keys returned in data.
func CreateRedemption(c *gin.Context) {
	if !service.PaymentComplianceConfirmed() {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": service.ErrPaymentComplianceRequired.Error()})
		return
	}
	var req struct {
		Name        string `json:"name"`
		Quota       int    `json:"quota"`
		ExpiredTime int64  `json:"expired_time"`
		Count       int    `json:"count"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if n := utf8.RuneCountInString(req.Name); n == 0 || n > 20 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "兑换码名称长度必须在1-20之间"})
		return
	}
	if req.Count <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "兑换码个数必须大于0"})
		return
	}
	if req.Count > 100 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "一次兑换码批量生成的个数不能大于 100"})
		return
	}
	if req.ExpiredTime != 0 && req.ExpiredTime < common.NowTimestamp() {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "过期时间不能早于当前时间"})
		return
	}
	if req.Quota <= 0 || !common.QuotaWithinBounds(req.Quota) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "兑换额度必须在安全范围内"})
		return
	}
	keys, err := service.CreateRedemptionBatch(common.GetUserId(c), req.Name, req.Quota, req.ExpiredTime, req.Count)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "创建兑换码失败，请稍后重试",
			"data":    keys,
		})
		return
	}
	service.RecordSystemLog(common.GetUserId(c), service.LogTypeManage,
		"redemption.create name="+req.Name+" count="+common.Int2Str(req.Count))
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": keys})
}

// GetRedemptions lists redemption codes with the reference pageInfo paging.
func GetRedemptions(c *gin.Context) {
	pi := getPageQuery(c)
	items, total, err := service.GetPagedRedemptions(pi.Offset(), pi.PageSize)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	pi.Total = int(total)
	pi.Items = items
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": pi})
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
	result, err := service.CheckInWithResult(common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusOK, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "签到成功",
		"data": gin.H{
			"quota_awarded": result.QuotaAwarded,
			"checkin_date":  result.CheckinDate,
		},
	})
}

// CheckInStatus reports whether the user has checked in today.
func CheckInStatus(c *gin.Context) {
	checkedIn, err := service.CheckInStatusChecked(common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询签到状态失败"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"checked_in": checkedIn}))
}

// --- Wallet top-up ---

// TopUp redeems a server-issued recharge code. The reference API uses the
// historical /user/topup path for redemption; clients must never be allowed to
// create and immediately complete a self-declared "balance" recharge.
func TopUp(c *gin.Context) {
	if !service.PaymentComplianceConfirmed() {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": service.ErrPaymentComplianceRequired.Error()})
		return
	}
	var req struct {
		Key string `json:"key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "兑换码无效"})
		return
	}
	userId := common.GetUserId(c)
	quota, err := service.Redeem(userId, req.Key)
	if err != nil {
		// Keep redemption failures intentionally indistinguishable so callers
		// cannot enumerate whether a code exists, was used, or has expired.
		common.SysLog("top-up redemption failed for user " + common.Int2Str(userId) + ": " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "兑换失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": quota})
}

// GetSelfTopUps lists the user's top-up orders.
func GetSelfTopUps(c *gin.Context) {
	page := getPageQuery(c)
	orders, total, err := service.ListUserTopUps(common.GetUserId(c), c.Query("keyword"), page.Page, page.PageSize)
	if err != nil {
		if errors.Is(err, service.ErrTopUpQueryInvalid) {
			c.JSON(http.StatusOK, dto.Fail(err.Error()))
			return
		}
		c.JSON(http.StatusInternalServerError, dto.Fail("查询充值记录失败"))
		return
	}
	page.Total = int(total)
	page.Items = orders
	c.JSON(http.StatusOK, dto.Ok(page))
}
