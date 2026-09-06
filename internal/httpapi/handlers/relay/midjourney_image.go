package relay

import (
	"errors"
	"github.com/gin-gonic/gin"
	operationssvc "github.com/tokenrouter/tokenrouter/internal/operations"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"gorm.io/gorm"
	"mime"
	"net/http"
	"strings"
	"time"
)

const (
	maxMidjourneyImageBytes int64 = 32 << 20
	maxMidjourneyErrorBytes int64 = 16 << 10
)

var midjourneyImageHTTPClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           httpx.SafeDialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       30 * time.Second,
}

func safeMidjourneyImageContentType(value string) (string, bool) {
	if value == "" {
		return "image/jpeg", true
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "application/octet-stream", false
	}
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/avif":
		return value, true
	default:
		return "application/octet-stream", false
	}
}

// RelayMidjourneyImage serves a stored Midjourney result through the SSRF-safe
// gateway so upstream image URLs are not disclosed to dashboard clients.
func RelayMidjourneyImage(c *gin.Context) {
	task, err := operationssvc.GetMidjourneyByMJID(c.Request.Context(), c.Param("id"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "midjourney_task_not_found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "midjourney_task_lookup_failed"})
		return
	}
	if !httpx.SSRFDisabled() {
		if err := httpx.ValidateURL(task.ImageUrl); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "request blocked: " + err.Error()})
			return
		}
	}

	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, task.ImageUrl, nil)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "request blocked: " + err.Error()})
		return
	}
	response, err := midjourneyImageHTTPClient.Do(request)
	if err != nil {
		if c.Request.Context().Err() != nil {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "http_get_image_failed"})
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.ContentLength > maxMidjourneyErrorBytes {
			c.JSON(http.StatusBadGateway, gin.H{"error": "upstream_error_response_too_large"})
			return
		}
		body, readErr := httpx.ReadAllLimited(response.Body, maxMidjourneyErrorBytes)
		if readErr != nil {
			if errors.Is(readErr, httpx.ErrBodyTooLarge) {
				c.JSON(http.StatusBadGateway, gin.H{"error": "upstream_error_response_too_large"})
				return
			}
			c.JSON(http.StatusBadGateway, gin.H{"error": "http_read_image_failed"})
			return
		}
		status := response.StatusCode
		if status < http.StatusBadRequest || status > 599 {
			status = http.StatusBadGateway
		}
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = "upstream_image_request_failed"
		}
		c.JSON(status, gin.H{"error": message})
		return
	}
	if response.ContentLength > maxMidjourneyImageBytes {
		c.JSON(http.StatusBadGateway, gin.H{"error": "image_response_too_large"})
		return
	}
	body, err := httpx.ReadAllLimited(response.Body, maxMidjourneyImageBytes)
	if err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			c.JSON(http.StatusBadGateway, gin.H{"error": "image_response_too_large"})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "http_read_image_failed"})
		return
	}

	contentType, safeInline := safeMidjourneyImageContentType(response.Header.Get("Content-Type"))
	if !safeInline {
		c.Header("Content-Disposition", "attachment")
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, body)
}
