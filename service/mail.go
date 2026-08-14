package service

import (
	"fmt"
	"net/smtp"
	"strings"

	"github.com/tokenrouter/tokenrouter/common"
)

// Mailer sends transactional email. A no-op is used when SMTP is unconfigured,
// and tests can inject a mock.
type Mailer interface {
	Send(to, subject, body string) error
}

// Mail is the process-wide mailer.
var Mail Mailer = &noopMailer{}

// InitMailer configures the SMTP mailer from environment. When SMTP_HOST is
// unset, email stays a no-op (no delivery, no error) so the gateway runs
// without external services.
func InitMailer() {
	host := common.GetEnv("SMTP_HOST", "")
	if host == "" {
		return
	}
	Mail = &smtpMailer{
		host: host,
		port: common.GetEnv("SMTP_PORT", "587"),
		user: common.GetEnv("SMTP_USER", ""),
		pass: common.GetEnv("SMTP_PASSWORD", ""),
		from: common.GetEnv("SMTP_FROM", "TokenRouter <noreply@localhost>"),
	}
}

type noopMailer struct{}

func (noopMailer) Send(to, subject, body string) error { return nil }

type smtpMailer struct {
	host, port, user, pass, from string
}

func (m *smtpMailer) Send(to, subject, body string) error {
	addr := m.host + ":" + m.port
	msg := buildMessage(m.from, to, subject, body)
	var auth smtp.Auth
	if m.user != "" {
		auth = smtp.PlainAuth("", m.user, m.pass, m.host)
	}
	return smtp.SendMail(addr, auth, m.from, []string{to}, []byte(msg))
}

func buildMessage(from, to, subject, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(body)
	return b.String()
}
