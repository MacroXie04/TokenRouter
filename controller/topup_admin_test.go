package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

func TestAdminTopUpRoutesReplaceConflictingUserAlias(t *testing.T) {
	handler, do, userID := setupChannelRead(t, constant.RoleCommonUser)
	_, err := service.CreateTopUpWithTradeNo(userID, 10, 1, "alipay", service.PaymentProviderEpay, "self-order")
	require.NoError(t, err)

	// The explicit self route remains available to an ordinary user.
	self := do(http.MethodGet, "/api/user/topup/self", "")
	require.Equal(t, http.StatusOK, self.Code, self.Body.String())
	assert.Equal(t, true, decodeBody(t, self)["success"])

	// GET /api/user/topup is now the reference admin route, not a second user
	// alias. Manual completion is protected by the same role boundary.
	adminList := do(http.MethodGet, "/api/user/topup", "")
	assert.Equal(t, http.StatusForbidden, adminList.Code)
	complete := do(http.MethodPost, "/api/user/topup/complete", `{"trade_no":"self-order"}`)
	assert.Equal(t, http.StatusForbidden, complete.Code)

	anonymousRequest := httptest.NewRequest(http.MethodGet, "/api/user/topup", nil)
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, anonymousRequest)
	assert.Equal(t, http.StatusUnauthorized, anonymous.Code)
}

func TestSelfTopUpListPaginationSearchAndOwnershipContract(t *testing.T) {
	_, do, userID := setupChannelRead(t, constant.RoleCommonUser)
	for _, tradeNo := range []string{"self-alpha", "self-beta", "self-gamma"} {
		_, err := service.CreateTopUpWithTradeNo(userID, 10, 1, "alipay", service.PaymentProviderEpay, tradeNo)
		require.NoError(t, err)
	}
	_, err := service.CreateTopUpWithTradeNo(userID+1000, 10, 1, "alipay", service.PaymentProviderEpay, "other-owner")
	require.NoError(t, err)

	rec := do(http.MethodGet, "/api/user/topup/self?p=1&page_size=2", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	page := decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, float64(1), page["page"])
	assert.Equal(t, float64(2), page["page_size"])
	assert.Equal(t, float64(3), page["total"])
	items := page["items"].([]any)
	require.Len(t, items, 2)
	assert.Equal(t, "self-gamma", items[0].(map[string]any)["trade_no"])
	for _, item := range items {
		assert.Equal(t, float64(userID), item.(map[string]any)["user_id"])
		assert.NotContains(t, item.(map[string]any), "checkout_request")
		assert.NotContains(t, item.(map[string]any), "provider_session_id")
	}

	rec = do(http.MethodGet, "/api/user/topup/self?keyword=%25alpha%25", "")
	page = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, float64(1), page["total"])
	require.Len(t, page["items"].([]any), 1)
	assert.Equal(t, "self-alpha", page["items"].([]any)[0].(map[string]any)["trade_no"])

	rec = do(http.MethodGet, "/api/user/topup/self?keyword=%25x%25", "")
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "查询参数无效")
}

func TestAdminTopUpListSearchAndPaginationContract(t *testing.T) {
	_, do, adminID := setupChannelRead(t, constant.RoleAdminUser)
	for index, tradeNo := range []string{"alpha", "prefix-alpha-suffix", "beta_1"} {
		_, err := service.CreateTopUpWithTradeNo(adminID, int64(index+1), 1, "alipay", service.PaymentProviderEpay, tradeNo)
		require.NoError(t, err)
	}

	rec := do(http.MethodGet, "/api/user/topup?p=1&page_size=2", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	page := body["data"].(map[string]any)
	assert.Equal(t, float64(1), page["page"])
	assert.Equal(t, float64(2), page["page_size"])
	assert.Equal(t, float64(3), page["total"])
	items := page["items"].([]any)
	require.Len(t, items, 2)
	assert.Equal(t, "beta_1", items[0].(map[string]any)["trade_no"])

	rec = do(http.MethodGet, "/api/user/topup?p=2&page_size=2", "")
	page = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, float64(3), page["total"])
	require.Len(t, page["items"].([]any), 1)
	assert.Equal(t, "alpha", page["items"].([]any)[0].(map[string]any)["trade_no"])

	rec = do(http.MethodGet, "/api/user/topup?keyword=alpha", "")
	page = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, float64(1), page["total"])
	assert.Equal(t, "alpha", page["items"].([]any)[0].(map[string]any)["trade_no"])

	rec = do(http.MethodGet, "/api/user/topup?keyword=%25alpha%25", "")
	page = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, float64(2), page["total"])

	rec = do(http.MethodGet, "/api/user/topup?keyword=%25a%25", "")
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "查询参数无效")
}

func TestAdminCompleteTopUpContract(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleAdminUser)
	setPaymentCompliance(t, false)
	owner := model.User{Username: "manual-topup-owner", Password: "pw", Status: model.UserStatusEnabled, Role: constant.RoleCommonUser}
	require.NoError(t, model.DB.Create(&owner).Error)
	order, err := service.CreateTopUpWithTradeNo(owner.Id, 100, 1, "alipay", service.PaymentProviderEpay, "admin-manual")
	require.NoError(t, err)

	var rec *httptest.ResponseRecorder
	for _, requestBody := range []string{"{}", `{"trade_no":""}`, fmt.Sprintf(`{"trade_no":%q}`, strings.Repeat("x", 256))} {
		rec = do(http.MethodPost, "/api/user/topup/complete", requestBody)
		assert.Equal(t, false, decodeBody(t, rec)["success"])
	}
	rec = do(http.MethodPost, "/api/user/topup/complete", `{"trade_no":"missing"}`)
	assert.Equal(t, service.ErrTopUpNotFound.Error(), decodeBody(t, rec)["message"])

	failed, err := service.CreateTopUpWithTradeNo(owner.Id, 10, 1, "alipay", service.PaymentProviderEpay, "admin-failed")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("id = ?", failed.Id).Update("status", service.TopUpStatusFailed).Error)
	rec = do(http.MethodPost, "/api/user/topup/complete", `{"trade_no":"admin-failed"}`)
	assert.Equal(t, service.ErrTopUpStatusInvalid.Error(), decodeBody(t, rec)["message"])

	oversized := model.TopUp{UserId: owner.Id, Amount: common.MaxQuota + 1, Money: 1, TradeNo: "admin-oversized", Status: service.TopUpStatusPending}
	require.NoError(t, model.DB.Create(&oversized).Error)
	rec = do(http.MethodPost, "/api/user/topup/complete", `{"trade_no":"admin-oversized"}`)
	assert.Equal(t, service.ErrTopUpAmountMismatch.Error(), decodeBody(t, rec)["message"])

	rec = do(http.MethodPost, "/api/user/topup/complete", `{"trade_no":"admin-manual"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	var ownerAfter model.User
	require.NoError(t, model.DB.First(&ownerAfter, owner.Id).Error)
	assert.Equal(t, 100*common.QuotaPerUnit, ownerAfter.Quota,
		"an existing order remains recoverable while new-payment compliance is disabled")

	// Duplicate completion is an idempotent success and never double-credits.
	rec = do(http.MethodPost, "/api/user/topup/complete", `{"trade_no":"admin-manual"}`)
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	require.NoError(t, model.DB.First(&ownerAfter, owner.Id).Error)
	assert.Equal(t, 100*common.QuotaPerUnit, ownerAfter.Quota)
	var topupLogs int64
	require.NoError(t, model.DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ? AND content = ?", owner.Id, service.LogTypeTopup, order.TradeNo).
		Count(&topupLogs).Error)
	assert.EqualValues(t, 1, topupLogs)
}
