package middleware

import (
	"bytes"
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/jimeng"
	"io"
	"net/http"
)

const (
	jimengRequestContextKey   = "jimeng_request"
	jimengActionContextKey    = "jimeng_action"
	maxJimengRequestBodyBytes = int64(16 * 1024 * 1024)
)

type JimengRequestContext struct {
	Action  string
	Request jimeng.Request
	RawBody []byte
}

func JimengActionValidate() gin.HandlerFunc {
	return func(c *gin.Context) {
		action, ok := validateJimengAction(c)
		if !ok {
			return
		}
		c.Set(jimengActionContextKey, action)
		c.Next()
	}
}

func JimengRequestConvert() gin.HandlerFunc {
	return func(c *gin.Context) {
		action := c.GetString(jimengActionContextKey)
		if action == "" {
			var ok bool
			action, ok = validateJimengAction(c)
			if !ok {
				return
			}
		}
		if c.Request.ContentLength > maxJimengRequestBodyBytes {
			abortJimengMiddleware(c, http.StatusRequestEntityTooLarge, "Request body exceeds 16 MiB")
			return
		}

		body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxJimengRequestBodyBytes+1))
		if err != nil {
			abortJimengMiddleware(c, http.StatusBadRequest, "Invalid request body")
			return
		}
		if int64(len(body)) > maxJimengRequestBodyBytes {
			abortJimengMiddleware(c, http.StatusRequestEntityTooLarge, "Request body exceeds 16 MiB")
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))
		var request jimeng.Request
		if err := jsonutil.Unmarshal(body, &request); err != nil {
			abortJimengMiddleware(c, http.StatusBadRequest, "Invalid request body")
			return
		}
		if action == jimeng.FetchAction && request.TaskID == "" {
			abortJimengMiddleware(c, http.StatusBadRequest, "task_id is required for CVSync2AsyncGetResult")
			return
		}
		c.Set(jimengRequestContextKey, JimengRequestContext{Action: action, Request: request, RawBody: body})
		c.Next()
	}
}

func validateJimengAction(c *gin.Context) (string, bool) {
	action := c.Query("Action")
	if action == "" {
		abortJimengMiddleware(c, http.StatusBadRequest, "Action query parameter is required")
		return "", false
	}
	if action != jimeng.SubmitAction && action != jimeng.FetchAction {
		abortJimengMiddleware(c, http.StatusBadRequest, "Unsupported Action query parameter")
		return "", false
	}
	return action, true
}

func GetJimengRequest(c *gin.Context) (JimengRequestContext, bool) {
	value, ok := c.Get(jimengRequestContextKey)
	if !ok {
		return JimengRequestContext{}, false
	}
	request, ok := value.(JimengRequestContext)
	return request, ok
}

func abortJimengMiddleware(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{
		"message": message,
		"type":    "new_api_error",
		"code":    "",
	}})
}
