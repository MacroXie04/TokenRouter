package middleware

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
)

// RelayCORS returns the CORS middleware for the relay plane. Relay endpoints
// accept cross-origin calls from arbitrary clients, so origins are permissive
// while credentials are disabled.
func RelayCORS() gin.HandlerFunc {
	return cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowHeaders:     []string{"*"},
		ExposeHeaders:    []string{"Content-Length", "X-Request-Id"},
		AllowCredentials: false,
	})
}

// DashboardCORS permits credentialed browser requests only from the current
// origin or an explicitly configured frontend/trusted origin. It intentionally
// leaves the relay plane alone because relay credentials are bearer tokens and
// RelayCORS has its own non-credentialed policy.
func DashboardCORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isDashboardBrowserPath(c.Request.URL.Path) {
			c.Next()
			return
		}

		origin := strings.TrimSpace(c.GetHeader("Origin"))
		if origin == "" {
			c.Next()
			return
		}
		c.Header("Vary", "Origin")
		if !dashboardOriginAllowed(c.Request, origin) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": "非法来源",
			})
			return
		}

		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id, X-Security-Proof")
		c.Header("Access-Control-Expose-Headers", "X-Request-Id")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func isDashboardBrowserPath(path string) bool {
	// These two read-only, bearer-token surfaces intentionally use RelayCORS
	// so third-party usage dashboards can call them without browser cookies.
	if path == "/api/log/token" || path == "/api/usage/token" || path == "/api/usage/token/" {
		return false
	}
	return path == "/api" || strings.HasPrefix(path, "/api/") ||
		path == "/pg" || strings.HasPrefix(path, "/pg/")
}

func dashboardOriginAllowed(request *http.Request, origin string) bool {
	normalized, ok := normalizeOrigin(origin, false)
	if !ok {
		return false
	}

	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	if current, valid := normalizeOrigin(scheme+"://"+request.Host, false); valid && normalized == current {
		return true
	}

	configured := append([]string{common.GetEnv("FRONTEND_BASE_URL", "")},
		common.GetEnvStrings("SESSION_COOKIE_TRUSTED_URL")...)
	for _, candidate := range configured {
		if allowed, valid := normalizeOrigin(candidate, true); valid && normalized == allowed {
			return true
		}
	}
	return false
}

// normalizeOrigin canonicalizes an HTTP(S) origin. Configured frontend URLs
// may contain a path, while an Origin request header may not.
func normalizeOrigin(raw string, allowPath bool) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	if !allowPath && parsed.Path != "" && parsed.Path != "/" {
		return "", false
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), true
}
