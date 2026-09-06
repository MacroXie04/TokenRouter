package middleware

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
)

// MaxRequestBodyBytes is the hard ceiling for every HTTP request body. Relay
// handlers may accept payloads up to this size; narrower endpoint-specific
// limits (for example the anonymous API limit) are layered on top.
const MaxRequestBodyBytes int64 = 16 << 20

// RequestBodyLimit installs a streaming hard limit without eagerly buffering
// the request. Known oversized Content-Length values are rejected before any
// route middleware runs; chunked bodies surface http.MaxBytesError as soon as
// a handler attempts to read beyond the ceiling.
func RequestBodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if maxBytes <= 0 || c.Request.Body == nil {
			c.Next()
			return
		}
		if c.Request.ContentLength > maxBytes {
			_ = c.Request.Body.Close()
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// AnonymousRequestBodyLimit bounds the request-body size on anonymous
// endpoints (login, register, password reset, OAuth state, webhooks) to slow
// memory-exhaustion abuse. The limit is configured via
// ANONYMOUS_REQUEST_BODY_LIMIT_KB (default 512 KB; zero disables the limit).
// Oversized bodies get 413; unreadable bodies get 400.
func AnonymousRequestBodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		maxBytes := common.GetAnonymousRequestBodyLimitBytes()
		if maxBytes <= 0 || c.Request.Body == nil {
			c.Next()
			return
		}

		originalBody := c.Request.Body
		limitedBody, err := readAnonymousRequestBody(originalBody, maxBytes)
		_ = originalBody.Close()
		if err != nil {
			if common.IsRequestBodyTooLargeError(err) {
				c.AbortWithStatus(http.StatusRequestEntityTooLarge)
				return
			}
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}

		c.Request.Body = io.NopCloser(bytes.NewReader(limitedBody))
		c.Request.ContentLength = int64(len(limitedBody))
		c.Next()
	}
}

func readAnonymousRequestBody(body io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, common.ErrRequestBodyTooLarge
	}
	return data, nil
}
