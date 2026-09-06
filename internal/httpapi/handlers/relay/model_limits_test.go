package relay

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRelayModelLimitRejectsEveryProtocolBeforeDispatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		middleware.SetupRelayTokenContext(c, &model.Token{
			ModelLimitsEnabled: true,
			ModelLimits:        "allowed-model",
		})
		c.Set(requestctx.ContextKeyGroup, userssvc.GroupDefault)
		c.Next()
	})
	router.POST("/v1/chat/completions", relayOpenAI)
	router.POST("/v1/messages", RelayClaudeMessages)
	router.POST("/v1beta/models/*path", RelayGeminiNative)
	router.GET("/v1/realtime", middleware.RequireRelayQueryModel("model"), RelayRealtime)

	tests := []struct {
		name         string
		method       string
		path         string
		body         string
		wantFragment string
	}{
		{
			name:         "OpenAI-compatible",
			method:       http.MethodPost,
			path:         "/v1/chat/completions",
			body:         `{"model":"denied-model","messages":[{"role":"user","content":"hello"}]}`,
			wantFragment: `"code":"model_not_allowed"`,
		},
		{
			name:         "Claude",
			method:       http.MethodPost,
			path:         "/v1/messages",
			body:         `{"model":"denied-model","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`,
			wantFragment: `"type":"permission_error"`,
		},
		{
			name:         "Gemini",
			method:       http.MethodPost,
			path:         "/v1beta/models/denied-model:generateContent",
			body:         `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			wantFragment: `"code":"model_not_allowed"`,
		},
		{
			name:         "Realtime WebSocket",
			method:       http.MethodGet,
			path:         "/v1/realtime?model=denied-model",
			wantFragment: `"code":"model_not_allowed"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), test.wantFragment)
		})
	}
}
