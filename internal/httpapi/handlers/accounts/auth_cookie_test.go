package accounts

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authsvc "github.com/tokenrouter/tokenrouter/internal/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthCookiesUseExplicitBrowserSecurityAttributes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, secure := range []string{"false", "true"} {
		t.Run("secure="+secure, func(t *testing.T) {
			t.Setenv("SESSION_COOKIE_SECURE", secure)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)

			setAuthCookies(context, "session", "access", "refresh")

			cookies := recorder.Header().Values("Set-Cookie")
			assert.Len(t, cookies, 2)
			for _, cookie := range cookies {
				assert.Contains(t, cookie, "HttpOnly")
				assert.Contains(t, cookie, "SameSite=Lax")
				assert.Equal(t, secure == "true", strings.Contains(cookie, "; Secure"))
			}
		})
	}
}

func TestRefreshedAuthCookiesRetainAttributesAndAbsoluteExpiry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, secure := range []string{"false", "true"} {
		t.Run("secure="+secure, func(t *testing.T) {
			t.Setenv("SESSION_COOKIE_SECURE", secure)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			validatedAt := int64(1_700_000_000)
			expiresAt := validatedAt + int64(time.Hour/time.Second)

			require.NoError(t, setRefreshedAuthCookies(
				context, "session", "access", "refresh", expiresAt, validatedAt,
			))
			var refresh *http.Cookie
			for _, cookie := range recorder.Result().Cookies() {
				if cookie.Name == refreshCookie {
					refresh = cookie
				}
			}
			require.NotNil(t, refresh)
			assert.Equal(t, "session.refresh", refresh.Value)
			assert.Equal(t, "/", refresh.Path)
			assert.Equal(t, expiresAt, refresh.Expires.Unix())
			assert.InDelta(t, int(time.Hour/time.Second), refresh.MaxAge, 1)
			assert.Equal(t, secure == "true", refresh.Secure)
			assert.True(t, refresh.HttpOnly)
			assert.Equal(t, http.SameSiteLaxMode, refresh.SameSite)
		})
	}
}

func TestRefreshedAuthCookiesRejectInvalidExpiryBeforeWriting(t *testing.T) {
	for _, test := range []struct {
		name        string
		expiresAt   int64
		validatedAt int64
	}{
		{name: "expired", expiresAt: 99, validatedAt: 100},
		{name: "missing database timestamp", expiresAt: 100, validatedAt: 0},
		{name: "longer than refresh ttl", expiresAt: 100 + int64(authsvc.RefreshTokenTTL/time.Second) + 1, validatedAt: 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			err := setRefreshedAuthCookies(
				context, "session", "access", "refresh", test.expiresAt, test.validatedAt,
			)
			assert.True(t, errors.Is(err, authsvc.ErrSessionExpiryInvalid))
			assert.Empty(t, recorder.Header().Values("Set-Cookie"))
		})
	}
}

func TestClearedAuthCookiesMatchConfiguredSecurityAttributes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("SESSION_COOKIE_SECURE", "true")
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)

	clearAuthCookies(context)

	cookies := recorder.Header().Values("Set-Cookie")
	assert.Len(t, cookies, 2)
	for _, cookie := range cookies {
		assert.Contains(t, cookie, "Max-Age=0")
		assert.Contains(t, cookie, "HttpOnly")
		assert.Contains(t, cookie, "Secure")
		assert.Contains(t, cookie, "SameSite=Lax")
	}
}
