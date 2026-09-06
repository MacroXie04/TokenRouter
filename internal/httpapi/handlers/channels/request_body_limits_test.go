package channels

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

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
