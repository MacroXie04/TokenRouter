package router

import (
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/accounts"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/commerce"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/operations"
	publicapi "github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/public"
	relayhandlers "github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/relay"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
)

// setupAPIRouter registers the public dashboard API routes.
func setupAPIRouter(r *gin.Engine) {
	api := r.Group("/api")
	api.Use(middleware.GlobalRateLimit())
	api.GET("/status", publicapi.GetStatus)
	api.GET("/status/test", middleware.AdminAuth(), publicapi.TestStatus)
	api.GET("/uptime/status", publicapi.GetUptimeKumaStatus)
	api.GET("/perf-metrics/summary", middleware.HeaderNavModulePublicOrUserAuth("pricing"), operations.GetPerfMetricsSummary)
	api.GET("/perf-metrics", middleware.HeaderNavModulePublicOrUserAuth("pricing"), operations.GetPerfMetrics)
	api.GET("/setup", publicapi.GetSetup)
	api.POST("/setup", middleware.AnonymousRequestBodyLimit(), publicapi.PostSetup)
	api.GET("/notice", publicapi.GetNotice)
	api.GET("/about", publicapi.GetAbout)
	api.GET("/user-agreement", publicapi.GetUserAgreement)
	api.GET("/privacy-policy", publicapi.GetPrivacyPolicy)
	api.GET("/home_page_content", publicapi.GetHomePageContent)
	api.GET("/pricing", middleware.HeaderNavModuleAuth("pricing"), publicapi.GetPricing)
	api.GET("/rankings", middleware.HeaderNavModuleAuth("rankings"), publicapi.GetRankings)
	api.GET("/models", middleware.UserAuth(), publicapi.GetModels)
	api.GET("/ratio_config", middleware.CriticalRateLimit(), publicapi.GetRatioConfig)
	api.GET("/user/groups", publicapi.GetUserGroups)

	// Keep every reference /api/data route beneath the already rate-limited
	// API group, with authentication applied exactly once per route.
	data := api.Group("/data")
	data.GET("/", middleware.AdminAuth(), operations.GetAllQuotaDates)
	data.GET("/users", middleware.AdminAuth(), operations.GetQuotaDatesByUser)
	data.GET("/self", middleware.UserAuth(), operations.GetUserQuotaDates)
	data.GET("/flow", middleware.AdminAuth(), operations.GetAllFlowQuotaDates)
	data.GET("/flow/self", middleware.UserAuth(), operations.GetUserFlowQuotaDates)

	// User subscription surface (reference /api/subscription group).
	sub := r.Group("/api/subscription")
	sub.Use(middleware.GlobalRateLimit(), middleware.UserAuth())
	sub.GET("/plans", commerce.ListSubscriptionPlans)
	sub.GET("/self", commerce.GetSelfSubscription)
	sub.PUT("/self/preference", commerce.UpdateSubscriptionPreference)
	sub.POST("/balance/pay", middleware.CriticalRateLimit(), commerce.SubscriptionRequestBalancePay)
	sub.POST("/epay/pay", middleware.CriticalRateLimit(), commerce.SubscriptionRequestEpay)
	sub.POST("/stripe/pay", middleware.CriticalRateLimit(), commerce.SubscriptionRequestStripePay)
	sub.POST("/creem/pay", middleware.CriticalRateLimit(), commerce.SubscriptionRequestCreemPay)
	sub.POST("/waffo-pancake/pay", middleware.CriticalRateLimit(), commerce.SubscriptionRequestWaffoPancakePay)

	// Authentication.
	api.POST("/user/register", middleware.CriticalRateLimit(), middleware.AnonymousRequestBodyLimit(), middleware.TurnstileCheck(), accounts.Register)
	api.POST("/user/login", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.AnonymousRequestBodyLimit(), middleware.TurnstileCheck(), accounts.Login)
	api.POST("/user/login/2fa", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.AnonymousRequestBodyLimit(), accounts.Login2FA)
	api.POST("/user/auth/refresh", middleware.CriticalRateLimit(), middleware.OriginGuard(), middleware.DisableCache(), accounts.RefreshAuth)
	api.POST("/user/auth/logout", middleware.CriticalRateLimit(), middleware.OriginGuard(), accounts.AuthLogout)
	api.POST("/user/passkey/login/begin", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.AnonymousRequestBodyLimit(), accounts.PasskeyLoginBegin)
	api.POST("/user/passkey/login/finish", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.AnonymousRequestBodyLimit(), accounts.PasskeyLoginFinish)

	// OAuth.
	api.POST("/oauth/state", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.TryUserAuth(), middleware.AnonymousRequestBodyLimit(), accounts.GenerateOAuthCode)
	api.POST("/oauth/email/bind", middleware.UserAuth(), middleware.CriticalRateLimit(), accounts.EmailBind)
	api.GET("/oauth/wechat", middleware.CriticalRateLimit(), middleware.DisableCache(), accounts.WeChatAuth)
	api.POST("/oauth/wechat/bind", middleware.UserAuth(), middleware.CriticalRateLimit(), accounts.WeChatBind)
	api.GET("/oauth/telegram/login", middleware.CriticalRateLimit(), middleware.DisableCache(), accounts.TelegramLogin)
	api.POST("/oauth/telegram/bind/start", middleware.UserAuth(), middleware.CriticalRateLimit(), middleware.DisableCache(), accounts.TelegramBindStart)
	api.GET("/oauth/telegram/bind/:flow_token", middleware.CriticalRateLimit(), middleware.DisableCache(), accounts.TelegramBindFinish)
	api.GET("/oauth/:provider", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.TryUserAuth(), accounts.HandleOAuth)
	api.GET("/oauth/:provider/callback", middleware.DisableCache(), accounts.OAuthCallback)

	// Email / password reset.
	api.GET("/reset_password", middleware.CriticalRateLimit(), middleware.TurnstileCheck(), accounts.SendPasswordResetEmail)
	api.POST("/reset_password", middleware.CriticalRateLimit(), middleware.TurnstileCheck(), accounts.SendPasswordResetEmail)
	api.POST("/user/reset", middleware.CriticalRateLimit(), middleware.AnonymousRequestBodyLimit(), accounts.ResetPassword)
	api.GET("/verification", middleware.EmailVerificationRateLimit(), middleware.TurnstileCheck(), accounts.SendEmailVerification)
	api.POST("/verification", middleware.EmailVerificationRateLimit(), middleware.TurnstileCheck(), accounts.SendEmailVerification)
	api.POST("/verify", middleware.UserAuth(), middleware.CriticalRateLimit(), middleware.DisableCache(), accounts.UniversalVerify)

	// Payment webhooks.
	api.POST("/stripe/webhook", middleware.AnonymousRequestBodyLimit(), commerce.StripeWebhook)
	api.POST("/creem/webhook", middleware.AnonymousRequestBodyLimit(), commerce.CreemWebhook)
	api.POST("/waffo/webhook", middleware.AnonymousRequestBodyLimit(), commerce.WaffoWebhook)
	api.POST("/waffo-pancake/webhook/:env", middleware.AnonymousRequestBodyLimit(), commerce.WaffoPancakeWebhook)
	api.POST("/user/epay/notify", middleware.AnonymousRequestBodyLimit(), commerce.EpayNotify)
	api.GET("/user/epay/notify", commerce.EpayNotify)
	api.POST("/subscription/epay/notify", middleware.AnonymousRequestBodyLimit(), commerce.SubscriptionEpayNotify)
	api.GET("/subscription/epay/notify", commerce.SubscriptionEpayNotify)
	api.GET("/subscription/epay/return", commerce.SubscriptionEpayReturn)
	api.POST("/subscription/epay/return", middleware.AnonymousRequestBodyLimit(), commerce.SubscriptionEpayReturn)

	// User self-service.
	user := api.Group("/user")
	user.Use(middleware.UserAuth())
	user.GET("/self", accounts.GetSelf)
	user.PUT("/self", accounts.UpdateSelf)
	user.DELETE("/self", accounts.DeleteSelf)
	user.GET("/self/groups", publicapi.GetUserGroups)
	user.GET("/token", middleware.CriticalRateLimit(), middleware.UserCriticalRateLimit("access-token"),
		middleware.DisableCache(), accounts.GenerateAccessToken)
	user.GET("/models", publicapi.GetUserModels)
	user.GET("/2fa/status", accounts.GetTwoFAStatus)
	user.POST("/2fa/setup", accounts.StartTwoFA)
	user.POST("/2fa/start", accounts.StartTwoFA)
	user.POST("/2fa/enable", middleware.DisableCache(), accounts.EnableTwoFA)
	user.POST("/2fa/disable", accounts.DisableTwoFA)
	user.POST("/2fa/verify", accounts.VerifyTwoFAForAction)
	user.GET("/passkey/status", accounts.PasskeyStatus)
	user.POST("/passkey/register/begin", accounts.PasskeyRegisterBegin)
	user.POST("/passkey/register/finish", accounts.PasskeyRegisterFinish)
	user.POST("/passkey/verify/begin", middleware.DisableCache(), accounts.PasskeyVerifyBegin)
	user.POST("/passkey/verify/finish", middleware.DisableCache(), accounts.PasskeyVerifyFinish)
	user.POST("/redemption/redeem", commerce.Redeem)
	user.POST("/checkin", middleware.TurnstileCheck(), commerce.CheckIn)
	user.GET("/checkin", commerce.CheckInStatus)
	user.GET("/checkin/status", commerce.CheckInStatus)
	user.POST("/topup", middleware.CriticalRateLimit(), commerce.TopUp)
	user.GET("/topup/self", commerce.GetSelfTopUps)
	user.GET("/topup/info", commerce.GetTopUpInfo)
	user.POST("/pay", middleware.CriticalRateLimit(), commerce.RequestEpay)
	user.POST("/amount", commerce.RequestAmount)
	user.POST("/stripe/pay", middleware.CriticalRateLimit(), commerce.RequestStripePay)
	user.POST("/stripe/amount", commerce.RequestStripeAmount)
	user.POST("/creem/pay", middleware.CriticalRateLimit(), commerce.RequestCreemPay)
	user.POST("/waffo/amount", commerce.RequestWaffoAmount)
	user.POST("/waffo/pay", middleware.CriticalRateLimit(), commerce.RequestWaffoPay)
	user.POST("/waffo-pancake/amount", commerce.RequestWaffoPancakeAmount)
	user.POST("/waffo-pancake/pay", middleware.CriticalRateLimit(), commerce.RequestWaffoPancakePay)
	user.POST("/email/bind", accounts.BindEmail)
	user.GET("/sessions", accounts.GetLoginSessions)
	user.DELETE("/sessions/:sid", accounts.DeleteLoginSession)
	user.POST("/sessions/revoke-others", accounts.RevokeOtherSessions)
	user.GET("/passkey", accounts.GetSelfPasskeys)
	user.DELETE("/passkey", accounts.DeleteSelfPasskeys)
	user.POST("/2fa/backup_codes", accounts.RegenerateBackupCodes)
	user.PUT("/setting", accounts.UpdateUserSetting)
	user.GET("/oauth/bindings", accounts.GetOAuthBindings)
	user.DELETE("/oauth/bindings/:provider_id", accounts.UnbindOAuth)
	user.GET("/aff", accounts.GetSelfAff)
	user.POST("/aff_transfer", accounts.TransferAffQuota)

	// User log self-service (reference /api/log/self group).
	logSelf := r.Group("/api/log")
	logSelf.Use(middleware.UserAuth())
	logSelf.GET("/self", operations.GetUserLogs)
	logSelf.GET("/self/stat", operations.GetLogsSelfStat)
	logSelf.GET("/self/search", middleware.SearchRateLimit(), operations.SearchUserLogs)
	// Relay-token scoped log listing.
	api.GET("/log/token", middleware.RelayCORS(), middleware.CriticalRateLimit(),
		middleware.TokenAuthReadOnly(), operations.GetLogByKey)
	api.OPTIONS("/log/token", middleware.RelayCORS(), func(c *gin.Context) { c.AbortWithStatus(204) })

	// Asynchronous task history is session-scoped; ownership always comes from
	// UserAuth rather than a caller-controlled query parameter.
	taskHistorySelf := r.Group("/api/task")
	taskHistorySelf.Use(middleware.UserAuth())
	taskHistorySelf.GET("/self", relayhandlers.GetUserTask)
	midjourneyHistorySelf := r.Group("/api/mj")
	midjourneyHistorySelf.Use(middleware.UserAuth())
	midjourneyHistorySelf.GET("/self", relayhandlers.GetUserMidjourney)
}
