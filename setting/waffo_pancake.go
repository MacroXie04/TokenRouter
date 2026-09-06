package setting

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Waffo Pancake is a separate hosted-checkout product from classic Waffo.
// Keep its option names and credential validation isolated so a key for one
// integration can never silently enable the other.
const (
	WaffoPancakeMerchantIDOption = "WaffoPancakeMerchantID"
	WaffoPancakePrivateKeyOption = "WaffoPancakePrivateKey"
	WaffoPancakeReturnURLOption  = "WaffoPancakeReturnURL"
	WaffoPancakeUnitPriceOption  = "WaffoPancakeUnitPrice"
	WaffoPancakeMinTopUpOption   = "WaffoPancakeMinTopUp"
	WaffoPancakeStoreIDOption    = "WaffoPancakeStoreID"
	WaffoPancakeProductIDOption  = "WaffoPancakeProductID"

	WaffoPancakeBaseURL = "https://api.waffo.ai"

	defaultWaffoPancakeUnitPrice = 1.0
	defaultWaffoPancakeMinTopUp  = int64(1)
	maxWaffoPancakeKeyBytes      = 16 << 10
	maxWaffoPancakeMoneyText     = 64
)

// WaffoPancakeConfig is an all-or-nothing, hot-reloadable configuration
// snapshot. PrivateKey is deliberately excluded from JSON serialization.
type WaffoPancakeConfig struct {
	MerchantID string  `json:"merchant_id"`
	PrivateKey string  `json:"-"`
	ReturnURL  string  `json:"return_url"`
	UnitPrice  float64 `json:"unit_price"`
	MinTopUp   int64   `json:"min_topup"`
	StoreID    string  `json:"store_id"`
	ProductID  string  `json:"product_id"`
	APIBaseURL string  `json:"-"`
}

// GetWaffoPancakeConfigChecked returns a coherent snapshot. Malformed values
// written outside this process fail closed instead of changing price or
// credential semantics at runtime.
func GetWaffoPancakeConfigChecked() (WaffoPancakeConfig, error) {
	return buildWaffoPancakeConfig(GetOptions(
		WaffoPancakeMerchantIDOption,
		WaffoPancakePrivateKeyOption,
		WaffoPancakeReturnURLOption,
		WaffoPancakeUnitPriceOption,
		WaffoPancakeMinTopUpOption,
		WaffoPancakeStoreIDOption,
		WaffoPancakeProductIDOption,
	))
}

// WaffoPancakeTopUpConfigured requires both checkout credentials and the
// wallet product. Subscription plans may use their own product, but the
// shared webhook remains disabled unless the wallet integration is complete,
// matching the public availability contract.
func WaffoPancakeTopUpConfigured() bool {
	config, err := GetWaffoPancakeConfigChecked()
	return err == nil && config.MerchantID != "" && config.PrivateKey != "" &&
		config.StoreID != "" && config.ProductID != ""
}

// NewWaffoPancakeCredentialConfig validates transient credentials supplied by
// the root-only setup workflow without publishing them to the option cache.
func NewWaffoPancakeCredentialConfig(merchantID, privateKey string) (WaffoPancakeConfig, error) {
	return buildWaffoPancakeConfig(map[string]string{
		WaffoPancakeMerchantIDOption: merchantID,
		WaffoPancakePrivateKeyOption: privateKey,
	})
}

func buildWaffoPancakeConfig(options map[string]string) (WaffoPancakeConfig, error) {
	config := WaffoPancakeConfig{
		MerchantID: options[WaffoPancakeMerchantIDOption],
		PrivateKey: options[WaffoPancakePrivateKeyOption],
		ReturnURL:  options[WaffoPancakeReturnURLOption],
		StoreID:    options[WaffoPancakeStoreIDOption],
		ProductID:  options[WaffoPancakeProductIDOption],
		UnitPrice:  defaultWaffoPancakeUnitPrice,
		MinTopUp:   defaultWaffoPancakeMinTopUp,
		APIBaseURL: WaffoPancakeBaseURL,
	}

	for key, value := range map[string]string{
		WaffoPancakeMerchantIDOption: config.MerchantID,
		WaffoPancakeStoreIDOption:    config.StoreID,
		WaffoPancakeProductIDOption:  config.ProductID,
	} {
		if value != "" && !ValidWaffoPancakeShortID(value, waffoPancakePrefixForOption(key)) {
			return WaffoPancakeConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, key)
		}
	}
	if config.PrivateKey != "" {
		if _, err := ParseWaffoPancakePrivateKey(config.PrivateKey); err != nil {
			return WaffoPancakeConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoPancakePrivateKeyOption)
		}
	}
	if config.ReturnURL != "" && !ValidWaffoCallbackURL(config.ReturnURL) {
		return WaffoPancakeConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoPancakeReturnURLOption)
	}

	if raw := options[WaffoPancakeUnitPriceOption]; raw != "" {
		if raw != strings.TrimSpace(raw) || len(raw) > maxWaffoPancakeMoneyText {
			return WaffoPancakeConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoPancakeUnitPriceOption)
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value <= 0 || value > maxBillingUnitPrice || math.IsNaN(value) || math.IsInf(value, 0) {
			return WaffoPancakeConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoPancakeUnitPriceOption)
		}
		config.UnitPrice = value
	}
	if raw := options[WaffoPancakeMinTopUpOption]; raw != "" {
		if raw != strings.TrimSpace(raw) || len(raw) > 32 {
			return WaffoPancakeConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoPancakeMinTopUpOption)
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 || value > MaxTopUpReferenceAmount {
			return WaffoPancakeConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoPancakeMinTopUpOption)
		}
		config.MinTopUp = value
	}
	return config, nil
}

func waffoPancakePrefixForOption(key string) string {
	switch key {
	case WaffoPancakeMerchantIDOption:
		return "MER"
	case WaffoPancakeStoreIDOption:
		return "STO"
	case WaffoPancakeProductIDOption:
		return "PROD"
	default:
		return ""
	}
}

// ValidWaffoPancakeShortID validates the provider's PREFIX_ + 22 base62
// identifier grammar without a regular expression or unbounded work.
func ValidWaffoPancakeShortID(value, prefix string) bool {
	if prefix == "" || len(value) != len(prefix)+1+22 || !strings.HasPrefix(value, prefix+"_") {
		return false
	}
	for i := len(prefix) + 1; i < len(value); i++ {
		char := value[i]
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')) {
			return false
		}
	}
	return true
}

// ParseWaffoPancakePrivateKey accepts the provider's documented PEM, escaped
// PEM, PKCS#1, PKCS#8, and raw-PKCS#8 base64 forms. It rejects trailing data,
// oversized keys, non-RSA keys, and RSA keys below 2048 bits.
func ParseWaffoPancakePrivateKey(raw string) (*rsa.PrivateKey, error) {
	if raw == "" || len(raw) > maxWaffoPancakeKeyBytes {
		return nil, fmt.Errorf("invalid Waffo Pancake private key")
	}
	normalized := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(raw, "\\n", "\n"), "\r\n", "\n"))
	if normalized == "" || len(normalized) > maxWaffoPancakeKeyBytes {
		return nil, fmt.Errorf("invalid Waffo Pancake private key")
	}
	var der []byte
	var blockType string
	if strings.Contains(normalized, "-----BEGIN") {
		block, rest := pem.Decode([]byte(normalized))
		if block == nil || strings.TrimSpace(string(rest)) != "" {
			return nil, fmt.Errorf("invalid Waffo Pancake private key")
		}
		der = block.Bytes
		blockType = block.Type
	} else {
		compact := strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "").Replace(normalized)
		decoded, err := base64.StdEncoding.Strict().DecodeString(compact)
		if err != nil {
			return nil, fmt.Errorf("invalid Waffo Pancake private key")
		}
		der = decoded
		blockType = "PRIVATE KEY"
	}
	if len(der) == 0 || len(der) > maxWaffoPancakeKeyBytes {
		return nil, fmt.Errorf("invalid Waffo Pancake private key")
	}
	var key *rsa.PrivateKey
	var err error
	switch blockType {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(der)
	case "PRIVATE KEY":
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(der)
		if err == nil {
			var ok bool
			key, ok = parsed.(*rsa.PrivateKey)
			if !ok {
				err = fmt.Errorf("private key is not RSA")
			}
		}
	default:
		err = fmt.Errorf("unsupported private key type")
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, fmt.Errorf("invalid Waffo Pancake private key")
	}
	return key, nil
}

func isWaffoPancakeOptionKey(key string) bool {
	switch key {
	case WaffoPancakeMerchantIDOption, WaffoPancakePrivateKeyOption, WaffoPancakeReturnURLOption,
		WaffoPancakeUnitPriceOption, WaffoPancakeMinTopUpOption, WaffoPancakeStoreIDOption,
		WaffoPancakeProductIDOption:
		return true
	default:
		return false
	}
}
