package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	smtpDialTimeout       = 10 * time.Second
	smtpDeliveryTimeout   = 30 * time.Second
	maxSMTPRecipientBytes = 320
	maxSMTPSubjectBytes   = 512
	maxSMTPBodyBytes      = 2 << 20
)

// Mailer sends transactional email. Tests can replace Mail with a deterministic
// implementation. Production uses a runtime mailer that reads one immutable
// settings snapshot for each delivery, so hot reload never requires a restart.
type Mailer interface {
	Send(to, subject, body string) error
}

// Mail is the process-wide mailer. It stays a no-op until InitMailer is called
// so package tests and tools that do not initialize the application remain
// independent of external services.
var Mail Mailer = &noopMailer{}

// InitMailer installs the hot-reloadable SMTP implementation. Configuration is
// resolved at delivery time rather than copied here because settings are loaded
// after this initializer during startup and may later be synchronized remotely.
func InitMailer(resolve ConfigResolver) {
	Mail = &smtpRuntimeMailer{resolve: resolve}
}

type noopMailer struct{}

func (noopMailer) Send(to, subject, body string) error { return nil }

// ConfigResolver supplies one immutable configuration snapshot per delivery.
type ConfigResolver func() (Config, bool, error)

type smtpRuntimeMailer struct{ resolve ConfigResolver }

func (mailer smtpRuntimeMailer) Send(to, subject, body string) error {
	if mailer.resolve == nil {
		return errors.New("SMTP configuration resolver is not installed")
	}
	configured, enabled, err := mailer.resolve()
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	return sendSMTP(configured, to, subject, body, smtpDialTimeout, smtpDeliveryTimeout)
}

func validMailInput(value string, maximumBytes int, allowLineBreaks bool) bool {
	if len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if allowLineBreaks && (character == '\r' || character == '\n' || character == '\t') {
			continue
		}
		if unicode.IsControl(character) || character == 0x061c || character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

func validateMailRecipient(value string) error {
	if value == "" || value != strings.TrimSpace(value) || !validMailInput(value, maxSMTPRecipientBytes, false) {
		return errors.New("email recipient is invalid")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || address.Address != value || strings.Count(value, "@") != 1 {
		return errors.New("email recipient must be one bare email address")
	}
	return nil
}

func buildSMTPMessage(from, to, subject, body string) ([]byte, error) {
	if err := validateMailRecipient(to); err != nil {
		return nil, err
	}
	if !validMailInput(subject, maxSMTPSubjectBytes, false) {
		return nil, errors.New("email subject exceeds safe limits")
	}
	if !validMailInput(body, maxSMTPBodyBytes, true) {
		return nil, errors.New("email body exceeds safe limits")
	}
	encodedSubject := mime.QEncoding.Encode("utf-8", subject)
	var message strings.Builder
	message.Grow(len(from) + len(to) + len(encodedSubject) + len(body) + 128)
	fmt.Fprintf(&message, "From: %s\r\n", from)
	fmt.Fprintf(&message, "To: %s\r\n", to)
	fmt.Fprintf(&message, "Subject: %s\r\n", encodedSubject)
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	message.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	message.WriteString(body)
	return []byte(message.String()), nil
}

func smtpTLSConfig(config Config) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         config.Server,
		InsecureSkipVerify: config.InsecureSkipVerify, // #nosec G402 -- explicit, redacted root-only compatibility setting.
	}
}

func dialSMTP(
	ctx context.Context,
	config Config,
	dialTimeout time.Duration,
) (*smtp.Client, net.Conn, error) {
	address := net.JoinHostPort(config.Server, fmt.Sprintf("%d", config.Port))
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, err
	}
	if deadline, present := ctx.Deadline(); present {
		if err := connection.SetDeadline(deadline); err != nil {
			_ = connection.Close()
			return nil, nil, err
		}
	}
	if config.SSLEnabled || config.Port == 465 && !config.StartTLSEnabled {
		secure := tls.Client(connection, smtpTLSConfig(config))
		if err := secure.HandshakeContext(ctx); err != nil {
			_ = connection.Close()
			return nil, nil, err
		}
		client, err := smtp.NewClient(secure, config.Server)
		if err != nil {
			_ = secure.Close()
			return nil, nil, err
		}
		return client, secure, nil
	}
	client, err := smtp.NewClient(connection, config.Server)
	if err != nil {
		_ = connection.Close()
		return nil, nil, err
	}
	if config.StartTLSEnabled {
		supported, _ := client.Extension("STARTTLS")
		if !supported {
			_ = client.Close()
			return nil, nil, errors.New("SMTP server does not advertise STARTTLS")
		}
		if err := client.StartTLS(smtpTLSConfig(config)); err != nil {
			_ = client.Close()
			return nil, nil, err
		}
	}
	return client, connection, nil
}

type smtpLoginAuth struct {
	username string
	password string
}

func (auth *smtpLoginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if server == nil || !server.TLS {
		return "", nil, errors.New("SMTP LOGIN requires TLS")
	}
	return "LOGIN", []byte(auth.username), nil
}

func (auth *smtpLoginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	return []byte(auth.password), nil
}

func smtpAuthentication(config Config) smtp.Auth {
	if config.Account == "" {
		return nil
	}
	if config.ForceAuthLogin {
		return &smtpLoginAuth{username: config.Account, password: config.Token}
	}
	return smtp.PlainAuth("", config.Account, config.Token, config.Server)
}

func sendSMTP(
	config Config,
	to, subject, body string,
	dialTimeout, deliveryTimeout time.Duration,
) error {
	if err := config.ValidateRunnable(); err != nil {
		return err
	}
	message, err := buildSMTPMessage(config.EnvelopeFrom(), to, subject, body)
	if err != nil {
		return err
	}
	if dialTimeout <= 0 || deliveryTimeout <= 0 {
		return errors.New("SMTP timeouts must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), deliveryTimeout)
	defer cancel()
	client, connection, err := dialSMTP(ctx, config, dialTimeout)
	if err != nil {
		return fmt.Errorf("connect to SMTP server: %w", err)
	}
	defer func() {
		_ = client.Close()
		_ = connection.Close()
	}()
	if deadline, present := ctx.Deadline(); present {
		if err := connection.SetDeadline(deadline); err != nil {
			return fmt.Errorf("set SMTP deadline: %w", err)
		}
	}
	if auth := smtpAuthentication(config); auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("authenticate with SMTP server: %w", err)
		}
	}
	if err := client.Mail(config.EnvelopeFrom()); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("start SMTP message: %w", err)
	}
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write SMTP message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish SMTP message: %w", err)
	}
	if err := client.Quit(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("finish SMTP session: %w", err)
	}
	return nil
}
