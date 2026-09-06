package router

import (
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/tokens"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
)

// setupTokenRouter registers the user-scoped token management group (the
// reference contract: every route operates on the signed-in user's own
// tokens, keys are masked everywhere except the disclosure endpoints).
func setupTokenRouter(r *gin.Engine) {
	token := r.Group("/api/token")
	token.Use(middleware.UserAuth())
	token.GET("/", tokens.GetAllTokens)
	token.GET("/search", middleware.SearchRateLimit(), tokens.SearchTokens)
	token.GET("/auto-groups", tokens.GetTokenAutoGroups)
	token.GET("/:id", tokens.GetToken)
	token.POST("/:id/key", middleware.CriticalRateLimit(), middleware.DisableCache(), tokens.GetTokenKey)
	token.POST("/", tokens.AddToken)
	token.PUT("/", tokens.UpdateToken)
	token.DELETE("/:id", tokens.DeleteToken)
	token.POST("/batch", tokens.DeleteTokenBatch)
	token.POST("/batch/keys", middleware.CriticalRateLimit(), middleware.DisableCache(), tokens.GetTokenKeysBatch)

	usage := r.Group("/api/usage")
	usage.Use(middleware.RelayCORS(), middleware.TokenAuthReadOnly())
	usage.GET("/token/", tokens.GetTokenUsage)
	usage.OPTIONS("/token/", func(c *gin.Context) { c.AbortWithStatus(204) })
}
