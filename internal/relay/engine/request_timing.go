package engine

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"sync"
	"time"
)

const (
	defaultRelayRequestTimeout = 10 * time.Minute
	maxRelayRequestTimeout     = 24 * time.Hour
)

type firstResponseWriter struct {
	gin.ResponseWriter
	once         sync.Once
	writeErrOnce sync.Once
	at           time.Time
	writeErr     error
}

func (writer *firstResponseWriter) Write(data []byte) (int, error) {
	writer.mark()
	n, err := writer.ResponseWriter.Write(data)
	writer.recordWriteError(err)
	return n, err
}

func (writer *firstResponseWriter) WriteString(data string) (int, error) {
	writer.mark()
	n, err := writer.ResponseWriter.WriteString(data)
	writer.recordWriteError(err)
	return n, err
}

func (writer *firstResponseWriter) mark() {
	writer.once.Do(func() { writer.at = time.Now() })
}

func (writer *firstResponseWriter) recordWriteError(err error) {
	if err != nil {
		writer.writeErrOnce.Do(func() { writer.writeErr = err })
	}
}

func relayRequestTimeout() time.Duration {
	seconds := env.GetEnvInt("RELAY_TIMEOUT", int(defaultRelayRequestTimeout/time.Second))
	if seconds <= 0 {
		return defaultRelayRequestTimeout
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout <= 0 || timeout > maxRelayRequestTimeout {
		return maxRelayRequestTimeout
	}
	return timeout
}

func applyRelayRequestDeadline(c *gin.Context) context.CancelFunc {
	ctx, cancel := WithRequestDeadline(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	return cancel
}

// WithRequestDeadline applies the bounded relay lifetime to the caller's context.
// The HTTP adapter calls it before decoding; durable accounting retains its
// independent database context when provider work is cancelled.
func WithRequestDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, relayRequestTimeout())
}
