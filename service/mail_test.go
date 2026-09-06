package service

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

type smtpTestTranscript struct {
	mu            sync.Mutex
	startTLS      bool
	secure        bool
	authMechanism string
	authUsername  string
	authPassword  string
	mailFrom      string
	recipient     string
	message       string
}

func (transcript *smtpTestTranscript) update(fn func(*smtpTestTranscript)) {
	transcript.mu.Lock()
	defer transcript.mu.Unlock()
	fn(transcript)
}

func (transcript *smtpTestTranscript) snapshot() smtpTestTranscript {
	transcript.mu.Lock()
	defer transcript.mu.Unlock()
	return smtpTestTranscript{
		startTLS:      transcript.startTLS,
		secure:        transcript.secure,
		authMechanism: transcript.authMechanism,
		authUsername:  transcript.authUsername,
		authPassword:  transcript.authPassword,
		mailFrom:      transcript.mailFrom,
		recipient:     transcript.recipient,
		message:       transcript.message,
	}
}

type smtpTestServer struct {
	listener          net.Listener
	certificate       tls.Certificate
	implicitTLS       bool
	advertiseSTARTTLS bool
	transcript        smtpTestTranscript
	done              chan error
}

func newSMTPTestCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Unix(1_700_000_000, 0),
		NotAfter:     time.Unix(2_000_000_000, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)
	key, err := x509.MarshalECPrivateKey(privateKey)
	require.NoError(t, err)
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: key}),
	)
	require.NoError(t, err)
	return certificate
}

func newSMTPTestServer(t *testing.T, implicitTLS, advertiseSTARTTLS bool) *smtpTestServer {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	server := &smtpTestServer{
		listener:          listener,
		certificate:       newSMTPTestCertificate(t),
		implicitTLS:       implicitTLS,
		advertiseSTARTTLS: advertiseSTARTTLS,
		done:              make(chan error, 1),
	}
	go func() { server.done <- server.serve() }()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func (server *smtpTestServer) port(t *testing.T) int {
	t.Helper()
	address, ok := server.listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return address.Port
}

func writeSMTPTestLine(writer *bufio.Writer, line string) error {
	if _, err := writer.WriteString(line + "\r\n"); err != nil {
		return err
	}
	return writer.Flush()
}

func readSMTPTestLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), err
}

func smtpCommandArgument(line string) string {
	if index := strings.IndexByte(line, ' '); index >= 0 {
		return line[index+1:]
	}
	return ""
}

func (server *smtpTestServer) serve() error {
	connection, err := server.listener.Accept()
	if err != nil {
		return err
	}
	defer connection.Close()
	secure := false
	if server.implicitTLS {
		connection = tls.Server(connection, &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{server.certificate},
		})
		if err := connection.(*tls.Conn).Handshake(); err != nil {
			return err
		}
		secure = true
	}
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	if err := writeSMTPTestLine(writer, "220 smtp.test ESMTP ready"); err != nil {
		return err
	}
	for {
		line, err := readSMTPTestLine(reader)
		if err != nil {
			return err
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO "), strings.HasPrefix(upper, "HELO "):
			if !secure && server.advertiseSTARTTLS {
				if _, err := writer.WriteString("250-smtp.test\r\n250 STARTTLS\r\n"); err != nil {
					return err
				}
			} else if secure {
				if _, err := writer.WriteString("250-smtp.test\r\n250 AUTH LOGIN PLAIN\r\n"); err != nil {
					return err
				}
			} else if err := writeSMTPTestLine(writer, "250 smtp.test"); err != nil {
				return err
			}
			if err := writer.Flush(); err != nil {
				return err
			}
		case upper == "STARTTLS":
			if !server.advertiseSTARTTLS || secure {
				if err := writeSMTPTestLine(writer, "454 TLS unavailable"); err != nil {
					return err
				}
				continue
			}
			if err := writeSMTPTestLine(writer, "220 Begin TLS"); err != nil {
				return err
			}
			secureConnection := tls.Server(connection, &tls.Config{
				MinVersion:   tls.VersionTLS12,
				Certificates: []tls.Certificate{server.certificate},
			})
			if err := secureConnection.Handshake(); err != nil {
				return err
			}
			connection = secureConnection
			reader = bufio.NewReader(connection)
			writer = bufio.NewWriter(connection)
			secure = true
			server.transcript.update(func(transcript *smtpTestTranscript) {
				transcript.startTLS = true
				transcript.secure = true
			})
		case strings.HasPrefix(upper, "AUTH LOGIN"):
			parts := strings.Fields(line)
			username := ""
			if len(parts) == 3 {
				decoded, decodeErr := base64.StdEncoding.DecodeString(parts[2])
				if decodeErr != nil {
					return decodeErr
				}
				username = string(decoded)
			} else {
				if err := writeSMTPTestLine(writer, "334 VXNlcm5hbWU6"); err != nil {
					return err
				}
				encoded, err := readSMTPTestLine(reader)
				if err != nil {
					return err
				}
				decoded, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					return err
				}
				username = string(decoded)
			}
			if err := writeSMTPTestLine(writer, "334 UGFzc3dvcmQ6"); err != nil {
				return err
			}
			encodedPassword, err := readSMTPTestLine(reader)
			if err != nil {
				return err
			}
			password, err := base64.StdEncoding.DecodeString(encodedPassword)
			if err != nil {
				return err
			}
			server.transcript.update(func(transcript *smtpTestTranscript) {
				transcript.authMechanism = "LOGIN"
				transcript.authUsername = username
				transcript.authPassword = string(password)
			})
			if err := writeSMTPTestLine(writer, "235 authenticated"); err != nil {
				return err
			}
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			parts := strings.Fields(line)
			if len(parts) != 3 {
				return errors.New("PLAIN auth did not include an initial response")
			}
			decoded, err := base64.StdEncoding.DecodeString(parts[2])
			if err != nil {
				return err
			}
			credentials := strings.Split(string(decoded), "\x00")
			if len(credentials) != 3 {
				return errors.New("invalid PLAIN auth response")
			}
			server.transcript.update(func(transcript *smtpTestTranscript) {
				transcript.authMechanism = "PLAIN"
				transcript.authUsername = credentials[1]
				transcript.authPassword = credentials[2]
			})
			if err := writeSMTPTestLine(writer, "235 authenticated"); err != nil {
				return err
			}
		case strings.HasPrefix(upper, "MAIL FROM:"):
			server.transcript.update(func(transcript *smtpTestTranscript) {
				transcript.mailFrom = strings.TrimSpace(line[len("MAIL FROM:"):])
			})
			if err := writeSMTPTestLine(writer, "250 sender accepted"); err != nil {
				return err
			}
		case strings.HasPrefix(upper, "RCPT TO:"):
			server.transcript.update(func(transcript *smtpTestTranscript) {
				transcript.recipient = strings.TrimSpace(line[len("RCPT TO:"):])
			})
			if err := writeSMTPTestLine(writer, "250 recipient accepted"); err != nil {
				return err
			}
		case upper == "DATA":
			if err := writeSMTPTestLine(writer, "354 end with a single dot"); err != nil {
				return err
			}
			var message strings.Builder
			for {
				dataLine, err := readSMTPTestLine(reader)
				if err != nil {
					return err
				}
				if dataLine == "." {
					break
				}
				message.WriteString(dataLine)
				message.WriteString("\r\n")
			}
			server.transcript.update(func(transcript *smtpTestTranscript) { transcript.message = message.String() })
			if err := writeSMTPTestLine(writer, "250 queued"); err != nil {
				return err
			}
		case upper == "QUIT":
			if err := writeSMTPTestLine(writer, "221 bye"); err != nil {
				return err
			}
			return nil
		default:
			return fmt.Errorf("unexpected SMTP command %q", smtpCommandArgument(line))
		}
	}
}

func (server *smtpTestServer) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-server.done:
		return err
	case <-time.After(2 * time.Second):
		return errors.New("SMTP test server did not finish")
	}
}

func clearSMTPEnvironment(t *testing.T) {
	t.Helper()
	seen := make(map[string]struct{})
	for _, aliases := range smtpEnvironmentAliases {
		for _, key := range aliases {
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			value, present := os.LookupEnv(key)
			require.NoError(t, os.Unsetenv(key))
			t.Cleanup(func() {
				if present {
					_ = os.Setenv(key, value)
				} else {
					_ = os.Unsetenv(key)
				}
			})
		}
	}
}

func setupSMTPSettingTest(t *testing.T) {
	t.Helper()
	clearSMTPEnvironment(t)
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "smtp.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Option{}))
	previous := model.DB
	model.DB = database
	require.NoError(t, setting.Init())
	t.Cleanup(func() {
		_ = setting.UpdateOptions(setting.OperationsOptionDefaults())
		model.DB = previous
	})
}

func TestEffectiveSMTPSettingUsesWholeEnvironmentDomain(t *testing.T) {
	setupSMTPSettingTest(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.SMTPServerOption:     "option.example.com",
		setting.SMTPPortOption:       "465",
		setting.SMTPAccountOption:    "option-user",
		setting.SMTPFromOption:       "option@example.com",
		setting.SMTPTokenOption:      "option-secret",
		setting.SMTPSSLEnabledOption: "true",
	}))

	configured, enabled, err := EffectiveSMTPSetting()
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.Equal(t, "option.example.com", configured.Server)
	assert.Equal(t, "option-secret", configured.Token)

	t.Setenv("SMTP_HOST", "env.example.com")
	t.Setenv("SMTP_PORT", "465")
	t.Setenv("SMTP_USER", "env-user")
	t.Setenv("SMTP_PASSWORD", "env-secret")
	t.Setenv("SMTP_FROM", "env@example.com")
	configured, enabled, err = EffectiveSMTPSetting()
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.Equal(t, "env.example.com", configured.Server)
	assert.Equal(t, "env-secret", configured.Token)
	assert.False(t, configured.SSLEnabled, "absent environment fields use environment defaults, never option values")

	t.Setenv("SMTP_PASSWORD", "")
	_, enabled, err = EffectiveSMTPSetting()
	assert.False(t, enabled)
	assert.ErrorIs(t, err, setting.ErrSMTPIncompleteCredentials)
	assert.NotContains(t, err.Error(), "option-secret")

	t.Setenv("SMTP_SERVER", "conflict.example.com")
	_, enabled, err = EffectiveSMTPSetting()
	assert.False(t, enabled)
	assert.ErrorContains(t, err, "conflicting SMTP environment aliases")
}

func TestSendSMTPRequiresAndUsesSTARTTLSWithLoginAuthentication(t *testing.T) {
	server := newSMTPTestServer(t, false, true)
	config := setting.SMTPSetting{
		Server: "127.0.0.1", Port: server.port(t), Account: "mailer@example.com",
		From: "no-reply@example.com", Token: "test-secret", StartTLSEnabled: true,
		InsecureSkipVerify: true, ForceAuthLogin: true,
	}
	require.NoError(t, sendSMTP(config, "person@example.com", "Verification ✓", "code: 123456", time.Second, 2*time.Second))
	require.NoError(t, server.wait(t))

	transcript := server.transcript.snapshot()
	assert.True(t, transcript.startTLS)
	assert.True(t, transcript.secure)
	assert.Equal(t, "LOGIN", transcript.authMechanism)
	assert.Equal(t, "mailer@example.com", transcript.authUsername)
	assert.Equal(t, "test-secret", transcript.authPassword)
	assert.Equal(t, "<no-reply@example.com>", transcript.mailFrom)
	assert.Equal(t, "<person@example.com>", transcript.recipient)
	assert.Contains(t, transcript.message, "Subject: =?utf-8?q?Verification_=E2=9C=93?=")
	assert.Contains(t, transcript.message, "code: 123456")
}

func TestSendSMTPUsesImplicitTLSAndPlainAuth(t *testing.T) {
	server := newSMTPTestServer(t, true, false)
	config := setting.SMTPSetting{
		Server: "127.0.0.1", Port: server.port(t), Account: "mailer@example.com",
		From: "no-reply@example.com", Token: "test-secret", SSLEnabled: true,
		InsecureSkipVerify: true,
	}
	require.NoError(t, sendSMTP(config, "person@example.com", "Notice", "body", time.Second, 2*time.Second))
	require.NoError(t, server.wait(t))

	transcript := server.transcript.snapshot()
	assert.Equal(t, "PLAIN", transcript.authMechanism)
	assert.Equal(t, "test-secret", transcript.authPassword)
}

func TestSendSMTPAllowsExplicitPlaintextOnlyOnLoopback(t *testing.T) {
	server := newSMTPTestServer(t, false, false)
	config := setting.SMTPSetting{
		Server: "127.0.0.1", Port: server.port(t), From: "no-reply@example.com",
	}
	require.NoError(t, sendSMTP(config, "person@example.com", "Local notice", "body", time.Second, 2*time.Second))
	require.NoError(t, server.wait(t))
	assert.Contains(t, server.transcript.snapshot().message, "Local notice")

	config.Server = "smtp.example.com"
	assert.ErrorContains(t, config.ValidateRunnable(), "non-loopback server requires SSL/TLS or STARTTLS")
}

func TestSMTPRuntimeMailerUsesHotReloadedOptionSnapshot(t *testing.T) {
	setupSMTPSettingTest(t)
	previousMailer := Mail
	InitMailer()
	t.Cleanup(func() { Mail = previousMailer })

	first := newSMTPTestServer(t, true, false)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.SMTPServerOption:             "127.0.0.1",
		setting.SMTPPortOption:               fmt.Sprintf("%d", first.port(t)),
		setting.SMTPFromOption:               "no-reply@example.com",
		setting.SMTPSSLEnabledOption:         "true",
		setting.SMTPStartTLSEnabledOption:    "false",
		setting.SMTPInsecureSkipVerifyOption: "true",
	}))
	require.NoError(t, Mail.Send("first@example.com", "First", "one"))
	require.NoError(t, first.wait(t))
	assert.Contains(t, first.transcript.snapshot().message, "First")

	second := newSMTPTestServer(t, true, false)
	require.NoError(t, setting.UpdateOption(setting.SMTPPortOption, fmt.Sprintf("%d", second.port(t))))
	require.NoError(t, Mail.Send("second@example.com", "Second", "two"))
	require.NoError(t, second.wait(t))
	assert.Contains(t, second.transcript.snapshot().message, "Second")
}

func TestSendSMTPNeverDowngradesRequestedTLS(t *testing.T) {
	server := newSMTPTestServer(t, false, false)
	config := setting.SMTPSetting{
		Server: "127.0.0.1", Port: server.port(t), From: "no-reply@example.com",
		StartTLSEnabled: true, InsecureSkipVerify: true,
	}
	err := sendSMTP(config, "person@example.com", "Notice", "body", time.Second, 2*time.Second)
	assert.ErrorContains(t, err, "does not advertise STARTTLS")
	assert.Empty(t, server.transcript.snapshot().message)

	trustedServer := newSMTPTestServer(t, false, true)
	config.Port = trustedServer.port(t)
	config.InsecureSkipVerify = false
	err = sendSMTP(config, "person@example.com", "Notice", "body", time.Second, 2*time.Second)
	assert.Error(t, err, "an untrusted certificate must fail when verification is enabled")
	assert.Empty(t, trustedServer.transcript.snapshot().message)
}

func TestSendSMTPBoundsInputsAndGreetingWait(t *testing.T) {
	config := setting.SMTPSetting{Server: "127.0.0.1", Port: 2525, From: "no-reply@example.com"}
	_, err := buildSMTPMessage(config.From, "one@example.com\r\nBcc: hidden@example.com", "safe", "body")
	assert.Error(t, err)
	_, err = buildSMTPMessage(config.From, "one@example.com", "unsafe\r\nBcc: hidden@example.com", "body")
	assert.Error(t, err)
	_, err = buildSMTPMessage(config.From, "one@example.com", "safe", strings.Repeat("x", maxSMTPBodyBytes+1))
	assert.Error(t, err)

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()
	config.Port = listener.Addr().(*net.TCPAddr).Port
	started := time.Now()
	err = sendSMTP(config, "one@example.com", "safe", "body", time.Second, 75*time.Millisecond)
	assert.Error(t, err)
	assert.Less(t, time.Since(started), time.Second)
	select {
	case connection := <-accepted:
		_ = connection.Close()
	case <-time.After(time.Second):
		t.Fatal("silent SMTP test server did not accept a connection")
	}
}
