package settings

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	WaffoEnabledOption           = "WaffoEnabled"
	WaffoAPIKeyOption            = "WaffoApiKey"
	WaffoPrivateKeyOption        = "WaffoPrivateKey"
	WaffoPublicCertOption        = "WaffoPublicCert"
	WaffoSandboxPublicCertOption = "WaffoSandboxPublicCert"
	WaffoSandboxAPIKeyOption     = "WaffoSandboxApiKey"
	WaffoSandboxPrivateKeyOption = "WaffoSandboxPrivateKey"
	WaffoSandboxOption           = "WaffoSandbox"
	WaffoMerchantIDOption        = "WaffoMerchantId"
	WaffoNotifyURLOption         = "WaffoNotifyUrl"
	WaffoReturnURLOption         = "WaffoReturnUrl"
	WaffoCurrencyOption          = "WaffoCurrency"
	WaffoUnitPriceOption         = "WaffoUnitPrice"
	WaffoMinTopUpOption          = "WaffoMinTopUp"
	WaffoPayMethodsOption        = "WaffoPayMethods"

	WaffoProductionBaseURL = "https://api.waffo.com/api/v1"
	WaffoSandboxBaseURL    = "https://api-sandbox.waffo.com/api/v1"

	maxWaffoCredentialBytes = 16 << 10
	maxWaffoURLBytes        = 2048
	maxWaffoPayMethodsJSON  = 64 << 10
	maxWaffoPayMethods      = 32
	maxWaffoMethodString    = 128
	maxWaffoDisplayName     = 255
	maxWaffoMerchantID      = 255
)

// WaffoPayMethod is one server-owned checkout-method choice. Clients select
// an index (or an exact legacy type/name pair) and therefore cannot introduce
// arbitrary provider payment parameters.
type WaffoPayMethod struct {
	Name          string `json:"name"`
	Icon          string `json:"icon"`
	PayMethodType string `json:"payMethodType"`
	PayMethodName string `json:"payMethodName"`
}

var defaultWaffoPayMethods = []WaffoPayMethod{
	{Name: "Card", Icon: "/pay-card.png", PayMethodType: "CREDITCARD,DEBITCARD"},
	{Name: "Apple Pay", Icon: "/pay-apple.png", PayMethodType: "APPLEPAY", PayMethodName: "APPLEPAY"},
	{Name: "Google Pay", Icon: "/pay-google.png", PayMethodType: "GOOGLEPAY", PayMethodName: "GOOGLEPAY"},
}

// WaffoConfig is a coherent, validated snapshot of classic Waffo wallet
// settings. Private material is deliberately excluded from JSON output.
type WaffoConfig struct {
	Enabled    bool             `json:"enabled"`
	Sandbox    bool             `json:"sandbox"`
	APIKey     string           `json:"-"`
	PrivateKey string           `json:"-"`
	PublicKey  string           `json:"-"`
	MerchantID string           `json:"merchant_id"`
	NotifyURL  string           `json:"notify_url"`
	ReturnURL  string           `json:"return_url"`
	Currency   string           `json:"currency"`
	UnitPrice  float64          `json:"unit_price"`
	MinTopUp   int64            `json:"min_topup"`
	PayMethods []WaffoPayMethod `json:"pay_methods"`
	APIBaseURL string           `json:"-"`
}

// GetWaffoConfigChecked returns one all-or-nothing snapshot. Invalid or
// oversized externally-written options disable checkout instead of silently
// falling back to a different credential, price, or payment method.
func GetWaffoConfigChecked() (WaffoConfig, error) {
	return buildWaffoConfig(GetOptions(
		WaffoEnabledOption,
		WaffoAPIKeyOption,
		WaffoPrivateKeyOption,
		WaffoPublicCertOption,
		WaffoSandboxPublicCertOption,
		WaffoSandboxAPIKeyOption,
		WaffoSandboxPrivateKeyOption,
		WaffoSandboxOption,
		WaffoMerchantIDOption,
		WaffoNotifyURLOption,
		WaffoReturnURLOption,
		WaffoCurrencyOption,
		WaffoUnitPriceOption,
		WaffoMinTopUpOption,
		WaffoPayMethodsOption,
	))
}

// WaffoTopUpConfigured reports whether checkout and signed webhook handling
// are both usable. The separate compliance gate remains owned by service.
func WaffoTopUpConfigured() bool {
	config, err := GetWaffoConfigChecked()
	return err == nil && config.Enabled && config.APIKey != "" && config.PrivateKey != "" && config.PublicKey != ""
}

// GetWaffoPayMethods returns a defensive copy of the validated server-owned
// method catalog. Invalid configuration yields an empty fail-closed catalog.
func GetWaffoPayMethods() []WaffoPayMethod {
	config, err := GetWaffoConfigChecked()
	if err != nil {
		return []WaffoPayMethod{}
	}
	return append([]WaffoPayMethod(nil), config.PayMethods...)
}

func buildWaffoConfig(options map[string]string) (WaffoConfig, error) {
	enabled, err := strictWaffoBool(options[WaffoEnabledOption], false)
	if err != nil {
		return WaffoConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoEnabledOption)
	}
	sandbox, err := strictWaffoBool(options[WaffoSandboxOption], false)
	if err != nil {
		return WaffoConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoSandboxOption)
	}

	credentialKeys := []string{
		WaffoAPIKeyOption, WaffoPrivateKeyOption, WaffoPublicCertOption,
		WaffoSandboxAPIKeyOption, WaffoSandboxPrivateKeyOption, WaffoSandboxPublicCertOption,
	}
	for _, key := range credentialKeys {
		if !validWaffoCredential(options[key]) {
			return WaffoConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, key)
		}
	}
	for _, key := range []string{WaffoPrivateKeyOption, WaffoSandboxPrivateKeyOption} {
		if value := options[key]; value != "" && !validWaffoPrivateKey(value) {
			return WaffoConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, key)
		}
	}
	for _, key := range []string{WaffoPublicCertOption, WaffoSandboxPublicCertOption} {
		if value := options[key]; value != "" && !validWaffoPublicKey(value) {
			return WaffoConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, key)
		}
	}

	merchantID := options[WaffoMerchantIDOption]
	if !validWaffoPlainValue(merchantID, maxWaffoMerchantID, true) {
		return WaffoConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoMerchantIDOption)
	}
	notifyURL := options[WaffoNotifyURLOption]
	returnURL := options[WaffoReturnURLOption]
	for key, value := range map[string]string{WaffoNotifyURLOption: notifyURL, WaffoReturnURLOption: returnURL} {
		if value != "" && !ValidWaffoCallbackURL(value) {
			return WaffoConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, key)
		}
	}

	currency := options[WaffoCurrencyOption]
	if currency == "" {
		currency = "USD"
	}
	if !validWaffoCurrency(currency) {
		return WaffoConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoCurrencyOption)
	}
	currency = strings.ToUpper(currency)

	unitPrice := 1.0
	if raw := options[WaffoUnitPriceOption]; raw != "" {
		if raw != strings.TrimSpace(raw) || len(raw) > 64 {
			return WaffoConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoUnitPriceOption)
		}
		unitPrice, err = strconv.ParseFloat(raw, 64)
		if err != nil || unitPrice <= 0 || unitPrice > maxBillingUnitPrice || math.IsNaN(unitPrice) || math.IsInf(unitPrice, 0) {
			return WaffoConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoUnitPriceOption)
		}
	}
	minimum := int64(1)
	if raw := options[WaffoMinTopUpOption]; raw != "" {
		if raw != strings.TrimSpace(raw) || len(raw) > 32 {
			return WaffoConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoMinTopUpOption)
		}
		minimum, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || minimum <= 0 || minimum > MaxTopUpReferenceAmount {
			return WaffoConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoMinTopUpOption)
		}
	}

	methods, err := parseWaffoPayMethods(options[WaffoPayMethodsOption])
	if err != nil {
		return WaffoConfig{}, err
	}
	config := WaffoConfig{
		Enabled: enabled, Sandbox: sandbox, MerchantID: merchantID,
		NotifyURL: notifyURL, ReturnURL: returnURL, Currency: currency,
		UnitPrice: unitPrice, MinTopUp: minimum, PayMethods: methods,
		APIBaseURL: WaffoProductionBaseURL,
	}
	if sandbox {
		config.APIKey = options[WaffoSandboxAPIKeyOption]
		config.PrivateKey = options[WaffoSandboxPrivateKeyOption]
		config.PublicKey = options[WaffoSandboxPublicCertOption]
		config.APIBaseURL = WaffoSandboxBaseURL
	} else {
		config.APIKey = options[WaffoAPIKeyOption]
		config.PrivateKey = options[WaffoPrivateKeyOption]
		config.PublicKey = options[WaffoPublicCertOption]
	}
	return config, nil
}

func parseWaffoPayMethods(raw string) ([]WaffoPayMethod, error) {
	if len(raw) > maxWaffoPayMethodsJSON {
		return nil, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoPayMethodsOption)
	}
	if raw == "" {
		return append([]WaffoPayMethod(nil), defaultWaffoPayMethods...), nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoPayMethodsOption)
	}
	var methods []WaffoPayMethod
	if err := jsonutil.Unmarshal([]byte(raw), &methods); err != nil || methods == nil || len(methods) > maxWaffoPayMethods {
		return nil, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, WaffoPayMethodsOption)
	}
	for _, method := range methods {
		if !validWaffoPlainValue(method.Name, maxWaffoDisplayName, false) ||
			!validWaffoPlainValue(method.Icon, maxWaffoURLBytes, true) ||
			!validWaffoMethodType(method.PayMethodType) ||
			(method.PayMethodName != "" && !validWaffoMethodToken(method.PayMethodName)) {
			return nil, fmt.Errorf("%w: %s entry", ErrInvalidPaymentSetting, WaffoPayMethodsOption)
		}
	}
	return append([]WaffoPayMethod(nil), methods...), nil
}

func strictWaffoBool(raw string, fallback bool) (bool, error) {
	if raw == "" {
		return fallback, nil
	}
	if raw == "true" {
		return true, nil
	}
	if raw == "false" {
		return false, nil
	}
	return false, ErrInvalidPaymentSetting
}

func validWaffoCredential(value string) bool {
	return value == "" || (len(value) <= maxWaffoCredentialBytes && value == strings.TrimSpace(value) && !waffoContainsControl(value))
}

func validWaffoPrivateKey(encoded string) bool {
	der, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(der) == 0 || len(der) > maxWaffoCredentialBytes {
		return false
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	rsaKey, ok := key.(*rsa.PrivateKey)
	return err == nil && ok && rsaKey.N.BitLen() >= 2048 && rsaKey.Validate() == nil
}

func validWaffoPublicKey(encoded string) bool {
	der, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(der) == 0 || len(der) > maxWaffoCredentialBytes {
		return false
	}
	key, err := x509.ParsePKIXPublicKey(der)
	rsaKey, ok := key.(*rsa.PublicKey)
	return err == nil && ok && rsaKey.N.BitLen() >= 2048 && rsaKey.E >= 3
}

func validWaffoCurrency(value string) bool {
	if len(value) != 3 || value != strings.TrimSpace(value) {
		return false
	}
	for i := range len(value) {
		char := value[i]
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')) {
			return false
		}
	}
	return true
}

func validWaffoPlainValue(value string, limit int, emptyOK bool) bool {
	if value == "" {
		return emptyOK
	}
	return len(value) <= limit && value == strings.TrimSpace(value) && !waffoContainsControl(value)
}

func validWaffoMethodType(value string) bool {
	if value == "" || len(value) > maxWaffoMethodString {
		return false
	}
	parts := strings.Split(value, ",")
	if len(parts) > 8 {
		return false
	}
	for _, part := range parts {
		if !validWaffoMethodToken(part) {
			return false
		}
	}
	return true
}

func validWaffoMethodToken(value string) bool {
	if value == "" || len(value) > maxWaffoMethodString {
		return false
	}
	for i := range len(value) {
		char := value[i]
		if !((char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

// ValidWaffoCallbackURL validates operator-controlled return and notification
// destinations. HTTP remains allowed for local deployments, while ambiguous
// authority, credential, fragment, and control-character forms are rejected.
func ValidWaffoCallbackURL(raw string) bool {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > maxWaffoURLBytes || waffoContainsControl(raw) {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return false
	}
	if parsed.Scheme == "http" && !isLoopbackWaffoHost(parsed.Hostname()) {
		return false
	}
	return true
}

func isLoopbackWaffoHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func waffoContainsControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func isWaffoOptionKey(key string) bool {
	switch key {
	case WaffoEnabledOption, WaffoAPIKeyOption, WaffoPrivateKeyOption, WaffoPublicCertOption,
		WaffoSandboxPublicCertOption, WaffoSandboxAPIKeyOption, WaffoSandboxPrivateKeyOption,
		WaffoSandboxOption, WaffoMerchantIDOption, WaffoNotifyURLOption, WaffoReturnURLOption,
		WaffoCurrencyOption, WaffoUnitPriceOption, WaffoMinTopUpOption, WaffoPayMethodsOption:
		return true
	default:
		return false
	}
}
