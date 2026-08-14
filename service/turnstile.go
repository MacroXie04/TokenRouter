package service

import (
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/setting"
)

// turnstileVerifyURL is the Cloudflare siteverify endpoint (overridable in
// tests via TURNSTILE_VERIFY_URL).
func turnstileVerifyURL() string {
	return common.GetEnv("TURNSTILE_VERIFY_URL", "https://challenges.cloudflare.com/turnstile/v0/siteverify")
}

// TurnstileEnabled reports whether Turnstile bot protection is configured.
// The option store is authoritative (TurnstileCheckEnabled, matching the
// reference key); the legacy TurnstileEnabled key and the TURNSTILE_SECRET_KEY
// environment variable remain accepted for single-node deployments.
func TurnstileEnabled() bool {
	return setting.GetOptionBool(setting.TurnstileCheckEnabledOption, false) ||
		setting.GetOptionBool(setting.TurnstileEnabledOption, false) ||
		common.GetEnv("TURNSTILE_SECRET_KEY", "") != ""
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
	return common.GetEnv("TURNSTILE_SECRET_KEY", "")
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
	resp, err := http.PostForm(turnstileVerifyURL(), form)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Success bool `json:"success"`
	}
	if err := common.Unmarshal(body, &result); err != nil {
		return false, err
	}
	return result.Success, nil
}
