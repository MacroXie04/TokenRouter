package controller_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func setPaymentCompliance(t *testing.T, confirmed bool) {
	t.Helper()
	version := ""
	if confirmed {
		version = service.CurrentPaymentComplianceTermsVersion
	}
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.PaymentComplianceConfirmedOption:    strconv.FormatBool(confirmed),
		setting.PaymentComplianceTermsVersionOption: version,
		setting.PaymentComplianceConfirmedAtOption:  "",
		setting.PaymentComplianceConfirmedByOption:  "",
		setting.PaymentComplianceConfirmedIPOption:  "",
		setting.LegacyPaymentComplianceOption:       "false",
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(map[string]string{
			setting.PaymentComplianceConfirmedOption:    "false",
			setting.PaymentComplianceTermsVersionOption: "",
		})
	})
}

func TestPaymentComplianceConfirmationContract(t *testing.T) {
	handler, do, rootID := setupChannelRead(t, constant.RoleRootUser)
	setPaymentCompliance(t, false)

	// Malformed and non-affirmative requests are HTTP-200 business failures.
	rec := do(http.MethodPost, "/api/option/payment_compliance", "{")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "参数错误", decodeBody(t, rec)["message"])
	rec = do(http.MethodPost, "/api/option/payment_compliance", `{"confirmed":false}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "请确认合规声明", decodeBody(t, rec)["message"])
	assert.False(t, service.PaymentComplianceConfirmed())

	// Dashboard PATs authenticate as root but cannot perform this ceremony.
	pat, err := service.GenerateUserAccessToken(rootID)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/api/option/payment_compliance", strings.NewReader(`{"confirmed":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+pat)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, decodeBody(t, rec)["message"], "dashboard session authentication")
	assert.False(t, service.PaymentComplianceConfirmed())

	// A root dashboard session records the current terms and makes the gate live.
	rec = do(http.MethodPost, "/api/option/payment_compliance", `{"confirmed":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data := body["data"].(map[string]any)
	assert.Equal(t, true, data["confirmed"])
	assert.Equal(t, service.CurrentPaymentComplianceTermsVersion, data["terms_version"])
	assert.Equal(t, float64(rootID), data["confirmed_by"])
	assert.NotZero(t, data["confirmed_at"])
	assert.True(t, service.PaymentComplianceConfirmed())
	assert.Equal(t, "true", setting.GetOption(setting.PaymentComplianceConfirmedOption))
	assert.Equal(t, service.CurrentPaymentComplianceTermsVersion, setting.GetOption(setting.PaymentComplianceTermsVersionOption))
	assert.Equal(t, strconv.Itoa(rootID), setting.GetOption(setting.PaymentComplianceConfirmedByOption))
	assert.Equal(t, "192.0.2.1", setting.GetOption(setting.PaymentComplianceConfirmedIPOption))

	// An old terms version invalidates the effective gate immediately.
	require.NoError(t, setting.UpdateOption(setting.PaymentComplianceTermsVersionOption, "v0"))
	assert.False(t, service.PaymentComplianceConfirmed())

	var audit model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", rootID, service.LogTypeManage).
		Order("id desc").First(&audit).Error)
	assert.Contains(t, audit.Content, "payment_compliance.confirmed")
	assert.Contains(t, audit.Content, "terms_version=v1")
	assert.NotContains(t, audit.Content, "Secret")
}

func TestRootOptionSecurityContract(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	setPaymentCompliance(t, false)
	require.NoError(t, setting.UpdateOption("VisibleOption", "visible"))
	require.NoError(t, setting.UpdateOption("StripeSecretKey", "sk_test_never_return"))
	require.NoError(t, setting.UpdateOption("WeChatServerToken", "secret-token"))
	require.NoError(t, setting.UpdateOption(setting.GitHubClientSecretOption, "github-option-secret"))
	require.NoError(t, setting.UpdateOption(setting.DiscordClientSecretOption, "discord-option-secret"))
	require.NoError(t, setting.UpdateOption(setting.OIDCClientSecretOption, "oidc-option-secret"))
	require.NoError(t, setting.UpdateOption(setting.LinuxDOClientSecretOption, "linuxdo-option-secret"))

	rec := do(http.MethodGet, "/api/option/", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "VisibleOption")
	assert.NotContains(t, rec.Body.String(), "sk_test_never_return")
	assert.NotContains(t, rec.Body.String(), "secret-token")
	assert.NotContains(t, rec.Body.String(), "github-option-secret")
	assert.NotContains(t, rec.Body.String(), "discord-option-secret")
	assert.NotContains(t, rec.Body.String(), "oidc-option-secret")
	assert.NotContains(t, rec.Body.String(), "linuxdo-option-secret")
	assert.Contains(t, rec.Body.String(), `"key":"oidc.client_secret","value":"","redacted":true`)
	assert.Contains(t, rec.Body.String(), setting.EmailDomainRestrictionEnabledOption)
	assert.Contains(t, rec.Body.String(), setting.PasskeyUserVerificationOption)

	// Ordinary updates use the reference key/value request and update cache.
	rec = do(http.MethodPut, "/api/option/", `{"key":"VisibleOption","value":42}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	assert.Equal(t, "42", setting.GetOption("VisibleOption"))

	// Compliance state and the legacy flat flag cannot be changed generically.
	for _, key := range []string{setting.PaymentComplianceConfirmedOption, setting.PaymentComplianceTermsVersionOption, setting.LegacyPaymentComplianceOption} {
		rec = do(http.MethodPut, "/api/option/", `{"key":"`+key+`","value":true}`)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "合规确认字段不允许通过通用设置接口修改", decodeBody(t, rec)["message"])
	}
	assert.False(t, service.PaymentComplianceConfirmed())

	// Positive affiliate rewards remain gated until confirmation.
	rec = do(http.MethodPut, "/api/option/", `{"key":"QuotaForInviter","value":100}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, service.ErrPaymentComplianceRequired.Error(), decodeBody(t, rec)["message"])
}

func TestRootOptionUpdateRejectsUnsafeOrStructuredValues(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)

	requests := []string{
		`{"key":"","value":"x"}`,
		`{"key":"bad key","value":"x"}`,
		`{"key":"Unsafe","value":{"nested":true}}`,
		`{"key":"Unsafe","value":["nested"]}`,
		`{"key":"Unsafe","value":null}`,
		`{"key":"Unsafe","value":"direction\u202Eoverride"}`,
	}
	for _, body := range requests {
		rec := do(http.MethodPut, "/api/option/", body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Equal(t, false, decodeBody(t, rec)["success"])
	}

	oversizedBody, err := json.Marshal(map[string]any{
		"key":   "OversizedOption",
		"value": strings.Repeat("x", (1<<20)+1),
	})
	require.NoError(t, err)
	rec := do(http.MethodPut, "/api/option/", string(oversizedBody))
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Empty(t, setting.GetOption("OversizedOption"))

	// Text settings may still contain localized content and normal formatting.
	validBody, err := json.Marshal(map[string]any{
		"key":   "VisibleOption",
		"value": "第一行\nsecond line",
	})
	require.NoError(t, err)
	rec = do(http.MethodPut, "/api/option/", string(validBody))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	assert.Equal(t, "第一行\nsecond line", setting.GetOption("VisibleOption"))

	// JSON integers retain their exact lexical value instead of passing through
	// a lossy float64 conversion.
	rec = do(http.MethodPut, "/api/option/", `{"key":"VisibleOption","value":9007199254740993}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "9007199254740993", setting.GetOption("VisibleOption"))
}

func TestRootOptionReadFailsClosedOnUnsafeStoredData(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	const key = "UnsafeStoredOption"
	require.NoError(t, setting.UpdateOption(key, "unsafe\x00value"))
	t.Cleanup(func() {
		_ = model.DB.Delete(&model.Option{}, "key = ?", key).Error
		_ = setting.Sync()
	})

	rec := do(http.MethodGet, "/api/option/", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	assert.NotContains(t, rec.Body.String(), "unsafe")
}

func TestOptionAndComplianceRoleGuards(t *testing.T) {
	// Anonymous requests are rejected.
	handler, _, _ := setupChannelRead(t, constant.RoleRootUser)
	req := httptest.NewRequest(http.MethodPost, "/api/option/payment_compliance", strings.NewReader(`{"confirmed":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// Common users and admins cannot read/write options or confirm compliance.
	for _, role := range []int{constant.RoleCommonUser, constant.RoleAdminUser} {
		_, do, _ := setupChannelRead(t, role)
		assert.Equal(t, http.StatusForbidden, do(http.MethodGet, "/api/option/", "").Code)
		assert.Equal(t, http.StatusForbidden, do(http.MethodPut, "/api/option/", `{"key":"VisibleOption","value":"x"}`).Code)
		assert.Equal(t, http.StatusForbidden, do(http.MethodPost, "/api/option/payment_compliance", `{"confirmed":true}`).Code)
	}
}
