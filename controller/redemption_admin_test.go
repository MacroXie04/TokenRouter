package controller_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

func createRedemptionRow(t *testing.T, name string, status int, quota, expiredTime int64) model.Redemption {
	t.Helper()
	r := model.Redemption{
		UserId: 1, Key: common.BestEffortUUID(), Status: status, Name: name,
		Quota: int(quota), CreatedTime: common.NowTimestamp(), ExpiredTime: expiredTime,
	}
	require.NoError(t, model.DB.Create(&r).Error)
	return r
}

// TestRedemptionStatusCodesMatchReference guards the data-parity fix: the
// reference numbering is enabled=1, disabled=2, used=3.
func TestRedemptionStatusCodesMatchReference(t *testing.T) {
	assert.Equal(t, 1, service.RedemptionStatusEnabled)
	assert.Equal(t, 2, service.RedemptionStatusDisabled)
	assert.Equal(t, 3, service.RedemptionStatusUsed)
}

func TestRedemptionCreateContract(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)

	// Compliance gate.
	setPaymentCompliance(t, false)
	rec := do(http.MethodPost, "/api/redemption", `{"name":"x","count":1,"quota":10}`)
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, service.ErrPaymentComplianceRequired.Error(), body["message"])
	setPaymentCompliance(t, true)

	// Name validation.
	rec = do(http.MethodPost, "/api/redemption", `{"name":"","count":1,"quota":10}`)
	assert.Equal(t, "兑换码名称长度必须在1-20之间", decodeBody(t, rec)["message"])
	rec = do(http.MethodPost, "/api/redemption", fmt.Sprintf(`{"name":%q,"count":1,"quota":10}`, "一二三四五六七八九十一二三四五六七八九十X"))
	assert.Equal(t, "兑换码名称长度必须在1-20之间", decodeBody(t, rec)["message"])

	// Count validation.
	rec = do(http.MethodPost, "/api/redemption", `{"name":"x","count":0,"quota":10}`)
	assert.Equal(t, "兑换码个数必须大于0", decodeBody(t, rec)["message"])
	rec = do(http.MethodPost, "/api/redemption", `{"name":"x","count":101,"quota":10}`)
	assert.Equal(t, "一次兑换码批量生成的个数不能大于 100", decodeBody(t, rec)["message"])

	// Expiry validation.
	rec = do(http.MethodPost, "/api/redemption", fmt.Sprintf(`{"name":"x","count":1,"quota":10,"expired_time":%d}`, common.NowTimestamp()-10))
	assert.Equal(t, "过期时间不能早于当前时间", decodeBody(t, rec)["message"])

	// Success: count rows with UUID keys returned.
	rec = do(http.MethodPost, "/api/redemption", `{"name":"batch-name","count":3,"quota":500}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	keys, ok := body["data"].([]any)
	require.True(t, ok, "data missing: %s", rec.Body.String())
	require.Len(t, keys, 3)
	var rows []model.Redemption
	require.NoError(t, model.DB.Where("name = ?", "batch-name").Find(&rows).Error)
	require.Len(t, rows, 3)
	for i, r := range rows {
		assert.Equal(t, service.RedemptionStatusEnabled, r.Status)
		assert.Equal(t, 500, r.Quota)
		assert.Equal(t, keys[i], r.Key)
		assert.Len(t, r.Key, 32, "UUID keys are 32 chars")
	}
}

func TestRedemptionListAndSearch(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	now := common.NowTimestamp()
	createRedemptionRow(t, "alpha-code", service.RedemptionStatusEnabled, 10, 0)
	used := createRedemptionRow(t, "beta-code", service.RedemptionStatusUsed, 10, 0)
	createRedemptionRow(t, "gamma-code", service.RedemptionStatusDisabled, 10, 0)
	createRedemptionRow(t, "delta-code", service.RedemptionStatusEnabled, 10, now-100)
	createRedemptionRow(t, "epsilon-code", service.RedemptionStatusEnabled, 10, now+1000)

	// Full list with pageInfo shape.
	rec := do(http.MethodGet, "/api/redemption", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "data missing: %s", rec.Body.String())
	assert.Equal(t, float64(5), data["total"])
	assert.Len(t, data["items"].([]any), 5)

	// Paging.
	rec = do(http.MethodGet, "/api/redemption?p=2&page_size=2", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Len(t, data["items"].([]any), 2)
	assert.Equal(t, float64(5), data["total"])

	// Keyword prefix match.
	rec = do(http.MethodGet, "/api/redemption/search?keyword=alpha", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	require.Len(t, data["items"].([]any), 1)
	assert.Equal(t, float64(1), data["total"])

	// Numeric keyword matches the id.
	rec = do(http.MethodGet, "/api/redemption/search?keyword="+common.Int2Str(used.Id), "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	require.Len(t, data["items"].([]any), 1)
	items := data["items"].([]any)
	assert.Equal(t, "beta-code", items[0].(map[string]any)["name"])

	// Status filters (reference numbering).
	rec = do(http.MethodGet, "/api/redemption/search?status=2", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	require.Len(t, data["items"].([]any), 1)
	assert.Equal(t, "gamma-code", data["items"].([]any)[0].(map[string]any)["name"])

	rec = do(http.MethodGet, "/api/redemption/search?status=3", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	require.Len(t, data["items"].([]any), 1)
	assert.Equal(t, "beta-code", data["items"].([]any)[0].(map[string]any)["name"])

	// Enabled = unexpired enabled codes.
	rec = do(http.MethodGet, "/api/redemption/search?status=1", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, float64(2), data["total"], "alpha + epsilon")

	// Expired filter.
	rec = do(http.MethodGet, "/api/redemption/search?status=expired", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, float64(1), data["total"])
	assert.Equal(t, "delta-code", data["items"].([]any)[0].(map[string]any)["name"])
}

func TestRedemptionGetUpdateDelete(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	now := common.NowTimestamp()
	r := createRedemptionRow(t, "upd-code", service.RedemptionStatusEnabled, 10, now+1000)

	// Get by id.
	rec := do(http.MethodGet, "/api/redemption/"+common.Int2Str(r.Id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upd-code", data["name"])

	// Get with a bad id.
	rec = do(http.MethodGet, "/api/redemption/abc", "")
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	rec = do(http.MethodGet, "/api/redemption/0", "")
	assert.Equal(t, "id 为空！", decodeBody(t, rec)["message"])

	// Full update with a past expiry is rejected.
	rec = do(http.MethodPut, "/api/redemption", fmt.Sprintf(`{"id":%d,"name":"n","quota":5,"expired_time":%d}`, r.Id, now-10))
	assert.Equal(t, "过期时间不能早于当前时间", decodeBody(t, rec)["message"])

	// Full update.
	rec = do(http.MethodPut, "/api/redemption", fmt.Sprintf(`{"id":%d,"name":"renamed","quota":77,"expired_time":%d}`, r.Id, now+2000))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	updated, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "renamed", updated["name"])
	assert.Equal(t, float64(77), updated["quota"])

	// status_only applies only the status.
	rec = do(http.MethodPut, "/api/redemption?status_only=1", fmt.Sprintf(`{"id":%d,"name":"other","quota":1,"status":%d}`, r.Id, service.RedemptionStatusDisabled))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	updated = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, float64(service.RedemptionStatusDisabled), updated["status"])
	assert.Equal(t, "renamed", updated["name"], "name untouched in status_only")
	assert.Equal(t, float64(77), updated["quota"])

	// Delete one.
	rec = do(http.MethodDelete, "/api/redemption/"+common.Int2Str(r.Id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	var count int64
	model.DB.Model(&model.Redemption{}).Where("id = ?", r.Id).Count(&count)
	assert.Zero(t, count)
	// Deleting again errors.
	rec = do(http.MethodDelete, "/api/redemption/"+common.Int2Str(r.Id), "")
	assert.Equal(t, false, decodeBody(t, rec)["success"])

	// Delete invalid: used/disabled/expired rows only.
	createRedemptionRow(t, "keep-me", service.RedemptionStatusEnabled, 10, now+1000)
	createRedemptionRow(t, "sweep-disabled", service.RedemptionStatusDisabled, 10, now+1000)
	rec = do(http.MethodDelete, "/api/redemption/invalid", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, float64(1), body["data"], "only the disabled row is swept")
	model.DB.Model(&model.Redemption{}).Count(&count)
	assert.Equal(t, int64(1), count, "only the valid enabled code survives")
}
