package logging

import (
	"bytes"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"log/slog"
	"strings"
	"testing"
)

func TestProcessLoggerRedactsCredentialsAcrossMessagesAndAttributes(t *testing.T) {
	var sink bytes.Buffer
	previous := Logger
	SetLogger(slog.New(slog.NewJSONHandler(&sink, nil)))
	t.Cleanup(func() { SetLogger(previous) })

	secrets := []string{
		"bearer-secret-value",
		"query-secret-value",
		"database-password",
		"sk-live-abcdefghijklmnop",
		"github_pat_abcdefghijklmnop",
		"AKIAABCDEFGHIJKLMNOP",
		"attribute-secret-value",
		"nested-secret-value",
		"redis-password-value",
		"json-access-token-value",
	}
	Logger.Error(
		"provider failed: Bearer "+secrets[0]+
			" https://user:"+secrets[2]+"@db.example.test/logs?access_token="+secrets[1]+
			" api_key="+secrets[3]+" "+secrets[4]+" "+secrets[5]+
			" redis://:"+secrets[8]+"@cache.example.test/0"+
			` {"access_token":"`+secrets[9]+`"}`,
		"authorization", secrets[6],
		"err", errors.New("password="+secrets[6]),
		"payload", map[string]any{"flow_token": secrets[7], "detail": "Bearer " + secrets[0]},
	)

	logged := sink.String()
	require.NotEmpty(t, logged)
	assert.Contains(t, logged, "[REDACTED")
	for _, secret := range secrets {
		assert.NotContains(t, logged, secret)
	}
}

func TestProcessLoggerBoundsUntrustedText(t *testing.T) {
	var sink bytes.Buffer
	previous := Logger
	SetLogger(slog.New(slog.NewJSONHandler(&sink, nil)))
	t.Cleanup(func() { SetLogger(previous) })

	Logger.Error(strings.Repeat("x", maxLogTextBytes*2))
	logged := sink.String()
	assert.Contains(t, logged, "[truncated]")
	assert.Less(t, len(logged), maxLogTextBytes+1024)
}
