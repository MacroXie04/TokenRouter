package common

import (
	"log/slog"
	"os"
	"strings"
)

// Logger is the process-wide structured logger. JSON text output to stderr.
var Logger *slog.Logger

func init() {
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("DEBUG"), "true") {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	Logger = slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

// SetLogger overrides the global logger (used by tests).
func SetLogger(l *slog.Logger) {
	if l != nil {
		Logger = l
	}
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
