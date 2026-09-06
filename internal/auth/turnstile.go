package auth

import (
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxTurnstileResponseBytes = int64(64 << 10)

var turnstileHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext: httpx.SafeDialContext,
	},
	// The verification body contains the site secret. Never replay it to a
	// redirect target, even when a custom test/self-hosted endpoint is used.
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// turnstileVerifyURL is the Cloudflare siteverify endpoint (overridable in
// tests via TURNSTILE_VERIFY_URL).
func turnstileVerifyURL() string {
	return env.GetEnv("TURNSTILE_VERIFY_URL", "https://challenges.cloudflare.com/turnstile/v0/siteverify")
}

// TurnstileEnabled reports whether Turnstile bot protection is configured.
// The option store is authoritative (TurnstileCheckEnabled, matching the
// reference key); the legacy TurnstileEnabled key and the TURNSTILE_SECRET_KEY
// environment variable remain accepted for single-node deployments.
func TurnstileEnabled() bool {
	return setting.GetOptionBool(setting.TurnstileCheckEnabledOption, false) ||
		setting.GetOptionBool(setting.TurnstileEnabledOption, false) ||
		env.GetEnv("TURNSTILE_SECRET_KEY", "") != ""
}

// TurnstileSiteKey returns the public Turnstile site key (frontend config).
func TurnstileSiteKey() string {
	return setting.GetOption(setting.TurnstileSiteKeyOption)
}

// TurnstileSecret returns the Turnstile secret key (option store first, then
// environment).
func TurnstileSecret() string {
	if v := setting.GetOption(setting.TurnstileSecretKeyOption); v != "" {
		return v
	}
	return env.GetEnv("TURNSTILE_SECRET_KEY", "")
}

// VerifyTurnstile verifies a Cloudflare Turnstile token. When Turnstile is not
// configured it fails open (returns true) so registration/login keep working
// without it.
func VerifyTurnstile(token, remoteIP string) (bool, error) {
	secret := TurnstileSecret()
	if secret == "" {
		return true, nil
	}
	if strings.TrimSpace(token) == "" {
		return false, nil
	}
	form := url.Values{
		"secret":   {secret},
		"response": {token},
	}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequest(http.MethodPost, turnstileVerifyURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := turnstileHTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, err := httpx.ReadAllLimited(resp.Body, maxTurnstileResponseBytes)
	if err != nil {
		return false, fmt.Errorf("read Turnstile response: %w", err)
	}
	var result struct {
		Success bool `json:"success"`
	}
	if err := jsonutil.Unmarshal(body, &result); err != nil {
		return false, err
	}
	return result.Success, nil
}
