package setting

import (
	"errors"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync/atomic"
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
	DefaultCollapseSidebarOption = "DefaultCollapseSidebar"

	defaultSMTPPort        = 587
	maxSMTPAccountBytes    = 512
	maxSMTPFromBytes       = 320
	maxSMTPTokenBytes      = 4096
	maxSMTPServerNameBytes = 253
)

var operationsConfig atomic.Pointer[OperationsSetting]

var (
	ErrSMTPNotConfigured         = errors.New("SMTP is not configured")
	ErrSMTPIncompleteCredentials = errors.New("SMTP account and token must either both be set or both be empty")
)

// SMTPSetting is the coherent database-backed SMTP configuration. It is
// immutable after publication so a delivery cannot observe half an option
// update or half a remote synchronization.
type SMTPSetting struct {
	Server             string
	Port               int
	Account            string
	From               string
	Token              string
	SSLEnabled         bool
	StartTLSEnabled    bool
	InsecureSkipVerify bool
	ForceAuthLogin     bool
}

// OperationsSetting contains public behavior and delivery configuration that
// must change without restarting the process.
type OperationsSetting struct {
	SMTP                   SMTPSetting
	DefaultCollapseSidebar bool
}

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

func parseOperationsBool(values map[string]string, key string, fallback bool) (bool, error) {
	raw, present := values[key]
	if !present {
		return fallback, nil
	}
	if raw == "true" {
		return true, nil
	}
	if raw == "false" {
		return false, nil
	}
	return false, errors.New(key + " must be true or false")
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

func validateSMTPMailbox(value string) error {
	if !validSMTPText(value, maxSMTPFromBytes, false, true) {
		return errors.New("SMTPFrom must be a valid email address")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || address.Address != value || strings.Count(value, "@") != 1 {
		return errors.New("SMTPFrom must be a bare email address")
	}
	return nil
}

func (smtp SMTPSetting) usesImplicitTLS() bool {
	return smtp.SSLEnabled || smtp.Port == 465 && !smtp.StartTLSEnabled
}

// UsesTLS reports whether transport encryption is required before any SMTP
// authentication is attempted.
func (smtp SMTPSetting) UsesTLS() bool {
	return smtp.usesImplicitTLS() || smtp.StartTLSEnabled
}

// EnvelopeFrom returns the explicitly configured sender or the compatible
// account fallback. The returned value has already passed mailbox validation
// whenever ValidateRunnable succeeds.
func (smtp SMTPSetting) EnvelopeFrom() string {
	if smtp.From != "" {
		return smtp.From
	}
	return smtp.Account
}

// ValidateRunnable applies cross-field constraints that are meaningful once a
// server is selected. Empty-server option sets may be staged one write-only
// field at a time, but a selected server must always be safe and complete.
func (smtp SMTPSetting) ValidateRunnable() error {
	if smtp.Server == "" {
		return ErrSMTPNotConfigured
	}
	if smtp.SSLEnabled && smtp.StartTLSEnabled {
		return errors.New("SMTP SSL/TLS and STARTTLS cannot both be enabled")
	}
	if (smtp.Account == "") != (smtp.Token == "") {
		return ErrSMTPIncompleteCredentials
	}
	if smtp.ForceAuthLogin && smtp.Account == "" {
		return errors.New("SMTP LOGIN authentication requires an account and token")
	}
	serverAddress := net.ParseIP(strings.ToLower(smtp.Server))
	loopback := strings.EqualFold(smtp.Server, "localhost") ||
		serverAddress != nil && serverAddress.IsLoopback()
	if !smtp.UsesTLS() && !loopback {
		return errors.New("SMTP transport to a non-loopback server requires SSL/TLS or STARTTLS")
	}
	if smtp.Account != "" && !smtp.UsesTLS() {
		return errors.New("SMTP authentication requires SSL/TLS or STARTTLS")
	}
	if smtp.InsecureSkipVerify && !smtp.UsesTLS() {
		return errors.New("SMTP certificate verification can only be disabled for a TLS transport")
	}
	if err := validateSMTPMailbox(smtp.EnvelopeFrom()); err != nil {
		return err
	}
	return nil
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
		if err := validateSMTPMailbox(smtp.From); err != nil {
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

func buildOperationsSetting(values map[string]string) (OperationsSetting, error) {
	smtp, err := ParseSMTPSetting(values)
	if err != nil {
		return OperationsSetting{}, err
	}
	collapse, err := parseOperationsBool(values, DefaultCollapseSidebarOption, false)
	if err != nil {
		return OperationsSetting{}, err
	}
	return OperationsSetting{SMTP: smtp, DefaultCollapseSidebar: collapse}, nil
}

// GetOperationsSetting returns the current immutable operations snapshot.
func GetOperationsSetting() OperationsSetting {
	if current := operationsConfig.Load(); current != nil {
		return *current
	}
	fallback, _ := buildOperationsSetting(nil)
	return fallback
}

// OperationsOptionDefaults supplies the complete admin settings surface even
// before rows exist in the options table. SMTPToken must still be redacted by
// the controller rather than serialized from this map.
func OperationsOptionDefaults() map[string]string {
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
		DefaultCollapseSidebarOption: "false",
	}
}
