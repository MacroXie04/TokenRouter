package router_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"testing"
)

func TestUserTopUpCannotMintQuotaFromClientDeclaredBalance(t *testing.T) {
	_, do, userID := setupChannelRead(t, roles.RoleCommonUser)
	setPaymentCompliance(t, true)

	var before model.User
	require.NoError(t, model.DB.First(&before, userID).Error)
	rec := do(http.MethodPost, "/api/user/topup", `{"amount":1000000,"money":0,"payment_method":"balance"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])

	var after model.User
	require.NoError(t, model.DB.First(&after, userID).Error)
	assert.Equal(t, before.Quota, after.Quota)
	var orders int64
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("user_id = ?", userID).Count(&orders).Error)
	assert.Zero(t, orders, "the redemption route must never create a client-declared recharge order")
}

func TestUserTopUpRedeemsServerIssuedCodeExactlyOnce(t *testing.T) {
	_, do, userID := setupChannelRead(t, roles.RoleCommonUser)
	setPaymentCompliance(t, true)

	redemption := model.Redemption{
		UserId: 1, Key: "0123456789abcdef0123456789abcdef",
		Status: billingsvc.RedemptionStatusEnabled, Name: "secure", Quota: 125,
	}
	require.NoError(t, model.DB.Create(&redemption).Error)
	var before model.User
	require.NoError(t, model.DB.First(&before, userID).Error)

	rec := do(http.MethodPost, "/api/user/topup", `{"key":"0123456789abcdef0123456789abcdef"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, float64(125), body["data"])

	var after model.User
	require.NoError(t, model.DB.First(&after, userID).Error)
	assert.Equal(t, before.Quota+125, after.Quota)

	rec = do(http.MethodPost, "/api/user/topup", `{"key":"0123456789abcdef0123456789abcdef"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	require.NoError(t, model.DB.First(&after, userID).Error)
	assert.Equal(t, before.Quota+125, after.Quota)
}

func TestPaymentComplianceGateCoversEveryRedemptionAndAffiliateMutation(t *testing.T) {
	_, do, userID := setupChannelRead(t, roles.RoleCommonUser)
	setPaymentCompliance(t, false)
	require.NoError(t, setting.UpdateOption(setting.QuotaPerUnitOption, "10"))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).
		Updates(map[string]any{"quota": 25, "aff_quota": 20}).Error)
	redemption := model.Redemption{
		UserId: 1, Key: "fedcba9876543210fedcba9876543210",
		Status: billingsvc.RedemptionStatusEnabled, Name: "compliance", Quota: 125,
	}
	require.NoError(t, model.DB.Create(&redemption).Error)

	for _, path := range []string{"/api/user/topup", "/api/user/redemption/redeem"} {
		rec := do(http.MethodPost, path, `{"key":"fedcba9876543210fedcba9876543210"}`)
		assert.False(t, decodeBody(t, rec)["success"].(bool), "path=%s body=%s", path, rec.Body.String())
		assert.Contains(t, rec.Body.String(), billingsvc.ErrPaymentComplianceRequired.Error())
	}
	rec := do(http.MethodPost, "/api/user/aff_transfer", `{"quota":10}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), billingsvc.ErrPaymentComplianceRequired.Error())

	var storedRedemption model.Redemption
	require.NoError(t, model.DB.First(&storedRedemption, redemption.Id).Error)
	assert.Equal(t, billingsvc.RedemptionStatusEnabled, storedRedemption.Status)
	assert.Zero(t, storedRedemption.UsedUserId)
	var storedUser model.User
	require.NoError(t, model.DB.First(&storedUser, userID).Error)
	assert.Equal(t, 25, storedUser.Quota)
	assert.Equal(t, 20, storedUser.AffQuota)
}
