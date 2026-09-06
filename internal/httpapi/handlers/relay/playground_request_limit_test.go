package relay

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var errInjectedPlaygroundRead = errors.New("injected playground read failure")

type playgroundErrorReader struct{}

func (playgroundErrorReader) Read([]byte) (int, error) { return 0, errInjectedPlaygroundRead }

func playgroundLimitContext(body io.Reader) *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/pg/chat/completions", body)
	return ctx
}

func TestPlaygroundRequestGroupBodyLimit(t *testing.T) {
	base := `{"group":"vip"}`
	require.Greater(t, maxPlaygroundRequestBodyBytes, int64(len(base)))
	exact := base + strings.Repeat(" ", int(maxPlaygroundRequestBodyBytes)-len(base))
	group, err := playgroundRequestGroup(playgroundLimitContext(strings.NewReader(exact)))
	require.NoError(t, err)
	assert.Equal(t, "vip", group)

	over := base + strings.Repeat(" ", int(maxPlaygroundRequestBodyBytes)+1-len(base))
	_, err = playgroundRequestGroup(playgroundLimitContext(strings.NewReader(over)))
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)

	_, err = playgroundRequestGroup(playgroundLimitContext(playgroundErrorReader{}))
	assert.ErrorIs(t, err, errInjectedPlaygroundRead)
}

func TestPlaygroundOversizedRequestUsesPayloadTooLargeStatus(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	writePlaygroundRequestError(ctx, httpx.ErrBodyTooLarge)
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "Playground 请求体过大")
}
