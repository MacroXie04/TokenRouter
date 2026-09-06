package relay

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRelayJimengMissingRequestContextPreservesProtocolError(t *testing.T) {
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/jimeng/submit", nil)
	RelayJimeng(c)
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.JSONEq(t, `{"code":"invalid_request","message":"Jimeng request context is missing","data":null}`, response.Body.String())
}
