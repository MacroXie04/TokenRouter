package settings

import (
	"errors"
	mailtransport "github.com/tokenrouter/tokenrouter/internal/platform/mail"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	SMTPServerOption             = "SMTPServer"
	SMTPPortOption               = "SMTPPort"
	SMTPAccountOption            = "SMTPAccount"
	SMTPFromOption               = "SMTPFrom"
	SMTPTokenOption              = "SMTPToken"
	SMTPSSLEnabledOption         = "SMTPSSLEnabled"
	SMTPStartTLSEnabledOption    = "SMTPStartTLSEnabled"
	SMTPInsecureSkipVerifyOption = "SMTPInsecureSkipVerify"
	SMTPForceAuthLoginOption     = "SMTPForceAuthLogin"

	defaultSMTPPort        = 587
	maxSMTPAccountBytes    = 512
	maxSMTPFromBytes       = 320
	maxSMTPTokenBytes      = 4096
	maxSMTPServerNameBytes = 253
)

// SMTPSetting preserves the persisted option shape while the SMTP transport
// owns its configuration type and delivery invariants.
type SMTPSetting = mailtransport.Config

var (
	ErrSMTPNotConfigured         = mailtransport.ErrSMTPNotConfigured
	ErrSMTPIncompleteCredentials = mailtransport.ErrSMTPIncompleteCredentials
)

func validSMTPText(value string, maximumBytes int, allowEmpty, requireTrimmed bool) bool {
	if (!allowEmpty && value == "") || len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	if requireTrimmed && value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == 0x061c || character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

func parseSMTPPort(values map[string]string) (int, error) {
	raw, present := values[SMTPPortOption]
	if !present {
		return defaultSMTPPort, nil
	}
	if raw == "" || raw != strings.TrimSpace(raw) {
		return 0, errors.New("SMTPPort must be an integer from 1 to 65535")
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != raw {
		return 0, errors.New("SMTPPort must be an integer from 1 to 65535")
	}
	return port, nil
}

// ParseSMTPSetting parses a complete SMTP value domain. It is exported so the
// service layer can validate an environment override with exactly the same
// primitive and cross-field rules as persisted options.
func ParseSMTPSetting(values map[string]string) (SMTPSetting, error) {
	port, err := parseSMTPPort(values)
	if err != nil {
		return SMTPSetting{}, err
	}
	sslEnabled, err := parseOperationsBool(values, SMTPSSLEnabledOption, false)
	if err != nil {
		return SMTPSetting{}, err
	}
	startTLSEnabled, err := parseOperationsBool(values, SMTPStartTLSEnabledOption, false)
	if err != nil {
		return SMTPSetting{}, err
	}
	insecureSkipVerify, err := parseOperationsBool(values, SMTPInsecureSkipVerifyOption, false)
	if err != nil {
		return SMTPSetting{}, err
	}
	forceAuthLogin, err := parseOperationsBool(values, SMTPForceAuthLoginOption, false)
	if err != nil {
		return SMTPSetting{}, err
	}

	smtp := SMTPSetting{
		Server:             values[SMTPServerOption],
		Port:               port,
		Account:            values[SMTPAccountOption],
		From:               values[SMTPFromOption],
		Token:              values[SMTPTokenOption],
		SSLEnabled:         sslEnabled,
		StartTLSEnabled:    startTLSEnabled,
		InsecureSkipVerify: insecureSkipVerify,
		ForceAuthLogin:     forceAuthLogin,
	}
	if !validSMTPText(smtp.Server, maxSMTPServerNameBytes, true, true) ||
		(smtp.Server != "" && !validDNSName(smtp.Server)) {
		return SMTPSetting{}, errors.New("SMTPServer must be a valid host name or IP address")
	}
	if !validSMTPText(smtp.Account, maxSMTPAccountBytes, true, true) {
		return SMTPSetting{}, errors.New("SMTPAccount is invalid")
	}
	if !validSMTPText(smtp.From, maxSMTPFromBytes, true, true) {
		return SMTPSetting{}, errors.New("SMTPFrom is invalid")
	}
	if smtp.From != "" {
		if err := mailtransport.ValidateSender(smtp.From); err != nil {
			return SMTPSetting{}, err
		}
	}
	if !validSMTPText(smtp.Token, maxSMTPTokenBytes, true, false) {
		return SMTPSetting{}, errors.New("SMTPToken is invalid")
	}
	if smtp.SSLEnabled && smtp.StartTLSEnabled {
		return SMTPSetting{}, errors.New("SMTP SSL/TLS and STARTTLS cannot both be enabled")
	}
	if smtp.InsecureSkipVerify && !smtp.UsesTLS() {
		return SMTPSetting{}, errors.New("SMTP certificate verification can only be disabled for a TLS transport")
	}
	if smtp.Server != "" {
		if err := smtp.ValidateRunnable(); err != nil {
			return SMTPSetting{}, err
		}
	}
	return smtp, nil
}

// SMTPOptionDefaults contains only the SMTP configuration domain.
func SMTPOptionDefaults() map[string]string {
	return map[string]string{
		SMTPServerOption:             "",
		SMTPPortOption:               strconv.Itoa(defaultSMTPPort),
		SMTPAccountOption:            "",
		SMTPFromOption:               "",
		SMTPTokenOption:              "",
		SMTPSSLEnabledOption:         "false",
		SMTPStartTLSEnabledOption:    "false",
		SMTPInsecureSkipVerifyOption: "false",
		SMTPForceAuthLoginOption:     "false",
	}
}
