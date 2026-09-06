package router_test

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), "body: %s", rec.Body.String())
	return m
}

// performSessionRequest sends a request through the fully assembled router.
func performSessionRequest(handler http.Handler, sid, access, refresh, method, path, body string, headers ...map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: sid + "." + refresh})
	for _, headerSet := range headers {
		for key, value := range headerSet {
			req.Header.Set(key, value)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
