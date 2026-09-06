// Package router wires HTTP routes for the dashboard API, relay data plane,
// and the embedded web frontend.
package router

import (
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/accounts"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/channels"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/commerce"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/operations"
	publicapi "github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/public"
	relayhandlers "github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/relay"
	settingshandlers "github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/settings"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/tokens"
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

// setupRelayRouter registers the OpenAI-compatible relay data plane.
func setupRelayRouter(r *gin.Engine) {
	playground := r.Group("/pg")
	playground.Use(middleware.UserAuth())
	playground.POST("/chat/completions", relayhandlers.Playground)

	kling := r.Group("/kling/v1")
	kling.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth())
	kling.OPTIONS("/*path", func(c *gin.Context) { c.AbortWithStatus(204) })
	kling.POST("/videos/text2video", relayhandlers.RelayKlingTask)
	kling.POST("/videos/image2video", relayhandlers.RelayKlingTask)
	kling.GET("/videos/text2video/:task_id", relayhandlers.RelayKlingTaskFetch)
	kling.GET("/videos/image2video/:task_id", relayhandlers.RelayKlingTaskFetch)

	suno := r.Group("/suno")
	suno.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth())
	suno.POST("/submit/:action", relayhandlers.RelaySunoSubmit)
	suno.POST("/fetch", relayhandlers.RelaySunoFetch)
	suno.GET("/fetch/:id", relayhandlers.RelaySunoFetch)

	relay := r.Group("/v1")
	relay.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth(), middleware.ModelRequestRateLimit())
	relay.OPTIONS("/*path", func(c *gin.Context) { c.AbortWithStatus(204) })
	relay.POST("/chat/completions", relayhandlers.RelayChatCompletions)
	relay.POST("/completions", relayhandlers.RelayCompletions)
	relay.POST("/embeddings", relayhandlers.RelayEmbeddings)
	relay.POST("/moderations", relayhandlers.RelayModerations)
	relay.POST("/images/generations", relayhandlers.RelayImageGenerations)
	relay.POST("/images/edits", relayhandlers.RelayImageEdits)
	relay.POST("/audio/speech", relayhandlers.RelayAudioSpeech)
	relay.POST("/audio/transcriptions", relayhandlers.RelayAudioTranscription)
	relay.POST("/audio/translations", relayhandlers.RelayAudioTranslation)
	relay.POST("/responses", relayhandlers.RelayResponses)
	relay.POST("/responses/compact", relayhandlers.RelayResponsesCompact)
	relay.POST("/video/generations", relayhandlers.RelayTask)
	relay.GET("/video/generations/:task_id", relayhandlers.RelayTaskFetch)
	relay.POST("/videos/:video_id/remix", relayhandlers.RelayTask)
	relay.POST("/videos", relayhandlers.RelayTask)
	relay.GET("/videos/:task_id", relayhandlers.RelayTaskFetch)
	relay.POST("/alpha/search", relayhandlers.RelayAlphaSearch)
	relay.POST("/messages", relayhandlers.RelayClaudeMessages)
	relay.POST("/rerank", relayhandlers.RelayRerank)
	relay.POST("/edits", relayhandlers.RelayEdits)
	relay.GET("/realtime", middleware.RequireRelayQueryModel("model"), relayhandlers.RelayRealtime)
	relay.POST("/engines/:model/embeddings", relayhandlers.RelayEnginesEmbeddings)
	relay.POST("/models/*path", relayhandlers.RelayGeminiNative)
	relay.GET("/models", relayhandlers.RelayListModels)
	relay.GET("/models/:model", relayhandlers.RelayRetrieveModel)
	relay.DELETE("/models/:model", relayhandlers.RelayNotImplemented)
	relay.POST("/images/variations", relayhandlers.RelayNotImplemented)
	relay.GET("/files", relayhandlers.RelayNotImplemented)
	relay.POST("/files", relayhandlers.RelayNotImplemented)
	relay.DELETE("/files/:id", relayhandlers.RelayNotImplemented)
	relay.GET("/files/:id", relayhandlers.RelayNotImplemented)
	relay.GET("/files/:id/content", relayhandlers.RelayNotImplemented)
	relay.POST("/fine-tunes", relayhandlers.RelayNotImplemented)
	relay.GET("/fine-tunes", relayhandlers.RelayNotImplemented)
	relay.GET("/fine-tunes/:id", relayhandlers.RelayNotImplemented)
	relay.POST("/fine-tunes/:id/cancel", relayhandlers.RelayNotImplemented)
	relay.GET("/fine-tunes/:id/events", relayhandlers.RelayNotImplemented)

	// Completed video content may be retrieved with either the original relay
	// token or the owning dashboard session. Ownership is checked again before
	// any provider credential is decrypted.
	videoContent := r.Group("/v1")
	videoContent.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenOrUserAuth())
	videoContent.GET("/videos/:task_id/content", relayhandlers.VideoProxy)

	// Gemini protocol surface: /v1beta model listing plus the native Gemini
	// generateContent passthrough.
	v1beta := r.Group("/v1beta")
	v1beta.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth(), middleware.ModelRequestRateLimit())
	v1beta.GET("/models", relayhandlers.RelayListModelsGemini)
	v1beta.GET("/openai/models", relayhandlers.RelayListModels)
	v1beta.POST("/models/*path", relayhandlers.RelayGeminiNative)
	v1beta.OPTIONS("/models/*path", func(c *gin.Context) { c.AbortWithStatus(204) })

	// Jimeng validates the cheap Action selector before authentication, then
	// authenticates and rate-limits before allocating or decoding its large body.
	jimeng := r.Group("/jimeng")
	jimeng.Use(
		middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.JimengActionValidate(),
		middleware.TokenAuth(), middleware.JimengRequestConvert(), middleware.RequireJimengModel(),
	)
	jimeng.POST("/", relayhandlers.RelayJimeng)

	// Legacy video task aliases share the task controller.
	task := r.Group("/v1")
	task.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth())
	task.POST("/video/submit", relayhandlers.RelayVideoSubmit)
	task.GET("/video/fetch", relayhandlers.RelayVideoFetch)

	registerMidjourneyRouterGroup(r, "/mj")
	registerMidjourneyRouterGroup(r, "/:mode/mj")
}

// registerMidjourneyRouterGroup mirrors the reference's middleware boundary:
// the image proxy is rate-limited but public, while every task route registered
// after it requires a usable relay token.
func registerMidjourneyRouterGroup(r *gin.Engine, prefix string) {
	mj := r.Group(prefix)
	mj.Use(middleware.RelayCORS(), middleware.GlobalRateLimit())
	mj.GET("/image/:id", relayhandlers.RelayMidjourneyImage)
	mj.Use(middleware.TokenAuth())
	mj.POST("/submit/action", relayhandlers.RelayMidjourney)
	mj.POST("/submit/shorten", relayhandlers.RelayMidjourney)
	mj.POST("/submit/modal", relayhandlers.RelayMidjourney)
	mj.POST("/submit/imagine", relayhandlers.RelayMidjourney)
	mj.POST("/submit/change", relayhandlers.RelayMidjourney)
	mj.POST("/submit/simple-change", relayhandlers.RelayMidjourney)
	mj.POST("/submit/describe", relayhandlers.RelayMidjourney)
	mj.POST("/submit/blend", relayhandlers.RelayMidjourney)
	mj.POST("/submit/edits", relayhandlers.RelayMidjourney)
	mj.POST("/submit/video", relayhandlers.RelayMidjourney)
	mj.GET("/task/:id/fetch", relayhandlers.RelayMidjourney)
	mj.GET("/task/:id/image-seed", relayhandlers.RelayMidjourney)
	mj.POST("/task/list-by-condition", relayhandlers.RelayMidjourney)
	mj.POST("/insight-face/swap", relayhandlers.RelayMidjourney)
	mj.POST("/submit/upload-discord-images", relayhandlers.RelayMidjourney)
}

// setupDashboardRouter registers the admin/root dashboard API.
func setupDashboardRouter(r *gin.Engine) {
	admin := r.Group("/api")
	admin.Use(middleware.GlobalRateLimit(), middleware.AdminAuth())
	admin.GET("/authz/catalog", accounts.GetPermissionCatalog)
	admin.GET("/channel", middleware.RequirePermission(auth.ChannelRead), channels.GetChannels)
	admin.GET("/channel/", middleware.RequirePermission(auth.ChannelRead), channels.GetChannels)
	admin.POST("/channel", middleware.RequirePermission(auth.ChannelSensitiveWrite), channels.AddChannel)
	admin.POST("/channel/", middleware.RequirePermission(auth.ChannelSensitiveWrite), channels.AddChannel)
	admin.GET("/channel/search", middleware.RequirePermission(auth.ChannelRead), channels.SearchChannels)
	admin.GET("/channel/models", middleware.RequirePermission(auth.ChannelRead), channels.ChannelListModels)
	admin.GET("/channel/models_enabled", middleware.RequirePermission(auth.ChannelRead), channels.EnabledListModels)
	admin.GET("/channel/ops", middleware.RequirePermission(auth.ChannelRead), channels.GetChannelOps)
	admin.GET("/channel/test", middleware.RequirePermission(auth.ChannelOperate), channels.TestAllChannels)
	admin.GET("/channel/test/:id", middleware.RequirePermission(auth.ChannelOperate), channels.TestChannel)
	admin.GET("/channel/update_balance", middleware.RequirePermission(auth.ChannelOperate), channels.UpdateAllChannelsBalance)
	admin.GET("/channel/update_balance/:id", middleware.RequirePermission(auth.ChannelOperate), channels.UpdateChannelBalance)
	admin.POST("/channel/status/batch", middleware.RequirePermission(auth.ChannelOperate), channels.BatchUpdateChannelStatus)
	admin.POST("/channel/:id/status", middleware.RequirePermission(auth.ChannelOperate), channels.UpdateChannelStatus)
	admin.DELETE("/channel/disabled", middleware.RequirePermission(auth.ChannelSensitiveWrite), channels.DeleteDisabledChannel)
	admin.POST("/channel/tag/disabled", middleware.RequirePermission(auth.ChannelOperate), channels.DisableTagChannels)
	admin.POST("/channel/tag/enabled", middleware.RequirePermission(auth.ChannelOperate), channels.EnableTagChannels)
	admin.PUT("/channel/tag", middleware.RequirePermission(auth.ChannelWrite), channels.EditTagChannels)
	admin.POST("/channel/batch", middleware.RequirePermission(auth.ChannelSensitiveWrite), channels.DeleteChannelBatch)
	admin.POST("/channel/fix", middleware.RequirePermission(auth.ChannelOperate), channels.FixChannelsAbilities)
	admin.GET("/channel/fetch_models/:id", middleware.RequirePermission(auth.ChannelOperate), channels.FetchUpstreamModels)
	admin.POST("/channel/fetch_models", middleware.RequirePermission(auth.ChannelSensitiveWrite), channels.FetchModels)
	admin.POST("/channel/batch/tag", middleware.RequirePermission(auth.ChannelWrite), channels.BatchSetChannelTag)
	admin.GET("/channel/tag/models", middleware.RequirePermission(auth.ChannelRead), channels.GetTagModels)
	admin.POST("/channel/copy/:id", middleware.RequirePermission(auth.ChannelSensitiveWrite), channels.CopyChannel)
	admin.POST("/channel/multi_key/manage", middleware.RequirePermission(auth.ChannelOperate), channels.ManageMultiKeys)
	admin.POST("/channel/upstream_updates/apply", middleware.RequirePermission(auth.ChannelWrite), channels.ApplyChannelUpstreamModelUpdates)
	admin.POST("/channel/upstream_updates/apply_all", middleware.RequirePermission(auth.ChannelWrite), channels.ApplyAllChannelUpstreamModelUpdates)
	admin.POST("/channel/upstream_updates/detect", middleware.RequirePermission(auth.ChannelOperate), channels.DetectChannelUpstreamModelUpdates)
	admin.POST("/channel/upstream_updates/detect_all", middleware.RequirePermission(auth.ChannelOperate), channels.DetectAllChannelUpstreamModelUpdates)
	admin.POST("/channel/:id/codex/refresh", middleware.RequirePermission(auth.ChannelSensitiveWrite), channels.RefreshCodexChannelCredential)
	admin.GET("/channel/:id/codex/usage", middleware.RequirePermission(auth.ChannelRead), channels.GetCodexChannelUsage)
	admin.GET("/channel/:id/codex/usage/reset-credits", middleware.RequirePermission(auth.ChannelRead), channels.GetCodexChannelRateLimitResetCredits)
	admin.POST("/channel/:id/codex/usage/reset", middleware.RequirePermission(auth.ChannelOperate), channels.ResetCodexChannelUsage)
	admin.POST("/channel/ollama/pull", middleware.RequirePermission(auth.ChannelSensitiveWrite), channels.OllamaPullModel)
	admin.POST("/channel/ollama/pull/stream", middleware.RequirePermission(auth.ChannelSensitiveWrite), channels.OllamaPullModelStream)
	admin.DELETE("/channel/ollama/delete", middleware.RequirePermission(auth.ChannelSensitiveWrite), channels.OllamaDeleteModel)
	admin.GET("/channel/ollama/version/:id", middleware.RequirePermission(auth.ChannelSensitiveWrite), channels.OllamaVersion)
	admin.GET("/channel/:id", middleware.RequirePermission(auth.ChannelRead), channels.GetChannel)
	admin.PUT("/channel", middleware.RequirePermission(auth.ChannelWrite), channels.UpdateChannel)
	admin.PUT("/channel/", middleware.RequirePermission(auth.ChannelWrite), channels.UpdateChannel)
	admin.DELETE("/channel/:id", middleware.RequirePermission(auth.ChannelSensitiveWrite), channels.DeleteChannel)
	admin.POST("/channel/:id/key", middleware.RootAuth(), middleware.CriticalRateLimit(),
		middleware.DisableCache(), middleware.SecureVerificationRequired(), channels.GetChannelKey)
	admin.GET("/ability", middleware.RequirePermission(auth.ChannelRead), channels.GetAbilities)
	admin.POST("/ability", middleware.RequirePermission(auth.ChannelWrite), channels.AddAbility)
	admin.DELETE("/ability", middleware.RequirePermission(auth.ChannelWrite), channels.DeleteAbility)
	admin.GET("/user", accounts.GetUsers)
	admin.GET("/user/", accounts.GetUsers)
	admin.POST("/user/", accounts.CreateUser)
	admin.GET("/user/search", accounts.SearchUsers)
	admin.GET("/user/topup", commerce.GetAllTopUps)
	admin.POST("/user/topup/complete", middleware.CriticalRateLimit(), commerce.AdminCompleteTopUp)
	admin.GET("/user/:id", accounts.GetUser)
	admin.PUT("/user", accounts.UpdateUser)
	admin.PUT("/user/", accounts.UpdateUser)
	admin.POST("/user/manage", accounts.ManageUser)
	admin.GET("/user/:id/oauth/bindings", accounts.GetUserOAuthBindingsByAdmin)
	admin.DELETE("/user/:id/oauth/bindings/:provider_id", accounts.UnbindCustomOAuthByAdmin)
	admin.DELETE("/user/:id/bindings/:binding_type", accounts.AdminClearUserBinding)
	admin.DELETE("/user/:id", accounts.AdminDeleteUser)
	admin.DELETE("/user/:id/reset_passkey", accounts.AdminResetPasskey)
	admin.DELETE("/user/:id/2fa", accounts.AdminDisable2FA)
	admin.GET("/user/2fa/stats", accounts.Admin2FAStats)
	admin.GET("/log", operations.GetLogs)
	admin.GET("/log/", operations.GetLogs)
	admin.GET("/log/stat", operations.GetLogsStat)
	admin.GET("/log/channel_affinity_usage_cache", channels.GetChannelAffinityUsageCacheStats)
	admin.GET("/log/search", operations.SearchAllLogs)
	admin.GET("/mj/", relayhandlers.GetAllMidjourney)
	admin.GET("/task/", relayhandlers.GetAllTask)
	admin.GET("/group/", catalog.GetGroups)
	modelMetadata := admin.Group("/models")
	modelMetadata.GET("/sync_upstream/preview", catalog.SyncUpstreamPreview)
	modelMetadata.POST("/sync_upstream", catalog.SyncUpstreamModels)
	modelMetadata.GET("/missing", catalog.GetMissingModels)
	modelMetadata.GET("/search", catalog.SearchModelsMeta)
	modelMetadata.GET("/", catalog.GetAllModelsMeta)
	modelMetadata.GET("/:id", catalog.GetModelMeta)
	modelMetadata.POST("/", catalog.CreateModelMeta)
	modelMetadata.PUT("/", catalog.UpdateModelMeta)
	modelMetadata.DELETE("/:id", catalog.DeleteModelMeta)
	vendorMetadata := admin.Group("/vendors")
	vendorMetadata.GET("/", catalog.GetAllVendors)
	vendorMetadata.GET("/search", catalog.SearchVendors)
	vendorMetadata.GET("/:id", catalog.GetVendorMeta)
	vendorMetadata.POST("/", catalog.CreateVendorMeta)
	vendorMetadata.PUT("/", catalog.UpdateVendorMeta)
	vendorMetadata.DELETE("/:id", catalog.DeleteVendorMeta)
	deployments := admin.Group("/deployments")
	deployments.GET("/settings", catalog.GetModelDeploymentSettings)
	deployments.POST("/settings/test-connection", catalog.TestIoNetConnection)
	deployments.GET("/", catalog.GetAllDeployments)
	deployments.GET("/search", catalog.SearchDeployments)
	deployments.POST("/test-connection", catalog.TestIoNetConnection)
	deployments.GET("/hardware-types", catalog.GetHardwareTypes)
	deployments.GET("/locations", catalog.GetLocations)
	deployments.GET("/available-replicas", catalog.GetAvailableReplicas)
	deployments.POST("/price-estimation", catalog.GetPriceEstimation)
	deployments.GET("/check-name", catalog.CheckClusterNameAvailability)
	deployments.POST("/", catalog.CreateDeployment)
	deployments.GET("/:id", catalog.GetDeployment)
	deployments.GET("/:id/logs", catalog.GetDeploymentLogs)
	deployments.GET("/:id/containers", catalog.ListDeploymentContainers)
	deployments.GET("/:id/containers/:container_id", catalog.GetContainerDetails)
	deployments.PUT("/:id", catalog.UpdateDeployment)
	deployments.PUT("/:id/name", catalog.UpdateDeploymentName)
	deployments.POST("/:id/extend", catalog.ExtendDeployment)
	deployments.DELETE("/:id", catalog.DeleteDeployment)
	prefillGroup := admin.Group("/prefill_group")
	prefillGroup.GET("/", catalog.GetPrefillGroups)
	prefillGroup.POST("/", catalog.CreatePrefillGroup)
	prefillGroup.PUT("/", catalog.UpdatePrefillGroup)
	prefillGroup.DELETE("/:id", catalog.DeletePrefillGroup)
	// House extension: admin dashboard counters (no reference counterpart;
	// the reference /api/data is the quota histogram above).
	admin.GET("/dashboard/stats", operations.GetDashboardData)

	admin.GET("/redemption", commerce.GetRedemptions)
	admin.GET("/redemption/", commerce.GetRedemptions)
	admin.GET("/redemption/search", commerce.SearchRedemptions)
	admin.GET("/redemption/:id", commerce.GetRedemption)
	admin.POST("/redemption", commerce.CreateRedemption)
	admin.POST("/redemption/", commerce.CreateRedemption)
	admin.PUT("/redemption", commerce.UpdateRedemption)
	admin.PUT("/redemption/", commerce.UpdateRedemption)
	admin.DELETE("/redemption/invalid", commerce.DeleteInvalidRedemption)
	admin.DELETE("/redemption/:id", commerce.DeleteRedemption)
	admin.POST("/subscription/plan", commerce.CreateSubscriptionPlan)
	admin.POST("/admin/subscription/plan", commerce.CreateSubscriptionPlan)
	admin.GET("/instance", operations.GetSystemInstances)

	// System options contain credentials and billing controls; the reference
	// exposes this entire surface to root operators only.
	optionRoute := r.Group("/api/option")
	optionRoute.Use(middleware.GlobalRateLimit(), middleware.RootAuth())
	optionRoute.GET("/", settingshandlers.GetOptions)
	optionRoute.PUT("/", settingshandlers.UpdateOptions)
	optionRoute.PUT("/smtp", settingshandlers.UpdateSMTPSettings)
	optionRoute.POST("/payment_compliance", commerce.ConfirmPaymentCompliance)
	optionRoute.GET("/channel_affinity_cache", channels.GetChannelAffinityCacheStats)
	optionRoute.DELETE("/channel_affinity_cache", channels.ClearChannelAffinityCache)
	optionRoute.POST("/rest_model_ratio", commerce.ResetModelRatio)
	optionRoute.GET("/waffo-pancake/catalog", commerce.ListWaffoPancakeCatalog)
	optionRoute.POST("/waffo-pancake/pair", commerce.CreateWaffoPancakePair)
	optionRoute.POST("/waffo-pancake/save", commerce.SaveWaffoPancake)
	optionRoute.POST("/waffo-pancake/subscription-product", commerce.CreateWaffoPancakeSubscriptionProduct)
	optionRoute.GET("/waffo-pancake/subscription-product-options", commerce.ListWaffoPancakeSubscriptionProductOptions)

	// Custom OAuth provider administration (root-only).
	customOAuth := r.Group("/api/custom-oauth-provider")
	customOAuth.Use(middleware.GlobalRateLimit(), middleware.RootAuth())
	customOAuth.POST("/discovery", accounts.FetchCustomOAuthDiscovery)
	customOAuth.GET("/", accounts.GetCustomOAuthProviders)
	customOAuth.GET("/:id", accounts.GetCustomOAuthProvider)
	customOAuth.POST("/", accounts.CreateCustomOAuthProvider)
	customOAuth.PUT("/:id", accounts.UpdateCustomOAuthProvider)
	customOAuth.DELETE("/:id", accounts.DeleteCustomOAuthProvider)

	// Runtime performance and local maintenance controls (root-only). The
	// destructive actions also use the critical limiter because GC and bounded
	// filesystem cleanup are intentionally synchronous operator operations.
	performance := r.Group("/api/performance")
	performance.Use(middleware.GlobalRateLimit(), middleware.RootAuth(), middleware.DisableCache())
	performance.GET("/stats", operations.GetPerformanceStats)
	performance.DELETE("/disk_cache", middleware.CriticalRateLimit(), operations.ClearDiskCache)
	performance.POST("/reset_stats", middleware.CriticalRateLimit(), operations.ResetPerformanceStats)
	performance.POST("/gc", middleware.CriticalRateLimit(), operations.ForceGC)
	performance.GET("/logs", operations.GetLogFiles)
	performance.DELETE("/logs", middleware.CriticalRateLimit(), operations.CleanupLogFiles)

	// Root-only upstream pricing comparison. Outbound destinations are
	// validated and dialed through the SSRF-safe direct transport in the
	// service; responses are explicitly non-cacheable operator data.
	ratioSync := r.Group("/api/ratio_sync")
	ratioSync.Use(middleware.GlobalRateLimit(), middleware.RootAuth(), middleware.DisableCache())
	ratioSync.GET("/channels", channels.GetSyncableChannels)
	ratioSync.POST("/fetch", channels.FetchUpstreamRatios)

	// System-task and system-info administration (root-only).
	systemTask := r.Group("/api/system-task")
	systemTask.Use(middleware.RootAuth())
	systemTask.POST("/log-cleanup", middleware.CriticalRateLimit(), operations.CreateLogCleanupSystemTask)
	systemTask.GET("/list", operations.ListSystemTasks)
	systemTask.GET("/current", operations.GetCurrentSystemTask)
	systemTask.GET("/:task_id", operations.GetSystemTask)

	systemInfo := r.Group("/api/system-info")
	systemInfo.Use(middleware.RootAuth())
	systemInfo.GET("/instances", operations.ListSystemInstances)
	systemInfo.DELETE("/stale-instances", operations.DeleteStaleSystemInstances)
	systemInfo.DELETE("/instances/:node_name", operations.DeleteStaleSystemInstance)

	// Durable accounting exceptions expose only a whitelisted, secret-free
	// snapshot. Financial state changes are root-only and append an atomic
	// primary-database audit event before returning success.
	relayQuotaReview := r.Group("/api/relay-quota-reservations")
	relayQuotaReview.Use(middleware.GlobalRateLimit(), middleware.RootAuth(), middleware.DisableCache())
	relayQuotaReview.GET("/manual-review", relayhandlers.ListManualReviewRelayQuotaReservations)
	relayQuotaReview.GET("/manual-review/:reservation_id", relayhandlers.GetManualReviewRelayQuotaReservation)
	relayQuotaReview.POST("/manual-review/:reservation_id/retry", middleware.CriticalRateLimit(),
		relayhandlers.RetryManualReviewRelayQuotaReservation)
	relayQuotaReview.POST("/manual-review/:reservation_id/resolve", middleware.CriticalRateLimit(),
		relayhandlers.ResolveManualReviewRelayQuotaReservation)

	// Subscription administration (plans + user subscriptions).
	subAdmin := r.Group("/api/subscription/admin")
	subAdmin.Use(middleware.AdminAuth())
	subAdmin.GET("/plans", commerce.AdminListSubscriptionPlans)
	subAdmin.POST("/plans", commerce.AdminCreateSubscriptionPlan)
	subAdmin.PUT("/plans/:id", commerce.AdminUpdateSubscriptionPlan)
	subAdmin.PATCH("/plans/:id", commerce.AdminUpdateSubscriptionPlanStatus)
	subAdmin.POST("/bind", commerce.AdminBindSubscription)
	subAdmin.POST("/plans/:id/subscriptions/reset", commerce.AdminResetPlanSubscriptions)
	subAdmin.GET("/users/:id/subscriptions", commerce.AdminListUserSubscriptions)
	subAdmin.POST("/users/:id/subscriptions", commerce.AdminCreateUserSubscription)
	subAdmin.POST("/users/:id/subscriptions/reset", commerce.AdminResetUserSubscriptionsByPlan)
	subAdmin.POST("/user_subscriptions/:id/invalidate", commerce.AdminInvalidateUserSubscription)
	subAdmin.DELETE("/user_subscriptions/:id", commerce.AdminDeleteUserSubscription)
	subAdmin.POST("/user_subscriptions/:id/entitlement-review/resolve", middleware.CriticalRateLimit(), middleware.RootAuth(),
		commerce.ResolveLegacySubscriptionEntitlementReview)
}
