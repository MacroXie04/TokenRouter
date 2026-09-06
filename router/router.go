// Package router wires HTTP routes for the dashboard API, relay data plane,
// and the embedded web frontend.
package router

import (
	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/controller"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/service"
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
	r.NoRoute(controller.RelayNotFoundRoute)
	return r
}

// setupLegacyDashboardBillingRouter exposes the OpenAI-compatible account
// balance endpoints consumed by older clients and by channel balance probes.
func setupLegacyDashboardBillingRouter(r *gin.Engine) {
	dashboard := r.Group("")
	dashboard.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth())
	for _, prefix := range []string{"", "/v1"} {
		dashboard.GET(prefix+"/dashboard/billing/subscription", controller.GetDashboardSubscription)
		dashboard.GET(prefix+"/dashboard/billing/usage", controller.GetDashboardUsage)
	}
}

// setupAPIRouter registers the public dashboard API routes.
func setupAPIRouter(r *gin.Engine) {
	api := r.Group("/api")
	api.Use(middleware.GlobalRateLimit())
	api.GET("/status", controller.GetStatus)
	api.GET("/status/test", middleware.AdminAuth(), controller.TestStatus)
	api.GET("/uptime/status", controller.GetUptimeKumaStatus)
	api.GET("/perf-metrics/summary", middleware.HeaderNavModulePublicOrUserAuth("pricing"), controller.GetPerfMetricsSummary)
	api.GET("/perf-metrics", middleware.HeaderNavModulePublicOrUserAuth("pricing"), controller.GetPerfMetrics)
	api.GET("/setup", controller.GetSetup)
	api.POST("/setup", middleware.AnonymousRequestBodyLimit(), controller.PostSetup)
	api.GET("/notice", controller.GetNotice)
	api.GET("/about", controller.GetAbout)
	api.GET("/user-agreement", controller.GetUserAgreement)
	api.GET("/privacy-policy", controller.GetPrivacyPolicy)
	api.GET("/home_page_content", controller.GetHomePageContent)
	api.GET("/pricing", middleware.HeaderNavModuleAuth("pricing"), controller.GetPricing)
	api.GET("/rankings", middleware.HeaderNavModuleAuth("rankings"), controller.GetRankings)
	api.GET("/models", middleware.UserAuth(), controller.GetModels)
	api.GET("/ratio_config", middleware.CriticalRateLimit(), controller.GetRatioConfig)
	api.GET("/user/groups", controller.GetUserGroups)

	// Keep every reference /api/data route beneath the already rate-limited
	// API group, with authentication applied exactly once per route.
	data := api.Group("/data")
	data.GET("/", middleware.AdminAuth(), controller.GetAllQuotaDates)
	data.GET("/users", middleware.AdminAuth(), controller.GetQuotaDatesByUser)
	data.GET("/self", middleware.UserAuth(), controller.GetUserQuotaDates)
	data.GET("/flow", middleware.AdminAuth(), controller.GetAllFlowQuotaDates)
	data.GET("/flow/self", middleware.UserAuth(), controller.GetUserFlowQuotaDates)

	// User subscription surface (reference /api/subscription group).
	sub := r.Group("/api/subscription")
	sub.Use(middleware.GlobalRateLimit(), middleware.UserAuth())
	sub.GET("/plans", controller.ListSubscriptionPlans)
	sub.GET("/self", controller.GetSelfSubscription)
	sub.PUT("/self/preference", controller.UpdateSubscriptionPreference)
	sub.POST("/balance/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestBalancePay)
	sub.POST("/epay/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestEpay)
	sub.POST("/stripe/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestStripePay)
	sub.POST("/creem/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestCreemPay)
	sub.POST("/waffo-pancake/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestWaffoPancakePay)

	// Authentication.
	api.POST("/user/register", middleware.CriticalRateLimit(), middleware.AnonymousRequestBodyLimit(), middleware.TurnstileCheck(), controller.Register)
	api.POST("/user/login", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.AnonymousRequestBodyLimit(), middleware.TurnstileCheck(), controller.Login)
	api.POST("/user/login/2fa", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.AnonymousRequestBodyLimit(), controller.Login2FA)
	api.POST("/user/auth/refresh", middleware.CriticalRateLimit(), middleware.OriginGuard(), middleware.DisableCache(), controller.RefreshAuth)
	api.POST("/user/auth/logout", middleware.CriticalRateLimit(), middleware.OriginGuard(), controller.AuthLogout)
	api.POST("/user/passkey/login/begin", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.AnonymousRequestBodyLimit(), controller.PasskeyLoginBegin)
	api.POST("/user/passkey/login/finish", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.AnonymousRequestBodyLimit(), controller.PasskeyLoginFinish)

	// OAuth.
	api.POST("/oauth/state", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.TryUserAuth(), middleware.AnonymousRequestBodyLimit(), controller.GenerateOAuthCode)
	api.POST("/oauth/email/bind", middleware.UserAuth(), middleware.CriticalRateLimit(), controller.EmailBind)
	api.GET("/oauth/wechat", middleware.CriticalRateLimit(), middleware.DisableCache(), controller.WeChatAuth)
	api.POST("/oauth/wechat/bind", middleware.UserAuth(), middleware.CriticalRateLimit(), controller.WeChatBind)
	api.GET("/oauth/telegram/login", middleware.CriticalRateLimit(), middleware.DisableCache(), controller.TelegramLogin)
	api.POST("/oauth/telegram/bind/start", middleware.UserAuth(), middleware.CriticalRateLimit(), middleware.DisableCache(), controller.TelegramBindStart)
	api.GET("/oauth/telegram/bind/:flow_token", middleware.CriticalRateLimit(), middleware.DisableCache(), controller.TelegramBindFinish)
	api.GET("/oauth/:provider", middleware.CriticalRateLimit(), middleware.DisableCache(), middleware.TryUserAuth(), controller.HandleOAuth)
	api.GET("/oauth/:provider/callback", middleware.DisableCache(), controller.OAuthCallback)

	// Email / password reset.
	api.GET("/reset_password", middleware.CriticalRateLimit(), middleware.TurnstileCheck(), controller.SendPasswordResetEmail)
	api.POST("/reset_password", middleware.CriticalRateLimit(), middleware.TurnstileCheck(), controller.SendPasswordResetEmail)
	api.POST("/user/reset", middleware.CriticalRateLimit(), middleware.AnonymousRequestBodyLimit(), controller.ResetPassword)
	api.GET("/verification", middleware.EmailVerificationRateLimit(), middleware.TurnstileCheck(), controller.SendEmailVerification)
	api.POST("/verification", middleware.EmailVerificationRateLimit(), middleware.TurnstileCheck(), controller.SendEmailVerification)
	api.POST("/verify", middleware.UserAuth(), middleware.CriticalRateLimit(), middleware.DisableCache(), controller.UniversalVerify)

	// Payment webhooks.
	api.POST("/stripe/webhook", middleware.AnonymousRequestBodyLimit(), controller.StripeWebhook)
	api.POST("/creem/webhook", middleware.AnonymousRequestBodyLimit(), controller.CreemWebhook)
	api.POST("/waffo/webhook", middleware.AnonymousRequestBodyLimit(), controller.WaffoWebhook)
	api.POST("/waffo-pancake/webhook/:env", middleware.AnonymousRequestBodyLimit(), controller.WaffoPancakeWebhook)
	api.POST("/user/epay/notify", middleware.AnonymousRequestBodyLimit(), controller.EpayNotify)
	api.GET("/user/epay/notify", controller.EpayNotify)
	api.POST("/subscription/epay/notify", middleware.AnonymousRequestBodyLimit(), controller.SubscriptionEpayNotify)
	api.GET("/subscription/epay/notify", controller.SubscriptionEpayNotify)
	api.GET("/subscription/epay/return", controller.SubscriptionEpayReturn)
	api.POST("/subscription/epay/return", middleware.AnonymousRequestBodyLimit(), controller.SubscriptionEpayReturn)

	// User self-service.
	user := api.Group("/user")
	user.Use(middleware.UserAuth())
	user.GET("/self", controller.GetSelf)
	user.PUT("/self", controller.UpdateSelf)
	user.DELETE("/self", controller.DeleteSelf)
	user.GET("/self/groups", controller.GetUserGroups)
	user.GET("/token", middleware.CriticalRateLimit(), middleware.UserCriticalRateLimit("access-token"),
		middleware.DisableCache(), controller.GenerateAccessToken)
	user.GET("/models", controller.GetUserModels)
	user.GET("/2fa/status", controller.GetTwoFAStatus)
	user.POST("/2fa/setup", controller.StartTwoFA)
	user.POST("/2fa/start", controller.StartTwoFA)
	user.POST("/2fa/enable", middleware.DisableCache(), controller.EnableTwoFA)
	user.POST("/2fa/disable", controller.DisableTwoFA)
	user.POST("/2fa/verify", controller.VerifyTwoFAForAction)
	user.GET("/passkey/status", controller.PasskeyStatus)
	user.POST("/passkey/register/begin", controller.PasskeyRegisterBegin)
	user.POST("/passkey/register/finish", controller.PasskeyRegisterFinish)
	user.POST("/passkey/verify/begin", middleware.DisableCache(), controller.PasskeyVerifyBegin)
	user.POST("/passkey/verify/finish", middleware.DisableCache(), controller.PasskeyVerifyFinish)
	user.POST("/redemption/redeem", controller.Redeem)
	user.POST("/checkin", middleware.TurnstileCheck(), controller.CheckIn)
	user.GET("/checkin", controller.CheckInStatus)
	user.GET("/checkin/status", controller.CheckInStatus)
	user.POST("/topup", middleware.CriticalRateLimit(), controller.TopUp)
	user.GET("/topup/self", controller.GetSelfTopUps)
	user.GET("/topup/info", controller.GetTopUpInfo)
	user.POST("/pay", middleware.CriticalRateLimit(), controller.RequestEpay)
	user.POST("/amount", controller.RequestAmount)
	user.POST("/stripe/pay", middleware.CriticalRateLimit(), controller.RequestStripePay)
	user.POST("/stripe/amount", controller.RequestStripeAmount)
	user.POST("/creem/pay", middleware.CriticalRateLimit(), controller.RequestCreemPay)
	user.POST("/waffo/amount", controller.RequestWaffoAmount)
	user.POST("/waffo/pay", middleware.CriticalRateLimit(), controller.RequestWaffoPay)
	user.POST("/waffo-pancake/amount", controller.RequestWaffoPancakeAmount)
	user.POST("/waffo-pancake/pay", middleware.CriticalRateLimit(), controller.RequestWaffoPancakePay)
	user.POST("/email/bind", controller.BindEmail)
	user.GET("/sessions", controller.GetLoginSessions)
	user.DELETE("/sessions/:sid", controller.DeleteLoginSession)
	user.POST("/sessions/revoke-others", controller.RevokeOtherSessions)
	user.GET("/passkey", controller.GetSelfPasskeys)
	user.DELETE("/passkey", controller.DeleteSelfPasskeys)
	user.POST("/2fa/backup_codes", controller.RegenerateBackupCodes)
	user.PUT("/setting", controller.UpdateUserSetting)
	user.GET("/oauth/bindings", controller.GetOAuthBindings)
	user.DELETE("/oauth/bindings/:provider_id", controller.UnbindOAuth)
	user.GET("/aff", controller.GetSelfAff)
	user.POST("/aff_transfer", controller.TransferAffQuota)

	// User log self-service (reference /api/log/self group).
	logSelf := r.Group("/api/log")
	logSelf.Use(middleware.UserAuth())
	logSelf.GET("/self", controller.GetUserLogs)
	logSelf.GET("/self/stat", controller.GetLogsSelfStat)
	logSelf.GET("/self/search", middleware.SearchRateLimit(), controller.SearchUserLogs)
	// Relay-token scoped log listing.
	api.GET("/log/token", middleware.RelayCORS(), middleware.CriticalRateLimit(),
		middleware.TokenAuthReadOnly(), controller.GetLogByKey)
	api.OPTIONS("/log/token", middleware.RelayCORS(), func(c *gin.Context) { c.AbortWithStatus(204) })

	// Asynchronous task history is session-scoped; ownership always comes from
	// UserAuth rather than a caller-controlled query parameter.
	taskHistorySelf := r.Group("/api/task")
	taskHistorySelf.Use(middleware.UserAuth())
	taskHistorySelf.GET("/self", controller.GetUserTask)
	midjourneyHistorySelf := r.Group("/api/mj")
	midjourneyHistorySelf.Use(middleware.UserAuth())
	midjourneyHistorySelf.GET("/self", controller.GetUserMidjourney)
}

// setupTokenRouter registers the user-scoped token management group (the
// reference contract: every route operates on the signed-in user's own
// tokens, keys are masked everywhere except the disclosure endpoints).
func setupTokenRouter(r *gin.Engine) {
	token := r.Group("/api/token")
	token.Use(middleware.UserAuth())
	token.GET("/", controller.GetAllTokens)
	token.GET("/search", middleware.SearchRateLimit(), controller.SearchTokens)
	token.GET("/auto-groups", controller.GetTokenAutoGroups)
	token.GET("/:id", controller.GetToken)
	token.POST("/:id/key", middleware.CriticalRateLimit(), middleware.DisableCache(), controller.GetTokenKey)
	token.POST("/", controller.AddToken)
	token.PUT("/", controller.UpdateToken)
	token.DELETE("/:id", controller.DeleteToken)
	token.POST("/batch", controller.DeleteTokenBatch)
	token.POST("/batch/keys", middleware.CriticalRateLimit(), middleware.DisableCache(), controller.GetTokenKeysBatch)

	usage := r.Group("/api/usage")
	usage.Use(middleware.RelayCORS(), middleware.TokenAuthReadOnly())
	usage.GET("/token/", controller.GetTokenUsage)
	usage.OPTIONS("/token/", func(c *gin.Context) { c.AbortWithStatus(204) })
}

// setupRelayRouter registers the OpenAI-compatible relay data plane.
func setupRelayRouter(r *gin.Engine) {
	playground := r.Group("/pg")
	playground.Use(middleware.UserAuth())
	playground.POST("/chat/completions", controller.Playground)

	kling := r.Group("/kling/v1")
	kling.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth())
	kling.OPTIONS("/*path", func(c *gin.Context) { c.AbortWithStatus(204) })
	kling.POST("/videos/text2video", controller.RelayKlingTask)
	kling.POST("/videos/image2video", controller.RelayKlingTask)
	kling.GET("/videos/text2video/:task_id", controller.RelayKlingTaskFetch)
	kling.GET("/videos/image2video/:task_id", controller.RelayKlingTaskFetch)

	suno := r.Group("/suno")
	suno.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth())
	suno.POST("/submit/:action", controller.RelaySunoSubmit)
	suno.POST("/fetch", controller.RelaySunoFetch)
	suno.GET("/fetch/:id", controller.RelaySunoFetch)

	relay := r.Group("/v1")
	relay.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth(), middleware.ModelRequestRateLimit())
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
	relay.POST("/responses/compact", controller.RelayResponsesCompact)
	relay.POST("/video/generations", controller.RelayTask)
	relay.GET("/video/generations/:task_id", controller.RelayTaskFetch)
	relay.POST("/videos/:video_id/remix", controller.RelayTask)
	relay.POST("/videos", controller.RelayTask)
	relay.GET("/videos/:task_id", controller.RelayTaskFetch)
	relay.POST("/alpha/search", controller.RelayAlphaSearch)
	relay.POST("/messages", controller.RelayClaudeMessages)
	relay.POST("/rerank", controller.RelayRerank)
	relay.POST("/edits", controller.RelayEdits)
	relay.GET("/realtime", middleware.RequireRelayQueryModel("model"), controller.RelayRealtime)
	relay.POST("/engines/:model/embeddings", controller.RelayEnginesEmbeddings)
	relay.POST("/models/*path", controller.RelayGeminiNative)
	relay.GET("/models", controller.RelayListModels)
	relay.GET("/models/:model", controller.RelayRetrieveModel)
	relay.DELETE("/models/:model", controller.RelayNotImplemented)
	relay.POST("/images/variations", controller.RelayNotImplemented)
	relay.GET("/files", controller.RelayNotImplemented)
	relay.POST("/files", controller.RelayNotImplemented)
	relay.DELETE("/files/:id", controller.RelayNotImplemented)
	relay.GET("/files/:id", controller.RelayNotImplemented)
	relay.GET("/files/:id/content", controller.RelayNotImplemented)
	relay.POST("/fine-tunes", controller.RelayNotImplemented)
	relay.GET("/fine-tunes", controller.RelayNotImplemented)
	relay.GET("/fine-tunes/:id", controller.RelayNotImplemented)
	relay.POST("/fine-tunes/:id/cancel", controller.RelayNotImplemented)
	relay.GET("/fine-tunes/:id/events", controller.RelayNotImplemented)

	// Completed video content may be retrieved with either the original relay
	// token or the owning dashboard session. Ownership is checked again before
	// any provider credential is decrypted.
	videoContent := r.Group("/v1")
	videoContent.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenOrUserAuth())
	videoContent.GET("/videos/:task_id/content", controller.VideoProxy)

	// Gemini protocol surface: /v1beta model listing plus the native Gemini
	// generateContent passthrough.
	v1beta := r.Group("/v1beta")
	v1beta.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth(), middleware.ModelRequestRateLimit())
	v1beta.GET("/models", controller.RelayListModelsGemini)
	v1beta.GET("/openai/models", controller.RelayListModels)
	v1beta.POST("/models/*path", controller.RelayGeminiNative)
	v1beta.OPTIONS("/models/*path", func(c *gin.Context) { c.AbortWithStatus(204) })

	// Jimeng validates the cheap Action selector before authentication, then
	// authenticates and rate-limits before allocating or decoding its large body.
	jimeng := r.Group("/jimeng")
	jimeng.Use(
		middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.JimengActionValidate(),
		middleware.TokenAuth(), middleware.JimengRequestConvert(), middleware.RequireJimengModel(),
	)
	jimeng.POST("/", controller.RelayJimeng)

	// Legacy video task aliases share the task controller.
	task := r.Group("/v1")
	task.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth())
	task.POST("/video/submit", controller.RelayVideoSubmit)
	task.GET("/video/fetch", controller.RelayVideoFetch)

	registerMidjourneyRouterGroup(r, "/mj")
	registerMidjourneyRouterGroup(r, "/:mode/mj")
}

// registerMidjourneyRouterGroup mirrors the reference's middleware boundary:
// the image proxy is rate-limited but public, while every task route registered
// after it requires a usable relay token.
func registerMidjourneyRouterGroup(r *gin.Engine, prefix string) {
	mj := r.Group(prefix)
	mj.Use(middleware.RelayCORS(), middleware.GlobalRateLimit())
	mj.GET("/image/:id", controller.RelayMidjourneyImage)
	mj.Use(middleware.TokenAuth())
	mj.POST("/submit/action", controller.RelayMidjourney)
	mj.POST("/submit/shorten", controller.RelayMidjourney)
	mj.POST("/submit/modal", controller.RelayMidjourney)
	mj.POST("/submit/imagine", controller.RelayMidjourney)
	mj.POST("/submit/change", controller.RelayMidjourney)
	mj.POST("/submit/simple-change", controller.RelayMidjourney)
	mj.POST("/submit/describe", controller.RelayMidjourney)
	mj.POST("/submit/blend", controller.RelayMidjourney)
	mj.POST("/submit/edits", controller.RelayMidjourney)
	mj.POST("/submit/video", controller.RelayMidjourney)
	mj.GET("/task/:id/fetch", controller.RelayMidjourney)
	mj.GET("/task/:id/image-seed", controller.RelayMidjourney)
	mj.POST("/task/list-by-condition", controller.RelayMidjourney)
	mj.POST("/insight-face/swap", controller.RelayMidjourney)
	mj.POST("/submit/upload-discord-images", controller.RelayMidjourney)
}

// setupDashboardRouter registers the admin/root dashboard API.
func setupDashboardRouter(r *gin.Engine) {
	admin := r.Group("/api")
	admin.Use(middleware.GlobalRateLimit(), middleware.AdminAuth())
	admin.GET("/authz/catalog", controller.GetPermissionCatalog)
	admin.GET("/channel", middleware.RequirePermission(service.ChannelRead), controller.GetChannels)
	admin.GET("/channel/", middleware.RequirePermission(service.ChannelRead), controller.GetChannels)
	admin.POST("/channel", middleware.RequirePermission(service.ChannelSensitiveWrite), controller.AddChannel)
	admin.POST("/channel/", middleware.RequirePermission(service.ChannelSensitiveWrite), controller.AddChannel)
	admin.GET("/channel/search", middleware.RequirePermission(service.ChannelRead), controller.SearchChannels)
	admin.GET("/channel/models", middleware.RequirePermission(service.ChannelRead), controller.ChannelListModels)
	admin.GET("/channel/models_enabled", middleware.RequirePermission(service.ChannelRead), controller.EnabledListModels)
	admin.GET("/channel/ops", middleware.RequirePermission(service.ChannelRead), controller.GetChannelOps)
	admin.GET("/channel/test", middleware.RequirePermission(service.ChannelOperate), controller.TestAllChannels)
	admin.GET("/channel/test/:id", middleware.RequirePermission(service.ChannelOperate), controller.TestChannel)
	admin.GET("/channel/update_balance", middleware.RequirePermission(service.ChannelOperate), controller.UpdateAllChannelsBalance)
	admin.GET("/channel/update_balance/:id", middleware.RequirePermission(service.ChannelOperate), controller.UpdateChannelBalance)
	admin.POST("/channel/status/batch", middleware.RequirePermission(service.ChannelOperate), controller.BatchUpdateChannelStatus)
	admin.POST("/channel/:id/status", middleware.RequirePermission(service.ChannelOperate), controller.UpdateChannelStatus)
	admin.DELETE("/channel/disabled", middleware.RequirePermission(service.ChannelSensitiveWrite), controller.DeleteDisabledChannel)
	admin.POST("/channel/tag/disabled", middleware.RequirePermission(service.ChannelOperate), controller.DisableTagChannels)
	admin.POST("/channel/tag/enabled", middleware.RequirePermission(service.ChannelOperate), controller.EnableTagChannels)
	admin.PUT("/channel/tag", middleware.RequirePermission(service.ChannelWrite), controller.EditTagChannels)
	admin.POST("/channel/batch", middleware.RequirePermission(service.ChannelSensitiveWrite), controller.DeleteChannelBatch)
	admin.POST("/channel/fix", middleware.RequirePermission(service.ChannelOperate), controller.FixChannelsAbilities)
	admin.GET("/channel/fetch_models/:id", middleware.RequirePermission(service.ChannelOperate), controller.FetchUpstreamModels)
	admin.POST("/channel/fetch_models", middleware.RequirePermission(service.ChannelSensitiveWrite), controller.FetchModels)
	admin.POST("/channel/batch/tag", middleware.RequirePermission(service.ChannelWrite), controller.BatchSetChannelTag)
	admin.GET("/channel/tag/models", middleware.RequirePermission(service.ChannelRead), controller.GetTagModels)
	admin.POST("/channel/copy/:id", middleware.RequirePermission(service.ChannelSensitiveWrite), controller.CopyChannel)
	admin.POST("/channel/multi_key/manage", middleware.RequirePermission(service.ChannelOperate), controller.ManageMultiKeys)
	admin.POST("/channel/upstream_updates/apply", middleware.RequirePermission(service.ChannelWrite), controller.ApplyChannelUpstreamModelUpdates)
	admin.POST("/channel/upstream_updates/apply_all", middleware.RequirePermission(service.ChannelWrite), controller.ApplyAllChannelUpstreamModelUpdates)
	admin.POST("/channel/upstream_updates/detect", middleware.RequirePermission(service.ChannelOperate), controller.DetectChannelUpstreamModelUpdates)
	admin.POST("/channel/upstream_updates/detect_all", middleware.RequirePermission(service.ChannelOperate), controller.DetectAllChannelUpstreamModelUpdates)
	admin.POST("/channel/:id/codex/refresh", middleware.RequirePermission(service.ChannelSensitiveWrite), controller.RefreshCodexChannelCredential)
	admin.GET("/channel/:id/codex/usage", middleware.RequirePermission(service.ChannelRead), controller.GetCodexChannelUsage)
	admin.GET("/channel/:id/codex/usage/reset-credits", middleware.RequirePermission(service.ChannelRead), controller.GetCodexChannelRateLimitResetCredits)
	admin.POST("/channel/:id/codex/usage/reset", middleware.RequirePermission(service.ChannelOperate), controller.ResetCodexChannelUsage)
	admin.POST("/channel/ollama/pull", middleware.RequirePermission(service.ChannelSensitiveWrite), controller.OllamaPullModel)
	admin.POST("/channel/ollama/pull/stream", middleware.RequirePermission(service.ChannelSensitiveWrite), controller.OllamaPullModelStream)
	admin.DELETE("/channel/ollama/delete", middleware.RequirePermission(service.ChannelSensitiveWrite), controller.OllamaDeleteModel)
	admin.GET("/channel/ollama/version/:id", middleware.RequirePermission(service.ChannelSensitiveWrite), controller.OllamaVersion)
	admin.GET("/channel/:id", middleware.RequirePermission(service.ChannelRead), controller.GetChannel)
	admin.PUT("/channel", middleware.RequirePermission(service.ChannelWrite), controller.UpdateChannel)
	admin.PUT("/channel/", middleware.RequirePermission(service.ChannelWrite), controller.UpdateChannel)
	admin.DELETE("/channel/:id", middleware.RequirePermission(service.ChannelSensitiveWrite), controller.DeleteChannel)
	admin.POST("/channel/:id/key", middleware.RootAuth(), middleware.CriticalRateLimit(),
		middleware.DisableCache(), middleware.SecureVerificationRequired(), controller.GetChannelKey)
	admin.GET("/ability", middleware.RequirePermission(service.ChannelRead), controller.GetAbilities)
	admin.POST("/ability", middleware.RequirePermission(service.ChannelWrite), controller.AddAbility)
	admin.DELETE("/ability", middleware.RequirePermission(service.ChannelWrite), controller.DeleteAbility)
	admin.GET("/user", controller.GetUsers)
	admin.GET("/user/", controller.GetUsers)
	admin.POST("/user/", controller.CreateUser)
	admin.GET("/user/search", controller.SearchUsers)
	admin.GET("/user/topup", controller.GetAllTopUps)
	admin.POST("/user/topup/complete", middleware.CriticalRateLimit(), controller.AdminCompleteTopUp)
	admin.GET("/user/:id", controller.GetUser)
	admin.PUT("/user", controller.UpdateUser)
	admin.PUT("/user/", controller.UpdateUser)
	admin.POST("/user/manage", controller.ManageUser)
	admin.GET("/user/:id/oauth/bindings", controller.GetUserOAuthBindingsByAdmin)
	admin.DELETE("/user/:id/oauth/bindings/:provider_id", controller.UnbindCustomOAuthByAdmin)
	admin.DELETE("/user/:id/bindings/:binding_type", controller.AdminClearUserBinding)
	admin.DELETE("/user/:id", controller.AdminDeleteUser)
	admin.DELETE("/user/:id/reset_passkey", controller.AdminResetPasskey)
	admin.DELETE("/user/:id/2fa", controller.AdminDisable2FA)
	admin.GET("/user/2fa/stats", controller.Admin2FAStats)
	admin.GET("/log", controller.GetLogs)
	admin.GET("/log/", controller.GetLogs)
	admin.GET("/log/stat", controller.GetLogsStat)
	admin.GET("/log/channel_affinity_usage_cache", controller.GetChannelAffinityUsageCacheStats)
	admin.GET("/log/search", controller.SearchAllLogs)
	admin.GET("/mj/", controller.GetAllMidjourney)
	admin.GET("/task/", controller.GetAllTask)
	admin.GET("/group/", controller.GetGroups)
	modelMetadata := admin.Group("/models")
	modelMetadata.GET("/sync_upstream/preview", controller.SyncUpstreamPreview)
	modelMetadata.POST("/sync_upstream", controller.SyncUpstreamModels)
	modelMetadata.GET("/missing", controller.GetMissingModels)
	modelMetadata.GET("/search", controller.SearchModelsMeta)
	modelMetadata.GET("/", controller.GetAllModelsMeta)
	modelMetadata.GET("/:id", controller.GetModelMeta)
	modelMetadata.POST("/", controller.CreateModelMeta)
	modelMetadata.PUT("/", controller.UpdateModelMeta)
	modelMetadata.DELETE("/:id", controller.DeleteModelMeta)
	vendorMetadata := admin.Group("/vendors")
	vendorMetadata.GET("/", controller.GetAllVendors)
	vendorMetadata.GET("/search", controller.SearchVendors)
	vendorMetadata.GET("/:id", controller.GetVendorMeta)
	vendorMetadata.POST("/", controller.CreateVendorMeta)
	vendorMetadata.PUT("/", controller.UpdateVendorMeta)
	vendorMetadata.DELETE("/:id", controller.DeleteVendorMeta)
	deployments := admin.Group("/deployments")
	deployments.GET("/settings", controller.GetModelDeploymentSettings)
	deployments.POST("/settings/test-connection", controller.TestIoNetConnection)
	deployments.GET("/", controller.GetAllDeployments)
	deployments.GET("/search", controller.SearchDeployments)
	deployments.POST("/test-connection", controller.TestIoNetConnection)
	deployments.GET("/hardware-types", controller.GetHardwareTypes)
	deployments.GET("/locations", controller.GetLocations)
	deployments.GET("/available-replicas", controller.GetAvailableReplicas)
	deployments.POST("/price-estimation", controller.GetPriceEstimation)
	deployments.GET("/check-name", controller.CheckClusterNameAvailability)
	deployments.POST("/", controller.CreateDeployment)
	deployments.GET("/:id", controller.GetDeployment)
	deployments.GET("/:id/logs", controller.GetDeploymentLogs)
	deployments.GET("/:id/containers", controller.ListDeploymentContainers)
	deployments.GET("/:id/containers/:container_id", controller.GetContainerDetails)
	deployments.PUT("/:id", controller.UpdateDeployment)
	deployments.PUT("/:id/name", controller.UpdateDeploymentName)
	deployments.POST("/:id/extend", controller.ExtendDeployment)
	deployments.DELETE("/:id", controller.DeleteDeployment)
	prefillGroup := admin.Group("/prefill_group")
	prefillGroup.GET("/", controller.GetPrefillGroups)
	prefillGroup.POST("/", controller.CreatePrefillGroup)
	prefillGroup.PUT("/", controller.UpdatePrefillGroup)
	prefillGroup.DELETE("/:id", controller.DeletePrefillGroup)
	// House extension: admin dashboard counters (no reference counterpart;
	// the reference /api/data is the quota histogram above).
	admin.GET("/dashboard/stats", controller.GetDashboardData)

	admin.GET("/redemption", controller.GetRedemptions)
	admin.GET("/redemption/", controller.GetRedemptions)
	admin.GET("/redemption/search", controller.SearchRedemptions)
	admin.GET("/redemption/:id", controller.GetRedemption)
	admin.POST("/redemption", controller.CreateRedemption)
	admin.POST("/redemption/", controller.CreateRedemption)
	admin.PUT("/redemption", controller.UpdateRedemption)
	admin.PUT("/redemption/", controller.UpdateRedemption)
	admin.DELETE("/redemption/invalid", controller.DeleteInvalidRedemption)
	admin.DELETE("/redemption/:id", controller.DeleteRedemption)
	admin.POST("/subscription/plan", controller.CreateSubscriptionPlan)
	admin.POST("/admin/subscription/plan", controller.CreateSubscriptionPlan)
	admin.GET("/instance", controller.GetSystemInstances)

	// System options contain credentials and billing controls; the reference
	// exposes this entire surface to root operators only.
	optionRoute := r.Group("/api/option")
	optionRoute.Use(middleware.GlobalRateLimit(), middleware.RootAuth())
	optionRoute.GET("/", controller.GetOptions)
	optionRoute.PUT("/", controller.UpdateOptions)
	optionRoute.PUT("/smtp", controller.UpdateSMTPSettings)
	optionRoute.POST("/payment_compliance", controller.ConfirmPaymentCompliance)
	optionRoute.GET("/channel_affinity_cache", controller.GetChannelAffinityCacheStats)
	optionRoute.DELETE("/channel_affinity_cache", controller.ClearChannelAffinityCache)
	optionRoute.POST("/rest_model_ratio", controller.ResetModelRatio)
	optionRoute.GET("/waffo-pancake/catalog", controller.ListWaffoPancakeCatalog)
	optionRoute.POST("/waffo-pancake/pair", controller.CreateWaffoPancakePair)
	optionRoute.POST("/waffo-pancake/save", controller.SaveWaffoPancake)
	optionRoute.POST("/waffo-pancake/subscription-product", controller.CreateWaffoPancakeSubscriptionProduct)
	optionRoute.GET("/waffo-pancake/subscription-product-options", controller.ListWaffoPancakeSubscriptionProductOptions)

	// Custom OAuth provider administration (root-only).
	customOAuth := r.Group("/api/custom-oauth-provider")
	customOAuth.Use(middleware.GlobalRateLimit(), middleware.RootAuth())
	customOAuth.POST("/discovery", controller.FetchCustomOAuthDiscovery)
	customOAuth.GET("/", controller.GetCustomOAuthProviders)
	customOAuth.GET("/:id", controller.GetCustomOAuthProvider)
	customOAuth.POST("/", controller.CreateCustomOAuthProvider)
	customOAuth.PUT("/:id", controller.UpdateCustomOAuthProvider)
	customOAuth.DELETE("/:id", controller.DeleteCustomOAuthProvider)

	// Runtime performance and local maintenance controls (root-only). The
	// destructive actions also use the critical limiter because GC and bounded
	// filesystem cleanup are intentionally synchronous operator operations.
	performance := r.Group("/api/performance")
	performance.Use(middleware.GlobalRateLimit(), middleware.RootAuth(), middleware.DisableCache())
	performance.GET("/stats", controller.GetPerformanceStats)
	performance.DELETE("/disk_cache", middleware.CriticalRateLimit(), controller.ClearDiskCache)
	performance.POST("/reset_stats", middleware.CriticalRateLimit(), controller.ResetPerformanceStats)
	performance.POST("/gc", middleware.CriticalRateLimit(), controller.ForceGC)
	performance.GET("/logs", controller.GetLogFiles)
	performance.DELETE("/logs", middleware.CriticalRateLimit(), controller.CleanupLogFiles)

	// Root-only upstream pricing comparison. Outbound destinations are
	// validated and dialed through the SSRF-safe direct transport in the
	// service; responses are explicitly non-cacheable operator data.
	ratioSync := r.Group("/api/ratio_sync")
	ratioSync.Use(middleware.GlobalRateLimit(), middleware.RootAuth(), middleware.DisableCache())
	ratioSync.GET("/channels", controller.GetSyncableChannels)
	ratioSync.POST("/fetch", controller.FetchUpstreamRatios)

	// System-task and system-info administration (root-only).
	systemTask := r.Group("/api/system-task")
	systemTask.Use(middleware.RootAuth())
	systemTask.POST("/log-cleanup", middleware.CriticalRateLimit(), controller.CreateLogCleanupSystemTask)
	systemTask.GET("/list", controller.ListSystemTasks)
	systemTask.GET("/current", controller.GetCurrentSystemTask)
	systemTask.GET("/:task_id", controller.GetSystemTask)

	systemInfo := r.Group("/api/system-info")
	systemInfo.Use(middleware.RootAuth())
	systemInfo.GET("/instances", controller.ListSystemInstances)
	systemInfo.DELETE("/stale-instances", controller.DeleteStaleSystemInstances)
	systemInfo.DELETE("/instances/:node_name", controller.DeleteStaleSystemInstance)

	// Durable accounting exceptions expose only a whitelisted, secret-free
	// snapshot. Financial state changes are root-only and append an atomic
	// primary-database audit event before returning success.
	relayQuotaReview := r.Group("/api/relay-quota-reservations")
	relayQuotaReview.Use(middleware.GlobalRateLimit(), middleware.RootAuth(), middleware.DisableCache())
	relayQuotaReview.GET("/manual-review", controller.ListManualReviewRelayQuotaReservations)
	relayQuotaReview.GET("/manual-review/:reservation_id", controller.GetManualReviewRelayQuotaReservation)
	relayQuotaReview.POST("/manual-review/:reservation_id/retry", middleware.CriticalRateLimit(),
		controller.RetryManualReviewRelayQuotaReservation)
	relayQuotaReview.POST("/manual-review/:reservation_id/resolve", middleware.CriticalRateLimit(),
		controller.ResolveManualReviewRelayQuotaReservation)

	// Subscription administration (plans + user subscriptions).
	subAdmin := r.Group("/api/subscription/admin")
	subAdmin.Use(middleware.AdminAuth())
	subAdmin.GET("/plans", controller.AdminListSubscriptionPlans)
	subAdmin.POST("/plans", controller.AdminCreateSubscriptionPlan)
	subAdmin.PUT("/plans/:id", controller.AdminUpdateSubscriptionPlan)
	subAdmin.PATCH("/plans/:id", controller.AdminUpdateSubscriptionPlanStatus)
	subAdmin.POST("/bind", controller.AdminBindSubscription)
	subAdmin.POST("/plans/:id/subscriptions/reset", controller.AdminResetPlanSubscriptions)
	subAdmin.GET("/users/:id/subscriptions", controller.AdminListUserSubscriptions)
	subAdmin.POST("/users/:id/subscriptions", controller.AdminCreateUserSubscription)
	subAdmin.POST("/users/:id/subscriptions/reset", controller.AdminResetUserSubscriptionsByPlan)
	subAdmin.POST("/user_subscriptions/:id/invalidate", controller.AdminInvalidateUserSubscription)
	subAdmin.DELETE("/user_subscriptions/:id", controller.AdminDeleteUserSubscription)
	subAdmin.POST("/user_subscriptions/:id/entitlement-review/resolve", middleware.CriticalRateLimit(), middleware.RootAuth(),
		controller.ResolveLegacySubscriptionEntitlementReview)
}
