package controller

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/service"
)

// GetAllTopUps lists or searches the full top-up history for administrators
// using the reference pageInfo response shape.
func GetAllTopUps(c *gin.Context) {
	page := getPageQuery(c)
	items, total, err := service.ListTopUps(c.Query("keyword"), page.Page, page.PageSize)
	if err != nil {
		if errors.Is(err, service.ErrTopUpQueryInvalid) {
			c.JSON(http.StatusOK, dto.Fail(err.Error()))
			return
		}
		c.JSON(http.StatusInternalServerError, dto.Fail("查询充值记录失败"))
		return
	}
	page.Total = int(total)
	page.Items = items
	c.JSON(http.StatusOK, dto.Ok(page))
}

// AdminCompleteTopUp manually settles one pending top-up. Authorization is
// enforced by the AdminAuth route; the service owns bounds, status, ownership,
// amount, and exactly-once credit checks.
func AdminCompleteTopUp(c *gin.Context) {
	var request struct {
		TradeNo string `json:"trade_no"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.TradeNo) == "" || len(strings.TrimSpace(request.TradeNo)) > 255 {
		c.JSON(http.StatusOK, dto.Fail("参数错误"))
		return
	}
	request.TradeNo = strings.TrimSpace(request.TradeNo)
	if err := service.ManualCompleteTopUp(request.TradeNo); err != nil {
		switch {
		case errors.Is(err, service.ErrTopUpTradeNoInvalid),
			errors.Is(err, service.ErrTopUpNotFound),
			errors.Is(err, service.ErrTopUpStatusInvalid),
			errors.Is(err, service.ErrTopUpAmountMismatch),
			errors.Is(err, service.ErrTopUpQuotaOverflow):
			c.JSON(http.StatusOK, dto.Fail(err.Error()))
		default:
			c.JSON(http.StatusInternalServerError, dto.Fail("补单失败"))
		}
		return
	}
	service.RecordSystemLog(common.GetUserId(c), service.LogTypeManage,
		"topup.manual_complete trade_no="+request.TradeNo+" ip="+c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}
