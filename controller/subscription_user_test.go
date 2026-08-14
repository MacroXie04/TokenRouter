package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
)

// subTestUser loads the signed-in account created by setupSubscriptionAdminTest.
func subTestUser(t *testing.T) *model.User {
	t.Helper()
	var u model.User
	require.NoError(t, model.DB.Where("username = ?", "subadmin").First(&u).Error)
	return &u
}

func seedUserPlan(t *testing.T, mutate func(*model.SubscriptionPlan)) *model.SubscriptionPlan {
	t.Helper()
	p := &model.SubscriptionPlan{
		Title:         "Starter",
		PriceAmount:   "9.99",
		Enabled:       true,
		TotalAmount:   1000,
		DurationUnit:  "month",
		DurationValue: 1,
	}
	if mutate != nil {
		mutate(p)
	}
	require.NoError(t, model.DB.Create(p).Error)
	return p
}

// TestUserSubscriptionPlans covers GET /api/subscription/plans: 401 anonymous,
// empty list under the compliance gate, and the {plan: ...} DTO list (enabled
// only, sort_order desc) once compliance is confirmed.
func TestUserSubscriptionPlans(t *testing.T) {
	do := setupSubscriptionAdminTest(t)

	// Anonymous requests are rejected (UserAuth group).
	anon := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(anon,
		httptest.NewRequest(http.MethodGet, "/api/subscription/plans", nil))
	assert.Equal(t, http.StatusUnauthorized, anon.Code)
	rec := anon

	seedUserPlan(t, func(p *model.SubscriptionPlan) { p.Title = "low"; p.SortOrder = 1 })
	seedUserPlan(t, func(p *model.SubscriptionPlan) { p.Title = "high"; p.SortOrder = 9 })
	seedUserPlan(t, func(p *model.SubscriptionPlan) { p.Title = "off"; p.Enabled = false })

	// Compliance unconfirmed: storefront shows an empty list, not an error.
	setPaymentCompliance(t, false)
	rec = do(http.MethodGet, "/api/subscription/plans", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := subDecode(t, rec)
	assert.Equal(t, true, body["success"])
	require.NotNil(t, body["data"])
	assert.Empty(t, body["data"])

	setPaymentCompliance(t, true)
	rec = do(http.MethodGet, "/api/subscription/plans", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body = subDecode(t, rec)
	items, ok := body["data"].([]any)
	require.True(t, ok, "data: %v", body["data"])
	require.Len(t, items, 2, "disabled plans excluded")
	first, ok := items[0].(map[string]any)["plan"].(map[string]any)
	require.True(t, ok, "rows are wrapped as {plan: ...}")
	assert.Equal(t, "high", first["title"])
	second := items[1].(map[string]any)["plan"].(map[string]any)
	assert.Equal(t, "low", second["title"])
	assert.Equal(t, true, first["allow_balance_pay"], "defaults normalized")
}

// TestSubscriptionBalancePay covers POST /api/subscription/balance/pay: the
// compliance gate, parameter validation, balance checks, and a funded purchase
// with its order/subscription/log side effects.
func TestSubscriptionBalancePay(t *testing.T) {
	do := setupSubscriptionAdminTest(t)
	user := subTestUser(t)
	plan := seedUserPlan(t, nil) // $9.99 => 4,995,000 quota

	setPaymentCompliance(t, false)
	rec := do(http.MethodPost, "/api/subscription/balance/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	subExpectFail(t, rec, service.ErrPaymentComplianceRequired.Error())

	setPaymentCompliance(t, true)

	// Parameter validation.
	subExpectFail(t, do(http.MethodPost, "/api/subscription/balance/pay", `{"plan_id":0}`), "参数错误")
	subExpectFail(t, do(http.MethodPost, "/api/subscription/balance/pay", `not json`), "参数错误")

	// Unknown plan id.
	rec = do(http.MethodPost, "/api/subscription/balance/pay", `{"plan_id":99999}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// Insufficient balance.
	subExpectFail(t, do(http.MethodPost, "/api/subscription/balance/pay",
		fmt.Sprintf(`{"plan_id":%d}`, plan.Id)), "余额不足")

	// Fund and purchase.
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).
		Update("quota", 5000000).Error)
	rec = do(http.MethodPost, "/api/subscription/balance/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := subDecode(t, rec)
	assert.Equal(t, true, body["success"])
	_, hasData := body["data"]
	assert.False(t, hasData, "success payload is empty")

	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, 5000, got.Quota, "9.99 * 500000 = 4,995,000 deducted")

	var sub model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ?", user.Id).First(&sub).Error)
	assert.Equal(t, "balance", sub.Source)
	assert.Equal(t, int64(1000), sub.AmountTotal)

	var order model.SubscriptionOrder
	require.NoError(t, model.DB.Where("user_id = ?", user.Id).First(&order).Error)
	assert.Equal(t, "balance", order.PaymentMethod)
	assert.Equal(t, "success", order.Status)
	assert.True(t, strings.HasPrefix(order.TradeNo, fmt.Sprintf("SUBBALUSR%dNO", user.Id)),
		"trade no: %s", order.TradeNo)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", user.Id, service.LogTypeTopup).
		First(&log).Error)
	assert.Contains(t, log.Content, "使用余额购买订阅成功")

	// The purchased subscription is visible on the self endpoint, wrapped in
	// the reference self-view shape.
	rec = do(http.MethodGet, "/api/subscription/self", "")
	require.Equal(t, http.StatusOK, rec.Code)
	body = subDecode(t, rec)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "body: %s", rec.Body.String())
	assert.Equal(t, "subscription_first", data["billing_preference"])
	subsList, ok := data["subscriptions"].([]any)
	require.True(t, ok, "subscriptions: %v", data["subscriptions"])
	require.Len(t, subsList, 1)
	subRow, ok := subsList[0].(map[string]any)["subscription"].(map[string]any)
	require.True(t, ok, "rows are wrapped as {subscription: ...}")
	assert.Equal(t, "balance", subRow["source"])
	allList, ok := data["all_subscriptions"].([]any)
	require.True(t, ok, "all_subscriptions: %v", data["all_subscriptions"])
	assert.Len(t, allList, 1)
}

// TestSubscriptionSelfView covers GET /api/subscription/self: 401 anonymous,
// the empty-state contract (default preference + empty arrays, never null),
// and active-vs-all list membership.
func TestSubscriptionSelfView(t *testing.T) {
	do := setupSubscriptionAdminTest(t)
	user := subTestUser(t)

	anon := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(anon,
		httptest.NewRequest(http.MethodGet, "/api/subscription/self", nil))
	assert.Equal(t, http.StatusUnauthorized, anon.Code)

	// Empty state: default preference, empty (non-null) arrays.
	rec := do(http.MethodGet, "/api/subscription/self", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	data, ok := subDecode(t, rec)["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "subscription_first", data["billing_preference"])
	subs, ok := data["subscriptions"].([]any)
	require.True(t, ok, "subscriptions must be an array, got %v", data["subscriptions"])
	assert.Empty(t, subs)
	all, ok := data["all_subscriptions"].([]any)
	require.True(t, ok, "all_subscriptions must be an array, got %v", data["all_subscriptions"])
	assert.Empty(t, all)

	// One live subscription and one expired: only the live one is "active",
	// both appear in all_subscriptions.
	now := time.Now().Unix()
	plan := seedUserPlan(t, nil)
	live := model.UserSubscription{UserId: user.Id, PlanId: plan.Id, AmountTotal: 1000,
		StartTime: now - 3600, EndTime: now + 86400, Status: service.SubscriptionStatusActive}
	require.NoError(t, model.DB.Create(&live).Error)
	expired := model.UserSubscription{UserId: user.Id, PlanId: plan.Id, AmountTotal: 1000,
		StartTime: now - 7200, EndTime: now - 3600, Status: service.SubscriptionStatusActive}
	require.NoError(t, model.DB.Create(&expired).Error)

	rec = do(http.MethodGet, "/api/subscription/self", "")
	require.Equal(t, http.StatusOK, rec.Code)
	data = subDecode(t, rec)["data"].(map[string]any)
	subs = data["subscriptions"].([]any)
	require.Len(t, subs, 1)
	row, ok := subs[0].(map[string]any)["subscription"].(map[string]any)
	require.True(t, ok, "rows are wrapped as {subscription: ...}")
	assert.Equal(t, float64(live.Id), row["id"])
	all = data["all_subscriptions"].([]any)
	assert.Len(t, all, 2)
}

// TestSubscriptionPreferenceUpdate covers PUT /api/subscription/self/preference:
// 401 anonymous, malformed JSON rejection, silent normalization of unknown
// values, and persistence visible on the self view.
func TestSubscriptionPreferenceUpdate(t *testing.T) {
	do := setupSubscriptionAdminTest(t)

	anon := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(anon,
		httptest.NewRequest(http.MethodPut, "/api/subscription/self/preference",
			strings.NewReader(`{"billing_preference":"wallet_only"}`)))
	assert.Equal(t, http.StatusUnauthorized, anon.Code)

	subExpectFail(t, do(http.MethodPut, "/api/subscription/self/preference", `not json`), "参数错误")

	rec := do(http.MethodPut, "/api/subscription/self/preference", `{"billing_preference":"wallet_only"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	data, ok := subDecode(t, rec)["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "wallet_only", data["billing_preference"])

	// Persisted: visible on the self view.
	rec = do(http.MethodGet, "/api/subscription/self", "")
	require.Equal(t, http.StatusOK, rec.Code)
	data = subDecode(t, rec)["data"].(map[string]any)
	assert.Equal(t, "wallet_only", data["billing_preference"])

	// Unknown values normalize to the default silently instead of erroring.
	rec = do(http.MethodPut, "/api/subscription/self/preference", `{"billing_preference":"bogus"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	data = subDecode(t, rec)["data"].(map[string]any)
	assert.Equal(t, "subscription_first", data["billing_preference"])
}
