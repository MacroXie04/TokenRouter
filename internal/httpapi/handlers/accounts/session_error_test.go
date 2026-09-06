package accounts

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authsvc "github.com/tokenrouter/tokenrouter/internal/auth"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteAuthSessionErrorMapsGrowthLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "active limit",
			err:        authsvc.ErrSessionLimit,
			wantStatus: http.StatusConflict,
			wantCode:   "AUTH_SESSION_LIMIT",
		},
		{
			name:       "issuance limit",
			err:        authsvc.ErrSessionIssuanceLimit,
			wantStatus: http.StatusTooManyRequests,
			wantCode:   "AUTH_SESSION_ISSUANCE_LIMIT",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", nil)
			writeAuthSessionError(context, test.err)

			assert.Equal(t, test.wantStatus, recorder.Code)
			assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
			var response struct {
				Success bool   `json:"success"`
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			assert.False(t, response.Success)
			assert.Equal(t, test.wantCode, response.Code)
			assert.Equal(t, http.StatusText(test.wantStatus), response.Message)
		})
	}
}

func TestWritePasswordAuthenticationErrorMapsCapacityToStable429(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", nil)

	writePasswordAuthenticationError(context, authsvc.ErrPasswordVerificationBusy)

	assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	assert.Equal(t, "1", recorder.Header().Get("Retry-After"))
	var response struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	assert.Equal(t, "AUTH_PASSWORD_VERIFICATION_BUSY", response.Code)
	assert.Equal(t, http.StatusText(http.StatusTooManyRequests), response.Message)
}
