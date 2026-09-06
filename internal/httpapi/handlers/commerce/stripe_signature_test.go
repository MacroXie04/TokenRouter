package commerce

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func signStripe(payload []byte, secret, ts string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(payload)))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyStripeSignature(t *testing.T) {
	secret := "whsec_test"
	payload := []byte(`{"type":"checkout.session.completed"}`)
	now := time.Unix(1_700_000_000, 0)
	ts := fmt.Sprintf("%d", now.Unix())
	sig := signStripe(payload, secret, ts)

	assert.True(t, verifyStripeSignatureAt(payload, sig, secret, now))
	assert.False(t, verifyStripeSignatureAt(payload, sig, "wrong-secret", now))
	assert.False(t, verifyStripeSignatureAt(payload, "t="+ts, secret, now))
}

func TestVerifyStripeSignatureTimestampTolerance(t *testing.T) {
	secret := "whsec_test"
	payload := []byte(`{"id":"evt_time"}`)
	now := time.Unix(1_700_000_000, 0)

	tests := []struct {
		name      string
		timestamp time.Time
		want      bool
	}{
		{name: "current", timestamp: now, want: true},
		{name: "past boundary", timestamp: now.Add(-stripeSignatureTolerance), want: true},
		{name: "future boundary", timestamp: now.Add(stripeSignatureTolerance), want: true},
		{name: "stale", timestamp: now.Add(-stripeSignatureTolerance - time.Second)},
		{name: "excessive future skew", timestamp: now.Add(stripeSignatureTolerance + time.Second)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := fmt.Sprintf("%d", tt.timestamp.Unix())
			header := signStripe(payload, secret, ts)
			assert.Equal(t, tt.want, verifyStripeSignatureAt(payload, header, secret, now))
		})
	}
}

func TestVerifyStripeSignatureRejectsMalformedOrDuplicateTimestamp(t *testing.T) {
	secret := "whsec_test"
	payload := []byte(`{"id":"evt_header"}`)
	now := time.Unix(1_700_000_000, 0)
	ts := fmt.Sprintf("%d", now.Unix())
	validSignature := strings.TrimPrefix(signStripe(payload, secret, ts), "t="+ts+",v1=")

	headers := []string{
		"",
		"t=" + ts,
		"v1=" + validSignature,
		"t=not-a-number,v1=" + validSignature,
		"t=-1,v1=" + validSignature,
		"t=9223372036854775808,v1=" + validSignature,
		"t=" + ts + ",t=" + ts + ",v1=" + validSignature,
		"t=" + ts + ",broken,v1=" + validSignature,
		"t=" + ts + ",v1=" + validSignature + "=",
	}
	for _, header := range headers {
		assert.False(t, verifyStripeSignatureAt(payload, header, secret, now), header)
	}
}

func TestVerifyStripeSignatureAcceptsAnyValidV1(t *testing.T) {
	secret := "whsec_test"
	payload := []byte(`{"id":"evt_rotation"}`)
	now := time.Unix(1_700_000_000, 0)
	ts := fmt.Sprintf("%d", now.Unix())
	validSignature := strings.TrimPrefix(signStripe(payload, secret, ts), "t="+ts+",v1=")
	wrongSignature := strings.Repeat("0", sha256.Size*2)

	assert.True(t, verifyStripeSignatureAt(payload,
		fmt.Sprintf("t=%s,v1=%s,v1=%s", ts, wrongSignature, validSignature), secret, now))
	assert.True(t, verifyStripeSignatureAt(payload,
		fmt.Sprintf("t=%s,v1=%s,v1=%s", ts, validSignature, wrongSignature), secret, now))
	assert.True(t, verifyStripeSignatureAt(payload,
		fmt.Sprintf("t=%s,v1=malformed,v1=%s", ts, validSignature), secret, now))
	assert.False(t, verifyStripeSignatureAt(payload,
		fmt.Sprintf("t=%s,v1=%s", ts, wrongSignature), secret, now))
}

func TestVerifyStripeSignatureBoundsHeaderWork(t *testing.T) {
	secret := "whsec_test"
	payload := []byte(`{"id":"evt_bounds"}`)
	now := time.Unix(1_700_000_000, 0)
	ts := fmt.Sprintf("%d", now.Unix())

	assert.False(t, verifyStripeSignatureAt(payload,
		strings.Repeat("x", stripeSignatureMaxHeaderBytes+1), secret, now))
	parts := []string{signStripe(payload, secret, ts)}
	for i := 0; i < stripeSignatureMaxParts; i++ {
		parts = append(parts, fmt.Sprintf("v0=%d", i))
	}
	assert.False(t, verifyStripeSignatureAt(payload, strings.Join(parts, ","), secret, now))
}

func TestStripeWebhookRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	r := gin.New()
	r.POST("/stripe/webhook", StripeWebhook)

	req := httptest.NewRequest(http.MethodPost, "/stripe/webhook",
		strings.NewReader(strings.Repeat("x", int(stripeWebhookMaxBodyBytes)+1)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
}
