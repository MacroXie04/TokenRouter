package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/web"
)

func newEmbeddedTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	t.Setenv("GLOBAL_WEB_RATE_LIMIT_ENABLE", "false")
	t.Setenv("GOOGLE_ANALYTICS_ID", "")
	t.Setenv("UMAMI_WEBSITE_ID", "")
	t.Setenv("UMAMI_SCRIPT_URL", "")
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	serveEmbedded(engine)
	return engine
}

func discoverEmbeddedAsset(t *testing.T) (string, []byte) {
	t.Helper()
	dist, err := fs.Sub(web.Dist, "dist")
	require.NoError(t, err)
	var selected string
	err = fs.WalkDir(dist, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)
		if selected == "" && !entry.IsDir() && name != "index.html" {
			info, infoErr := entry.Info()
			require.NoError(t, infoErr)
			if info.Mode().IsRegular() && info.Size() >= webCompressionMinBytes {
				selected = name
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, selected, "the built frontend must contain a non-index asset")
	content, err := fs.ReadFile(dist, selected)
	require.NoError(t, err)
	return selected, content
}

func performEmbeddedRequest(engine http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestEmbeddedWebServesRootAndSPAFallback(t *testing.T) {
	engine := newEmbeddedTestRouter(t)
	dist, err := fs.Sub(web.Dist, "dist")
	require.NoError(t, err)
	index, err := fs.ReadFile(dist, "index.html")
	require.NoError(t, err)

	root := performEmbeddedRequest(engine, http.MethodGet, "/", nil)
	require.Equal(t, http.StatusOK, root.Code)
	assert.Equal(t, index, root.Body.Bytes())
	assert.Equal(t, webDocumentCacheControl, root.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", root.Header().Get("X-Content-Type-Options"))
	assert.Contains(t, root.Header().Get("Content-Type"), "text/html")
	require.Len(t, root.Header().Get("Cache-Version"), sha256HexLength)

	spa := performEmbeddedRequest(engine, http.MethodGet, "/settings/billing?tab=history", nil)
	require.Equal(t, http.StatusOK, spa.Code)
	assert.Equal(t, index, spa.Body.Bytes())
	assert.Equal(t, root.Header().Get("Cache-Version"), spa.Header().Get("Cache-Version"))
	assert.Equal(t, webDocumentCacheControl, spa.Header().Get("Cache-Control"))

	missingAsset := performEmbeddedRequest(engine, http.MethodGet, "/static/not-built.js", nil)
	require.Equal(t, http.StatusOK, missingAsset.Code)
	assert.Equal(t, index, missingAsset.Body.Bytes(), "a valid missing web path is an SPA route")
}

const sha256HexLength = 64

func TestEmbeddedWebServesActualAssetWithHTTPMetadata(t *testing.T) {
	engine := newEmbeddedTestRouter(t)
	assetPath, asset := discoverEmbeddedAsset(t)
	target := "/" + assetPath

	response := performEmbeddedRequest(engine, http.MethodGet, target, nil)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, asset, response.Body.Bytes(), "asset lookup must strip the HTTP leading slash")
	assert.Equal(t, webAssetCacheControl, response.Header().Get("Cache-Control"))
	assert.Len(t, response.Header().Get("Cache-Version"), sha256HexLength)
	assert.NotEmpty(t, response.Header().Get("ETag"))
	assert.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
	expectedType := mime.TypeByExtension(strings.ToLower(filepath.Ext(assetPath)))
	if expectedType != "" {
		assert.Equal(t, expectedType, response.Header().Get("Content-Type"))
	} else {
		assert.Equal(t, http.DetectContentType(asset), response.Header().Get("Content-Type"))
	}

	head := performEmbeddedRequest(engine, http.MethodHead, target, nil)
	require.Equal(t, http.StatusOK, head.Code)
	assert.Empty(t, head.Body.Bytes())
	assert.Equal(t, strconv.Itoa(len(asset)), head.Header().Get("Content-Length"))
	assert.Equal(t, response.Header().Get("ETag"), head.Header().Get("ETag"))

	notModified := performEmbeddedRequest(engine, http.MethodGet, target, map[string]string{
		"If-None-Match": response.Header().Get("ETag"),
	})
	assert.Equal(t, http.StatusNotModified, notModified.Code)
	assert.Empty(t, notModified.Body.Bytes())

	ranged := performEmbeddedRequest(engine, http.MethodGet, target, map[string]string{"Range": "bytes=0-15"})
	require.Equal(t, http.StatusPartialContent, ranged.Code)
	assert.Equal(t, asset[:16], ranged.Body.Bytes())
	assert.Equal(t, "bytes 0-15/"+strconv.Itoa(len(asset)), ranged.Header().Get("Content-Range"))
}

func TestEmbeddedWebNegotiatesGzip(t *testing.T) {
	engine := newEmbeddedTestRouter(t)
	assetPath, asset := discoverEmbeddedAsset(t)
	target := "/" + assetPath

	compressed := performEmbeddedRequest(engine, http.MethodGet, target, map[string]string{
		"Accept-Encoding": "br, gzip; q=1",
	})
	require.Equal(t, http.StatusOK, compressed.Code)
	assert.Equal(t, "gzip", compressed.Header().Get("Content-Encoding"))
	assert.Contains(t, compressed.Header().Values("Vary"), "Accept-Encoding")
	decoded := decodeGzipResponse(t, compressed)
	assert.Equal(t, asset, decoded)

	spa := performEmbeddedRequest(engine, http.MethodGet, "/client-side/route", map[string]string{
		"Accept-Encoding": "gzip",
	})
	require.Equal(t, http.StatusOK, spa.Code)
	assert.Equal(t, "gzip", spa.Header().Get("Content-Encoding"))
	assert.Contains(t, string(decodeGzipResponse(t, spa)), `<div id="root"></div>`)

	identity := performEmbeddedRequest(engine, http.MethodGet, target, map[string]string{
		"Accept-Encoding": "*;q=1, gzip;q=0",
	})
	require.Equal(t, http.StatusOK, identity.Code)
	assert.Empty(t, identity.Header().Get("Content-Encoding"))
	assert.Equal(t, asset, identity.Body.Bytes())

	head := performEmbeddedRequest(engine, http.MethodHead, target, map[string]string{"Accept-Encoding": "gzip"})
	require.Equal(t, http.StatusOK, head.Code)
	assert.Empty(t, head.Body.Bytes())
	assert.Equal(t, "gzip", head.Header().Get("Content-Encoding"))
	assert.NotEqual(t, strconv.Itoa(len(asset)), head.Header().Get("Content-Length"))
}

func decodeGzipResponse(t *testing.T, response *httptest.ResponseRecorder) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(response.Body.Bytes()))
	require.NoError(t, err)
	decoded, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	return decoded
}

func TestEmbeddedWebStructuredServiceFallbacks(t *testing.T) {
	engine := newEmbeddedTestRouter(t)
	for _, target := range []string{
		"/api/not-a-route",
		"/v1/not-a-route?debug=true",
		"/assets/not-built.svg",
		"/apix",
	} {
		t.Run(target, func(t *testing.T) {
			response := performEmbeddedRequest(engine, http.MethodGet, target, nil)
			require.Equal(t, http.StatusNotFound, response.Code)
			assert.Equal(t, webErrorCacheControl, response.Header().Get("Cache-Control"))
			assert.Contains(t, response.Header().Get("Content-Type"), "application/json")
			var payload struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
			assert.Equal(t, "invalid_request_error", payload.Error.Type)
			assert.Contains(t, payload.Error.Message, "Invalid URL")
		})
	}
}

func TestEmbeddedWebRejectsTraversalAndMalformedPaths(t *testing.T) {
	engine := newEmbeddedTestRouter(t)
	assetPath, asset := discoverEmbeddedAsset(t)
	cases := []struct {
		name    string
		path    string
		rawPath string
	}{
		{name: "parent traversal", path: "/../" + assetPath},
		{name: "dot segment", path: "/./" + assetPath},
		{name: "backslash", path: `/static\\` + filepath.Base(assetPath)},
		{name: "encoded control", path: "/static\n" + filepath.Base(assetPath)},
		{name: "ambiguous double slash", path: "//" + assetPath},
		{name: "malformed raw escape", path: "/safe", rawPath: "/%zz"},
		{name: "raw path mismatch", path: "/safe", rawPath: "/different"},
		{name: "oversized", path: "/" + strings.Repeat("a", maxWebRequestPathBytes)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
			request.URL.Path = testCase.path
			request.URL.RawPath = testCase.rawPath
			request.RequestURI = testCase.path
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Equal(t, webErrorCacheControl, recorder.Header().Get("Cache-Control"))
			assert.NotEqual(t, asset, recorder.Body.Bytes())
			assert.Contains(t, recorder.Body.String(), "invalid_path")
		})
	}

	dist, err := fs.Sub(web.Dist, "dist")
	require.NoError(t, err)
	assert.True(t, fileExists(dist, "/"+assetPath), "HTTP paths must be normalized before fs.FS lookup")
	assert.False(t, fileExists(dist, "/../"+assetPath))
}

func TestEmbeddedWebRejectsNonReadMethods(t *testing.T) {
	engine := newEmbeddedTestRouter(t)
	response := performEmbeddedRequest(engine, http.MethodPost, "/dashboard", nil)
	require.Equal(t, http.StatusMethodNotAllowed, response.Code)
	assert.Equal(t, "GET, HEAD", response.Header().Get("Allow"))
	assert.Equal(t, webErrorCacheControl, response.Header().Get("Cache-Control"))
}

func TestEmbeddedWebDoesNotWrapRegisteredAPIRoutes(t *testing.T) {
	t.Setenv("GLOBAL_WEB_RATE_LIMIT_ENABLE", "true")
	t.Setenv("GLOBAL_WEB_RATE_LIMIT", "1")
	t.Setenv("GOOGLE_ANALYTICS_ID", "")
	t.Setenv("UMAMI_WEBSITE_ID", "")
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/api/registered", func(c *gin.Context) {
		c.Header("X-API-Handler", "yes")
		c.String(http.StatusOK, "api-response")
	})
	serveEmbedded(engine)

	for range 2 {
		response := performEmbeddedRequest(engine, http.MethodGet, "/api/registered", map[string]string{
			"Accept-Encoding": "gzip",
		})
		require.Equal(t, http.StatusOK, response.Code)
		assert.Equal(t, "api-response", response.Body.String())
		assert.Equal(t, "yes", response.Header().Get("X-API-Handler"))
		assert.Empty(t, response.Header().Get("Content-Encoding"))
		assert.Empty(t, response.Header().Get("Cache-Version"))
	}
}

type deterministicWebLimiter struct {
	mu    sync.Mutex
	limit int
	seen  map[string]int
	err   error
}

func (limiter *deterministicWebLimiter) Allow(_ context.Context, key string) (bool, error) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.err != nil {
		return false, limiter.err
	}
	limiter.seen[key]++
	return limiter.seen[key] <= limiter.limit, nil
}

func TestEmbeddedWebRateLimitIsWebOnly(t *testing.T) {
	t.Setenv("GLOBAL_WEB_RATE_LIMIT_ENABLE", "false")
	dist, err := fs.Sub(web.Dist, "dist")
	require.NoError(t, err)
	handler, err := newEmbeddedWebHandler(dist)
	require.NoError(t, err)
	handler.retryAfter = 37
	handler.limiter = &deterministicWebLimiter{limit: 1, seen: make(map[string]int)}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.NoRoute(handler.ServeGIN)

	first := httptest.NewRequest(http.MethodGet, "/one", nil)
	first.RemoteAddr = "198.51.100.20:1000"
	firstResponse := httptest.NewRecorder()
	engine.ServeHTTP(firstResponse, first)
	require.Equal(t, http.StatusOK, firstResponse.Code)

	second := httptest.NewRequest(http.MethodGet, "/two", nil)
	second.RemoteAddr = "198.51.100.20:1001"
	secondResponse := httptest.NewRecorder()
	engine.ServeHTTP(secondResponse, second)
	require.Equal(t, http.StatusTooManyRequests, secondResponse.Code)
	assert.Equal(t, "37", secondResponse.Header().Get("Retry-After"))
	assert.Equal(t, webErrorCacheControl, secondResponse.Header().Get("Cache-Control"))

	service := httptest.NewRequest(http.MethodGet, "/api/missing", nil)
	service.RemoteAddr = "198.51.100.20:1002"
	serviceResponse := httptest.NewRecorder()
	engine.ServeHTTP(serviceResponse, service)
	assert.Equal(t, http.StatusNotFound, serviceResponse.Code, "service fallbacks do not consume or observe the web limiter")
}

func TestEmbeddedWebRateLimitConfigurationIsBoundedAndUsesSeconds(t *testing.T) {
	dist, err := fs.Sub(web.Dist, "dist")
	require.NoError(t, err)
	t.Setenv("GLOBAL_WEB_RATE_LIMIT_ENABLE", "true")
	t.Setenv("GLOBAL_WEB_RATE_LIMIT", "7")
	t.Setenv("GLOBAL_WEB_RATE_LIMIT_DURATION", "11")
	handler, err := newEmbeddedWebHandler(dist)
	require.NoError(t, err)
	limiter, ok := handler.limiter.(*common.RateLimiter)
	require.True(t, ok)
	assert.Equal(t, 7, limiter.Limit)
	assert.Equal(t, 11*time.Second, limiter.Window)
	assert.Equal(t, 11, handler.retryAfter)

	for _, invalid := range []struct {
		limit    string
		duration string
	}{
		{limit: "0", duration: "0"},
		{limit: "100001", duration: "86401"},
		{limit: "not-a-number", duration: "not-a-number"},
	} {
		t.Setenv("GLOBAL_WEB_RATE_LIMIT", invalid.limit)
		t.Setenv("GLOBAL_WEB_RATE_LIMIT_DURATION", invalid.duration)
		handler, err = newEmbeddedWebHandler(dist)
		require.NoError(t, err)
		limiter, ok = handler.limiter.(*common.RateLimiter)
		require.True(t, ok)
		assert.Equal(t, 120, limiter.Limit)
		assert.Equal(t, 180*time.Second, limiter.Window)
	}

	t.Setenv("GLOBAL_WEB_RATE_LIMIT_ENABLE", "false")
	handler, err = newEmbeddedWebHandler(dist)
	require.NoError(t, err)
	assert.Nil(t, handler.limiter)
}

func TestInjectAnalyticsEscapesAndValidatesConfiguration(t *testing.T) {
	document := `<html><head><title>Safe</title></head><body></body></html>`
	t.Setenv("GOOGLE_ANALYTICS_ID", "G-ABC_123")
	t.Setenv("UMAMI_WEBSITE_ID", "site-123")
	t.Setenv("UMAMI_SCRIPT_URL", "https://stats.example.test/script.js?site=a&mode=b")
	output := injectAnalytics(document)
	assert.Contains(t, output, "googletagmanager.com/gtag/js?id=G-ABC_123")
	assert.Contains(t, output, "gtag('config','G-ABC_123')")
	assert.Contains(t, output, `src="https://stats.example.test/script.js?site=a&amp;mode=b"`)
	assert.Contains(t, output, `data-website-id="site-123"`)
	assert.Less(t, strings.Index(output, "googletagmanager.com"), strings.Index(output, "</head>"))

	t.Setenv("GOOGLE_ANALYTICS_ID", `G-X');alert(document.domain)//`)
	t.Setenv("UMAMI_WEBSITE_ID", `site\"><script>alert(1)</script>`)
	t.Setenv("UMAMI_SCRIPT_URL", "javascript:alert(1)")
	rejected := injectAnalytics(document)
	assert.Equal(t, document, rejected)
	assert.NotContains(t, rejected, "alert")

	for _, unsafeURL := range []string{
		"http://stats.example.test/script.js",
		"https://user:secret@stats.example.test/script.js",
		"https://stats.example.test/script.js#fragment",
		"//stats.example.test/script.js",
	} {
		t.Setenv("GOOGLE_ANALYTICS_ID", "")
		t.Setenv("UMAMI_WEBSITE_ID", "site-123")
		t.Setenv("UMAMI_SCRIPT_URL", unsafeURL)
		assert.Equal(t, document, injectAnalytics(document), unsafeURL)
	}

	t.Setenv("GOOGLE_ANALYTICS_ID", "G-VALID")
	t.Setenv("UMAMI_WEBSITE_ID", "")
	assert.Equal(t, "<html><body>no head</body></html>", injectAnalytics("<html><body>no head</body></html>"))
}

func TestNormalizedAssetPath(t *testing.T) {
	path, ok := normalizedAssetPath("/static/js/app.js")
	assert.True(t, ok)
	assert.Equal(t, "static/js/app.js", path)
	for _, invalid := range []string{"/../secret", "/static/../secret", `/static\\secret`, "//static/app.js"} {
		_, valid := normalizedAssetPath(invalid)
		assert.False(t, valid, invalid)
	}

	parsed, err := url.Parse("https://example.test/static/app.js")
	require.NoError(t, err)
	assert.True(t, validWebRequestPath(parsed))
}
