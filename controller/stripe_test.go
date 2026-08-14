package controller_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
)

func signStripe(payload []byte, secret, ts string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(payload)))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestStripeWebhookCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")

	dsn := "file:" + filepath.Join(t.TempDir(), "stripe.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.Log{}))
	model.DB = db
	model.LOG_DB = db

	user := model.User{Username: "payer", Password: "x", Role: 1, Status: 1, Quota: 0, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	order, err := service.CreateTopUp(user.Id, 1000, 10.0, "stripe", "stripe")
	require.NoError(t, err)

	r := router.SetUpRouter()

	body := fmt.Sprintf(`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"metadata":{"user_id":"%d","trade_no":"%s","quota":"1000"}}}}`, user.Id, order.TradeNo)
	sig := signStripe([]byte(body), "whsec_test", "1234567890")

	req := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, 1000, got.Quota, "webhook must credit quota")

	// Duplicate delivery is idempotent (no double-credit).
	req2 := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(body))
	req2.Header.Set("Stripe-Signature", sig)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	var got2 model.User
	require.NoError(t, model.DB.First(&got2, user.Id).Error)
	assert.Equal(t, 1000, got2.Quota, "duplicate webhook must not double-credit")
}
