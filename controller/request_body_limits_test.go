package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
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
	assert.ErrorIs(t, err, common.ErrBodyTooLarge)
}

func TestUpdateChannelRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prefix := `{"id":0}`
	exact := prefix + strings.Repeat(" ", int(maxChannelUpdateBodyBytes)-len(prefix))

	router := gin.New()
	router.PUT("/channel", UpdateChannel)

	request := httptest.NewRequest(http.MethodPut, "/channel", strings.NewReader(exact))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())

	request = httptest.NewRequest(http.MethodPut, "/channel", strings.NewReader(exact+" "))
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "请求体过大")
}

func TestAddChannelRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prefix := `{"name":"x","type":1,"key":"k"}`
	exact := prefix + strings.Repeat(" ", int(maxChannelUpdateBodyBytes)-len(prefix))

	router := gin.New()
	router.POST("/channel", AddChannel)

	request := httptest.NewRequest(http.MethodPost, "/channel", strings.NewReader(exact+" "))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "请求体过大")
}

func TestRejectPasskeyBodyMapsOversizeSeparately(t *testing.T) {
	router := gin.New()
	router.GET("/oversized", func(c *gin.Context) { rejectPasskeyBody(c, common.ErrBodyTooLarge) })
	router.GET("/malformed", func(c *gin.Context) { rejectPasskeyBody(c, errors.New("malformed")) })

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/oversized", nil))
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/malformed", nil))
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}
