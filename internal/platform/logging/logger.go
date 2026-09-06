package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Logger is the process-wide structured logger. JSON text output to stderr.
var Logger *slog.Logger

const maxLogTextBytes = 16 << 10

var logSecretPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)\b(?:Bearer|Basic)\s+[A-Za-z0-9._~+/=:-]+`), "credential [REDACTED]"},
	{regexp.MustCompile(`(?i)((?:"?(?:password|passwd|api[_ -]?key|access[_ -]?token|refresh[_ -]?token|client[_ -]?secret|webhook[_ -]?secret|authorization|cookie|signature|private[_ -]?key|recovery[_ -]?token|flow[_ -]?token)"?)\s*[:=]\s*["']?)[^\s,"';&]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)([?&](?:code|token|access_token|refresh_token|api_key|key|secret|signature|password)=)[^&\s]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^:/\s]*:)[^@\s]+(@)`), `${1}[REDACTED]${2}`},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), "[REDACTED_AWS_KEY]"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{12,}\b|\bgh[pousr]_[A-Za-z0-9]{12,}\b`), "[REDACTED_GITHUB_TOKEN]"},
	{regexp.MustCompile(`\bsk-(?:live|test|proj)[-_A-Za-z0-9]{8,}\b|\bsk-[A-Za-z0-9_-]{16,}\b`), "[REDACTED_API_KEY]"},
	{regexp.MustCompile(`\b[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`), "[REDACTED_JWT]"},
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), "[REDACTED_PRIVATE_KEY]"},
}

func init() {
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("DEBUG"), "true") {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	Logger = safeLogger(slog.New(slog.NewJSONHandler(os.Stderr, opts)))
}

// SetLogger overrides the global logger (used by tests).
func SetLogger(l *slog.Logger) {
	if l != nil {
		Logger = safeLogger(l)
	}
}

func safeLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return nil
	}
	if _, ok := logger.Handler().(*redactingLogHandler); ok {
		return logger
	}
	return slog.New(&redactingLogHandler{next: logger.Handler()})
}

type redactingLogHandler struct {
	next slog.Handler
}

func (handler *redactingLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

func (handler *redactingLogHandler) Handle(ctx context.Context, record slog.Record) error {
	safe := slog.NewRecord(record.Time, record.Level, redactLogText(record.Message), record.PC)
	record.Attrs(func(attribute slog.Attr) bool {
		safe.AddAttrs(redactLogAttr(attribute))
		return true
	})
	return handler.next.Handle(ctx, safe)
}

func (handler *redactingLogHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, 0, len(attributes))
	for _, attribute := range attributes {
		safe = append(safe, redactLogAttr(attribute))
	}
	return &redactingLogHandler{next: handler.next.WithAttrs(safe)}
}

func (handler *redactingLogHandler) WithGroup(name string) slog.Handler {
	return &redactingLogHandler{next: handler.next.WithGroup(name)}
}

func redactLogAttr(attribute slog.Attr) slog.Attr {
	attribute.Value = attribute.Value.Resolve()
	if sensitiveLogKey(attribute.Key) {
		return slog.String(attribute.Key, "[REDACTED]")
	}
	switch attribute.Value.Kind() {
	case slog.KindString:
		attribute.Value = slog.StringValue(redactLogText(attribute.Value.String()))
	case slog.KindAny:
		attribute.Value = slog.AnyValue(redactLogAny(attribute.Value.Any()))
	case slog.KindGroup:
		group := attribute.Value.Group()
		for index := range group {
			group[index] = redactLogAttr(group[index])
		}
		attribute.Value = slog.GroupValue(group...)
	}
	return attribute
}

func redactLogAny(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case error:
		return redactLogText(typed.Error())
	case fmt.Stringer:
		return redactLogText(typed.String())
	case string:
		return redactLogText(typed)
	case []string:
		result := make([]string, len(typed))
		for index := range typed {
			result[index] = redactLogText(typed[index])
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index := range typed {
			result[index] = redactLogAny(typed[index])
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveLogKey(key) {
				result[key] = "[REDACTED]"
			} else {
				result[key] = redactLogAny(item)
			}
		}
		return result
	default:
		return value
	}
}

func sensitiveLogKey(key string) bool {
	normalized := strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(strings.ToLower(strings.TrimSpace(key)))
	switch normalized {
	case "authorization", "proxy_authorization", "cookie", "set_cookie", "password", "passwd",
		"secret", "api_key", "apikey", "access_key", "access_token", "refresh_token",
		"client_secret", "webhook_secret", "private_key", "signature", "session",
		"session_id", "recovery_token", "flow_token", "channel_key", "stripe_secret_key":
		return true
	default:
		return false
	}
}

func redactLogText(message string) string {
	message = strings.ToValidUTF8(message, "�")
	for _, item := range logSecretPatterns {
		message = item.pattern.ReplaceAllString(message, item.replacement)
	}
	if len(message) <= maxLogTextBytes {
		return message
	}
	const suffix = "... [truncated]"
	cut := maxLogTextBytes - len(suffix)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + suffix
}

// RedactSensitiveText removes credential-shaped data from text crossing a
// trust boundary. Process logging and client-facing upstream errors share the
// same conservative vocabulary so a provider cannot reflect a secret that is
// safe in logs but exposed over HTTP.
func RedactSensitiveText(message string) string {
	return redactLogText(message)
}

// SysLog logs an informational system event.
func SysLog(msg string) {
	Logger.Info(msg)
}

// SysError logs an error-level system event. Used throughout billing paths so
// saturation/overflow anomalies are always visible in backend logs.
func SysError(msg string) {
	Logger.Error(msg)
}

// LogDebug logs a debug-level event.
func LogDebug(msg string, args ...any) {
	Logger.Debug(msg, args...)
}

// LogInfo logs an info-level event with structured fields.
func LogInfo(msg string, args ...any) {
	Logger.Info(msg, args...)
}

// LogError logs an error-level event with structured fields.
func LogError(msg string, args ...any) {
	Logger.Error(msg, args...)
}
