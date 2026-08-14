// Package middleware provides HTTP middleware: authentication, CORS, rate
// limiting, request IDs, and panic recovery.
package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// relayContextToken holds the authenticated relay token.
const relayTokenContext = "relay_token"

// TokenAuth authenticates a relay request by its bearer API key and loads the
// owning user and token onto the context.
func TokenAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := extractTokenKey(c)
		token, err := service.TokenByKey(key)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": &protocolError{Message: "无效的令牌", Type: "invalid_request_error", Code: "invalid_api_key"}})
			return
		}
		if err := service.CheckTokenUsable(token); err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": &protocolError{Message: err.Error(), Type: "invalid_request_error", Code: "invalid_api_key"}})
			return
		}
		user, err := service.GetUserByID(token.UserId)
		if err != nil || user.Status == model.UserStatusDisabled {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": &protocolError{Message: "用户不存在或已禁用", Type: "invalid_request_error", Code: "invalid_api_key"}})
			return
		}

		// IP allow-list enforcement.
		if token.AllowIps != "" {
			clientIP := c.ClientIP()
			if !ipInList(clientIP, token.AllowIps) {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": &protocolError{Message: "IP 不在白名单内", Type: "invalid_request_error", Code: "ip_not_allowed"}})
				return
			}
		}

		common.SetUserId(c, user.Id)
		common.SetUsername(c, user.Username)
		common.SetRole(c, user.Role)
		SetupRelayTokenContext(c, token)
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
		token, err := service.TokenByKey(key)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "令牌无效"})
			c.Abort()
			return
		}
		common.SetUserId(c, token.UserId)
		c.Set(common.ContextKeyToken, token)
		c.Set(common.ContextKeyTokenId, token.Id)
		c.Set(common.ContextKeyTokenName, token.Name)
		c.Set(common.ContextKeyGroup, token.Group)
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
	c.Set(common.ContextKeyToken, token)
	c.Set(common.ContextKeyTokenId, token.Id)
	c.Set(common.ContextKeyTokenName, token.Name)
	c.Set(common.ContextKeyGroup, token.Group)
}

// GetTokenGroup returns the effective token group for a relay request.
func GetTokenGroup(c *gin.Context) string {
	return common.GetString(c, common.ContextKeyGroup)
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
	return c.Query("sk-key")
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
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录或会话已过期"})
			return
		}
		c.Next()
	}
}

// authenticateUser resolves the signed-in user and stores id/role on the
// context without advancing the handler chain, so it can be composed inside
// AdminAuth/RootAuth safely (a middleware's c.Next() would otherwise run the
// downstream handler before the role check).
func authenticateUser(c *gin.Context) (*model.User, error) {
	claims, err := dashboardClaims(c)
	if err != nil {
		// Session token failed (or absent): the reference also accepts a
		// dashboard access token (PAT) as a bearer credential.
		if user, ok := accessTokenUser(c); ok {
			common.SetUserId(c, user.Id)
			common.SetUsername(c, user.Username)
			common.SetRole(c, user.Role)
			c.Set("use_access_token", true)
			return user, nil
		}
		return nil, err
	}
	user, err := service.GetUserByID(claims.UserID)
	if err != nil || user.Status == model.UserStatusDisabled {
		return nil, errUnauthorized
	}
	common.SetUserId(c, user.Id)
	common.SetUsername(c, user.Username)
	common.SetRole(c, user.Role)
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
	user, err := service.UserByAccessToken(token)
	if err != nil || user.Status == model.UserStatusDisabled {
		return nil, false
	}
	return user, true
}

// AdminAuth requires an authenticated admin user, then enforces fine-grained
// Casbin authorization on the resource/action.
func AdminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := authenticateUser(c); err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录或会话已过期"})
			return
		}
		role := common.GetRole(c)
		if !service.IsAdmin(role) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "message": "无权限"})
			return
		}
		// Fine-grained authorization (fail-open only if the engine is unset).
		if allowed, err := service.Authorize(role, "/api"+c.Request.URL.Path, c.Request.Method); err == nil && !allowed {
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
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录或会话已过期"})
			return
		}
		if !service.IsRoot(common.GetRole(c)) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "message": "无权限"})
			return
		}
		c.Next()
	}
}

// dashboardClaims reads and validates the access-token JWT from cookie or header.
func dashboardClaims(c *gin.Context) (*common.JWTClaims, error) {
	tokenStr := ""
	if cookie, err := c.Cookie("access_token"); err == nil {
		tokenStr = cookie
	} else if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tokenStr = strings.TrimPrefix(h, "Bearer ")
	}
	if tokenStr == "" {
		return nil, errUnauthorized
	}
	claims, err := common.ParseJWT(tokenStr, common.SessionSecret())
	if err != nil {
		return nil, err
	}
	return claims, nil
}

var errUnauthorized = errors.New("unauthorized")
