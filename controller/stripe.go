package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/service"
)

// StripeWebhook handles Stripe webhook deliveries with HMAC-SHA256 signature
// verification and idempotent completion of top-up orders.
func StripeWebhook(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("读取失败"))
		return
	}
	secret := common.GetEnv("STRIPE_WEBHOOK_SECRET", "")
	if secret == "" {
		c.JSON(http.StatusServiceUnavailable, dto.Fail("支付回调未配置"))
		return
	}
	if !VerifyStripeSignature(body, c.GetHeader("Stripe-Signature"), secret) {
		c.JSON(http.StatusBadRequest, dto.Fail("签名校验失败"))
		return
	}

	var event struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := common.Unmarshal(body, &event); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效事件"))
		return
	}

	switch event.Type {
	case "checkout.session.completed":
		userId := common.Str2Int(event.Data.Object.Metadata["user_id"])
		tradeNo := event.Data.Object.Metadata["trade_no"]
		quota := common.Str2Int(event.Data.Object.Metadata["quota"])
		if userId > 0 && tradeNo != "" && quota > 0 {
			// CompleteTopUp is idempotent: a duplicate delivery does not double-credit.
			_ = service.CompleteTopUp(userId, tradeNo, int64(quota))
		}
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

// VerifyStripeSignature validates the Stripe-Signature header (t=<ts>,v1=<hmac>)
// against the payload using the webhook secret.
func VerifyStripeSignature(payload []byte, header, secret string) bool {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts = kv[1]
		case "v1":
			sig = kv[1]
		}
	}
	if ts == "" || sig == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(payload)))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}
