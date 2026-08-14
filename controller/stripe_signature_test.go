package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
)

func signStripe(payload []byte, secret, ts string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(payload)))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyStripeSignature(t *testing.T) {
	secret := "whsec_test"
	payload := []byte(`{"type":"checkout.session.completed"}`)
	sig := signStripe(payload, secret, "1234567890")

	assert.True(t, VerifyStripeSignature(payload, sig, secret))
	assert.False(t, VerifyStripeSignature(payload, sig, "wrong-secret"))
	assert.False(t, VerifyStripeSignature(payload, "t=1234567890", secret))
}
