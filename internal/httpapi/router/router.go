// Package router wires HTTP routes for the dashboard API, relay data plane,
// and the embedded web frontend.
package router

import (
	"github.com/gin-gonic/gin"
	relayhandlers "github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/relay"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
)

// SetUpRouter builds the root gin engine.
func SetUpRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.RequestID(), middleware.RequestLogger(), middleware.Recovery(), middleware.RequestBodyLimit(middleware.MaxRequestBodyBytes), middleware.DashboardCORS())

	setupAPIRouter(r)
	setupLegacyDashboardBillingRouter(r)
	setupRelayRouter(r)
	setupTokenRouter(r)
	setupDashboardRouter(r)
	// Unknown relay/dashboard/api paths return the reference's structured
	// RelayNotFound 404; everything else falls through to the caller (the
	// binary installs the SPA NoRoute over this one).
	r.NoRoute(relayhandlers.RelayNotFoundRoute)
	return r
}
