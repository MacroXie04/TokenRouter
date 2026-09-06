package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	stripeWebhookMaxBodyBytes     int64 = 1 << 20
	stripeSignatureTolerance            = 5 * time.Minute
	stripeSignatureMaxHeaderBytes       = 8 << 10
	stripeSignatureMaxParts             = 32
)

// StripeWebhook handles Stripe webhook deliveries with HMAC-SHA256 signature
// verification and idempotent completion of top-up orders.
func StripeWebhook(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, stripeWebhookMaxBodyBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("读取失败"))
		return
	}
	if int64(len(body)) > stripeWebhookMaxBodyBytes {
		c.JSON(http.StatusRequestEntityTooLarge, dto.Fail("请求体过大"))
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
		ID      string `json:"id"`
		Type    string `json:"type"`
		Created int64  `json:"created"`
		Data    struct {
			Object stripeCheckoutObject `json:"object"`
		} `json:"data"`
	}
	if err := common.Unmarshal(body, &event); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效事件"))
		return
	}

	switch event.Type {
	case "checkout.session.completed":
		// A completed Checkout session can still be awaiting a delayed
		// payment method. Only "paid" may settle; Stripe will later send the
		// async-payment-succeeded event for delayed confirmation.
		if event.Data.Object.Status != "complete" || event.Data.Object.PaymentStatus != "paid" {
			break
		}
		if err := fulfillStripeCheckout(event.Type, &event.Data.Object); err != nil {
			common.SysLog(fmt.Sprintf("Stripe 订单处理失败 trade_no=%s event_type=%s error=%v",
				event.Data.Object.ClientReferenceID, event.Type, err))
			c.JSON(http.StatusServiceUnavailable, dto.Fail("支付回调处理失败，请重试"))
			return
		}
	case "checkout.session.async_payment_succeeded":
		if event.Data.Object.PaymentStatus != "paid" {
			break
		}
		if err := fulfillStripeCheckout(event.Type, &event.Data.Object); err != nil {
			common.SysLog(fmt.Sprintf("Stripe 异步订单处理失败 trade_no=%s event_type=%s error=%v",
				event.Data.Object.ClientReferenceID, event.Type, err))
			c.JSON(http.StatusServiceUnavailable, dto.Fail("支付回调处理失败，请重试"))
			return
		}
	case "checkout.session.async_payment_failed":
		if err := updateStripeCheckoutTerminal(&event.Data.Object, service.TopUpStatusFailed); err != nil {
			common.SysLog(fmt.Sprintf("Stripe 异步订单失败标记失败 trade_no=%s error=%v", event.Data.Object.ClientReferenceID, err))
			c.JSON(http.StatusServiceUnavailable, dto.Fail("支付回调处理失败，请重试"))
			return
		}
	case "checkout.session.expired":
		if event.Data.Object.Status != "expired" {
			break
		}
		if err := updateStripeCheckoutTerminal(&event.Data.Object, service.TopUpStatusExpired); err != nil {
			common.SysLog(fmt.Sprintf("Stripe 订单过期处理失败 trade_no=%s error=%v", event.Data.Object.ClientReferenceID, err))
			c.JSON(http.StatusServiceUnavailable, dto.Fail("支付回调处理失败，请重试"))
			return
		}
	case "charge.refunded", "charge.dispute.created", "charge.dispute.updated", "charge.dispute.closed":
		// Refund and dispute semantics cannot be inferred safely once wallet
		// credit or a time-bound entitlement may already have been consumed.
		// Never auto-debit here: persist a manual-review marker plus an atomic
		// audit event for a root operator.
		tradeNo := strings.TrimSpace(event.Data.Object.Metadata["trade_no"])
		if tradeNo == "" {
			if err := service.RecordUnmatchedStripePaymentReversalReview(event.ID, event.Type, event.Created); err != nil {
				common.SysLog(fmt.Sprintf("Stripe reversal correlation audit failed event_id=%s event_type=%s error=%v", event.ID, event.Type, err))
				c.JSON(http.StatusServiceUnavailable, dto.Fail("支付冲正记录失败，请重试"))
				return
			}
			break
		}
		if err := service.FlagStripePaymentReversalReview(tradeNo, event.ID, event.Type, event.Created); err != nil {
			common.SysLog(fmt.Sprintf("Stripe reversal review marker failed trade_no=%s event_id=%s event_type=%s error=%v",
				tradeNo, event.ID, event.Type, err))
			c.JSON(http.StatusServiceUnavailable, dto.Fail("支付冲正记录失败，请重试"))
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

type stripeCheckoutObject struct {
	ID                string            `json:"id"`
	Metadata          map[string]string `json:"metadata"`
	ClientReferenceID string            `json:"client_reference_id"`
	Mode              string            `json:"mode"`
	Status            string            `json:"status"`
	PaymentStatus     string            `json:"payment_status"`
	Customer          string            `json:"customer"`
	AmountTotal       *int64            `json:"amount_total"`
	Currency          string            `json:"currency"`
}

func fulfillStripeCheckout(eventType string, object *stripeCheckoutObject) error {
	if object == nil {
		return errors.New("Stripe Checkout 事件缺少对象")
	}
	referenceId := strings.TrimSpace(object.ClientReferenceID)
	orderType, err := stripeOrderTypeForReference(referenceId)
	if err != nil {
		return err
	}
	metadataOrderType := strings.TrimSpace(object.Metadata["order_type"])
	metadataPriceID := strings.TrimSpace(object.Metadata["price_id"])
	if orderType == service.StripeOrderTypeSubscription {
		payload, err := common.Marshal(gin.H{
			"session_id":   object.ID,
			"customer":     object.Customer,
			"amount_total": object.AmountTotal,
			"currency":     strings.ToUpper(object.Currency),
			"mode":         object.Mode,
			"order_type":   metadataOrderType,
			"price_id":     metadataPriceID,
			"event_type":   eventType,
		})
		if err != nil {
			return fmt.Errorf("encode Stripe subscription payload: %w", err)
		}
		return service.CompleteBoundStripeSubscriptionOrder(referenceId, string(payload), object.ID, object.Mode,
			metadataOrderType, metadataPriceID, object.Customer, object.AmountTotal, object.Currency)
	}
	return service.CompleteBoundStripeTopUpOrderWithCustomer(referenceId, object.ID, object.Mode, metadataOrderType,
		object.Customer, object.AmountTotal, object.Currency)
}

func updateStripeCheckoutTerminal(object *stripeCheckoutObject, status string) error {
	if object == nil {
		return errors.New("Stripe Checkout 事件缺少对象")
	}
	referenceID := strings.TrimSpace(object.ClientReferenceID)
	orderType, err := stripeOrderTypeForReference(referenceID)
	if err != nil {
		return err
	}
	metadataOrderType := strings.TrimSpace(object.Metadata["order_type"])
	if orderType == service.StripeOrderTypeSubscription {
		return service.UpdateBoundPendingSubscriptionOrderStatus(referenceID, object.ID, object.Mode, metadataOrderType,
			strings.TrimSpace(object.Metadata["price_id"]), status, object.AmountTotal, object.Currency)
	}
	return service.UpdateBoundPendingTopUpStatus(referenceID, object.ID, object.Mode, metadataOrderType, status,
		object.AmountTotal, object.Currency)
}

func stripeOrderTypeForReference(referenceID string) (string, error) {
	switch {
	case strings.HasPrefix(referenceID, "sub_ref_"):
		return service.StripeOrderTypeSubscription, nil
	case strings.HasPrefix(referenceID, "ref_"):
		return service.StripeOrderTypeWallet, nil
	default:
		return "", errors.New("支付回调缺少有效订单信息")
	}
}

// VerifyStripeSignature validates the Stripe-Signature header (t=<ts>,v1=<hmac>)
// against the payload using the webhook secret.
func VerifyStripeSignature(payload []byte, header, secret string) bool {
	return verifyStripeSignatureAt(payload, header, secret, time.Now())
}

func verifyStripeSignatureAt(payload []byte, header, secret string, now time.Time) bool {
	ts, signatures, ok := parseStripeSignatureHeader(header)
	if !ok || secret == "" {
		return false
	}
	// Stripe timestamps have second precision. Normalize the injected clock to
	// the same precision so both tolerance boundaries are deterministic.
	now = time.Unix(now.Unix(), 0)
	timestamp := time.Unix(ts, 0)
	if timestamp.Before(now.Add(-stripeSignatureTolerance)) || timestamp.After(now.Add(stripeSignatureTolerance)) {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strconv.FormatInt(ts, 10)))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(payload)
	expected := mac.Sum(nil)

	// Do not return on the first match. Stripe sends multiple v1 values while
	// rotating webhook secrets, so every bounded candidate gets the same
	// constant-time comparison and any valid signature is accepted.
	matched := 0
	for _, signature := range signatures {
		matched |= subtle.ConstantTimeCompare(expected, signature)
	}
	return matched == 1
}

func parseStripeSignatureHeader(header string) (int64, [][]byte, bool) {
	if header == "" || len(header) > stripeSignatureMaxHeaderBytes {
		return 0, nil, false
	}
	parts := strings.SplitN(header, ",", stripeSignatureMaxParts+1)
	if len(parts) > stripeSignatureMaxParts {
		return 0, nil, false
	}

	var timestamp int64
	timestampSeen := false
	signatures := make([][]byte, 0, len(parts))
	for _, rawPart := range parts {
		part := strings.TrimSpace(rawPart)
		if part == "" || strings.Count(part, "=") != 1 {
			return 0, nil, false
		}
		key, value, _ := strings.Cut(part, "=")
		switch key {
		case "t":
			if timestampSeen {
				return 0, nil, false
			}
			timestampSeen = true
			if len(value) == 0 || len(value) > 19 {
				return 0, nil, false
			}
			for i := range len(value) {
				if value[i] < '0' || value[i] > '9' {
					return 0, nil, false
				}
			}
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed <= 0 {
				return 0, nil, false
			}
			timestamp = parsed
		case "v1":
			// A SHA-256 signature is exactly 32 bytes / 64 hex characters.
			// Malformed candidates are ignored so another valid v1 value can
			// still authenticate a delivery during secret rotation.
			if len(value) != sha256.Size*2 {
				continue
			}
			signature, err := hex.DecodeString(value)
			if err == nil {
				signatures = append(signatures, signature)
			}
		default:
			// Stripe may add other signature schemes; bounded unknown fields
			// do not affect v1 verification.
		}
	}
	if !timestampSeen || len(signatures) == 0 {
		return 0, nil, false
	}
	return timestamp, signatures, true
}
