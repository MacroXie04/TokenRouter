package common

import "github.com/gin-gonic/gin"

// Context keys stored on the gin.Context for the duration of a request.
const (
	RequestIdKey             = "X-Request-Id"
	ContextKeyUserId         = "user_id"
	ContextKeyUsername       = "username"
	ContextKeyUserGroup      = "user_group"
	ContextKeyRole           = "role"
	ContextKeyTokenId        = "token_id"
	ContextKeyTokenName      = "token_name"
	ContextKeyGroup          = "group"
	ContextKeyToken          = "token"
	ContextKeyChannelId      = "channel_id"
	ContextKeyIsAdminRequest = "is_admin_request"
)

// SetUserId stores the authenticated user id on the context.
func SetUserId(c *gin.Context, id int) {
	c.Set(ContextKeyUserId, id)
}

// GetUserId returns the authenticated user id (0 if absent).
func GetUserId(c *gin.Context) int {
	if v, ok := c.Get(ContextKeyUserId); ok {
		if id, ok := v.(int); ok {
			return id
		}
	}
	return 0
}

// SetUsername stores the authenticated username on the context.
func SetUsername(c *gin.Context, username string) {
	c.Set(ContextKeyUsername, username)
}

// GetUsername returns the authenticated username ("" if absent).
func GetUsername(c *gin.Context) string {
	if v, ok := c.Get(ContextKeyUsername); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// SetUserGroup stores the authenticated user's own group. This is distinct
// from ContextKeyGroup, which is the channel group selected for a relay.
func SetUserGroup(c *gin.Context, group string) {
	c.Set(ContextKeyUserGroup, group)
}

// GetUserGroup returns the authenticated user's own group ("" if absent).
func GetUserGroup(c *gin.Context) string {
	return GetString(c, ContextKeyUserGroup)
}

// SetRole stores the user role on the context.
func SetRole(c *gin.Context, role int) {
	c.Set(ContextKeyRole, role)
}

// GetRole returns the user role (0 if absent).
func GetRole(c *gin.Context) int {
	if v, ok := c.Get(ContextKeyRole); ok {
		if r, ok := v.(int); ok {
			return r
		}
	}
	return 0
}

// SetRequestId stores the request id.
func SetRequestId(c *gin.Context, id string) {
	c.Set(RequestIdKey, id)
}

// GetRequestId returns the request id.
func GetRequestId(c *gin.Context) string {
	if v, ok := c.Get(RequestIdKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetString returns a context value as a string.
func GetString(c *gin.Context, key string) string {
	if v, ok := c.Get(key); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetInt returns a context value as an int.
func GetInt(c *gin.Context, key string) int {
	if v, ok := c.Get(key); ok {
		if i, ok := v.(int); ok {
			return i
		}
	}
	return 0
}
