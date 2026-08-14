package controller

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/service"
)

const maxMidjourneyImageBytes int64 = 32 << 20

var midjourneyImageHTTPClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           common.SafeDialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	},
	Timeout: 30 * time.Second,
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
	task, err := service.GetMidjourneyByMJID(c.Request.Context(), c.Param("id"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "midjourney_task_not_found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "midjourney_task_lookup_failed"})
		return
	}
	if !common.SSRFDisabled() {
		if err := common.ValidateURL(task.ImageUrl); err != nil {
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
	if response.ContentLength > maxMidjourneyImageBytes {
		c.JSON(http.StatusBadGateway, gin.H{"error": "image_response_too_large"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxMidjourneyImageBytes+1))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "http_read_image_failed"})
		return
	}
	if int64(len(body)) > maxMidjourneyImageBytes {
		c.JSON(http.StatusBadGateway, gin.H{"error": "image_response_too_large"})
		return
	}
	if response.StatusCode != http.StatusOK {
		c.JSON(response.StatusCode, gin.H{"error": string(body)})
		return
	}

	contentType, safeInline := safeMidjourneyImageContentType(response.Header.Get("Content-Type"))
	if !safeInline {
		c.Header("Content-Disposition", "attachment")
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, body)
}
