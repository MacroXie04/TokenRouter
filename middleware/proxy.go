package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
)

// InitTrustedProxies configures gin's trusted-proxy handling from TRUSTED_PROXIES.
//   - unset/empty: trust all proxies (with a startup warning)
//   - "none":      trust no proxies (strict mode)
//   - list:        trust only the given CIDRs/IPs
func InitTrustedProxies(r *gin.Engine) {
	trusted := common.GetEnv("TRUSTED_PROXIES", "")
	switch {
	case trusted == "":
		common.Logger.Warn("TRUSTED_PROXIES unset: trusting all proxies")
	case trusted == "none":
		_ = r.SetTrustedProxies([]string{})
	default:
		_ = r.SetTrustedProxies(strings.Split(trusted, ","))
	}
}

// OriginGuard protects cookie-based endpoints (refresh/logout) from
// cross-origin misuse. When SESSION_COOKIE_SECURE=true and trusted origins are
// configured, an Origin header that does not exactly match a trusted URL is
// rejected. Requests without an Origin header (same-origin) are allowed.
func OriginGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !common.GetEnvBool("SESSION_COOKIE_SECURE", false) {
			c.Next()
			return
		}
		trusted := common.GetEnvStrings("SESSION_COOKIE_TRUSTED_URL")
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
