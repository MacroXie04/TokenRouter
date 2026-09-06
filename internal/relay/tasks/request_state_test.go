package tasks

import (
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
)

func authenticatedTaskHandler(handler func(*gin.Context, relaycommon.RequestState)) gin.HandlerFunc {
	return func(c *gin.Context) { handler(c, middleware.CaptureRelayRequestState(c)) }
}
