package router_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func signStripe(payload []byte, secret, ts string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(payload)))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func currentStripeTimestamp() string {
	return fmt.Sprintf("%d", time.Now().Unix())
}

func TestStripeWebhookCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")

	dsn := "file:" + filepath.Join(t.TempDir(), "stripe.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.SubscriptionOrder{}, &model.Log{}, &model.AuditLogOutbox{}))
	model.DB = db
	model.LOG_DB = db

	user := model.User{Username: "payer", Password: "x", Role: 1, Status: 1, Quota: 0, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	order, err := billingsvc.CreateBoundStripeTopUpWithTradeNo(user.Id, 1000, 10.0, "USD", "ref_stripe-completion-order")
	require.NoError(t, err)
	assert.Equal(t, billingsvc.StripeReconciliationCreationUnknown, order.ReconciliationState)

	r := router.SetUpRouter()

	body := fmt.Sprintf(`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_wallet_completion","client_reference_id":%q,"mode":"payment","status":"complete","payment_status":"paid","amount_total":1000,"currency":"usd","metadata":{"order_type":"wallet","trade_no":%q}}}}`, order.TradeNo, order.TradeNo)
	wrongBody := strings.Replace(body, `"amount_total":1000`, `"amount_total":999`, 1)
	wrongReq := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(wrongBody))
	wrongReq.Header.Set("Stripe-Signature", signStripe([]byte(wrongBody), "whsec_test", currentStripeTimestamp()))
	wrongRec := httptest.NewRecorder()
	r.ServeHTTP(wrongRec, wrongReq)
	require.Equal(t, http.StatusServiceUnavailable, wrongRec.Code)
	var unchanged model.User
	require.NoError(t, model.DB.First(&unchanged, user.Id).Error)
	assert.Zero(t, unchanged.Quota, "mismatched Stripe amount must not credit quota")
	var pending model.TopUp
	require.NoError(t, model.DB.First(&pending, order.Id).Error)
	assert.Equal(t, billingsvc.TopUpStatusPending, pending.Status)
	assert.Equal(t, billingsvc.StripeReconciliationPaymentMismatch, pending.ReconciliationState)

	sig := signStripe([]byte(body), "whsec_test", currentStripeTimestamp())

	req := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, 1000*quotamath.QuotaPerUnit, got.Quota, "webhook must credit immutable internal quota")
	var completed model.TopUp
	require.NoError(t, model.DB.First(&completed, order.Id).Error)
	require.NotNil(t, completed.ProviderSessionId)
	assert.Equal(t, "cs_wallet_completion", *completed.ProviderSessionId,
		"the signed webhook claims a session left unbound by an ambiguous create/bind outcome")
	assert.Empty(t, completed.ReconciliationState)

	// Duplicate delivery is idempotent (no double-credit).
	req2 := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(body))
	req2.Header.Set("Stripe-Signature", sig)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	var got2 model.User
	require.NoError(t, model.DB.First(&got2, user.Id).Error)
	assert.Equal(t, 1000*quotamath.QuotaPerUnit, got2.Quota, "duplicate webhook must not double-credit")
}

func TestStripeWebhookReturnsRetryableFailureWithoutPartialCredit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")

	dsn := "file:" + filepath.Join(t.TempDir(), "stripe-failure.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.SubscriptionOrder{}, &model.Log{}, &model.AuditLogOutbox{}))
	model.DB = db
	model.LOG_DB = db

	user := model.User{Username: "retry-payer", Password: "x", Role: 1, Status: 1, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	order, err := billingsvc.CreateBoundStripeTopUpWithTradeNo(user.Id, 750, 7.5, "USD", "ref_stripe-retry-order")
	require.NoError(t, err)
	require.NoError(t, billingsvc.BindStripeTopUpSession(order.TradeNo, "cs_wallet_retry"))
	body := fmt.Sprintf(`{"id":"evt_retry","type":"checkout.session.completed","data":{"object":{"id":"cs_wallet_retry","client_reference_id":%q,"mode":"payment","status":"complete","payment_status":"paid","amount_total":750,"currency":"usd","metadata":{"order_type":"wallet","trade_no":%q}}}}`, order.TradeNo, order.TradeNo)
	sig := signStripe([]byte(body), "whsec_test", currentStripeTimestamp())
	r := router.SetUpRouter()

	injected := errors.New("injected durable credit failure")
	const callback = "test:fail_stripe_user_credit"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(injected)
		}
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.NoError(t, db.Callback().Update().Remove(callback))

	var stored model.TopUp
	require.NoError(t, db.First(&stored, order.Id).Error)
	assert.Equal(t, billingsvc.TopUpStatusPending, stored.Status)
	var unchanged model.User
	require.NoError(t, db.First(&unchanged, user.Id).Error)
	assert.Zero(t, unchanged.Quota)

	retry := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(body))
	retry.Header.Set("Stripe-Signature", sig)
	retryResponse := httptest.NewRecorder()
	r.ServeHTTP(retryResponse, retry)
	require.Equal(t, http.StatusOK, retryResponse.Code, retryResponse.Body.String())
	require.NoError(t, db.First(&unchanged, user.Id).Error)
	assert.Equal(t, 750*quotamath.QuotaPerUnit, unchanged.Quota)
}

func TestStripeWebhookPaymentStateMachine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	dsn := "file:" + filepath.Join(t.TempDir(), "stripe-state.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.TopUp{}, &model.Log{}, &model.SubscriptionOrder{},
		&model.AuditLogOutbox{},
	))
	model.DB = db
	model.LOG_DB = db
	user := model.User{Username: "delayed-payer", Password: "x", Role: 1, Status: 1, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	pending, err := billingsvc.CreateBoundStripeTopUpWithTradeNo(user.Id, 600, 6, "USD", "ref_stripe-delayed-order")
	require.NoError(t, err)
	require.NoError(t, billingsvc.BindStripeTopUpSession(pending.TradeNo, "cs_wallet_delayed"))
	r := router.SetUpRouter()
	send := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(payload))
		req.Header.Set("Stripe-Signature", signStripe([]byte(payload), "whsec_test", currentStripeTimestamp()))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	unpaid := fmt.Sprintf(`{"id":"evt_unpaid","type":"checkout.session.completed","data":{"object":{"id":"cs_wallet_delayed","client_reference_id":%q,"mode":"payment","status":"complete","payment_status":"unpaid","metadata":{"order_type":"wallet","trade_no":%q}}}}`, pending.TradeNo, pending.TradeNo)
	require.Equal(t, http.StatusOK, send(unpaid).Code)
	var gotUser model.User
	var gotOrder model.TopUp
	require.NoError(t, db.First(&gotUser, user.Id).Error)
	require.NoError(t, db.First(&gotOrder, pending.Id).Error)
	assert.Zero(t, gotUser.Quota)
	assert.Equal(t, billingsvc.TopUpStatusPending, gotOrder.Status)

	asyncSuccess := fmt.Sprintf(`{"id":"evt_async_ok","type":"checkout.session.async_payment_succeeded","data":{"object":{"id":"cs_wallet_delayed","client_reference_id":%q,"mode":"payment","payment_status":"paid","amount_total":600,"currency":"usd","metadata":{"order_type":"wallet","trade_no":%q}}}}`, pending.TradeNo, pending.TradeNo)
	require.Equal(t, http.StatusOK, send(asyncSuccess).Code)
	require.NoError(t, db.First(&gotUser, user.Id).Error)
	require.NoError(t, db.First(&gotOrder, pending.Id).Error)
	assert.Equal(t, 600*quotamath.QuotaPerUnit, gotUser.Quota)
	assert.Equal(t, billingsvc.TopUpStatusSuccess, gotOrder.Status)

	failed, err := billingsvc.CreateBoundStripeTopUpWithTradeNo(user.Id, 700, 7, "USD", "ref_stripe-failed-order")
	require.NoError(t, err)
	require.NoError(t, billingsvc.BindStripeTopUpSession(failed.TradeNo, "cs_wallet_failed"))
	asyncFailure := fmt.Sprintf(`{"id":"evt_async_fail","type":"checkout.session.async_payment_failed","data":{"object":{"id":"cs_wallet_failed","client_reference_id":%q,"mode":"payment","amount_total":700,"currency":"usd","metadata":{"order_type":"wallet","trade_no":%q}}}}`, failed.TradeNo, failed.TradeNo)
	require.Equal(t, http.StatusOK, send(asyncFailure).Code)
	gotOrder = model.TopUp{}
	require.NoError(t, db.First(&gotOrder, failed.Id).Error)
	assert.Equal(t, billingsvc.TopUpStatusFailed, gotOrder.Status)
	require.NoError(t, db.First(&gotUser, user.Id).Error)
	assert.Equal(t, 600*quotamath.QuotaPerUnit, gotUser.Quota, "a failed delayed payment must not grant quota")
}

func TestStripeWebhookRequiresDisjointTypeModeAndSessionBindings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	dsn := "file:" + filepath.Join(t.TempDir(), "stripe-routing.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.SubscriptionOrder{}, &model.Log{}, &model.AuditLogOutbox{}))
	model.DB = db
	model.LOG_DB = db
	user := model.User{Username: "routing-payer", Password: "x", Role: 1, Status: 1, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	r := router.SetUpRouter()
	send := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(payload))
		req.Header.Set("Stripe-Signature", signStripe([]byte(payload), "whsec_test", currentStripeTimestamp()))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	order, err := billingsvc.CreateBoundStripeTopUpWithTradeNo(user.Id, 100, 1, "USD", "ref_routing_wallet")
	require.NoError(t, err)
	require.NoError(t, billingsvc.BindStripeTopUpSession(order.TradeNo, "cs_routing_wallet"))
	wrong := fmt.Sprintf(`{"id":"evt_wrong_route","type":"checkout.session.completed","data":{"object":{"id":"cs_routing_wallet","client_reference_id":%q,"mode":"subscription","status":"complete","payment_status":"paid","amount_total":100,"currency":"usd","metadata":{"order_type":"subscription"}}}}`, order.TradeNo)
	require.Equal(t, http.StatusServiceUnavailable, send(wrong).Code)
	var stored model.TopUp
	require.NoError(t, db.First(&stored, order.Id).Error)
	assert.Equal(t, billingsvc.TopUpStatusPending, stored.Status)
	assert.Equal(t, billingsvc.StripeReconciliationBindingMismatch, stored.ReconciliationState)

	legacy, err := billingsvc.CreateStripeTopUpWithTradeNo(user.Id, 50, .5, "USD", "ref_legacy_webhook_pending")
	require.NoError(t, err)
	legacyEvent := fmt.Sprintf(`{"id":"evt_legacy","type":"checkout.session.completed","data":{"object":{"id":"cs_legacy","client_reference_id":%q,"mode":"payment","status":"complete","payment_status":"paid","amount_total":50,"currency":"usd","metadata":{"order_type":"wallet"}}}}`, legacy.TradeNo)
	require.Equal(t, http.StatusServiceUnavailable, send(legacyEvent).Code)
	stored = model.TopUp{}
	require.NoError(t, db.First(&stored, legacy.Id).Error)
	assert.Equal(t, billingsvc.StripeReconciliationLegacyBinding, stored.ReconciliationState)

	require.NoError(t, db.Model(&model.TopUp{}).Where("id = ?", legacy.Id).Update("status", billingsvc.TopUpStatusSuccess).Error)
	legacyDuplicate := fmt.Sprintf(`{"id":"evt_legacy_duplicate","type":"checkout.session.completed","data":{"object":{"client_reference_id":%q,"status":"complete","payment_status":"paid","metadata":{}}}}`, legacy.TradeNo)
	require.Equal(t, http.StatusOK, send(legacyDuplicate).Code)
}
