// Package router wires HTTP routes for the dashboard API, relay data plane,
// and the embedded web frontend.
package router

import (
	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/controller"
	"github.com/tokenrouter/tokenrouter/middleware"
)

// SetUpRouter builds the root gin engine.
func SetUpRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.RequestID(), middleware.Recovery())
	r.Use(gin.Logger())

	setupAPIRouter(r)
	setupRelayRouter(r)
	setupDashboardRouter(r)
	return r
}

// setupAPIRouter registers the public dashboard API routes.
func setupAPIRouter(r *gin.Engine) {
	api := r.Group("/api")
	api.Use(middleware.GlobalRateLimit())
	api.GET("/status", controller.GetStatus)
	api.GET("/uptime/status", controller.GetUptimeKumaStatus)
	api.GET("/perf-metrics/summary", controller.GetPerfMetricsSummary)
	api.GET("/setup", controller.GetSetup)
	api.POST("/setup", controller.PostSetup)
	api.GET("/notice", controller.GetNotice)
	api.GET("/about", controller.GetAbout)
	api.GET("/user-agreement", controller.GetUserAgreement)
	api.GET("/privacy-policy", controller.GetPrivacyPolicy)
	api.GET("/home_page_content", controller.GetHomePageContent)
	api.GET("/pricing", controller.GetPricing)
	api.GET("/rankings", controller.GetRankings)
	api.GET("/models", middleware.UserAuth(), controller.GetModels)
	api.GET("/ratio_config", controller.GetRatioConfig)
	api.GET("/user/groups", controller.GetUserGroups)
	api.GET("/subscription/plans", controller.ListSubscriptionPlans)

	// Authentication.
	api.POST("/user/register", middleware.CriticalRateLimit(), controller.Register)
	api.POST("/user/login", middleware.CriticalRateLimit(), controller.Login)
	api.POST("/user/login/2fa", middleware.CriticalRateLimit(), controller.Login2FA)
	api.POST("/user/auth/refresh", middleware.CriticalRateLimit(), middleware.OriginGuard(), controller.RefreshAuth)
	api.POST("/user/auth/logout", middleware.CriticalRateLimit(), middleware.OriginGuard(), controller.AuthLogout)
	api.POST("/user/passkey/login/begin", middleware.CriticalRateLimit(), controller.PasskeyLoginBegin)
	api.POST("/user/passkey/login/finish", middleware.CriticalRateLimit(), controller.PasskeyLoginFinish)

	// OAuth.
	api.GET("/oauth/:provider", controller.HandleOAuth)
	api.GET("/oauth/:provider/callback", controller.OAuthCallback)

	// Email / password reset.
	api.POST("/reset_password", middleware.CriticalRateLimit(), controller.SendPasswordResetEmail)
	api.POST("/user/reset", middleware.CriticalRateLimit(), controller.ResetPassword)
	api.POST("/verification", middleware.CriticalRateLimit(), controller.SendEmailVerification)

	// Payment webhooks.
	api.POST("/stripe/webhook", controller.StripeWebhook)

	// User self-service.
	user := api.Group("/user")
	user.Use(middleware.UserAuth())
	user.GET("/self", controller.GetSelf)
	user.PUT("/self", controller.UpdateSelf)
	user.DELETE("/self", controller.DeleteSelf)
	user.GET("/self/groups", controller.GetUserGroups)
	user.GET("/token", controller.GetSelfTokens)
	user.POST("/token", controller.AddToken)
	user.DELETE("/token/:id", controller.DeleteToken)
	user.GET("/models", controller.GetUserModels)
	user.GET("/2fa/status", controller.GetTwoFAStatus)
	user.POST("/2fa/start", controller.StartTwoFA)
	user.POST("/2fa/enable", controller.EnableTwoFA)
	user.POST("/2fa/disable", controller.DisableTwoFA)
	user.POST("/2fa/verify", controller.VerifyTwoFAForAction)
	user.GET("/passkey/status", controller.PasskeyStatus)
	user.POST("/passkey/register/begin", controller.PasskeyRegisterBegin)
	user.POST("/passkey/register/finish", controller.PasskeyRegisterFinish)
	user.POST("/redemption/redeem", controller.Redeem)
	user.POST("/checkin", controller.CheckIn)
	user.GET("/checkin/status", controller.CheckInStatus)
	user.POST("/topup", controller.TopUp)
	user.GET("/topup", controller.GetSelfTopUps)
	user.POST("/email/bind", controller.BindEmail)
	user.GET("/sessions", controller.GetLoginSessions)
	user.DELETE("/sessions/:sid", controller.DeleteLoginSession)
	user.POST("/subscription/purchase", controller.PurchaseSubscription)
	user.GET("/subscription", controller.GetSelfSubscription)
}

// setupRelayRouter registers the OpenAI-compatible relay data plane.
func setupRelayRouter(r *gin.Engine) {
	relay := r.Group("/v1")
	relay.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth())
	relay.OPTIONS("/*path", func(c *gin.Context) { c.AbortWithStatus(204) })
	relay.POST("/chat/completions", controller.RelayChatCompletions)
	relay.POST("/completions", controller.RelayCompletions)
	relay.POST("/embeddings", controller.RelayEmbeddings)
	relay.POST("/moderations", controller.RelayModerations)
	relay.POST("/images/generations", controller.RelayImageGenerations)
	relay.POST("/images/edits", controller.RelayImageEdits)
	relay.POST("/audio/speech", controller.RelayAudioSpeech)
	relay.POST("/audio/transcriptions", controller.RelayAudioTranscription)
	relay.POST("/audio/translations", controller.RelayAudioTranslation)
	relay.POST("/responses", controller.RelayResponses)
	relay.POST("/rerank", controller.RelayRerank)
	relay.GET("/realtime", controller.RelayRealtime)
	relay.GET("/models", controller.RelayListModels)

	// Task/async platforms (Midjourney, Suno, video) share the task controller.
	task := r.Group("/v1")
	task.Use(middleware.RelayCORS(), middleware.TokenAuth())
	task.POST("/suno/submit", controller.RelaySunoSubmit)
	task.GET("/suno/fetch", controller.RelaySunoFetch)
	task.POST("/video/submit", controller.RelayVideoSubmit)
	task.GET("/video/fetch", controller.RelayVideoFetch)

	mj := r.Group("/mj")
	mj.Use(middleware.RelayCORS(), middleware.TokenAuth())
	mj.POST("/submit/imagine", controller.RelayMidjourneyImagine)
	mj.GET("/task/:id/fetch", controller.RelayMidjourneyFetch)
}

// setupDashboardRouter registers the admin/root dashboard API.
func setupDashboardRouter(r *gin.Engine) {
	admin := r.Group("/api")
	admin.Use(middleware.AdminAuth())
	admin.GET("/channel", controller.GetChannels)
	admin.POST("/channel", controller.AddChannel)
	admin.GET("/channel/:id", controller.GetChannel)
	admin.PUT("/channel", controller.UpdateChannel)
	admin.DELETE("/channel/:id", controller.DeleteChannel)
	admin.GET("/channel/test", controller.TestChannel)
	admin.GET("/ability", controller.GetAbilities)
	admin.POST("/ability", controller.AddAbility)
	admin.DELETE("/ability", controller.DeleteAbility)
	admin.GET("/user", controller.GetUsers)
	admin.GET("/user/:id", controller.GetUser)
	admin.PUT("/user", controller.UpdateUser)
	admin.GET("/token", controller.GetAllTokens)
	admin.GET("/log", controller.GetLogs)
	admin.GET("/data", controller.GetDashboardData)
	admin.GET("/option", controller.GetOptions)
	admin.PUT("/option", controller.UpdateOptions)
	admin.GET("/redemption", controller.GetRedemptions)
	admin.POST("/redemption", controller.CreateRedemption)
	admin.POST("/subscription/plan", controller.CreateSubscriptionPlan)
	admin.GET("/instance", controller.GetSystemInstances)
}
