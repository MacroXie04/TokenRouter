package accounts

import (
	"errors"
	"github.com/gin-gonic/gin"
	authsvc "github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/dto"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// OAuth flow intents (mirroring the reference's AuthFlowIntent values).
const (
	oauthIntentLogin = "login"
	oauthIntentBind  = "bind"
)

// oauthAuthFlowTTL is the lifetime of an OAuth state flow.
const oauthAuthFlowTTL = 10 * time.Minute

const (
	maxIdentityQueryBytes      = 32 << 10
	maxIdentityQueryPairs      = 16
	maxIdentityQueryKeyBytes   = 64
	maxIdentityQueryValueBytes = 4096
	maxOAuthProviderBytes      = 64
	maxOAuthReturnTargetBytes  = 2048
)

type oauthFlowPayload struct {
	AffiliateCode string `json:"affiliate_code,omitempty"`
	ReturnTo      string `json:"return_to,omitempty"`
}

// safeOAuthReturnTarget accepts one same-origin browser path. Keeping the
// value in the server-side flow payload binds it to the unguessable state
// token instead of trusting a callback query supplied after authentication.
func safeOAuthReturnTarget(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	if value != strings.TrimSpace(value) || !boundedIdentityText(value, maxOAuthReturnTargetBytes, false) ||
		!strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, `\`) {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Opaque != "" || parsed.Host != "" || parsed.User != nil ||
		parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") ||
		strings.Contains(parsed.Path, `\`) {
		return "", false
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return "", false
		}
	}
	authPath := strings.TrimSuffix(parsed.Path, "/")
	if authPath == "" {
		authPath = "/"
	}
	switch authPath {
	case "/login", "/sign-in", "/sign-up", "/register", "/forgot-password", "/reset", "/user/reset", "/otp", "/oauth":
		return "", false
	}
	if strings.HasPrefix(authPath, "/oauth/") {
		return "", false
	}
	return value, true
}

func encodeOAuthFlowPayload(affiliateCode, returnTo string) (string, error) {
	payload, err := jsonutil.Marshal(oauthFlowPayload{AffiliateCode: affiliateCode, ReturnTo: returnTo})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func decodeOAuthFlowPayload(raw string) (oauthFlowPayload, bool) {
	if raw == "" {
		return oauthFlowPayload{}, true
	}
	var payload oauthFlowPayload
	if err := jsonutil.UnmarshalJsonStr(raw, &payload); err != nil ||
		payload.AffiliateCode != strings.TrimSpace(payload.AffiliateCode) ||
		!boundedIdentityText(payload.AffiliateCode, 32, true) {
		return oauthFlowPayload{}, false
	}
	returnTo, ok := safeOAuthReturnTarget(payload.ReturnTo)
	if !ok {
		return oauthFlowPayload{}, false
	}
	payload.ReturnTo = returnTo
	return payload, true
}

func oauthLoginRedirect(errorCode, message, returnTo string) string {
	query := url.Values{"error": {errorCode}}
	if message != "" {
		query.Set("message", message)
	}
	if returnTo != "" {
		query.Set("redirect", returnTo)
	}
	return "/login?" + query.Encode()
}

func oauthBoundRedirect(returnTo, result string) string {
	if returnTo == "" {
		returnTo = "/"
	}
	parsed, err := url.Parse(returnTo)
	if err != nil {
		return "/?oauth_bound=error"
	}
	query := parsed.Query()
	query.Set("oauth_bound", result)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func oauthFlowFailureRedirect(intent, errorCode, message, returnTo string) string {
	if intent == oauthIntentBind {
		return oauthBoundRedirect(returnTo, "error")
	}
	return oauthLoginRedirect(errorCode, message, returnTo)
}

// boundedIdentityQuery parses callback parameters once, rejects ambiguous
// duplicates, and places independent ceilings on cardinality, keys, values,
// and the aggregate query string.
func boundedIdentityQuery(c *gin.Context) (url.Values, bool) {
	if c == nil || c.Request == nil || c.Request.URL == nil || len(c.Request.URL.RawQuery) > maxIdentityQueryBytes {
		return nil, false
	}
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil || len(values) > maxIdentityQueryPairs {
		return nil, false
	}
	pairs := 0
	for key, items := range values {
		pairs += len(items)
		if key == "" || len(items) != 1 || !boundedIdentityText(key, maxIdentityQueryKeyBytes, false) ||
			!boundedIdentityText(items[0], maxIdentityQueryValueBytes, true) {
			return nil, false
		}
	}
	return values, pairs <= maxIdentityQueryPairs
}

func boundedIdentityText(value string, maximumBytes int, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

// GenerateOAuthCode creates a one-time OAuth state (flow token) for a
// provider + intent. Login flows may carry an affiliate code; bind flows are
// bound to the signed-in user's session and must not carry one.
func GenerateOAuthCode(c *gin.Context) {
	var request struct {
		Provider string `json:"provider"`
		Intent   string `json:"intent"`
		Aff      string `json:"aff"`
		Redirect string `json:"redirect"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	request.Provider = strings.TrimSpace(request.Provider)
	request.Intent = strings.TrimSpace(request.Intent)
	request.Aff = strings.TrimSpace(request.Aff)
	returnTo, validReturnTo := safeOAuthReturnTarget(request.Redirect)
	cfg := authsvc.GetOAuthConfig(request.Provider)
	knownProvider := cfg.Known || request.Provider == "telegram" && authsvc.TelegramOAuthEnabled()
	if !knownProvider || !boundedIdentityText(request.Provider, maxOAuthProviderBytes, false) ||
		(request.Intent != oauthIntentLogin && request.Intent != oauthIntentBind) ||
		!boundedIdentityText(request.Aff, 32, true) || !validReturnTo ||
		(request.Intent == oauthIntentBind && request.Aff != "") {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	userId := 0
	sessionId := ""
	if request.Intent == oauthIntentBind {
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "绑定操作需要登录"})
			return
		}
		userId = identity.UserID
		sessionId = identity.SessionID
	}
	payload, err := encodeOAuthFlowPayload(request.Aff, returnTo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("登录失败"))
		return
	}
	state, expiresAt, err := authsvc.CreateAuthFlowWithExpiry(
		authsvc.AuthFlowPurposeOAuth, request.Provider, request.Intent, userId, sessionId, payload, oauthAuthFlowTTL,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("登录失败"))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"flow_token": state,
			"expires_at": expiresAt,
		},
	})
}

// oauthRedirectURI builds the callback URL for a provider.
func oauthRedirectURI(provider string) (string, error) {
	base := env.GetEnv("FRONTEND_BASE_URL", "http://localhost:3000")
	return authsvc.BuildOAuthRedirectURI(base, provider)
}

// HandleOAuth starts an OAuth login: it stores a one-time state and redirects
// the browser to the provider's authorization endpoint.
func HandleOAuth(c *gin.Context) {
	provider := c.Param("provider")
	if !boundedIdentityText(provider, maxOAuthProviderBytes, false) {
		c.JSON(http.StatusBadRequest, dto.Fail("该登录方式未启用"))
		return
	}
	cfg := authsvc.GetOAuthConfig(provider)
	if !cfg.Enabled {
		c.JSON(http.StatusBadRequest, dto.Fail("该登录方式未启用"))
		return
	}
	query, ok := boundedIdentityQuery(c)
	if !ok || len(query) > 2 {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	affCode := ""
	returnTo := ""
	for key, values := range query {
		switch key {
		case "aff":
			if len(values) != 1 || values[0] != strings.TrimSpace(values[0]) ||
				!boundedIdentityText(values[0], 32, true) {
				c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
				return
			}
			affCode = values[0]
		case "redirect":
			var valid bool
			returnTo, valid = safeOAuthReturnTarget(values[0])
			if !valid || returnTo == "" {
				c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
				return
			}
		default:
			c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
			return
		}
	}
	redirectURI, err := oauthRedirectURI(provider)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("OAuth 回调地址配置无效"))
		return
	}
	payload, err := encodeOAuthFlowPayload(affCode, returnTo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("登录失败"))
		return
	}
	state, err := authsvc.CreateAuthFlow(authsvc.AuthFlowPurposeOAuth, provider, oauthIntentLogin, 0, "", payload, 10*time.Minute)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("登录失败"))
		return
	}
	authURL, err := authsvc.BuildAuthorizationURL(cfg, state, redirectURI)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("OAuth 登录配置无效"))
		return
	}
	c.Redirect(http.StatusFound, authURL)
}

// TelegramLogin completes a Telegram Login Widget authentication: it verifies
// the hash and signs the user in (or binds the identity).
func TelegramLogin(c *gin.Context) {
	if !authsvc.TelegramOAuthEnabled() {
		c.Redirect(http.StatusFound, oauthLoginRedirect("telegram", "", ""))
		return
	}
	query, ok := boundedIdentityQuery(c)
	if !ok {
		c.Redirect(http.StatusFound, oauthLoginRedirect("telegram", "", ""))
		return
	}
	flowToken := query.Get("flow_token")
	pendingFlow, err := authsvc.PeekAuthFlow(flowToken, authsvc.AuthFlowPurposeOAuth)
	if err != nil || pendingFlow.Provider != "telegram" || pendingFlow.Intent != oauthIntentLogin ||
		pendingFlow.UserId != 0 || pendingFlow.SessionId != "" {
		c.Redirect(http.StatusFound, oauthLoginRedirect("oauth_state", "", ""))
		return
	}
	payload, validPayload := decodeOAuthFlowPayload(pendingFlow.Payload)
	if !validPayload {
		c.Redirect(http.StatusFound, oauthLoginRedirect("oauth_state", "", ""))
		return
	}
	delete(query, "flow_token")
	data := make(map[string]string, len(query))
	for key, values := range query {
		data[key] = values[0]
	}
	if !authsvc.VerifyTelegramLogin(data) {
		c.Redirect(http.StatusFound, oauthLoginRedirect("telegram", "", payload.ReturnTo))
		return
	}
	pu := &authsvc.ProviderUser{
		ProviderID:  data["id"],
		Username:    data["username"],
		DisplayName: firstNonEmpty(data["first_name"], data["username"]),
	}
	if pu.ProviderID == "" {
		c.Redirect(http.StatusFound, oauthLoginRedirect("telegram", "", payload.ReturnTo))
		return
	}
	match := authsvc.AuthFlowMatch{
		Purpose: authsvc.AuthFlowPurposeOAuth, Provider: "telegram", Intent: oauthIntentLogin,
	}
	_, user, _, err := authsvc.ConsumeProviderLoginFlow(flowToken, match, "telegram", pu, payload.AffiliateCode)
	if errors.Is(err, authsvc.ErrRegistrationDisabled) {
		c.Redirect(http.StatusFound, oauthLoginRedirect("register_disabled", "", payload.ReturnTo))
		return
	}
	if err != nil || user.Status != model.UserStatusEnabled {
		c.Redirect(http.StatusFound, oauthLoginRedirect("telegram", "", payload.ReturnTo))
		return
	}
	sid, access, refresh, err := authsvc.CompleteLogin(user, c.ClientIP(), c.GetHeader("User-Agent"), "telegram")
	if err != nil {
		c.Redirect(http.StatusFound, oauthLoginRedirect(oauthSessionFailureCode(err, "telegram"), "", payload.ReturnTo))
		return
	}
	setAuthCookies(c, sid, access, refresh)
	if payload.ReturnTo == "" {
		payload.ReturnTo = "/"
	}
	c.Redirect(http.StatusFound, payload.ReturnTo)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// TelegramBindStart begins a Telegram bind ceremony for the signed-in user and
// returns the one-time flow token.
func TelegramBindStart(c *gin.Context) {
	identity, ok := requireLoginSession(c)
	if !ok {
		return
	}
	if !authsvc.TelegramOAuthEnabled() {
		c.JSON(http.StatusBadRequest, dto.Fail("该登录方式未启用"))
		return
	}
	token, err := authsvc.CreateAuthFlow(authsvc.AuthFlowPurposeTelegramBind, "telegram", "",
		identity.UserID, identity.SessionID, "", 10*time.Minute)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("发起失败"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"flow_token": token}))
}

// TelegramBindFinish completes the bind ceremony with Telegram Login Widget
// data and binds the identity to the flow's user.
func TelegramBindFinish(c *gin.Context) {
	fail := func() {
		c.Redirect(http.StatusFound, "/?telegram_bound=error")
	}
	if !authsvc.TelegramOAuthEnabled() {
		fail()
		return
	}
	flowToken := c.Param("flow_token")
	flow, err := authsvc.PeekAuthFlow(flowToken, authsvc.AuthFlowPurposeTelegramBind)
	if err != nil || flow.UserId == 0 || flow.Provider != "telegram" || flow.Intent != "" {
		fail()
		return
	}
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok || identity.UserID != flow.UserId || identity.SessionID != flow.SessionId {
		fail()
		return
	}
	query, ok := boundedIdentityQuery(c)
	if !ok {
		fail()
		return
	}
	data := make(map[string]string, len(query))
	for key, values := range query {
		data[key] = values[0]
	}
	if !authsvc.VerifyTelegramLogin(data) || data["id"] == "" {
		fail()
		return
	}
	match := authsvc.AuthFlowMatch{
		Purpose: authsvc.AuthFlowPurposeTelegramBind, Provider: "telegram",
		UserId: flow.UserId, SessionId: flow.SessionId,
	}
	if _, err := authsvc.ConsumeProviderBindFlow(flowToken, match, "telegram", &authsvc.ProviderUser{ProviderID: data["id"]}); err != nil {
		if errors.Is(err, authsvc.ErrBindingTaken) {
			c.Redirect(http.StatusFound, "/?telegram_bound=taken")
			return
		}
		fail()
		return
	}
	c.Redirect(http.StatusFound, "/?telegram_bound=1")
}

// OAuthCallback completes the OAuth2 code flow. State is first validated
// without consumption so transient provider failures remain retryable. Once
// provider identity is available, an exact atomic consume selects the single
// callback allowed to apply that identity.
func OAuthCallback(c *gin.Context) {
	provider := c.Param("provider")
	if !boundedIdentityText(provider, maxOAuthProviderBytes, false) {
		c.Redirect(http.StatusFound, "/login?error=oauth_disabled")
		return
	}
	cfg := authsvc.GetOAuthConfig(provider)
	if !cfg.Enabled {
		c.Redirect(http.StatusFound, "/login?error=oauth_disabled")
		return
	}
	query, ok := boundedIdentityQuery(c)
	if !ok {
		c.Redirect(http.StatusFound, "/login?error=invalid_oauth")
		return
	}
	state := query.Get("state")
	code := query.Get("code")
	providerError := query.Get("error")
	if state == "" || (code == "") == (providerError == "") {
		c.Redirect(http.StatusFound, "/login?error=invalid_oauth")
		return
	}
	pendingFlow, err := authsvc.PeekAuthFlow(state, authsvc.AuthFlowPurposeOAuth)
	if err != nil || pendingFlow.Provider != provider {
		c.Redirect(http.StatusFound, "/login?error=oauth_state")
		return
	}
	payload, validPayload := decodeOAuthFlowPayload(pendingFlow.Payload)
	if !validPayload {
		c.Redirect(http.StatusFound, oauthLoginRedirect("oauth_state", "", ""))
		return
	}
	match := authsvc.AuthFlowMatch{
		Purpose:   authsvc.AuthFlowPurposeOAuth,
		Provider:  provider,
		Intent:    pendingFlow.Intent,
		UserId:    pendingFlow.UserId,
		SessionId: pendingFlow.SessionId,
	}
	if pendingFlow.Intent == oauthIntentBind {
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok || identity.UserID != pendingFlow.UserId || identity.SessionID != pendingFlow.SessionId {
			c.Redirect(http.StatusFound, oauthLoginRedirect("oauth_state", "", payload.ReturnTo))
			return
		}
	} else if pendingFlow.Intent != oauthIntentLogin || pendingFlow.UserId != 0 || pendingFlow.SessionId != "" {
		c.Redirect(http.StatusFound, oauthLoginRedirect("oauth_state", "", payload.ReturnTo))
		return
	}
	if providerError != "" {
		if _, err := authsvc.ConsumeAuthFlowExact(state, match); err != nil {
			c.Redirect(http.StatusFound, oauthLoginRedirect("oauth_state", "", payload.ReturnTo))
			return
		}
		c.Redirect(http.StatusFound,
			oauthFlowFailureRedirect(pendingFlow.Intent, "oauth_denied", "", payload.ReturnTo))
		return
	}
	redirectURI, err := oauthRedirectURI(provider)
	if err != nil {
		c.Redirect(http.StatusFound,
			oauthFlowFailureRedirect(pendingFlow.Intent, "oauth_config", "", payload.ReturnTo))
		return
	}
	token, err := authsvc.ExchangeCode(cfg, code, redirectURI)
	if err != nil {
		c.Redirect(http.StatusFound,
			oauthFlowFailureRedirect(pendingFlow.Intent, "oauth_token", "", payload.ReturnTo))
		return
	}
	pu, err := authsvc.FetchUserInfo(cfg, provider, token)
	if err != nil {
		// A policy denial carries a rendered, user-facing message; surface it
		// to the login page instead of the generic userinfo failure.
		var denied *authsvc.OAuthAccessDeniedError
		if errors.As(err, &denied) {
			c.Redirect(http.StatusFound,
				oauthFlowFailureRedirect(pendingFlow.Intent, "oauth_access_denied", denied.Message, payload.ReturnTo))
			return
		}
		c.Redirect(http.StatusFound,
			oauthFlowFailureRedirect(pendingFlow.Intent, "oauth_userinfo", "", payload.ReturnTo))
		return
	}
	// Bind intent: attach the provider identity to the exact live dashboard
	// session that created the state flow.
	if pendingFlow.Intent == oauthIntentBind {
		if _, err := authsvc.ConsumeProviderBindFlow(state, match, provider, pu); err != nil {
			if errors.Is(err, authsvc.ErrBindingTaken) {
				c.Redirect(http.StatusFound, oauthBoundRedirect(payload.ReturnTo, "taken"))
				return
			}
			c.Redirect(http.StatusFound, oauthBoundRedirect(payload.ReturnTo, "error"))
			return
		}
		c.Redirect(http.StatusFound, oauthBoundRedirect(payload.ReturnTo, "1"))
		return
	}

	_, user, _, err := authsvc.ConsumeProviderLoginFlow(state, match, provider, pu, payload.AffiliateCode)
	if errors.Is(err, authsvc.ErrRegistrationDisabled) {
		c.Redirect(http.StatusFound, oauthLoginRedirect("register_disabled", "", payload.ReturnTo))
		return
	}
	if err != nil || user.Status != model.UserStatusEnabled {
		c.Redirect(http.StatusFound, oauthLoginRedirect("oauth_user", "", payload.ReturnTo))
		return
	}
	loginMethod := "oauth:" + provider
	if len(loginMethod) > 32 {
		loginMethod = loginMethod[:32]
	}
	sid, access, refresh, err := authsvc.CompleteLogin(user, c.ClientIP(), c.GetHeader("User-Agent"), loginMethod)
	if err != nil {
		c.Redirect(http.StatusFound, oauthLoginRedirect(oauthSessionFailureCode(err, "oauth_session"), "", payload.ReturnTo))
		return
	}
	setAuthCookies(c, sid, access, refresh)
	if payload.ReturnTo == "" {
		payload.ReturnTo = "/"
	}
	c.Redirect(http.StatusFound, payload.ReturnTo)
}

func oauthSessionFailureCode(err error, fallback string) string {
	switch {
	case errors.Is(err, authsvc.ErrSessionLimit):
		return "auth_session_limit"
	case errors.Is(err, authsvc.ErrSessionIssuanceLimit):
		return "auth_session_issuance_limit"
	default:
		return fallback
	}
}
