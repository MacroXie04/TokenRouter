package router

import (
	"github.com/gin-gonic/gin"
	relayhandlers "github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/relay"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
)

// setupLegacyDashboardBillingRouter exposes the OpenAI-compatible account
// balance endpoints consumed by older clients and by channel balance probes.
func setupLegacyDashboardBillingRouter(r *gin.Engine) {
	dashboard := r.Group("")
	dashboard.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth())
	for _, prefix := range []string{"", "/v1"} {
		dashboard.GET(prefix+"/dashboard/billing/subscription", relayhandlers.GetDashboardSubscription)
		dashboard.GET(prefix+"/dashboard/billing/usage", relayhandlers.GetDashboardUsage)
	}
}
