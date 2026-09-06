package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	model "github.com/tokenrouter/tokenrouter/internal/store"
)

type midjourneyRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn midjourneyRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func setupMidjourneyImageTest(t *testing.T, response func() *http.Response) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "midjourney-image.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Midjourney{}))
	previousDB := model.DB
	previousClient := midjourneyImageHTTPClient
	model.DB = db
	midjourneyImageHTTPClient = &http.Client{Transport: midjourneyRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(), nil
	})}
	t.Cleanup(func() {
		model.DB = previousDB
		midjourneyImageHTTPClient = previousClient
	})
	require.NoError(t, db.Create(&model.Midjourney{MjId: "task-id", ImageUrl: "https://93.184.216.34/image"}).Error)
	router := gin.New()
	router.GET("/mj/image/:id", RelayMidjourneyImage)
	return router
}

func TestRelayMidjourneyImageBoundsErrorResponses(t *testing.T) {
	secretPrefix := "must-not-reflect:"
	router := setupMidjourneyImageTest(t, func() *http.Response {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body: io.NopCloser(strings.NewReader(secretPrefix +
				strings.Repeat("x", int(maxMidjourneyErrorBytes)))),
			Header: make(http.Header),
		}
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/mj/image/task-id", nil))
	assert.Equal(t, http.StatusBadGateway, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "upstream_error_response_too_large")
	assert.NotContains(t, recorder.Body.String(), secretPrefix)
}

func TestRelayMidjourneyImagePreservesBoundedErrorAndNormalizesInvalidStatus(t *testing.T) {
	responses := []*http.Response{
		{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader("rate limited")), Header: make(http.Header)},
		{StatusCode: http.StatusContinue, Body: io.NopCloser(strings.NewReader("invalid upstream status")), Header: make(http.Header)},
	}
	router := setupMidjourneyImageTest(t, func() *http.Response {
		response := responses[0]
		responses = responses[1:]
		return response
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/mj/image/task-id", nil))
	assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "rate limited")

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/mj/image/task-id", nil))
	assert.Equal(t, http.StatusBadGateway, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "invalid upstream status")
}

func TestRelayMidjourneyImageRejectsDeclaredOversizedSuccessWithoutReading(t *testing.T) {
	router := setupMidjourneyImageTest(t, func() *http.Response {
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: maxMidjourneyImageBytes + 1,
			Body:          io.NopCloser(strings.NewReader("must-not-read")),
			Header:        http.Header{"Content-Type": []string{"image/png"}},
		}
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/mj/image/task-id", nil))
	assert.Equal(t, http.StatusBadGateway, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "image_response_too_large")
	assert.NotContains(t, recorder.Body.String(), "must-not-read")
}
