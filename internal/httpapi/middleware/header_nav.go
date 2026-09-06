package middleware

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"net/http"
	"strings"
)

type headerNavAccess struct {
	enabled     bool
	requireAuth bool
}

func configuredHeaderNavAccess(module string) headerNavAccess {
	fallback := headerNavAccess{enabled: true}
	raw := strings.TrimSpace(setting.GetOption(setting.HeaderNavModulesOption))
	if raw == "" {
		return fallback
	}
	var modules map[string]any
	if err := jsonutil.Unmarshal([]byte(raw), &modules); err != nil {
		return fallback
	}
	value, exists := modules[module]
	if !exists {
		return fallback
	}
	return parseHeaderNavAccess(value, fallback)
}

func parseHeaderNavAccess(value any, fallback headerNavAccess) headerNavAccess {
	if scalar, ok := parseHeaderNavBool(value); ok {
		return headerNavAccess{enabled: scalar, requireAuth: fallback.requireAuth}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return fallback
	}
	access := fallback
	if enabled, exists := object["enabled"]; exists {
		if parsed, valid := parseHeaderNavBool(enabled); valid {
			access.enabled = parsed
		}
	}
	if requireAuth, exists := object["requireAuth"]; exists {
		if parsed, valid := parseHeaderNavBool(requireAuth); valid {
			access.requireAuth = parsed
		}
	}
	return access
}

func parseHeaderNavBool(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1":
			return true, true
		case "false", "0":
			return false, true
		}
	case float64:
		if typed == 1 {
			return true, true
		}
		if typed == 0 {
			return false, true
		}
	}
	return false, false
}

// HeaderNavModuleAuth enforces the configured visibility of a public
// navigation module. An enabled module is public unless requireAuth is set;
// a disabled module is unavailable even to an authenticated user.
func HeaderNavModuleAuth(module string) gin.HandlerFunc {
	return func(c *gin.Context) {
		access := configuredHeaderNavAccess(module)
		if !access.enabled {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": fmt.Sprintf("%s is disabled", module),
			})
			return
		}
		if access.requireAuth {
			requireOptionalDashboardIdentity(c, true)
			return
		}
		requireOptionalDashboardIdentity(c, false)
	}
}

// HeaderNavModulePublicOrUserAuth leaves an enabled public module anonymous,
// while a hidden or auth-only module remains available to signed-in users.
func HeaderNavModulePublicOrUserAuth(module string) gin.HandlerFunc {
	return func(c *gin.Context) {
		access := configuredHeaderNavAccess(module)
		requireOptionalDashboardIdentity(c, !access.enabled || access.requireAuth)
	}
}

func requireOptionalDashboardIdentity(c *gin.Context, required bool) {
	_, cookieErr := c.Cookie("access_token")
	hasCredential := cookieErr == nil || strings.TrimSpace(c.GetHeader("Authorization")) != ""
	if !required && !hasCredential {
		c.Next()
		return
	}
	if _, err := authenticateUser(c); err != nil {
		abortDashboardAuth(c, err)
		return
	}
	c.Next()
}
