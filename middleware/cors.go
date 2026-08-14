package middleware

import (
	"net/http"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// RelayCORS returns the CORS middleware for the relay plane. Relay endpoints
// accept cross-origin calls from arbitrary clients, so origins are permissive
// while credentials are disabled.
func RelayCORS() gin.HandlerFunc {
	return cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowHeaders:     []string{"*"},
		ExposeHeaders:    []string{"Content-Length", "X-Request-Id"},
		AllowCredentials: false,
	})
}

// DashboardCORS returns CORS for the dashboard API, restricted to the frontend
// base URL when configured (defaults to localhost origins).
func DashboardCORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", c.GetHeader("Origin"))
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
