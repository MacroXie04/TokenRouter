package commerce

import (
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/pagination"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	"net/http"
	"strconv"
)

// SearchRedemptions searches redemption codes (keyword prefix/id match plus
// the reference status filter) with pageInfo paging.
func SearchRedemptions(c *gin.Context) {
	keyword := c.Query("keyword")
	status := c.Query("status")
	pi := pagination.FromContext(c)
	items, total, err := billingsvc.SearchRedemptions(keyword, status, pi.Offset(), pi.PageSize)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	pi.Total = int(total)
	pi.Items = items
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": pi})
}

// GetRedemption returns a single redemption code.
func GetRedemption(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	redemption, err := billingsvc.GetRedemptionByID(id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": redemption})
}

// UpdateRedemption updates a redemption code. With ?status_only the status is
// the only field applied; otherwise name/quota/expired_time are applied after
// the reference expiry validation.
func UpdateRedemption(c *gin.Context) {
	statusOnly := c.Query("status_only")
	var req struct {
		Id          int    `json:"id"`
		Name        string `json:"name"`
		Quota       int    `json:"quota"`
		ExpiredTime int64  `json:"expired_time"`
		Status      int    `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if _, err := billingsvc.GetRedemptionByID(req.Id); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if statusOnly == "" {
		if req.ExpiredTime != 0 && req.ExpiredTime < wallclock.NowTimestamp() {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "过期时间不能早于当前时间"})
			return
		}
		if req.Quota <= 0 || !quotamath.QuotaWithinBounds(req.Quota) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "兑换额度必须在安全范围内"})
			return
		}
	}
	updated, err := billingsvc.UpdateRedemption(req.Id, statusOnly != "", req.Name, req.Quota, req.ExpiredTime, req.Status)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage,
		"redemption.update id="+textutil.Int2Str(req.Id))
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": updated})
}

// DeleteInvalidRedemption hard-deletes used/disabled/expired codes and
// returns the count.
func DeleteInvalidRedemption(c *gin.Context) {
	rows, err := billingsvc.DeleteInvalidRedemptions()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": rows})
}

// DeleteRedemption deletes one redemption code.
func DeleteRedemption(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err := billingsvc.DeleteRedemptionByID(id); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}
