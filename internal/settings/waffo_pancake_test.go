package settings

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testPancakeMerchantID = "MER_AbCdEfGhIjKlMnOpQrStUv"
	testPancakeStoreID    = "STO_AbCdEfGhIjKlMnOpQrStUv"
	testPancakeProductID  = "PROD_AbCdEfGhIjKlMnOpQrStUv"
)

func pancakeTestPrivateKeys(t *testing.T) (rawPKCS8, escapedPEM, pkcs1PEM string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	rawPKCS8 = base64.StdEncoding.EncodeToString(pkcs8)
	pemValue := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	escapedPEM = strings.ReplaceAll(strings.TrimSpace(pemValue), "\n", "\\n")
	pkcs1PEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	return rawPKCS8, escapedPEM, pkcs1PEM
}

func TestBuildWaffoPancakeConfigDefaultsAndValidatesCredentials(t *testing.T) {
	rawKey, _, _ := pancakeTestPrivateKeys(t)
	config, err := buildWaffoPancakeConfig(map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, defaultWaffoPancakeUnitPrice, config.UnitPrice)
	assert.Equal(t, defaultWaffoPancakeMinTopUp, config.MinTopUp)
	assert.Equal(t, WaffoPancakeBaseURL, config.APIBaseURL)

	config, err = buildWaffoPancakeConfig(map[string]string{
		WaffoPancakeMerchantIDOption: testPancakeMerchantID,
		WaffoPancakePrivateKeyOption: rawKey,
		WaffoPancakeReturnURLOption:  "https://merchant.example.test/wallet",
		WaffoPancakeUnitPriceOption:  "1.25",
		WaffoPancakeMinTopUpOption:   "2",
		WaffoPancakeStoreIDOption:    testPancakeStoreID,
		WaffoPancakeProductIDOption:  testPancakeProductID,
	})
	require.NoError(t, err)
	assert.Equal(t, testPancakeMerchantID, config.MerchantID)
	assert.Equal(t, rawKey, config.PrivateKey)
	assert.Equal(t, 1.25, config.UnitPrice)
	assert.Equal(t, int64(2), config.MinTopUp)
}

func TestParseWaffoPancakePrivateKeyAcceptsDocumentedForms(t *testing.T) {
	raw, escaped, pkcs1 := pancakeTestPrivateKeys(t)
	for _, value := range []string{raw, escaped, pkcs1} {
		key, err := ParseWaffoPancakePrivateKey(value)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, key.N.BitLen(), 2048)
	}
	_, err := ParseWaffoPancakePrivateKey(escaped + "garbage")
	assert.Error(t, err)
}

func TestBuildWaffoPancakeConfigRejectsUnsafeValues(t *testing.T) {
	rawKey, _, _ := pancakeTestPrivateKeys(t)
	base := map[string]string{
		WaffoPancakeMerchantIDOption: testPancakeMerchantID,
		WaffoPancakePrivateKeyOption: rawKey,
		WaffoPancakeStoreIDOption:    testPancakeStoreID,
		WaffoPancakeProductIDOption:  testPancakeProductID,
	}
	tests := map[string]map[string]string{
		"merchant grammar": {WaffoPancakeMerchantIDOption: "MER_short"},
		"store grammar":    {WaffoPancakeStoreIDOption: "PROD_AbCdEfGhIjKlMnOpQrStUv"},
		"product grammar":  {WaffoPancakeProductIDOption: " product"},
		"private key":      {WaffoPancakePrivateKeyOption: "not-a-key"},
		"unsafe return":    {WaffoPancakeReturnURLOption: "https://user:pass@example.test/return"},
		"nan price":        {WaffoPancakeUnitPriceOption: "NaN"},
		"oversized price":  {WaffoPancakeUnitPriceOption: "1000000"},
		"zero price":       {WaffoPancakeUnitPriceOption: "0"},
		"zero minimum":     {WaffoPancakeMinTopUpOption: "0"},
		"large minimum":    {WaffoPancakeMinTopUpOption: strconv.FormatInt(MaxTopUpReferenceAmount+1, 10)},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := make(map[string]string, len(base)+1)
			for key, value := range base {
				candidate[key] = value
			}
			for key, value := range change {
				candidate[key] = value
			}
			_, err := buildWaffoPancakeConfig(candidate)
			assert.Error(t, err)
		})
	}
	assert.False(t, math.IsNaN(defaultWaffoPancakeUnitPrice))
}

func TestWaffoPancakeOptionValidationRejectsBeforePublication(t *testing.T) {
	setupAffinitySettingTest(t)
	rawKey, _, _ := pancakeTestPrivateKeys(t)
	require.NoError(t, UpdateOptions(map[string]string{
		WaffoPancakeMerchantIDOption: testPancakeMerchantID,
		WaffoPancakePrivateKeyOption: rawKey,
		WaffoPancakeStoreIDOption:    testPancakeStoreID,
		WaffoPancakeProductIDOption:  testPancakeProductID,
		WaffoPancakeUnitPriceOption:  "1.50",
	}))
	assert.True(t, WaffoPancakeTopUpConfigured())

	err := UpdateOption(WaffoPancakeUnitPriceOption, "Infinity")
	assert.Error(t, err)
	assert.Equal(t, "1.50", GetOption(WaffoPancakeUnitPriceOption))
	config, configErr := GetWaffoPancakeConfigChecked()
	require.NoError(t, configErr)
	assert.Equal(t, 1.5, config.UnitPrice)

	require.NoError(t, UpdateOption(WaffoPancakeStoreIDOption, ""))
	assert.False(t, WaffoPancakeTopUpConfigured(), "webhook settlement must be bound to this merchant's store")
}
