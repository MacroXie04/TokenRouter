package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSunoRoutesUseExactRootSurface(t *testing.T) {
	router := SetUpRouter()
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/suno/submit/MUSIC"},
		{http.MethodPost, "/suno/fetch"},
		{http.MethodGet, "/suno/fetch/task_0123456789abcdefghijklmnopqrstuv"},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, test.method+" "+test.path)
	}

	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/suno/submit"},
		{http.MethodGet, "/v1/suno/fetch"},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusNotFound, recorder.Code, test.method+" "+test.path)
	}
}
