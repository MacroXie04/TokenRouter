package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildTurnstileRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/protected", TurnstileCheck(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

func postTurnstile(t *testing.T, r *gin.Engine, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/protected"+query, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// newTurnstileMock serves the siteverify contract: success only when the
// submitted token is "good-token" and the secret matches.
func newTurnstileMock(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		if r.Form.Get("secret") == "" || r.Form.Get("response") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"success":false}`))
			return
		}
		if r.Form.Get("response") == "good-token" {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTurnstileCheckDisabledFailsOpen(t *testing.T) {
	r := buildTurnstileRouter()
	rec := postTurnstile(t, r, "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"ok":true`)
}

func TestTurnstileCheckEmptyToken(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")
	srv := newTurnstileMock(t)
	t.Setenv("TURNSTILE_VERIFY_URL", srv.URL)
	r := buildTurnstileRouter()
	rec := postTurnstile(t, r, "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"success":false`)
	assert.Contains(t, rec.Body.String(), "Turnstile token 为空")
}

func TestTurnstileCheckValidToken(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")
	srv := newTurnstileMock(t)
	t.Setenv("TURNSTILE_VERIFY_URL", srv.URL)
	r := buildTurnstileRouter()
	rec := postTurnstile(t, r, "?turnstile=good-token")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"ok":true`)
}

func TestTurnstileCheckInvalidToken(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")
	srv := newTurnstileMock(t)
	t.Setenv("TURNSTILE_VERIFY_URL", srv.URL)
	r := buildTurnstileRouter()
	rec := postTurnstile(t, r, "?turnstile=bad-token")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"success":false`)
	assert.Contains(t, rec.Body.String(), "Turnstile 校验失败，请刷新重试！")
}

func TestTurnstileCheckVerifyError(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")
	// Unreachable verify endpoint: the middleware surfaces the transport error.
	t.Setenv("TURNSTILE_VERIFY_URL", "http://127.0.0.1:1/siteverify")
	r := buildTurnstileRouter()
	rec := postTurnstile(t, r, "?turnstile=x")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"success":false`)
	assert.NotContains(t, rec.Body.String(), `"ok":true`)
}
