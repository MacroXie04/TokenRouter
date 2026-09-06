package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/relay"
	"github.com/tokenrouter/tokenrouter/internal/platform/cache"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	"html"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	webAssetCacheControl      = "max-age=604800"
	webDocumentCacheControl   = "no-cache"
	webErrorCacheControl      = "no-store"
	webCompressionMinBytes    = 256
	maxWebRequestPathBytes    = 8 * 1024
	maxAnalyticsIDBytes       = 128
	maxAnalyticsScriptURLSize = 2 * 1024
)

var analyticsIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// embeddedWebHandler owns the web-only behavior installed as Gin's NoRoute
// handler. Its filesystem and index bytes are immutable after construction and
// therefore safe to serve concurrently.
type embeddedWebHandler struct {
	dist         fs.FS
	index        []byte
	cacheVersion string
	limiter      webRateLimiter
	retryAfter   int
}

type webRateLimiter interface {
	Allow(context.Context, string) (bool, error)
}

func newEmbeddedWebHandler(dist fs.FS) (*embeddedWebHandler, error) {
	if dist == nil {
		return nil, fmt.Errorf("nil embedded filesystem")
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read index.html: %w", err)
	}
	version, err := embeddedDistVersion(dist)
	if err != nil {
		return nil, fmt.Errorf("fingerprint embedded assets: %w", err)
	}

	handler := &embeddedWebHandler{
		dist:         dist,
		index:        index,
		cacheVersion: version,
	}
	if env.GetEnvBool("GLOBAL_WEB_RATE_LIMIT_ENABLE", true) {
		limit := boundedEnvInt("GLOBAL_WEB_RATE_LIMIT", 120, 1, 100_000)
		durationSeconds := boundedEnvInt("GLOBAL_WEB_RATE_LIMIT_DURATION", 180, 1, 86_400)
		handler.retryAfter = durationSeconds
		handler.limiter = cache.NewRateLimiter(limit, time.Duration(durationSeconds)*time.Second, "web")
	}
	return handler, nil
}

func boundedEnvInt(name string, fallback, minimum, maximum int) int {
	value := env.GetEnvInt(name, fallback)
	if value < minimum || value > maximum {
		return fallback
	}
	return value
}

func embeddedDistVersion(dist fs.FS) (string, error) {
	hash := sha256.New()
	err := fs.WalkDir(dist, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := fs.ReadFile(dist, name)
		if err != nil {
			return err
		}
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (handler *embeddedWebHandler) ServeGIN(c *gin.Context) {
	requestPath := c.Request.URL.Path

	// Unknown service endpoints must remain machine-readable errors and must
	// not consume the web-page limiter. Registered API/relay handlers never
	// reach NoRoute in the first place.
	if isServicePath(requestPath) {
		c.Header("Cache-Control", webErrorCacheControl)
		relay.RelayNotFound(c)
		return
	}

	if !validWebRequestPath(c.Request.URL) {
		writeWebError(c, http.StatusBadRequest, "invalid_path", "Invalid request path")
		return
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Header("Allow", "GET, HEAD")
		writeWebError(c, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	if !handler.allow(c) {
		return
	}

	if assetPath, ok := normalizedAssetPath(requestPath); ok && assetPath != "" {
		if info, err := fs.Stat(handler.dist, assetPath); err == nil && info.Mode().IsRegular() {
			asset, readErr := fs.ReadFile(handler.dist, assetPath)
			if readErr != nil {
				writeWebError(c, http.StatusInternalServerError, "asset_unavailable", "Static asset unavailable")
				return
			}
			handler.serveContent(c, filepath.Base(assetPath), asset, webAssetCacheControl)
			return
		}
	}

	// A valid non-file path is a client-side route. The base index remains
	// immutable; analytics are validated and injected into a request-local copy.
	index := []byte(injectAnalytics(string(handler.index)))
	handler.serveContent(c, "index.html", index, webDocumentCacheControl)
}

func (handler *embeddedWebHandler) allow(c *gin.Context) bool {
	if handler.limiter == nil {
		return true
	}
	key := c.ClientIP()
	if key == "" {
		key = "unknown"
	}
	allowed, err := handler.limiter.Allow(c.Request.Context(), key)
	if err != nil {
		// Static availability should not depend on a remote counter store. The
		// in-memory store used by default is atomic and does not return errors.
		logging.Logger.Warn("web rate limiter store error; failing open", "err", err.Error())
		return true
	}
	if allowed {
		return true
	}
	c.Header("Retry-After", strconv.Itoa(handler.retryAfter))
	writeWebError(c, http.StatusTooManyRequests, "rate_limit_exceeded", "Too many requests")
	return false
}

func (handler *embeddedWebHandler) serveContent(c *gin.Context, name string, content []byte, cacheControl string) {
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if contentType == "" {
		contentType = http.DetectContentType(content)
	}
	c.Header("Cache-Control", cacheControl)
	c.Header("Cache-Version", handler.cacheVersion)
	c.Header("Content-Type", contentType)
	c.Header("X-Content-Type-Options", "nosniff")

	compress := len(content) >= webCompressionMinBytes &&
		isCompressibleContentType(contentType) &&
		acceptsGzip(c.GetHeader("Accept-Encoding")) &&
		c.GetHeader("Range") == ""
	if isCompressibleContentType(contentType) {
		addVary(c.Writer.Header(), "Accept-Encoding")
	}

	representation := content
	etagSuffix := "identity"
	if compress {
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(content); err != nil {
			writeWebError(c, http.StatusInternalServerError, "compression_failed", "Static asset unavailable")
			return
		}
		if err := writer.Close(); err != nil {
			writeWebError(c, http.StatusInternalServerError, "compression_failed", "Static asset unavailable")
			return
		}
		representation = compressed.Bytes()
		etagSuffix = "gzip"
		c.Header("Content-Encoding", "gzip")
	}

	etag := contentETag(content, etagSuffix)
	c.Header("ETag", etag)
	if etagMatches(c.GetHeader("If-None-Match"), etag) {
		c.Status(http.StatusNotModified)
		return
	}

	if compress {
		c.Header("Content-Length", strconv.Itoa(len(representation)))
		c.Status(http.StatusOK)
		if c.Request.Method != http.MethodHead {
			_, _ = c.Writer.Write(representation)
		}
		return
	}

	// ServeContent adds standards-compliant HEAD and byte-range handling for
	// the uncompressed representation. Embedded files have no meaningful
	// modification time, so ETag is the validator.
	http.ServeContent(c.Writer, c.Request, name, time.Time{}, bytes.NewReader(content))
}

func isServicePath(path string) bool {
	return strings.HasPrefix(path, "/v1") || strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/assets")
}

func validWebRequestPath(requestURL *url.URL) bool {
	if requestURL == nil {
		return false
	}
	path := requestURL.Path
	if path == "" || len(path) > maxWebRequestPathBytes || !utf8.ValidString(path) ||
		!strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") ||
		containsPathControl(path) || strings.Contains(path, `\`) {
		return false
	}
	if requestURL.RawPath != "" {
		decoded, err := url.PathUnescape(requestURL.RawPath)
		if err != nil || decoded != path {
			return false
		}
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// normalizedAssetPath converts an HTTP path to the slash-less fs.FS form.
// fs.ValidPath rejects traversal and platform-specific separators. This is the
// crucial distinction from passing a leading slash directly to fs.FS.Open.
func normalizedAssetPath(requestPath string) (string, bool) {
	if !strings.HasPrefix(requestPath, "/") || strings.HasPrefix(requestPath, "//") ||
		containsPathControl(requestPath) || strings.Contains(requestPath, `\`) {
		return "", false
	}
	assetPath := strings.TrimPrefix(requestPath, "/")
	if assetPath == "" {
		return "", true
	}
	if !fs.ValidPath(assetPath) {
		return "", false
	}
	return assetPath, true
}

func containsPathControl(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] == 0x7f {
			return true
		}
	}
	return false
}

func isCompressibleContentType(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/javascript" ||
		mediaType == "application/json" ||
		mediaType == "application/wasm" ||
		mediaType == "image/svg+xml"
}

func acceptsGzip(header string) bool {
	explicit := false
	gzipQuality := 0.0
	wildcardQuality := 0.0
	for _, item := range strings.Split(header, ",") {
		parts := strings.Split(item, ";")
		coding := strings.ToLower(strings.TrimSpace(parts[0]))
		quality := 1.0
		for _, parameter := range parts[1:] {
			key, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if !found || !strings.EqualFold(strings.TrimSpace(key), "q") {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil || parsed < 0 || parsed > 1 {
				quality = 0
			} else {
				quality = parsed
			}
		}
		switch coding {
		case "gzip":
			explicit = true
			gzipQuality = quality
		case "*":
			wildcardQuality = quality
		}
	}
	if explicit {
		return gzipQuality > 0
	}
	return wildcardQuality > 0
}

func addVary(header http.Header, value string) {
	for _, existing := range header.Values("Vary") {
		for _, item := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(item), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}

func contentETag(content []byte, suffix string) string {
	digest := sha256.Sum256(content)
	return `"` + hex.EncodeToString(digest[:]) + `-` + suffix + `"`
}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

func writeWebError(c *gin.Context, status int, code, message string) {
	c.Header("Cache-Control", webErrorCacheControl)
	c.Header("X-Content-Type-Options", "nosniff")
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{
		"message": message,
		"type":    "invalid_request_error",
		"param":   "",
		"code":    code,
	}})
}

// injectAnalytics inserts only validated analytics settings. IDs are limited
// to inert token characters, and custom Umami script URLs must be absolute
// HTTPS URLs without credentials or fragments. Attribute values are escaped
// even after validation so future URL-policy changes remain safe.
func injectAnalytics(document string) string {
	var scripts strings.Builder
	if gaID, ok := safeAnalyticsID(env.GetEnv("GOOGLE_ANALYTICS_ID", "")); ok {
		escapedID := html.EscapeString(gaID)
		scripts.WriteString(`<script async src="https://www.googletagmanager.com/gtag/js?id=`)
		scripts.WriteString(url.QueryEscape(gaID))
		scripts.WriteString(`"></script><script>window.dataLayer=window.dataLayer||[];function gtag(){dataLayer.push(arguments);}gtag('js',new Date());gtag('config','`)
		scripts.WriteString(escapedID)
		scripts.WriteString(`');</script>`)
	}
	if websiteID, ok := safeAnalyticsID(env.GetEnv("UMAMI_WEBSITE_ID", "")); ok {
		if scriptURL, valid := safeAnalyticsScriptURL(env.GetEnv("UMAMI_SCRIPT_URL", "https://analytics.umami.is/script.js")); valid {
			scripts.WriteString(`<script defer src="`)
			scripts.WriteString(html.EscapeString(scriptURL))
			scripts.WriteString(`" data-website-id="`)
			scripts.WriteString(html.EscapeString(websiteID))
			scripts.WriteString(`"></script>`)
		}
	}
	if scripts.Len() == 0 {
		return document
	}
	headEnd := strings.LastIndex(strings.ToLower(document), "</head>")
	if headEnd < 0 {
		return document
	}
	return document[:headEnd] + scripts.String() + document[headEnd:]
}

func safeAnalyticsID(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	return value, value != "" && len(value) <= maxAnalyticsIDBytes && analyticsIDPattern.MatchString(value)
}

func safeAnalyticsScriptURL(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > maxAnalyticsScriptURLSize || strings.Contains(value, "#") {
		return "", false
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", false
	}
	return parsed.String(), true
}
