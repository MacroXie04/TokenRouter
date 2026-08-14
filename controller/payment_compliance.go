package controller

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/service"
)

// PaymentComplianceRequest requires an explicit affirmative acknowledgement.
type PaymentComplianceRequest struct {
	Confirmed bool `json:"confirmed"`
}

// ConfirmPaymentCompliance records the current compliance terms for the root
// operator. A dashboard PAT is intentionally insufficient for this ceremony.
func ConfirmPaymentCompliance(c *gin.Context) {
	if c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "This operation requires dashboard session authentication. API access token is not allowed.",
		})
		return
	}
	var request PaymentComplianceRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}
	if !request.Confirmed {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "请确认合规声明"})
		return
	}

	now := time.Now().Unix()
	userID := common.GetUserId(c)
	status, err := service.ConfirmPaymentCompliance(userID, c.ClientIP(), now)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	service.RecordSystemLog(userID, service.LogTypeManage,
		"payment_compliance.confirmed user_id="+strconv.Itoa(userID)+
			" ip="+c.ClientIP()+" terms_version="+status.TermsVersion+
			" confirmed_at="+strconv.FormatInt(status.ConfirmedAt, 10))
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": status})
}
