package controller_test

import (
	"encoding/json"
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

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
)

// setupSubscriptionAdminTest prepares a DB, group ratios, a signed-in admin,
// and returns a cookie-authed request factory.
func setupSubscriptionAdminTest(t *testing.T) func(method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// The critical rate-limit counter store is process-global and keyed by
	// client IP; raise the limit before the router builds its limiters so
	// shared counters from earlier tests cannot trip this suite's endpoints.
	t.Setenv("CRITICAL_RATE_LIMIT", "1000")
	dsn := "file:" + filepath.Join(t.TempDir(), "subadmin.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Option{}, &model.UserSession{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionOrder{}, &model.Log{}))
	model.DB = db
	model.LOG_DB = db
	service.SetGroupRatios(map[string]float64{"default": 1.0, "vip": 2.0})

	admin := model.User{Username: "subadmin", Password: "pw", Role: constant.RoleAdminUser,
		Status: model.UserStatusEnabled, Group: "default", AuthVersion: 1}
	require.NoError(t, model.DB.Create(&admin).Error)
	sid, access, refresh, err := service.CompleteLogin(&admin, "127.0.0.1", "ua", "test")
	require.NoError(t, err)

	r := router.SetUpRouter()
	return func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: sid + "." + refresh})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
}

func subDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body: %s", rec.Body.String())
	return body
}

func subExpectFail(t *testing.T, rec *httptest.ResponseRecorder, message string) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	body := subDecode(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, message, body["message"])
}

func subExpectOK(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := subDecode(t, rec)
	require.Equal(t, true, body["success"])
	return body
}

func TestAdminSubscriptionPlanCRUD(t *testing.T) {
	do := setupSubscriptionAdminTest(t)

	// Payment-adjacent admin operations are gated on the compliance statement.
	setPaymentCompliance(t, false)
	rec := do(http.MethodPost, "/api/subscription/admin/plans", `{"plan":{"title":"Pro"}}`)
	subExpectFail(t, rec, "支付、兑换码、订阅计划和邀请返利功能已禁用。管理员需先确认合规声明后方可启用。")
	setPaymentCompliance(t, true)

	// Validation contract (message per rejected field).
	for body, msg := range map[string]string{
		`{"plan":{"title":"  "}}`:                              "套餐标题不能为空",
		`{"plan":{"title":"P","price_amount":"-1"}}`:           "价格不能为负数",
		`{"plan":{"title":"P","price_amount":"10000"}}`:        "价格不能超过9999",
		`{"plan":{"title":"P","max_purchase_per_user":-1}}`:    "购买上限不能为负数",
		`{"plan":{"title":"P","total_amount":-1}}`:             "总额度不能为负数",
		`{"plan":{"title":"P","upgrade_group":"nope"}}`:        "升级分组不存在",
		`{"plan":{"title":"P","downgrade_group":"nope"}}`:      "降级分组不存在",
		`{"plan":{"title":"P","quota_reset_period":"custom"}}`: "自定义重置周期需大于0秒",
		`{"plan":{"title":"P","price_amount":"not-a-number"}}`: "参数错误",
	} {
		subExpectFail(t, do(http.MethodPost, "/api/subscription/admin/plans", body), msg)
	}

	// Create normalizes currency/duration/flags.
	body := subExpectOK(t, do(http.MethodPost, "/api/subscription/admin/plans",
		`{"plan":{"title":"Pro","subtitle":"best","price_amount":"9.99","currency":"CNY","total_amount":1000000,"upgrade_group":"vip","quota_reset_period":"monthly","sort_order":5,"enabled":true}}`))
	plan := body["data"].(map[string]any)
	assert.Equal(t, "USD", plan["currency"])
	assert.Equal(t, "month", plan["duration_unit"])
	assert.Equal(t, float64(1), plan["duration_value"])
	assert.Equal(t, true, plan["allow_balance_pay"])
	assert.Equal(t, true, plan["allow_wallet_overflow"])
	planId := int(plan["id"].(float64))
	require.Greater(t, planId, 0)

	// Unknown reset periods normalize to "never".
	body = subExpectOK(t, do(http.MethodPost, "/api/subscription/admin/plans",
		`{"plan":{"title":"Lite","quota_reset_period":"sometimes","sort_order":1,"enabled":false}}`))
	litePlan := body["data"].(map[string]any)
	assert.Equal(t, "never", litePlan["quota_reset_period"])
	liteId := int(litePlan["id"].(float64))

	// Admin list returns every plan (disabled included) wrapped as {"plan":...},
	// ordered by sort_order desc then id desc.
	body = subExpectOK(t, do(http.MethodGet, "/api/subscription/admin/plans", ""))
	items := body["data"].([]any)
	require.Len(t, items, 2)
	first := items[0].(map[string]any)["plan"].(map[string]any)
	second := items[1].(map[string]any)["plan"].(map[string]any)
	assert.Equal(t, "Pro", first["title"])
	assert.Equal(t, "Lite", second["title"])

	// Full update persists zero values through the map update; nil flag
	// pointers leave the stored flags untouched.
	subExpectFail(t, do(http.MethodPut, "/api/subscription/admin/plans/0", `{"plan":{"title":"x"}}`), "无效的ID")
	subExpectOK(t, do(http.MethodPut, fmt.Sprintf("/api/subscription/admin/plans/%d", planId),
		`{"plan":{"title":"Pro2","sort_order":0,"enabled":false,"allow_balance_pay":false,"total_amount":500}}`))
	var stored model.SubscriptionPlan
	require.NoError(t, model.DB.First(&stored, planId).Error)
	assert.Equal(t, "Pro2", stored.Title)
	assert.Equal(t, 0, stored.SortOrder)
	assert.False(t, stored.Enabled)
	assert.Equal(t, int64(500), stored.TotalAmount)
	require.NotNil(t, stored.AllowBalancePay)
	assert.False(t, *stored.AllowBalancePay)

	// PATCH toggles enabled only; a missing flag is a parameter error.
	subExpectFail(t, do(http.MethodPatch, fmt.Sprintf("/api/subscription/admin/plans/%d", planId), `{}`), "参数错误")
	subExpectOK(t, do(http.MethodPatch, fmt.Sprintf("/api/subscription/admin/plans/%d", planId), `{"enabled":true}`))
	require.NoError(t, model.DB.First(&stored, planId).Error)
	assert.True(t, stored.Enabled)
	_ = liteId
}

func TestAdminSubscriptionBindResetInvalidateDelete(t *testing.T) {
	do := setupSubscriptionAdminTest(t)
	setPaymentCompliance(t, true)

	// Plan granting the vip group, capped at 2 purchases per user.
	body := subExpectOK(t, do(http.MethodPost, "/api/subscription/admin/plans",
		`{"plan":{"title":"Vip套餐","total_amount":1000,"upgrade_group":"vip","max_purchase_per_user":2,"quota_reset_period":"monthly","enabled":true}}`))
	planId := int(body["data"].(map[string]any)["id"].(float64))
	body = subExpectOK(t, do(http.MethodPost, "/api/subscription/admin/plans",
		`{"plan":{"title":"Empty","total_amount":10,"enabled":true}}`))
	emptyPlanId := int(body["data"].(map[string]any)["id"].(float64))

	u1 := model.User{Username: "sub1", Password: "pw", Role: 1, Status: model.UserStatusEnabled, Group: "default", AuthVersion: 1}
	u2 := model.User{Username: "sub2", Password: "pw", Role: 1, Status: model.UserStatusEnabled, Group: "default", AuthVersion: 1}
	require.NoError(t, model.DB.Create(&u1).Error)
	require.NoError(t, model.DB.Create(&u2).Error)

	// Bind upgrades the group and snapshots the previous one.
	subExpectFail(t, do(http.MethodPost, "/api/subscription/admin/bind", `{"user_id":0,"plan_id":1}`), "参数错误")
	rec := do(http.MethodPost, "/api/subscription/admin/bind", fmt.Sprintf(`{"user_id":99999,"plan_id":%d}`, planId))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	body = subExpectOK(t, do(http.MethodPost, "/api/subscription/admin/bind", fmt.Sprintf(`{"user_id":%d,"plan_id":%d}`, u1.Id, planId)))
	assert.Equal(t, "用户分组将升级到 vip", body["data"].(map[string]any)["message"])
	var u1Row model.User
	require.NoError(t, model.DB.First(&u1Row, u1.Id).Error)
	assert.Equal(t, "vip", u1Row.Group)
	var sub1 model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ?", u1.Id).First(&sub1).Error)
	assert.Equal(t, "admin", sub1.Source)
	assert.Equal(t, "default", sub1.PrevUserGroup)
	assert.Equal(t, "active", sub1.Status)
	assert.Equal(t, int64(1000), sub1.AmountTotal)
	assert.Greater(t, sub1.NextResetTime, int64(0))
	assert.Greater(t, sub1.LastResetTime, int64(0))

	// Second bind: group already vip, no message; third hits the purchase cap.
	body = subExpectOK(t, do(http.MethodPost, "/api/subscription/admin/bind", fmt.Sprintf(`{"user_id":%d,"plan_id":%d}`, u1.Id, planId)))
	assert.Nil(t, body["data"])
	subExpectFail(t, do(http.MethodPost, "/api/subscription/admin/bind", fmt.Sprintf(`{"user_id":%d,"plan_id":%d}`, u1.Id, planId)), "已达到该套餐购买上限")

	// Create-user-subscription is the bind with the user in the path.
	body = subExpectOK(t, do(http.MethodPost, fmt.Sprintf("/api/subscription/admin/users/%d/subscriptions", u2.Id), fmt.Sprintf(`{"plan_id":%d}`, planId)))
	assert.Equal(t, "用户分组将升级到 vip", body["data"].(map[string]any)["message"])

	// Listing returns every subscription wrapped as {"subscription":...},
	// newest first.
	subExpectFail(t, do(http.MethodGet, "/api/subscription/admin/users/0/subscriptions", ""), "无效的用户ID")
	body = subExpectOK(t, do(http.MethodGet, fmt.Sprintf("/api/subscription/admin/users/%d/subscriptions", u1.Id), ""))
	subs := body["data"].([]any)
	require.Len(t, subs, 2)
	firstSub := subs[0].(map[string]any)["subscription"].(map[string]any)
	secondSub := subs[1].(map[string]any)["subscription"].(map[string]any)
	assert.Greater(t, firstSub["id"].(float64), secondSub["id"].(float64))

	// Reset for one user: zeroes usage, reports counts, records manage logs.
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", u1.Id).Update("amount_used", 500).Error)
	subExpectFail(t, do(http.MethodPost, fmt.Sprintf("/api/subscription/admin/users/%d/subscriptions/reset", u1.Id), `{}`), "参数错误")
	subExpectFail(t, do(http.MethodPost, fmt.Sprintf("/api/subscription/admin/users/%d/subscriptions/reset", u2.Id), fmt.Sprintf(`{"plan_id":%d}`, emptyPlanId)), "该用户没有有效的此套餐订阅")
	body = subExpectOK(t, do(http.MethodPost, fmt.Sprintf("/api/subscription/admin/users/%d/subscriptions/reset", u1.Id), fmt.Sprintf(`{"plan_id":%d}`, planId)))
	result := body["data"].(map[string]any)
	assert.Equal(t, float64(planId), result["plan_id"])
	assert.Equal(t, float64(2), result["matched_count"])
	assert.Equal(t, float64(2), result["reset_count"])
	assert.Equal(t, float64(1), result["user_count"])
	assert.Equal(t, true, result["advance_reset_time"])
	var used int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", u1.Id).Select("sum(amount_used)").Scan(&used).Error)
	assert.Equal(t, int64(0), used)
	var logCount int64
	require.NoError(t, model.DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ? AND content LIKE ?", u1.Id, service.LogTypeManage, "管理员重置订阅套餐%").
		Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)

	// advance_reset_time=false keeps the stored reset schedule untouched.
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", sub1.Id).
		Updates(map[string]any{"amount_used": 100, "next_reset_time": 12345, "last_reset_time": 67890}).Error)
	subExpectOK(t, do(http.MethodPost, fmt.Sprintf("/api/subscription/admin/users/%d/subscriptions/reset", u1.Id), fmt.Sprintf(`{"plan_id":%d,"advance_reset_time":false}`, planId)))
	var frozen model.UserSubscription
	require.NoError(t, model.DB.First(&frozen, sub1.Id).Error)
	assert.Equal(t, int64(0), frozen.AmountUsed)
	assert.Equal(t, int64(12345), frozen.NextResetTime)
	assert.Equal(t, int64(67890), frozen.LastResetTime)

	// Plan-wide reset covers all users; a plan with no subscriptions reports
	// zero counts without failing.
	body = subExpectOK(t, do(http.MethodPost, fmt.Sprintf("/api/subscription/admin/plans/%d/subscriptions/reset", planId), `{}`))
	result = body["data"].(map[string]any)
	assert.Equal(t, float64(3), result["matched_count"])
	assert.Equal(t, float64(2), result["user_count"])
	body = subExpectOK(t, do(http.MethodPost, fmt.Sprintf("/api/subscription/admin/plans/%d/subscriptions/reset", emptyPlanId), `{}`))
	result = body["data"].(map[string]any)
	assert.Equal(t, float64(0), result["matched_count"])
	assert.Equal(t, float64(0), result["user_count"])

	// Invalidate cancels immediately and reverts the group (u2 has a single
	// upgraded subscription).
	subExpectFail(t, do(http.MethodPost, "/api/subscription/admin/user_subscriptions/0/invalidate", ""), "无效的订阅ID")
	var u2Sub model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ?", u2.Id).First(&u2Sub).Error)
	body = subExpectOK(t, do(http.MethodPost, fmt.Sprintf("/api/subscription/admin/user_subscriptions/%d/invalidate", u2Sub.Id), ""))
	assert.Equal(t, "用户分组将回退到 default", body["data"].(map[string]any)["message"])
	require.NoError(t, model.DB.First(&u2Sub, u2Sub.Id).Error)
	assert.Equal(t, "cancelled", u2Sub.Status)
	var u2Row model.User
	require.NoError(t, model.DB.First(&u2Row, u2.Id).Error)
	assert.Equal(t, "default", u2Row.Group)

	// Delete: while another active upgraded subscription remains the group is
	// kept; deleting the one holding the group snapshot last reverts it.
	var u1Subs []model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ?", u1.Id).Order("id asc").Find(&u1Subs).Error)
	require.Len(t, u1Subs, 2)
	body = subExpectOK(t, do(http.MethodDelete, fmt.Sprintf("/api/subscription/admin/user_subscriptions/%d", u1Subs[1].Id), ""))
	assert.Nil(t, body["data"])
	require.NoError(t, model.DB.First(&u1Row, u1.Id).Error)
	assert.Equal(t, "vip", u1Row.Group)
	body = subExpectOK(t, do(http.MethodDelete, fmt.Sprintf("/api/subscription/admin/user_subscriptions/%d", u1Subs[0].Id), ""))
	assert.Equal(t, "用户分组将回退到 default", body["data"].(map[string]any)["message"])
	var remaining int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", u1.Id).Count(&remaining).Error)
	assert.Equal(t, int64(0), remaining)

	// The whole admin surface requires AdminAuth: an anonymous request is
	// rejected before reaching the handlers.
	anon := httptest.NewRequest(http.MethodGet, "/api/subscription/admin/plans", nil)
	anonRec := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(anonRec, anon)
	assert.Equal(t, http.StatusUnauthorized, anonRec.Code)
}
