package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// SecureVerificationRequired protects channel key disclosure. Other sensitive
// operations validate their narrower proof scopes in their controllers.
func SecureVerificationRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !RequireSecurityProof(c, service.SecurityProofScopeChannelKeyRead,
			[]string{service.SecurityProofMethod2FA, service.SecurityProofMethodPasskey}) {
			return
		}
		c.Set("secure_verified", true)
		c.Next()
	}
}

// GetSessionAuthIdentity resolves the authenticated dashboard session identity
// (user + session + auth/version counters) that security proofs bind to.
func GetSessionAuthIdentity(c *gin.Context) (service.SessionIdentity, bool) {
	claims, err := dashboardClaims(c)
	if err != nil || claims.UserID <= 0 {
		return service.SessionIdentity{}, false
	}
	// The session id travels in the refresh cookie as "<sid>.<refresh>" and is
	// the only cookie-scoped handle to the current session.
	var sid string
	if cookie, err := c.Cookie("refresh_token"); err == nil {
		if i := strings.Index(cookie, "."); i > 0 {
			sid = cookie[:i]
		}
	}
	if sid == "" {
		return service.SessionIdentity{}, false
	}
	var session model.UserSession
	if err := model.DB.Where("sid = ? AND user_id = ?", sid, claims.UserID).First(&session).Error; err != nil {
		return service.SessionIdentity{}, false
	}
	if session.Status == "revoked" || session.RevokedAt > 0 {
		return service.SessionIdentity{}, false
	}
	var user model.User
	if err := model.DB.First(&user, claims.UserID).Error; err != nil {
		return service.SessionIdentity{}, false
	}
	return service.SessionIdentity{
		UserID:          claims.UserID,
		SessionID:       sid,
		UserAuthVersion: user.AuthVersion,
		SessionVersion:  session.Version,
	}, true
}

// RequireSecurityProof validates a proof against the authenticated dashboard
// session and writes the shared proof error contract on failure.
func RequireSecurityProof(c *gin.Context, requiredScope string, allowedMethods []string) bool {
	identity, ok := GetSessionAuthIdentity(c)
	if !ok {
		securityProofError(c, "SECURITY_PROOF_INVALID", "安全验证状态无效")
		return false
	}
	raw := strings.TrimSpace(c.GetHeader("X-Security-Proof"))
	if raw == "" {
		securityProofError(c, "SECURITY_PROOF_REQUIRED", "需要安全验证")
		return false
	}
	if _, err := service.VerifySecurityProof(raw, identity, requiredScope, allowedMethods); err != nil {
		switch {
		case errors.Is(err, service.ErrSecurityProofExpired):
			securityProofError(c, "SECURITY_PROOF_EXPIRED", "安全验证已过期")
		case errors.Is(err, service.ErrProofScope):
			securityProofError(c, "SECURITY_PROOF_SCOPE_MISMATCH", "安全验证范围不匹配")
		case errors.Is(err, service.ErrProofMethod):
			securityProofError(c, "SECURITY_PROOF_METHOD_MISMATCH", "安全验证方式不匹配")
		default:
			securityProofError(c, "SECURITY_PROOF_INVALID", "安全验证状态无效")
		}
		return false
	}
	return true
}

func securityProofError(c *gin.Context, code, message string) {
	c.JSON(http.StatusForbidden, gin.H{
		"success": false,
		"message": message,
		"code":    code,
	})
	c.Abort()
}

// RefreshCookieSID extracts the session id from a refresh cookie value
// ("<sid>.<refresh>") — shared with controller/session.go.
func RefreshCookieSID(cookie string) string {
	if i := strings.Index(cookie, "."); i > 0 {
		return cookie[:i]
	}
	return ""
}
