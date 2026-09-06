package router_test

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func smtpUpdateBody(t *testing.T, overrides map[string]any) string {
	t.Helper()
	values := map[string]any{
		"SMTPServer":             "smtp.initial.example",
		"SMTPPort":               "465",
		"SMTPAccount":            "mailer@example.com",
		"SMTPFrom":               "no-reply@example.com",
		"SMTPSSLEnabled":         true,
		"SMTPStartTLSEnabled":    false,
		"SMTPInsecureSkipVerify": false,
		"SMTPForceAuthLogin":     false,
	}
	for key, value := range overrides {
		values[key] = value
	}
	encoded, err := json.Marshal(values)
	require.NoError(t, err)
	return string(encoded)
}

func TestRootSMTPAtomicUpdateTransitionsTLSAndPreservesSecret(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	require.NoError(t, setting.Init())

	const secret = "smtp-option-secret"
	response := do(http.MethodPut, "/api/option/smtp", smtpUpdateBody(t, map[string]any{
		"SMTPToken": secret,
	}))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, true, decodeBody(t, response)["success"])
	assert.NotContains(t, response.Body.String(), secret)
	assert.Contains(t, response.Body.String(), `"SMTPToken":""`)
	assert.Contains(t, response.Body.String(), `"SMTPTokenRedacted":true`)
	initial := setting.GetOperationsSetting().SMTP
	assert.True(t, initial.SSLEnabled)
	assert.False(t, initial.StartTLSEnabled)
	assert.Equal(t, secret, initial.Token)

	// The complete value domain is updated in one transaction. The omitted
	// write-only placeholder preserves the token while both TLS mode bits flip.
	response = do(http.MethodPut, "/api/option/smtp", smtpUpdateBody(t, map[string]any{
		"SMTPServer":          "smtp.next.example",
		"SMTPPort":            "587",
		"SMTPSSLEnabled":      false,
		"SMTPStartTLSEnabled": true,
		"SMTPForceAuthLogin":  true,
	}))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, true, decodeBody(t, response)["success"])
	transitioned := setting.GetOperationsSetting().SMTP
	assert.Equal(t, "smtp.next.example", transitioned.Server)
	assert.False(t, transitioned.SSLEnabled)
	assert.True(t, transitioned.StartTLSEnabled)
	assert.Equal(t, secret, transitioned.Token)
	assert.Equal(t, secret, setting.GetOption(setting.SMTPTokenOption))

	options := do(http.MethodGet, "/api/option/", "")
	require.Equal(t, http.StatusOK, options.Code, options.Body.String())
	assert.NotContains(t, options.Body.String(), secret)
	assert.Contains(t, options.Body.String(), `"key":"SMTPToken","value":"","redacted":true`)
	assert.Contains(t, options.Body.String(), setting.DefaultCollapseSidebarOption)

	// An invalid transition publishes and persists nothing.
	response = do(http.MethodPut, "/api/option/smtp", smtpUpdateBody(t, map[string]any{
		"SMTPServer":          "smtp.invalid.example",
		"SMTPPort":            "587",
		"SMTPSSLEnabled":      true,
		"SMTPStartTLSEnabled": true,
	}))
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, false, decodeBody(t, response)["success"])
	assert.Equal(t, transitioned, setting.GetOperationsSetting().SMTP)
	assert.Equal(t, "smtp.next.example", setting.GetOption(setting.SMTPServerOption))

	// Clearing a token is explicit and coherent with clearing the account.
	response = do(http.MethodPut, "/api/option/smtp", smtpUpdateBody(t, map[string]any{
		"SMTPServer":          "smtp.next.example",
		"SMTPPort":            "587",
		"SMTPAccount":         "",
		"SMTPSSLEnabled":      false,
		"SMTPStartTLSEnabled": true,
		"clear_smtp_token":    true,
	}))
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, true, decodeBody(t, response)["success"])
	assert.Empty(t, setting.GetOperationsSetting().SMTP.Token)
	assert.Empty(t, setting.GetOption(setting.SMTPTokenOption))
}

func TestRootSMTPAtomicUpdateIsStrictAndBounded(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	require.NoError(t, setting.Init())

	missing := do(http.MethodPut, "/api/option/smtp", `{"SMTPServer":"smtp.example.com"}`)
	assert.Equal(t, http.StatusBadRequest, missing.Code)

	unknown := do(http.MethodPut, "/api/option/smtp", smtpUpdateBody(t, map[string]any{"unexpected": true}))
	assert.Equal(t, http.StatusBadRequest, unknown.Code)
	caseVariant := do(http.MethodPut, "/api/option/smtp", `{
		"smtpserver":"smtp.example.com","SMTPPort":"465","SMTPAccount":"","SMTPFrom":"no-reply@example.com",
		"SMTPSSLEnabled":true,"SMTPStartTLSEnabled":false,"SMTPInsecureSkipVerify":false,"SMTPForceAuthLogin":false
	}`)
	assert.Equal(t, http.StatusBadRequest, caseVariant.Code)
	duplicate := do(http.MethodPut, "/api/option/smtp", `{
		"SMTPServer":"smtp.example.com","SMTPServer":"attacker.example.com","SMTPPort":"465",
		"SMTPAccount":"","SMTPFrom":"no-reply@example.com","SMTPSSLEnabled":true,
		"SMTPStartTLSEnabled":false,"SMTPInsecureSkipVerify":false,"SMTPForceAuthLogin":false
	}`)
	assert.Equal(t, http.StatusBadRequest, duplicate.Code)

	conflictingSecret := do(http.MethodPut, "/api/option/smtp", smtpUpdateBody(t, map[string]any{
		"SMTPToken":        "secret",
		"clear_smtp_token": true,
	}))
	assert.Equal(t, http.StatusBadRequest, conflictingSecret.Code)

	oversized := do(http.MethodPut, "/api/option/smtp", smtpUpdateBody(t, map[string]any{
		"SMTPToken": strings.Repeat("x", (16<<10)+1),
	}))
	assert.Equal(t, http.StatusBadRequest, oversized.Code)
	assert.Empty(t, setting.GetOption(setting.SMTPServerOption))
}

func TestSMTPAtomicUpdateRequiresRootAndStatusExposesOnlyCollapseBehavior(t *testing.T) {
	handler, do, _ := setupChannelRead(t, roles.RoleAdminUser)
	require.NoError(t, setting.Init())
	response := do(http.MethodPut, "/api/option/smtp", smtpUpdateBody(t, nil))
	assert.Equal(t, http.StatusForbidden, response.Code)

	require.NoError(t, setting.UpdateOption(setting.DefaultCollapseSidebarOption, "true"))
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	status := httptest.NewRecorder()
	handler.ServeHTTP(status, request)
	require.Equal(t, http.StatusOK, status.Code)
	assert.Contains(t, status.Body.String(), `"default_collapse_sidebar":true`)
	assert.NotContains(t, status.Body.String(), "SMTP")
}
