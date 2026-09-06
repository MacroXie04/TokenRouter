package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitTrustedProxiesIgnoresForwardedHeadersByDefault(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", "")

	for _, test := range []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{
			name:       "X-Forwarded-For",
			remoteAddr: "198.51.100.10:4312",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.25"},
			want:       "198.51.100.10",
		},
		{
			name:       "X-Real-IP",
			remoteAddr: "198.51.100.11:4313",
			headers:    map[string]string{"X-Real-IP": "203.0.113.26"},
			want:       "198.51.100.11",
		},
		{
			name:       "direct IPv6 remote address",
			remoteAddr: "[2001:db8::10]:4314",
			want:       "2001:db8::10",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := newClientIPRouter()
			InitTrustedProxies(router)
			assert.Equal(t, test.want, observedClientIP(t, router, test.remoteAddr, test.headers))
		})
	}
}

func TestInitTrustedProxiesHonorsHeadersOnlyFromConfiguredProxy(t *testing.T) {
	for _, test := range []struct {
		name         string
		trusted      string
		remoteAddr   string
		headerName   string
		headerValue  string
		wantClientIP string
	}{
		{
			name:         "CIDR trusts X-Forwarded-For",
			trusted:      "192.0.2.0/24",
			remoteAddr:   "192.0.2.40:8000",
			headerName:   "X-Forwarded-For",
			headerValue:  "198.51.100.20, 192.0.2.41",
			wantClientIP: "198.51.100.20",
		},
		{
			name:         "exact IP trusts X-Real-IP",
			trusted:      "192.0.2.42",
			remoteAddr:   "192.0.2.42:8001",
			headerName:   "X-Real-IP",
			headerValue:  "198.51.100.21",
			wantClientIP: "198.51.100.21",
		},
		{
			name:         "unlisted remote ignores forwarded header",
			trusted:      "192.0.2.0/24",
			remoteAddr:   "198.51.100.22:8002",
			headerName:   "X-Forwarded-For",
			headerValue:  "203.0.113.27",
			wantClientIP: "198.51.100.22",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TRUSTED_PROXIES", test.trusted)
			router := newClientIPRouter()
			InitTrustedProxies(router)
			headers := map[string]string{test.headerName: test.headerValue}
			assert.Equal(t, test.wantClientIP, observedClientIP(t, router, test.remoteAddr, headers))
		})
	}
}

func TestInitTrustedProxiesInvalidConfigurationFailsClosed(t *testing.T) {
	for _, trusted := range []string{
		"192.0.2.0/24,not-an-ip",
		"192.0.2.0/24,,198.51.100.1",
		"192.0.2.0/24,none",
	} {
		t.Run(trusted, func(t *testing.T) {
			t.Setenv("TRUSTED_PROXIES", trusted)
			router := newClientIPRouter()
			InitTrustedProxies(router)

			got := observedClientIP(t, router, "192.0.2.40:9000", map[string]string{
				"X-Forwarded-For": "203.0.113.30",
				"X-Real-IP":       "203.0.113.31",
			})
			assert.Equal(t, "192.0.2.40", got)
		})
	}
}

func TestInitTrustedProxiesInvalidConfigurationClearsPriorTrust(t *testing.T) {
	router := newClientIPRouter()
	t.Setenv("TRUSTED_PROXIES", "192.0.2.0/24")
	InitTrustedProxies(router)
	require.Equal(t, "203.0.113.40", observedClientIP(t, router, "192.0.2.40:9001", map[string]string{
		"X-Forwarded-For": "203.0.113.40",
	}))

	t.Setenv("TRUSTED_PROXIES", "192.0.2.0/24,invalid")
	InitTrustedProxies(router)
	assert.Equal(t, "192.0.2.40", observedClientIP(t, router, "192.0.2.40:9001", map[string]string{
		"X-Forwarded-For": "203.0.113.40",
	}))
}

func observedClientIP(
	t *testing.T,
	router *gin.Engine,
	remoteAddr string,
	headers map[string]string,
) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/client-ip", nil)
	request.RemoteAddr = remoteAddr
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	return recorder.Body.String()
}

func newClientIPRouter() *gin.Engine {
	router := gin.New()
	router.GET("/client-ip", func(c *gin.Context) {
		c.String(http.StatusOK, c.ClientIP())
	})
	return router
}
