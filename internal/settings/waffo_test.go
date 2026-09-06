package settings

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func waffoTestKeyPair(t *testing.T) (string, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(privateDER), base64.StdEncoding.EncodeToString(publicDER)
}

func TestBuildWaffoConfigSelectsExactEnvironmentAndDefensiveMethods(t *testing.T) {
	merchantPrivate, _ := waffoTestKeyPair(t)
	_, providerPublic := waffoTestKeyPair(t)
	sandboxPrivate, _ := waffoTestKeyPair(t)
	_, sandboxPublic := waffoTestKeyPair(t)
	options := map[string]string{
		WaffoEnabledOption:           "true",
		WaffoAPIKeyOption:            "prod-api-key",
		WaffoPrivateKeyOption:        merchantPrivate,
		WaffoPublicCertOption:        providerPublic,
		WaffoSandboxAPIKeyOption:     "sandbox-api-key",
		WaffoSandboxPrivateKeyOption: sandboxPrivate,
		WaffoSandboxPublicCertOption: sandboxPublic,
		WaffoMerchantIDOption:        "merchant_123",
		WaffoNotifyURLOption:         "https://payments.example.test/api/waffo/webhook",
		WaffoReturnURLOption:         "https://payments.example.test/wallet?show_history=true",
		WaffoCurrencyOption:          "usd",
		WaffoUnitPriceOption:         "1.25",
		WaffoMinTopUpOption:          "2",
	}
	config, err := buildWaffoConfig(options)
	require.NoError(t, err)
	assert.True(t, config.Enabled)
	assert.False(t, config.Sandbox)
	assert.Equal(t, "prod-api-key", config.APIKey)
	assert.Equal(t, merchantPrivate, config.PrivateKey)
	assert.Equal(t, providerPublic, config.PublicKey)
	assert.Equal(t, WaffoProductionBaseURL, config.APIBaseURL)
	assert.Equal(t, "USD", config.Currency)
	assert.Equal(t, 1.25, config.UnitPrice)
	assert.Equal(t, int64(2), config.MinTopUp)
	require.Len(t, config.PayMethods, 3)
	config.PayMethods[0].Name = "mutated"
	second, err := buildWaffoConfig(options)
	require.NoError(t, err)
	assert.Equal(t, "Card", second.PayMethods[0].Name)

	options[WaffoSandboxOption] = "true"
	config, err = buildWaffoConfig(options)
	require.NoError(t, err)
	assert.True(t, config.Sandbox)
	assert.Equal(t, "sandbox-api-key", config.APIKey)
	assert.Equal(t, sandboxPrivate, config.PrivateKey)
	assert.Equal(t, sandboxPublic, config.PublicKey)
	assert.Equal(t, WaffoSandboxBaseURL, config.APIBaseURL)
}

func TestBuildWaffoConfigFailsClosedOnUnsafeValues(t *testing.T) {
	privateKey, publicKey := waffoTestKeyPair(t)
	base := map[string]string{
		WaffoEnabledOption:    "true",
		WaffoAPIKeyOption:     "api-key",
		WaffoPrivateKeyOption: privateKey,
		WaffoPublicCertOption: publicKey,
	}
	tests := map[string]map[string]string{
		"invalid enabled":       {WaffoEnabledOption: "TRUE"},
		"invalid sandbox":       {WaffoSandboxOption: "1"},
		"credential whitespace": {WaffoAPIKeyOption: " api-key"},
		"invalid private key":   {WaffoPrivateKeyOption: "bm90LWEta2V5"},
		"invalid public key":    {WaffoPublicCertOption: "bm90LWEta2V5"},
		"invalid currency":      {WaffoCurrencyOption: "US$"},
		"non-finite unit price": {WaffoUnitPriceOption: "NaN"},
		"oversized unit price":  {WaffoUnitPriceOption: "1000000"},
		"zero unit price":       {WaffoUnitPriceOption: "0"},
		"zero minimum":          {WaffoMinTopUpOption: "0"},
		"unreachable minimum":   {WaffoMinTopUpOption: "4295"},
		"unsafe notify URL":     {WaffoNotifyURLOption: "https://user:pass@example.test/webhook"},
		"unsafe return URL":     {WaffoReturnURLOption: "javascript:alert(1)"},
		"null methods":          {WaffoPayMethodsOption: "null"},
		"blank methods":         {WaffoPayMethodsOption: "   "},
		"invalid method token":  {WaffoPayMethodsOption: `[{"name":"Card","icon":"","payMethodType":"CARD OTHER","payMethodName":""}]`},
		"oversized methods":     {WaffoPayMethodsOption: strings.Repeat("x", maxWaffoPayMethodsJSON+1)},
	}
	for name, changes := range tests {
		t.Run(name, func(t *testing.T) {
			options := make(map[string]string, len(base)+len(changes))
			for key, value := range base {
				options[key] = value
			}
			for key, value := range changes {
				options[key] = value
			}
			_, err := buildWaffoConfig(options)
			assert.ErrorIs(t, err, ErrInvalidPaymentSetting)
		})
	}
}

func TestWaffoOptionValidationRejectsBeforePublication(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOption(WaffoUnitPriceOption, "1.5"))
	assert.Equal(t, "1.5", GetOption(WaffoUnitPriceOption))

	err := UpdateOption(WaffoUnitPriceOption, "NaN")
	assert.ErrorIs(t, err, ErrInvalidPaymentSetting)
	assert.Equal(t, "1.5", GetOption(WaffoUnitPriceOption))

	require.NoError(t, UpdateOption(WaffoPayMethodsOption,
		`[{"name":"Local card","icon":"/card.png","payMethodType":"CREDITCARD","payMethodName":""}]`))
	methods := GetWaffoPayMethods()
	require.Len(t, methods, 1)
	assert.Equal(t, "CREDITCARD", methods[0].PayMethodType)
}

func TestValidWaffoCallbackURL(t *testing.T) {
	for _, raw := range []string{
		"https://example.test/api/waffo/webhook",
		"http://127.0.0.1:3000/wallet?show_history=true",
	} {
		assert.True(t, ValidWaffoCallbackURL(raw), raw)
	}
	for _, raw := range []string{
		"", "//example.test/webhook", "ftp://example.test/webhook",
		"http://example.test/webhook",
		"https://user@example.test/webhook", "https://example.test/webhook#fragment",
		"https://example.test/webhook\nX-Test: injected",
	} {
		assert.False(t, ValidWaffoCallbackURL(raw), raw)
	}
}
