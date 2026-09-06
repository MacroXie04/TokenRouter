package mail

import (
	"errors"
	"net"
	"net/mail"
	"strings"
)

var (
	ErrSMTPNotConfigured         = errors.New("SMTP is not configured")
	ErrSMTPIncompleteCredentials = errors.New("SMTP account and token must either both be set or both be empty")
)

// Config is a coherent SMTP delivery configuration. It is
// immutable after publication so a delivery cannot observe half an option
// update or half a remote synchronization.
type Config struct {
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

// ValidateSender requires a single bare mailbox suitable for an SMTP envelope.
func ValidateSender(value string) error {
	if value == "" || value != strings.TrimSpace(value) || !validMailInput(value, maxSMTPRecipientBytes, false) {
		return errors.New("SMTPFrom must be a valid email address")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || address.Address != value || strings.Count(value, "@") != 1 {
		return errors.New("SMTPFrom must be a bare email address")
	}
	return nil
}

func (smtp Config) usesImplicitTLS() bool {
	return smtp.SSLEnabled || smtp.Port == 465 && !smtp.StartTLSEnabled
}

// UsesTLS reports whether transport encryption is required before any SMTP
// authentication is attempted.
func (smtp Config) UsesTLS() bool {
	return smtp.usesImplicitTLS() || smtp.StartTLSEnabled
}

// EnvelopeFrom returns the explicitly configured sender or the compatible
// account fallback. The returned value has already passed mailbox validation
// whenever ValidateRunnable succeeds.
func (smtp Config) EnvelopeFrom() string {
	if smtp.From != "" {
		return smtp.From
	}
	return smtp.Account
}

// ValidateRunnable applies cross-field constraints that are meaningful once a
// server is selected. Empty-server option sets may be staged one write-only
// field at a time, but a selected server must always be safe and complete.
func (smtp Config) ValidateRunnable() error {
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
	if err := ValidateSender(smtp.EnvelopeFrom()); err != nil {
		return err
	}
	return nil
}
