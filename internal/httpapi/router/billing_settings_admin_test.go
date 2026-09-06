package router_test

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"net/http"
	"net/http/httptest"
	"testing"
)

func updateBillingOption(t *testing.T, do func(string, string, string) *httptest.ResponseRecorder, key string, value any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"key": key, "value": value})
	require.NoError(t, err)
	recorder := do(http.MethodPut, "/api/option/", string(body))
	require.Contains(t, []int{http.StatusOK, http.StatusBadRequest}, recorder.Code, recorder.Body.String())
	return decodeBody(t, recorder)
}

func TestBillingSettingsAreValidatedAndAffectLivePricing(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	t.Cleanup(func() { billingsvc.SetTopUpGroupRatios(map[string]float64{"default": 1, "vip": 1, "svip": 1}) })

	assert.Equal(t, true, updateBillingOption(t, do, setting.PriceOption, "1")["success"])
	assert.Equal(t, true, updateBillingOption(t, do, setting.TopUpGroupRatioOption, `{"default":1,"vip":1.25}`)["success"])
	money, err := billingsvc.GetTopupMoney(10, "vip")
	require.NoError(t, err)
	assert.Equal(t, 12.5, money)

	persisted := setting.GetOption(setting.TopUpGroupRatioOption)
	for _, invalid := range []string{`{"default":0}`, `{"default":1e100}`, `null`} {
		assert.Equal(t, false, updateBillingOption(t, do, setting.TopUpGroupRatioOption, invalid)["success"], invalid)
		assert.Equal(t, persisted, setting.GetOption(setting.TopUpGroupRatioOption), invalid)
	}

	for key, invalid := range map[string]string{
		setting.InitialQuotaOption:               "-1",
		setting.PreConsumedQuotaOption:           "9007199254740992",
		setting.USDExchangeRateOption:            "0",
		setting.CustomCurrencyExchangeRateOption: "1000001",
		setting.CustomCurrencySymbolOption:       "\u202e$",
		setting.QuotaDisplayTypeOption:           "credits",
		setting.TopUpLinkOption:                  "javascript:alert(1)",
		setting.PayAddressOption:                 "http://pay.example.test",
		setting.PaymentSettingOption:             `{"amount_options":[10,10]}`,
	} {
		assert.Equal(t, false, updateBillingOption(t, do, key, invalid)["success"], key)
		assert.Empty(t, setting.GetOption(key), key)
	}

	assert.Equal(t, true, updateBillingOption(t, do, setting.QuotaDisplayTypeOption, "CUSTOM")["success"])
	assert.Equal(t, true, updateBillingOption(t, do, setting.CustomCurrencySymbolOption, "HK$")["success"])
	assert.Equal(t, true, updateBillingOption(t, do, setting.CustomCurrencyExchangeRateOption, "7.8")["success"])
	assert.Equal(t, true, updateBillingOption(t, do, setting.TopUpLinkOption, "https://billing.example.test/codes?from=wallet")["success"])
	config, err := setting.GetCurrencyDisplaySettingChecked()
	require.NoError(t, err)
	assert.Equal(t, setting.CurrencyDisplayTypeCustom, config.Type)
	assert.Equal(t, "HK$", config.Symbol)
	assert.Equal(t, 7.8, config.CurrencyExchangeRate())
	assert.Equal(t, "https://billing.example.test/codes?from=wallet", setting.GetTopUpLink())
}

func TestBillingSettingsDefaultsAndSecretsAreReadSafe(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	assert.Equal(t, true, updateBillingOption(t, do, setting.EpayKeyOption, "private-epay-signing-key")["success"])

	recorder := do(http.MethodGet, "/api/option/", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	payload := decodeBody(t, recorder)
	options := payload["data"].([]any)
	byKey := make(map[string]string, len(options))
	redacted := make(map[string]bool, len(options))
	for _, raw := range options {
		option := raw.(map[string]any)
		key := option["key"].(string)
		value := option["value"].(string)
		byKey[key] = value
		redacted[key], _ = option["redacted"].(bool)
	}
	assert.Equal(t, "", byKey[setting.EpayKeyOption])
	assert.True(t, redacted[setting.EpayKeyOption])
	assert.NotContains(t, recorder.Body.String(), "private-epay-signing-key")
	assert.Equal(t, "", byKey[setting.TopUpLinkOption])
	assert.Equal(t, "7.3", byKey[setting.USDExchangeRateOption])
	assert.Equal(t, "¤", byKey[setting.CustomCurrencySymbolOption])
	assert.Equal(t, "1", byKey[setting.CustomCurrencyExchangeRateOption])
	assert.JSONEq(t, `{"default":1,"svip":1,"vip":1}`, byKey[setting.TopUpGroupRatioOption])
}

func TestGrokViolationSettingsDefaultsAndStrictAdminValidation(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)

	recorder := do(http.MethodGet, "/api/option/", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	payload := decodeBody(t, recorder)
	byKey := map[string]string{}
	for _, raw := range payload["data"].([]any) {
		option := raw.(map[string]any)
		byKey[option["key"].(string)] = option["value"].(string)
	}
	assert.Equal(t, "true", byKey[setting.GrokViolationDeductionEnabledOption])
	assert.Equal(t, "0.05", byKey[setting.GrokViolationDeductionAmountOption])

	assert.Equal(t, true, updateBillingOption(t, do, setting.GrokViolationDeductionEnabledOption, false)["success"])
	assert.Equal(t, true, updateBillingOption(t, do, setting.GrokViolationDeductionAmountOption, "0.125")["success"])
	configured := setting.GetGrokSetting()
	assert.False(t, configured.ViolationDeductionEnabled)
	assert.Equal(t, "0.125", configured.ViolationDeductionAmountDecimal().String())

	for _, invalid := range []any{"", "NaN", "+Inf", "-0.01", "4294.967295", 1e100} {
		assert.Equal(t, false,
			updateBillingOption(t, do, setting.GrokViolationDeductionAmountOption, invalid)["success"], invalid)
		assert.Equal(t, "0.125", setting.GetGrokSetting().ViolationDeductionAmountDecimal().String())
	}
	assert.Equal(t, false,
		updateBillingOption(t, do, setting.GrokViolationDeductionEnabledOption, "1")["success"])
	assert.False(t, setting.GetGrokSetting().ViolationDeductionEnabled)
}
