package accounts

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadPasskeyBodyEnforcesExactByteLimit(t *testing.T) {
	prefix := `{"flow_token":"flow"}`
	exact := prefix + strings.Repeat(" ", int(maxPasskeyRequestBodyBytes)-len(prefix))
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(exact))
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	body, token, err := readPasskeyBody(context)
	require.NoError(t, err)
	assert.Len(t, body, int(maxPasskeyRequestBodyBytes))
	assert.Equal(t, "flow", token)

	request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(exact+" "))
	context, _ = gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	_, _, err = readPasskeyBody(context)
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)
}

func TestRejectPasskeyBodyMapsOversizeSeparately(t *testing.T) {
	router := gin.New()
	router.GET("/oversized", func(c *gin.Context) { rejectPasskeyBody(c, httpx.ErrBodyTooLarge) })
	router.GET("/malformed", func(c *gin.Context) { rejectPasskeyBody(c, errors.New("malformed")) })

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/oversized", nil))
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/malformed", nil))
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}
