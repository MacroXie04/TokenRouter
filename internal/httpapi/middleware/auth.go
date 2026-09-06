// Package middleware provides HTTP middleware: authentication, CORS, rate
// limiting, request IDs, and panic recovery.
package middleware

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	"github.com/tokenrouter/tokenrouter/internal/relay/policy"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net/http"
	"strings"
	"time"
)

// relayContextToken holds the authenticated relay token.
const relayTokenContext = "relay_token"

const (
	relayGroupPolicyContext = "relay_group_policy"
	dashboardClaimsContext  = "dashboard_session_claims"
	dashboardUserContext    = "dashboard_session_user"
)

// ErrDashboardSessionInvalid marks an unusable session access credential.
// Live-session storage errors remain unwrapped so callers such as logout can
// distinguish an invalid credential from an unavailable database.
var ErrDashboardSessionInvalid = errors.New("dashboard session credential invalid")

// errDashboardTokenExpired is joined with ErrDashboardSessionInvalid so
// existing non-HTTP callers continue to treat an expired token as an invalid
// credential while the dashboard middleware can expose a stable, specific
// machine-readable code.
var errDashboardTokenExpired = errors.New("dashboard access token expired")

const (
	dashboardAuthUnauthorizedCode = "AUTH_UNAUTHORIZED"
	dashboardAuthExpiredCode      = "AUTH_TOKEN_EXPIRED"
	dashboardAuthRevokedCode      = "AUTH_SESSION_REVOKED"
	dashboardAuthInternalCode     = "AUTH_INTERNAL_ERROR"
	dashboardAuthMessage          = "未登录或会话已过期"
)

// TokenAuth authenticates a relay request by its bearer API key and loads the
// owning user and token onto the context.
func TokenAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := extractTokenKey(c)
		token, err := billingsvc.TokenByKey(key)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": &protocolError{Message: "无效的令牌", Type: "invalid_request_error", Code: "invalid_api_key"}})
			return
		}
		if err := billingsvc.CheckTokenUsable(token); err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": &protocolError{Message: err.Error(), Type: "invalid_request_error", Code: "invalid_api_key"}})
			return
		}
		user, err := userssvc.GetUserByID(token.UserId)
		if err != nil || user.Status == model.UserStatusDisabled {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": &protocolError{Message: "用户不存在或已禁用", Type: "invalid_request_error", Code: "invalid_api_key"}})
			return
		}

		// IP allow-list enforcement.
		if token.AllowIps != nil && *token.AllowIps != "" {
			clientIP := c.ClientIP()
			if !ipInList(clientIP, *token.AllowIps) {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": &protocolError{Message: "IP 不在白名单内", Type: "invalid_request_error", Code: "ip_not_allowed"}})
				return
			}
		}

		groupPolicy, err := billingsvc.ResolveRelayGroupPolicy(user.Group, token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": &protocolError{
				Message: "令牌分组不可用", Type: "invalid_request_error", Code: "group_not_allowed",
			}})
			return
		}

		userGroup := user.Group
		if userGroup == "" {
			userGroup = userssvc.GroupDefault
		}
		requestctx.SetUserId(c, user.Id)
		requestctx.SetUsername(c, user.Username)
		requestctx.SetUserGroup(c, userGroup)
		requestctx.SetRole(c, user.Role)
		SetupRelayTokenContext(c, token)
		SetRelayGroupPolicy(c, groupPolicy)
		c.Next()
	}
}

// TokenOrUserAuth authenticates either a relay API key or a dashboard
// identity. It is used only for user-owned task content: API keys receive the
// same status, expiry, IP, and group checks as TokenAuth, while browser
// sessions/PATs fall back to the normal dashboard validator. A presented sk-
// credential is never silently reinterpreted as a dashboard credential.
func TokenOrUserAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := extractTokenKey(c)
		if key != "" {
			token, tokenErr := billingsvc.TokenByKey(key)
			if tokenErr == nil {
				if err := billingsvc.CheckTokenUsable(token); err != nil {
					c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": &protocolError{
						Message: err.Error(), Type: "invalid_request_error", Code: "invalid_api_key",
					}})
					return
				}
				user, err := userssvc.GetUserByID(token.UserId)
				if err != nil || user.Status == model.UserStatusDisabled {
					c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": &protocolError{
						Message: "用户不存在或已禁用", Type: "invalid_request_error", Code: "invalid_api_key",
					}})
					return
				}
				if token.AllowIps != nil && *token.AllowIps != "" && !ipInList(c.ClientIP(), *token.AllowIps) {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": &protocolError{
						Message: "IP 不在白名单内", Type: "invalid_request_error", Code: "ip_not_allowed",
					}})
					return
				}
				groupPolicy, err := billingsvc.ResolveRelayGroupPolicy(user.Group, token)
				if err != nil {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": &protocolError{
						Message: "令牌分组不可用", Type: "invalid_request_error", Code: "group_not_allowed",
					}})
					return
				}
				userGroup := user.Group
				if userGroup == "" {
					userGroup = userssvc.GroupDefault
				}
				requestctx.SetUserId(c, user.Id)
				requestctx.SetUsername(c, user.Username)
				requestctx.SetUserGroup(c, userGroup)
				requestctx.SetRole(c, user.Role)
				SetupRelayTokenContext(c, token)
				SetRelayGroupPolicy(c, groupPolicy)
				c.Next()
				return
			}
			if strings.HasPrefix(key, "sk-") || c.Query("sk-key") != "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": &protocolError{
					Message: "无效的令牌", Type: "invalid_request_error", Code: "invalid_api_key",
				}})
				return
			}
		}
		user, err := authenticateUser(c)
		if err != nil {
			abortDashboardAuth(c, err)
			return
		}
		userGroup := user.Group
		if userGroup == "" {
			userGroup = userssvc.GroupDefault
		}
		requestctx.SetUserGroup(c, userGroup)
		c.Next()
	}
}

// TokenAuthReadOnly authenticates a relay bearer token without enforcing
// usability (the reference contract for the usage endpoint: expired/disabled
// tokens can still query their usage). TokenRouter stores keys with the sk-
// prefix, so the header value is matched as-is after the scheme.
func TokenAuthReadOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.Request.Header.Get("Authorization")
		if key == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未提供令牌"})
			c.Abort()
			return
		}
		key = strings.TrimPrefix(key, "Bearer ")
		key = strings.TrimPrefix(key, "bearer ")
		token, err := billingsvc.TokenByKey(key)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "令牌无效"})
			c.Abort()
			return
		}
		requestctx.SetUserId(c, token.UserId)
		c.Set(requestctx.ContextKeyToken, token)
		c.Set(requestctx.ContextKeyTokenId, token.Id)
		c.Set(requestctx.ContextKeyTokenName, token.Name)
		c.Set(requestctx.ContextKeyGroup, token.Group)
		c.Next()
	}
}

// GetRelayToken returns the authenticated relay token (nil if absent).
func GetRelayToken(c *gin.Context) *model.Token {
	if v, ok := c.Get(relayTokenContext); ok {
		if t, ok := v.(*model.Token); ok {
			return t
		}
	}
	return nil
}

// SetupRelayTokenContext installs token metadata for the shared relay lifecycle.
func SetupRelayTokenContext(c *gin.Context, token *model.Token) {
	c.Set(relayTokenContext, token)
	c.Set(relayModelPolicyContext, policy.NewTokenModelPolicy(token))
	c.Set(requestctx.ContextKeyToken, token)
	c.Set(requestctx.ContextKeyTokenId, token.Id)
	c.Set(requestctx.ContextKeyTokenName, token.Name)
	if token.Group == userssvc.GroupAuto {
		c.Set(requestctx.ContextKeyGroup, "")
	} else {
		c.Set(requestctx.ContextKeyGroup, token.Group)
	}
}

// SetRelayGroupPolicy installs the current fail-closed authorization decision.
func SetRelayGroupPolicy(c *gin.Context, policy billingsvc.RelayGroupPolicy) {
	policy.Groups = append([]string(nil), policy.Groups...)
	c.Set(relayGroupPolicyContext, policy)
	if len(policy.Groups) > 0 {
		// The first candidate is safe for legacy readers; relay selection replaces
		// it with the group that actually supplied the channel.
		c.Set(requestctx.ContextKeyGroup, policy.Groups[0])
	} else {
		c.Set(requestctx.ContextKeyGroup, "")
	}
}

// GetRelayGroupPolicy returns a defensive copy of the request policy.
func GetRelayGroupPolicy(c *gin.Context) billingsvc.RelayGroupPolicy {
	if value, ok := c.Get(relayGroupPolicyContext); ok {
		if policy, ok := value.(billingsvc.RelayGroupPolicy); ok {
			policy.Groups = append([]string(nil), policy.Groups...)
			return policy
		}
	}
	group := requestctx.GetString(c, requestctx.ContextKeyGroup)
	if group == "" || group == userssvc.GroupAuto {
		return billingsvc.RelayGroupPolicy{}
	}
	return billingsvc.RelayGroupPolicy{Groups: []string{group}}
}

// GetTokenGroups returns the ordered authorized group candidates.
func GetTokenGroups(c *gin.Context) []string {
	return GetRelayGroupPolicy(c).Groups
}

// GetTokenGroup returns the effective token group for a relay request.
func GetTokenGroup(c *gin.Context) string {
	return requestctx.GetString(c, requestctx.ContextKeyGroup)
}

// extractTokenKey extracts the token key from the Authorization header, or the
// sk-key query/form parameter.
func extractTokenKey(c *gin.Context) string {
	key := c.Request.Header.Get("Authorization")
	if key != "" {
		key = strings.TrimPrefix(key, "Bearer ")
		key = strings.TrimPrefix(key, "bearer ")
		return key
	}
	if key = c.Query("sk-key"); key != "" {
		return key
	}
	// Midjourney Proxy clients conventionally authenticate the relay with the
	// same header name used on the provider wire. Restrict the fallback to the
	// exact Midjourney route family so other protocols cannot gain a new token
	// transport accidentally.
	mode := channelcatalog.PathToRelayModeMidjourney(c.Request.URL.Path)
	if mode != channelcatalog.RelayModeUnknown && mode != channelcatalog.RelayModeMidjourneyNotify {
		key = c.Request.Header.Get("mj-api-secret")
		key = strings.TrimPrefix(key, "Bearer ")
		key = strings.TrimPrefix(key, "bearer ")
	}
	return key
}

// ipInList checks whether ip matches a comma-separated allow-list.
func ipInList(ip, list string) bool {
	for _, entry := range strings.Split(list, ",") {
		e := strings.TrimSpace(entry)
		if e == "" {
			continue
		}
		if e == ip {
			return true
		}
	}
	return false
}

// protocolError is the OpenAI-compatible error shape used on the relay plane.
type protocolError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// UserAuth authenticates a dashboard request via access-token cookie or bearer.
func UserAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := authenticateUser(c); err != nil {
			abortDashboardAuth(c, err)
			return
		}
		c.Next()
	}
}

// TryUserAuth accepts a request without dashboard credentials, but validates
// any credential that is presented. This prevents expired or forged browser
// credentials from being silently treated as an anonymous request.
func TryUserAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		requireOptionalDashboardIdentity(c, false)
	}
}

// authenticateUser resolves the signed-in user and stores id/role on the
// context without advancing the handler chain, so it can be composed inside
// AdminAuth/RootAuth safely (a middleware's c.Next() would otherwise run the
// downstream handler before the role check).
func authenticateUser(c *gin.Context) (*model.User, error) {
	_, err := dashboardClaims(c)
	if err != nil {
		// Session token failed (or absent): the reference also accepts a
		// dashboard access token (PAT) as a bearer credential.
		if user, ok := accessTokenUser(c); ok {
			requestctx.SetUserId(c, user.Id)
			requestctx.SetUsername(c, user.Username)
			requestctx.SetRole(c, user.Role)
			c.Set("use_access_token", true)
			return user, nil
		}
		return nil, err
	}
	value, ok := c.Get(dashboardUserContext)
	user, ok := value.(*model.User)
	if !ok || user == nil {
		return nil, errUnauthorized
	}
	requestctx.SetUserId(c, user.Id)
	requestctx.SetUsername(c, user.Username)
	requestctx.SetRole(c, user.Role)
	c.Set("use_access_token", false)
	return user, nil
}

// accessTokenUser resolves a bearer dashboard access token from the
// Authorization header (users.access_token, reference PAT contract).
func accessTokenUser(c *gin.Context) (*model.User, bool) {
	h := c.GetHeader("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return nil, false
	}
	token := strings.TrimPrefix(h, "Bearer ")
	if token == "" {
		return nil, false
	}
	user, err := auth.UserByAccessToken(token)
	if err != nil || user.Status == model.UserStatusDisabled {
		return nil, false
	}
	return user, true
}

// AdminAuth requires an authenticated admin user, then enforces fine-grained
// Casbin authorization on the resource/action.
var authorizeAdminRequest = auth.Authorize

func AdminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := authenticateUser(c); err != nil {
			abortDashboardAuth(c, err)
			return
		}
		role := requestctx.GetRole(c)
		if !userssvc.IsAdmin(role) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "message": "无权限"})
			return
		}
		allowed, err := authorizeAdminRequest(role, "/api"+c.Request.URL.Path, c.Request.Method)
		if err != nil {
			logging.SysError("admin authorization check failed: " + err.Error())
		}
		if err != nil || !allowed {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "message": "无权限"})
			return
		}
		c.Next()
	}
}

// RootAuth requires an authenticated root user.
func RootAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := authenticateUser(c); err != nil {
			abortDashboardAuth(c, err)
			return
		}
		if !userssvc.IsRoot(requestctx.GetRole(c)) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "message": "无权限"})
			return
		}
		c.Next()
	}
}

// dashboardClaims reads and validates the access-token JWT from cookie or header.
func dashboardClaims(c *gin.Context) (*cryptoutil.JWTClaims, error) {
	if value, ok := c.Get(dashboardClaimsContext); ok {
		if claims, ok := value.(*cryptoutil.JWTClaims); ok && claims != nil {
			return claims, nil
		}
	}
	tokenStr := ""
	if cookie, err := c.Cookie("access_token"); err == nil {
		tokenStr = cookie
	} else if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tokenStr = strings.TrimPrefix(h, "Bearer ")
	}
	if tokenStr == "" {
		return nil, ErrDashboardSessionInvalid
	}
	claims, err := cryptoutil.ParseJWTSigned(tokenStr, cryptoutil.SessionSecret())
	if err != nil {
		return nil, ErrDashboardSessionInvalid
	}
	now, err := model.PrimaryDatabaseUnixTimestamp(c.Request.Context())
	if err != nil {
		return nil, err
	}
	if err := cryptoutil.ValidateJWTClaimsAt(claims, time.Unix(now, 0).UTC()); err != nil {
		if isSoleJWTExpiryError(err) {
			return nil, errors.Join(ErrDashboardSessionInvalid, errDashboardTokenExpired)
		}
		return nil, ErrDashboardSessionInvalid
	}
	_, user, err := auth.ValidateAccessTokenClaimsAt(claims, now)
	if err != nil {
		return nil, err
	}
	c.Set(dashboardClaimsContext, claims)
	c.Set(dashboardUserContext, user)
	return claims, nil
}

// GetDashboardSessionClaims returns a fully validated session-backed access
// identity. PAT-authenticated requests intentionally have no session claims.
func GetDashboardSessionClaims(c *gin.Context) (*cryptoutil.JWTClaims, error) {
	return dashboardClaims(c)
}

// isSoleJWTExpiryError recognizes an otherwise-valid, correctly signed token
// whose only claims failure is expiration. A token that is also malformed or
// violates another registered claim remains a generic invalid credential.
func isSoleJWTExpiryError(err error) bool {
	if !errors.Is(err, jwt.ErrTokenExpired) {
		return false
	}
	for _, competing := range []error{
		jwt.ErrTokenMalformed,
		jwt.ErrTokenUnverifiable,
		jwt.ErrTokenSignatureInvalid,
		jwt.ErrTokenRequiredClaimMissing,
		jwt.ErrTokenInvalidAudience,
		jwt.ErrTokenUsedBeforeIssued,
		jwt.ErrTokenInvalidIssuer,
		jwt.ErrTokenInvalidSubject,
		jwt.ErrTokenNotValidYet,
		jwt.ErrTokenInvalidId,
		jwt.ErrInvalidType,
	} {
		if errors.Is(err, competing) {
			return false
		}
	}
	return true
}

func abortDashboardAuth(c *gin.Context, err error) {
	status := http.StatusUnauthorized
	code := dashboardAuthUnauthorizedCode
	message := dashboardAuthMessage
	switch {
	case errors.Is(err, errDashboardTokenExpired):
		code = dashboardAuthExpiredCode
	case errors.Is(err, auth.ErrSessionRevoked):
		code = dashboardAuthRevokedCode
	case errors.Is(err, ErrDashboardSessionInvalid), errors.Is(err, errUnauthorized):
		// Keep the historical unauthorized message for missing or invalid
		// credentials while adding the stable machine-readable code.
	default:
		status = http.StatusInternalServerError
		code = dashboardAuthInternalCode
		message = http.StatusText(status)
		logging.SysError("dashboard authentication failed: " + err.Error())
	}
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(status, gin.H{
		"success": false,
		"code":    code,
		"message": message,
	})
}

var errUnauthorized = errors.New("unauthorized")
