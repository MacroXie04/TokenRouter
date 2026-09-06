package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	"net/http"
	"strings"
)

// InitTrustedProxies configures gin's trusted-proxy handling from TRUSTED_PROXIES.
//   - unset/empty or "none": trust no proxies
//   - list:                  trust only the given CIDRs/IPs
//
// Invalid lists fail closed. Gin retains the valid prefix of a proxy list when
// parsing a later entry fails, so every error must explicitly clear that state.
func InitTrustedProxies(r *gin.Engine) {
	// Gin trusts every proxy by default. Disable that behavior before parsing
	// configuration so every early return remains fail-closed.
	r.ForwardedByClientIP = false
	_ = r.SetTrustedProxies(nil)

	configured := strings.TrimSpace(env.GetEnv("TRUSTED_PROXIES", ""))
	if configured == "" || strings.EqualFold(configured, "none") {
		return
	}

	parts := strings.Split(configured, ",")
	trusted := make([]string, 0, len(parts))
	for _, part := range parts {
		proxy := strings.TrimSpace(part)
		if proxy == "" || strings.EqualFold(proxy, "none") {
			logging.Logger.Error("invalid TRUSTED_PROXIES; forwarded client IP headers disabled",
				"value", configured)
			return
		}
		trusted = append(trusted, proxy)
	}

	if err := r.SetTrustedProxies(trusted); err != nil {
		// SetTrustedProxies assigns Gin's partially parsed CIDRs even on error.
		_ = r.SetTrustedProxies(nil)
		logging.Logger.Error("invalid TRUSTED_PROXIES; forwarded client IP headers disabled",
			"err", err.Error())
		return
	}
	r.ForwardedByClientIP = true
}

// OriginGuard protects cookie-based endpoints (refresh/logout) from
// cross-origin misuse. When SESSION_COOKIE_SECURE=true and trusted origins are
// configured, an Origin header that does not exactly match a trusted URL is
// rejected. Requests without an Origin header (same-origin) are allowed.
func OriginGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !env.GetEnvBool("SESSION_COOKIE_SECURE", false) {
			c.Next()
			return
		}
		trusted := env.GetEnvStrings("SESSION_COOKIE_TRUSTED_URL")
		if len(trusted) == 0 {
			c.Next()
			return
		}
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		for _, t := range trusted {
			if origin == t {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "非法来源",
		})
	}
}
