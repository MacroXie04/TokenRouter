# TokenRouter Forensic Inventory

> Generated from a read-only inspection of the reference system. This document is
> a neutral functional specification, not a copy of reference code.

## Summary

- **HTTP Routes**: 341 entries
- **Persistent Entities**: 35 entries
- **Channel Types & Providers**: 211 entries
- **Relay Formats, Modes & DTOs**: 58 entries
- **Configuration & Settings**: 270 entries
- **Frontend Routes, Settings Pages & Locales**: 98 entries
- **Billing, Security & Background Jobs**: 67 entries

## HTTP Routes

| Name | Detail | Evidence |
|---|---|---|
| GET /api/setup | Public: controller.GetSetup (no auth; group: API status/setup) | router/api-router.go:22 |
| POST /api/setup | Public: controller.PostSetup + middleware.AnonymousRequestBodyLimit | router/api-router.go:23 |
| GET /api/status | Public: controller.GetStatus (health/status endpoint) | router/api-router.go:24 |
| GET /api/uptime/status | Public: controller.GetUptimeKumaStatus | router/api-router.go:25 |
| GET /api/models | UserAuth: controller.DashboardListModels | router/api-router.go:26 |
| GET /api/status/test | AdminAuth: controller.TestStatus | router/api-router.go:27 |
| GET /api/notice | Public: controller.GetNotice | router/api-router.go:28 |
| GET /api/user-agreement | Public: controller.GetUserAgreement | router/api-router.go:29 |
| GET /api/privacy-policy | Public: controller.GetPrivacyPolicy | router/api-router.go:30 |
| GET /api/about | Public: controller.GetAbout | router/api-router.go:31 |
| GET /api/home_page_content | Public: controller.GetHomePageContent | router/api-router.go:33 |
| GET /api/pricing | HeaderNavModuleAuth('pricing'): controller.GetPricing | router/api-router.go:34 |
| GET /api/perf-metrics/summary | HeaderNavModulePublicOrUserAuth('pricing'): controller.GetPerfMetricsSummary | router/api-router.go:38 |
| GET /api/perf-metrics | HeaderNavModulePublicOrUserAuth('pricing'): controller.GetPerfMetrics | router/api-router.go:39 |
| GET /api/rankings | HeaderNavModuleAuth('rankings'): controller.GetRankings | router/api-router.go:41 |
| GET /api/verification | EmailVerificationRateLimit + TurnstileCheck: controller.SendEmailVerification | router/api-router.go:42 |
| GET /api/reset_password | CriticalRateLimit + TurnstileCheck: controller.SendPasswordResetEmail | router/api-router.go:43 |
| POST /api/user/reset | CriticalRateLimit + AnonymousRequestBodyLimit: controller.ResetPassword | router/api-router.go:44 |
| POST /api/oauth/state | CriticalRateLimit + DisableCache + TryUserAuth + AnonymousRequestBodyLimit: controller.GenerateOAuthCode | router/api-router.go:46 |
| POST /api/oauth/email/bind | UserAuth + CriticalRateLimit: controller.EmailBind | router/api-router.go:47 |
| GET /api/oauth/wechat | CriticalRateLimit + DisableCache: controller.WeChatAuth | router/api-router.go:49 |
| POST /api/oauth/wechat/bind | UserAuth + CriticalRateLimit: controller.WeChatBind | router/api-router.go:50 |
| GET /api/oauth/telegram/login | CriticalRateLimit + DisableCache: controller.TelegramLogin | router/api-router.go:51 |
| POST /api/oauth/telegram/bind/start | UserAuth + CriticalRateLimit + DisableCache: controller.TelegramBindStart | router/api-router.go:52 |
| GET /api/oauth/telegram/bind/:flow_token | CriticalRateLimit + DisableCache: controller.TelegramBind | router/api-router.go:53 |
| GET /api/oauth/:provider | CriticalRateLimit + DisableCache + TryUserAuth: controller.HandleOAuth (GitHub/Discord/OIDC/LinuxDO) | router/api-router.go:55 |
| GET /api/ratio_config | CriticalRateLimit: controller.GetRatioConfig | router/api-router.go:56 |
| POST /api/stripe/webhook | AnonymousRequestBodyLimit: controller.StripeWebhook | router/api-router.go:58 |
| POST /api/creem/webhook | AnonymousRequestBodyLimit: controller.CreemWebhook | router/api-router.go:59 |
| POST /api/waffo/webhook | AnonymousRequestBodyLimit: controller.WaffoWebhook | router/api-router.go:60 |
| POST /api/waffo-pancake/webhook/:env | AnonymousRequestBodyLimit: controller.WaffoPancakeWebhook | router/api-router.go:63 |
| POST /api/verify | UserAuth + CriticalRateLimit + DisableCache: controller.UniversalVerify | router/api-router.go:66 |
| POST /api/user/auth/refresh | SessionCookieOriginGuard + CriticalRateLimit + DisableCache: controller.RefreshAuth | router/api-router.go:70 |
| POST /api/user/auth/logout | SessionCookieOriginGuard + CriticalRateLimit + DisableCache: controller.AuthLogout | router/api-router.go:71 |
| POST /api/user/register | CriticalRateLimit + AnonymousRequestBodyLimit + TurnstileCheck: controller.Register | router/api-router.go:72 |
| POST /api/user/login | CriticalRateLimit + DisableCache + AnonymousRequestBodyLimit + TurnstileCheck: controller.Login | router/api-router.go:73 |
| POST /api/user/login/2fa | CriticalRateLimit + DisableCache + AnonymousRequestBodyLimit: controller.Verify2FALogin | router/api-router.go:74 |
| POST /api/user/passkey/login/begin | CriticalRateLimit + DisableCache + AnonymousRequestBodyLimit: controller.PasskeyLoginBegin | router/api-router.go:75 |
| POST /api/user/passkey/login/finish | CriticalRateLimit + DisableCache + AnonymousRequestBodyLimit: controller.PasskeyLoginFinish | router/api-router.go:76 |
| POST /api/user/epay/notify | AnonymousRequestBodyLimit: controller.EpayNotify | router/api-router.go:78 |
| GET /api/user/epay/notify | Public: controller.EpayNotify | router/api-router.go:79 |
| GET /api/user/groups | Public: controller.GetUserGroups | router/api-router.go:80 |
| GET /api/user/sessions | UserAuth + DisableCache: controller.GetLoginSessions | router/api-router.go:85 |
| DELETE /api/user/sessions/:sid | UserAuth + DisableCache: controller.DeleteLoginSession | router/api-router.go:86 |
| POST /api/user/sessions/revoke-others | UserAuth + DisableCache: controller.RevokeOtherLoginSessions | router/api-router.go:87 |
| GET /api/user/self/groups | UserAuth: controller.GetUserGroups | router/api-router.go:88 |
| GET /api/user/self | UserAuth: controller.GetSelf | router/api-router.go:89 |
| GET /api/user/models | UserAuth: controller.GetUserModels | router/api-router.go:90 |
| PUT /api/user/self | UserAuth + CriticalRateLimit + DisableCache: controller.UpdateSelf | router/api-router.go:91 |
| DELETE /api/user/self | UserAuth: controller.DeleteSelf | router/api-router.go:92 |
| GET /api/user/token | UserAuth + CriticalRateLimit + UserCriticalRateLimit('access-token') + DisableCache: controller.GenerateAccessToken | router/api-router.go:93 |
| GET /api/user/passkey | UserAuth: controller.PasskeyStatus | router/api-router.go:94 |
| POST /api/user/passkey/register/begin | UserAuth + DisableCache: controller.PasskeyRegisterBegin | router/api-router.go:95 |
| POST /api/user/passkey/register/finish | UserAuth + DisableCache: controller.PasskeyRegisterFinish | router/api-router.go:96 |
| POST /api/user/passkey/verify/begin | UserAuth + DisableCache: controller.PasskeyVerifyBegin | router/api-router.go:97 |
| POST /api/user/passkey/verify/finish | UserAuth + DisableCache: controller.PasskeyVerifyFinish | router/api-router.go:98 |
| DELETE /api/user/passkey | UserAuth + DisableCache: controller.PasskeyDelete | router/api-router.go:99 |
| GET /api/user/aff | UserAuth: controller.GetAffCode | router/api-router.go:100 |
| GET /api/user/topup/info | UserAuth: controller.GetTopUpInfo | router/api-router.go:101 |
| GET /api/user/topup/self | UserAuth: controller.GetUserTopUps | router/api-router.go:102 |
| POST /api/user/topup | UserAuth + CriticalRateLimit: controller.TopUp | router/api-router.go:103 |
| POST /api/user/pay | UserAuth + CriticalRateLimit: controller.RequestEpay | router/api-router.go:104 |
| POST /api/user/amount | UserAuth: controller.RequestAmount | router/api-router.go:105 |
| POST /api/user/stripe/pay | UserAuth + CriticalRateLimit: controller.RequestStripePay | router/api-router.go:106 |
| POST /api/user/stripe/amount | UserAuth: controller.RequestStripeAmount | router/api-router.go:107 |
| POST /api/user/creem/pay | UserAuth + CriticalRateLimit: controller.RequestCreemPay | router/api-router.go:108 |
| POST /api/user/waffo/amount | UserAuth: controller.RequestWaffoAmount | router/api-router.go:109 |
| POST /api/user/waffo/pay | UserAuth + CriticalRateLimit: controller.RequestWaffoPay | router/api-router.go:110 |
| POST /api/user/waffo-pancake/amount | UserAuth: controller.RequestWaffoPancakeAmount | router/api-router.go:111 |
| POST /api/user/waffo-pancake/pay | UserAuth + CriticalRateLimit: controller.RequestWaffoPancakePay | router/api-router.go:112 |
| POST /api/user/aff_transfer | UserAuth + UserCriticalRateLimit('aff-transfer'): controller.TransferAffQuota | router/api-router.go:113 |
| PUT /api/user/setting | UserAuth: controller.UpdateUserSetting | router/api-router.go:114 |
| GET /api/user/2fa/status | UserAuth: controller.Get2FAStatus | router/api-router.go:117 |
| POST /api/user/2fa/setup | UserAuth + DisableCache: controller.Setup2FA | router/api-router.go:118 |
| POST /api/user/2fa/enable | UserAuth + DisableCache: controller.Enable2FA | router/api-router.go:119 |
| POST /api/user/2fa/disable | UserAuth + DisableCache: controller.Disable2FA | router/api-router.go:120 |
| POST /api/user/2fa/backup_codes | UserAuth + DisableCache: controller.RegenerateBackupCodes | router/api-router.go:121 |
| GET /api/user/checkin | UserAuth: controller.GetCheckinStatus | router/api-router.go:124 |
| POST /api/user/checkin | UserAuth + TurnstileCheck: controller.DoCheckin | router/api-router.go:125 |
| GET /api/user/oauth/bindings | UserAuth: controller.GetUserOAuthBindings | router/api-router.go:128 |
| DELETE /api/user/oauth/bindings/:provider_id | UserAuth: controller.UnbindCustomOAuth | router/api-router.go:129 |
| GET /api/user/ | AdminAuth: controller.GetAllUsers (admin user management) | router/api-router.go:135 |
| GET /api/user/topup | AdminAuth: controller.GetAllTopUps | router/api-router.go:136 |
| POST /api/user/topup/complete | AdminAuth: controller.AdminCompleteTopUp | router/api-router.go:137 |
| GET /api/user/search | AdminAuth: controller.SearchUsers | router/api-router.go:138 |
| GET /api/user/:id/oauth/bindings | AdminAuth: controller.GetUserOAuthBindingsByAdmin | router/api-router.go:139 |
| DELETE /api/user/:id/oauth/bindings/:provider_id | AdminAuth: controller.UnbindCustomOAuthByAdmin | router/api-router.go:140 |
| DELETE /api/user/:id/bindings/:binding_type | AdminAuth: controller.AdminClearUserBinding | router/api-router.go:141 |
| GET /api/user/:id | AdminAuth: controller.GetUser | router/api-router.go:142 |
| POST /api/user/ | AdminAuth: controller.CreateUser | router/api-router.go:143 |
| POST /api/user/manage | AdminAuth: controller.ManageUser | router/api-router.go:144 |
| PUT /api/user/ | AdminAuth: controller.UpdateUser | router/api-router.go:145 |
| DELETE /api/user/:id | AdminAuth: controller.DeleteUser | router/api-router.go:146 |
| DELETE /api/user/:id/reset_passkey | AdminAuth: controller.AdminResetPasskey | router/api-router.go:147 |
| GET /api/user/2fa/stats | AdminAuth: controller.Admin2FAStats | router/api-router.go:150 |
| DELETE /api/user/:id/2fa | AdminAuth: controller.AdminDisable2FA | router/api-router.go:151 |
| GET /api/subscription/plans | UserAuth: controller.GetSubscriptionPlans | router/api-router.go:159 |
| GET /api/subscription/self | UserAuth: controller.GetSubscriptionSelf | router/api-router.go:160 |
| PUT /api/subscription/self/preference | UserAuth: controller.UpdateSubscriptionPreference | router/api-router.go:161 |
| POST /api/subscription/balance/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestBalancePay | router/api-router.go:162 |
| POST /api/subscription/epay/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestEpay | router/api-router.go:163 |
| POST /api/subscription/stripe/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestStripePay | router/api-router.go:164 |
| POST /api/subscription/creem/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestCreemPay | router/api-router.go:165 |
| POST /api/subscription/waffo-pancake/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestWaffoPancakePay | router/api-router.go:166 |
| GET /api/subscription/admin/plans | AdminAuth: controller.AdminListSubscriptionPlans | router/api-router.go:171 |
| POST /api/subscription/admin/plans | AdminAuth: controller.AdminCreateSubscriptionPlan | router/api-router.go:172 |
| PUT /api/subscription/admin/plans/:id | AdminAuth: controller.AdminUpdateSubscriptionPlan | router/api-router.go:173 |
| PATCH /api/subscription/admin/plans/:id | AdminAuth: controller.AdminUpdateSubscriptionPlanStatus | router/api-router.go:174 |
| POST /api/subscription/admin/bind | AdminAuth: controller.AdminBindSubscription | router/api-router.go:175 |
| POST /api/subscription/admin/plans/:id/subscriptions/reset | AdminAuth: controller.AdminResetPlanSubscriptions | router/api-router.go:176 |
| GET /api/subscription/admin/users/:id/subscriptions | AdminAuth: controller.AdminListUserSubscriptions | router/api-router.go:179 |
| POST /api/subscription/admin/users/:id/subscriptions | AdminAuth: controller.AdminCreateUserSubscription | router/api-router.go:180 |
| POST /api/subscription/admin/users/:id/subscriptions/reset | AdminAuth: controller.AdminResetUserSubscriptionsByPlan | router/api-router.go:181 |
| POST /api/subscription/admin/user_subscriptions/:id/invalidate | AdminAuth: controller.AdminInvalidateUserSubscription | router/api-router.go:182 |
| DELETE /api/subscription/admin/user_subscriptions/:id | AdminAuth: controller.AdminDeleteUserSubscription | router/api-router.go:183 |
| POST /api/subscription/epay/notify | AnonymousRequestBodyLimit: controller.SubscriptionEpayNotify (no auth callback) | router/api-router.go:187 |
| GET /api/subscription/epay/notify | Public: controller.SubscriptionEpayNotify | router/api-router.go:188 |
| GET /api/subscription/epay/return | Public: controller.SubscriptionEpayReturn | router/api-router.go:189 |
| POST /api/subscription/epay/return | AnonymousRequestBodyLimit: controller.SubscriptionEpayReturn | router/api-router.go:190 |
| GET /api/option/ | RootAuth: controller.GetOptions | router/api-router.go:194 |
| PUT /api/option/ | RootAuth: controller.UpdateOption | router/api-router.go:195 |
| POST /api/option/payment_compliance | RootAuth: controller.ConfirmPaymentCompliance | router/api-router.go:196 |
| GET /api/option/channel_affinity_cache | RootAuth: controller.GetChannelAffinityCacheStats | router/api-router.go:197 |
| DELETE /api/option/channel_affinity_cache | RootAuth: controller.ClearChannelAffinityCache | router/api-router.go:198 |
| POST /api/option/rest_model_ratio | RootAuth: controller.ResetModelRatio | router/api-router.go:199 |
| GET /api/option/waffo-pancake/catalog | RootAuth: controller.ListWaffoPancakeCatalog | router/api-router.go:200 |
| POST /api/option/waffo-pancake/pair | RootAuth: controller.CreateWaffoPancakePair | router/api-router.go:201 |
| POST /api/option/waffo-pancake/save | RootAuth: controller.SaveWaffoPancake | router/api-router.go:202 |
| POST /api/option/waffo-pancake/subscription-product | RootAuth: controller.CreateWaffoPancakeSubscriptionProduct | router/api-router.go:203 |
| GET /api/option/waffo-pancake/subscription-product-options | RootAuth: controller.ListWaffoPancakeSubscriptionProductOptions | router/api-router.go:204 |
| POST /api/custom-oauth-provider/discovery | RootAuth: controller.FetchCustomOAuthDiscovery | router/api-router.go:211 |
| GET /api/custom-oauth-provider/ | RootAuth: controller.GetCustomOAuthProviders | router/api-router.go:212 |
| GET /api/custom-oauth-provider/:id | RootAuth: controller.GetCustomOAuthProvider | router/api-router.go:213 |
| POST /api/custom-oauth-provider/ | RootAuth: controller.CreateCustomOAuthProvider | router/api-router.go:214 |
| PUT /api/custom-oauth-provider/:id | RootAuth: controller.UpdateCustomOAuthProvider | router/api-router.go:215 |
| DELETE /api/custom-oauth-provider/:id | RootAuth: controller.DeleteCustomOAuthProvider | router/api-router.go:216 |
| GET /api/performance/stats | RootAuth: controller.GetPerformanceStats | router/api-router.go:221 |
| DELETE /api/performance/disk_cache | RootAuth: controller.ClearDiskCache | router/api-router.go:222 |
| POST /api/performance/reset_stats | RootAuth: controller.ResetPerformanceStats | router/api-router.go:223 |
| POST /api/performance/gc | RootAuth: controller.ForceGC | router/api-router.go:224 |
| GET /api/performance/logs | RootAuth: controller.GetLogFiles | router/api-router.go:225 |
| DELETE /api/performance/logs | RootAuth: controller.CleanupLogFiles | router/api-router.go:226 |
| GET /api/ratio_sync/channels | RootAuth: controller.GetSyncableChannels | router/api-router.go:231 |
| POST /api/ratio_sync/fetch | RootAuth: controller.FetchUpstreamRatios | router/api-router.go:232 |
| POST /api/channel/:id/key | AdminAuth + RootAuth + CriticalRateLimit + DisableCache + SecureVerificationRequired: controller.GetChannelKey | router/channel-router.go:23 |
| GET /api/channel/ | AdminAuth + RequirePermission(ChannelRead): controller.GetAllChannels | router/channel-router.go:40 |
| GET /api/channel/search | AdminAuth + RequirePermission(ChannelRead): controller.SearchChannels | router/channel-router.go:41 |
| GET /api/channel/models | AdminAuth + RequirePermission(ChannelRead): controller.ChannelListModels | router/channel-router.go:42 |
| GET /api/channel/models_enabled | AdminAuth + RequirePermission(ChannelRead): controller.EnabledListModels | router/channel-router.go:43 |
| GET /api/channel/ops | AdminAuth + RequirePermission(ChannelRead): controller.GetChannelOps | router/channel-router.go:44 |
| GET /api/channel/:id | AdminAuth + RequirePermission(ChannelRead): controller.GetChannel | router/channel-router.go:45 |
| GET /api/channel/test | AdminAuth + RequirePermission(ChannelOperate): controller.TestAllChannels | router/channel-router.go:46 |
| GET /api/channel/test/:id | AdminAuth + RequirePermission(ChannelOperate): controller.TestChannel | router/channel-router.go:47 |
| GET /api/channel/update_balance | AdminAuth + RequirePermission(ChannelOperate): controller.UpdateAllChannelsBalance | router/channel-router.go:48 |
| GET /api/channel/update_balance/:id | AdminAuth + RequirePermission(ChannelOperate): controller.UpdateChannelBalance | router/channel-router.go:49 |
| POST /api/channel/ | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.AddChannel | router/channel-router.go:50 |
| PUT /api/channel/ | AdminAuth + RequirePermission(ChannelWrite): controller.UpdateChannel | router/channel-router.go:51 |
| POST /api/channel/status/batch | AdminAuth + RequirePermission(ChannelOperate): controller.BatchUpdateChannelStatus | router/channel-router.go:52 |
| POST /api/channel/:id/status | AdminAuth + RequirePermission(ChannelOperate): controller.UpdateChannelStatus | router/channel-router.go:53 |
| DELETE /api/channel/disabled | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.DeleteDisabledChannel | router/channel-router.go:54 |
| POST /api/channel/tag/disabled | AdminAuth + RequirePermission(ChannelOperate): controller.DisableTagChannels | router/channel-router.go:55 |
| POST /api/channel/tag/enabled | AdminAuth + RequirePermission(ChannelOperate): controller.EnableTagChannels | router/channel-router.go:56 |
| PUT /api/channel/tag | AdminAuth + RequirePermission(ChannelWrite): controller.EditTagChannels | router/channel-router.go:57 |
| DELETE /api/channel/:id | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.DeleteChannel | router/channel-router.go:58 |
| POST /api/channel/batch | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.DeleteChannelBatch | router/channel-router.go:59 |
| POST /api/channel/fix | AdminAuth + RequirePermission(ChannelOperate): controller.FixChannelsAbilities | router/channel-router.go:60 |
| GET /api/channel/fetch_models/:id | AdminAuth + RequirePermission(ChannelOperate): controller.FetchUpstreamModels | router/channel-router.go:61 |
| POST /api/channel/fetch_models | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.FetchModels | router/channel-router.go:62 |
| POST /api/channel/:id/codex/refresh | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.RefreshCodexChannelCredential | router/channel-router.go:63 |
| GET /api/channel/:id/codex/usage | AdminAuth + RequirePermission(ChannelRead): controller.GetCodexChannelUsage | router/channel-router.go:64 |
| GET /api/channel/:id/codex/usage/reset-credits | AdminAuth + RequirePermission(ChannelRead): controller.GetCodexChannelRateLimitResetCredits | router/channel-router.go:65 |
| POST /api/channel/:id/codex/usage/reset | AdminAuth + RequirePermission(ChannelOperate): controller.ResetCodexChannelUsage | router/channel-router.go:66 |
| POST /api/channel/ollama/pull | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.OllamaPullModel | router/channel-router.go:67 |
| POST /api/channel/ollama/pull/stream | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.OllamaPullModelStream | router/channel-router.go:68 |
| DELETE /api/channel/ollama/delete | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.OllamaDeleteModel | router/channel-router.go:69 |
| GET /api/channel/ollama/version/:id | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.OllamaVersion | router/channel-router.go:70 |
| POST /api/channel/batch/tag | AdminAuth + RequirePermission(ChannelWrite): controller.BatchSetChannelTag | router/channel-router.go:71 |
| GET /api/channel/tag/models | AdminAuth + RequirePermission(ChannelRead): controller.GetTagModels | router/channel-router.go:72 |
| POST /api/channel/copy/:id | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.CopyChannel | router/channel-router.go:73 |
| POST /api/channel/multi_key/manage | AdminAuth + RequirePermission(ChannelOperate): controller.ManageMultiKeys | router/channel-router.go:74 |
| POST /api/channel/upstream_updates/apply | AdminAuth + RequirePermission(ChannelWrite): controller.ApplyChannelUpstreamModelUpdates | router/channel-router.go:75 |
| POST /api/channel/upstream_updates/apply_all | AdminAuth + RequirePermission(ChannelWrite): controller.ApplyAllChannelUpstreamModelUpdates | router/channel-router.go:76 |
| POST /api/channel/upstream_updates/detect | AdminAuth + RequirePermission(ChannelOperate): controller.DetectChannelUpstreamModelUpdates | router/channel-router.go:77 |
| POST /api/channel/upstream_updates/detect_all | AdminAuth + RequirePermission(ChannelOperate): controller.DetectAllChannelUpstreamModelUpdates | router/channel-router.go:78 |
| GET /api/authz/catalog | AdminAuth: controller.GetPermissionCatalog | router/authz-router.go:17 |
| GET /api/token/ | UserAuth: controller.GetAllTokens | router/api-router.go:239 |
| GET /api/token/search | UserAuth + SearchRateLimit: controller.SearchTokens | router/api-router.go:240 |
| GET /api/token/auto-groups | UserAuth: controller.GetTokenAutoGroups | router/api-router.go:241 |
| GET /api/token/:id | UserAuth: controller.GetToken | router/api-router.go:242 |
| POST /api/token/:id/key | UserAuth + CriticalRateLimit + DisableCache: controller.GetTokenKey | router/api-router.go:243 |
| POST /api/token/ | UserAuth: controller.AddToken | router/api-router.go:244 |
| PUT /api/token/ | UserAuth: controller.UpdateToken | router/api-router.go:245 |
| DELETE /api/token/:id | UserAuth: controller.DeleteToken | router/api-router.go:246 |
| POST /api/token/batch | UserAuth: controller.DeleteTokenBatch | router/api-router.go:247 |
| POST /api/token/batch/keys | UserAuth + CriticalRateLimit + DisableCache: controller.GetTokenKeysBatch | router/api-router.go:248 |
| GET /api/usage/token/ | CORS + CriticalRateLimit + TokenAuthReadOnly: controller.GetTokenUsage | router/api-router.go:257 |
| GET /api/redemption/ | AdminAuth: controller.GetAllRedemptions | router/api-router.go:264 |
| GET /api/redemption/search | AdminAuth: controller.SearchRedemptions | router/api-router.go:265 |
| GET /api/redemption/:id | AdminAuth: controller.GetRedemption | router/api-router.go:266 |
| POST /api/redemption/ | AdminAuth: controller.AddRedemption | router/api-router.go:267 |
| PUT /api/redemption/ | AdminAuth: controller.UpdateRedemption | router/api-router.go:268 |
| DELETE /api/redemption/invalid | AdminAuth: controller.DeleteInvalidRedemption | router/api-router.go:269 |
| DELETE /api/redemption/:id | AdminAuth: controller.DeleteRedemption | router/api-router.go:270 |
| GET /api/log/ | AdminAuth: controller.GetAllLogs | router/api-router.go:273 |
| GET /api/log/stat | AdminAuth: controller.GetLogsStat | router/api-router.go:274 |
| GET /api/log/self/stat | UserAuth: controller.GetLogsSelfStat | router/api-router.go:275 |
| GET /api/log/channel_affinity_usage_cache | AdminAuth: controller.GetChannelAffinityUsageCacheStats | router/api-router.go:276 |
| GET /api/log/search | AdminAuth: controller.SearchAllLogs | router/api-router.go:277 |
| GET /api/log/self | UserAuth: controller.GetUserLogs | router/api-router.go:278 |
| GET /api/log/self/search | UserAuth + SearchRateLimit: controller.SearchUserLogs | router/api-router.go:279 |
| GET /api/log/token | CORS + CriticalRateLimit + TokenAuthReadOnly: controller.GetLogByKey | router/api-router.go:306 |
| POST /api/system-task/log-cleanup | RootAuth: controller.CreateLogCleanupSystemTask | router/api-router.go:284 |
| GET /api/system-task/list | RootAuth: controller.ListSystemTasks | router/api-router.go:285 |
| GET /api/system-task/current | RootAuth: controller.GetCurrentSystemTask | router/api-router.go:286 |
| GET /api/system-task/:task_id | RootAuth: controller.GetSystemTask | router/api-router.go:287 |
| GET /api/system-info/instances | RootAuth: controller.ListSystemInstances | router/api-router.go:292 |
| DELETE /api/system-info/stale-instances | RootAuth: controller.DeleteStaleSystemInstances | router/api-router.go:293 |
| DELETE /api/system-info/instances/:node_name | RootAuth: controller.DeleteStaleSystemInstance | router/api-router.go:294 |
| GET /api/data/ | AdminAuth: controller.GetAllQuotaDates | router/api-router.go:298 |
| GET /api/data/users | AdminAuth: controller.GetQuotaDatesByUser | router/api-router.go:299 |
| GET /api/data/self | UserAuth: controller.GetUserQuotaDates | router/api-router.go:300 |
| GET /api/data/flow | AdminAuth: controller.GetAllFlowQuotaDates | router/api-router.go:301 |
| GET /api/data/flow/self | UserAuth: controller.GetUserFlowQuotaDates | router/api-router.go:302 |
| GET /api/group/ | AdminAuth: controller.GetGroups | router/api-router.go:311 |
| GET /api/prefill_group/ | AdminAuth: controller.GetPrefillGroups | router/api-router.go:317 |
| POST /api/prefill_group/ | AdminAuth: controller.CreatePrefillGroup | router/api-router.go:318 |
| PUT /api/prefill_group/ | AdminAuth: controller.UpdatePrefillGroup | router/api-router.go:319 |
| DELETE /api/prefill_group/:id | AdminAuth: controller.DeletePrefillGroup | router/api-router.go:320 |
| GET /api/mj/self | UserAuth: controller.GetUserMidjourney | router/api-router.go:324 |
| GET /api/mj/ | AdminAuth: controller.GetAllMidjourney | router/api-router.go:325 |
| GET /api/task/self | UserAuth: controller.GetUserTask | router/api-router.go:329 |
| GET /api/task/ | AdminAuth: controller.GetAllTask | router/api-router.go:330 |
| GET /api/vendors/ | AdminAuth: controller.GetAllVendors | router/api-router.go:336 |
| GET /api/vendors/search | AdminAuth: controller.SearchVendors | router/api-router.go:337 |
| GET /api/vendors/:id | AdminAuth: controller.GetVendorMeta | router/api-router.go:338 |
| POST /api/vendors/ | AdminAuth: controller.CreateVendorMeta | router/api-router.go:339 |
| PUT /api/vendors/ | AdminAuth: controller.UpdateVendorMeta | router/api-router.go:340 |
| DELETE /api/vendors/:id | AdminAuth: controller.DeleteVendorMeta | router/api-router.go:341 |
| GET /api/models/sync_upstream/preview | AdminAuth: controller.SyncUpstreamPreview | router/api-router.go:347 |
| POST /api/models/sync_upstream | AdminAuth: controller.SyncUpstreamModels | router/api-router.go:348 |
| GET /api/models/missing | AdminAuth: controller.GetMissingModels | router/api-router.go:349 |
| GET /api/models/ | AdminAuth: controller.GetAllModelsMeta | router/api-router.go:350 |
| GET /api/models/search | AdminAuth: controller.SearchModelsMeta | router/api-router.go:351 |
| GET /api/models/:id | AdminAuth: controller.GetModelMeta | router/api-router.go:352 |
| POST /api/models/ | AdminAuth: controller.CreateModelMeta | router/api-router.go:353 |
| PUT /api/models/ | AdminAuth: controller.UpdateModelMeta | router/api-router.go:354 |
| DELETE /api/models/:id | AdminAuth: controller.DeleteModelMeta | router/api-router.go:355 |
| GET /api/deployments/settings | AdminAuth: controller.GetModelDeploymentSettings | router/api-router.go:362 |
| POST /api/deployments/settings/test-connection | AdminAuth: controller.TestIoNetConnection | router/api-router.go:363 |
| GET /api/deployments/ | AdminAuth: controller.GetAllDeployments | router/api-router.go:364 |
| GET /api/deployments/search | AdminAuth: controller.SearchDeployments | router/api-router.go:365 |
| POST /api/deployments/test-connection | AdminAuth: controller.TestIoNetConnection | router/api-router.go:366 |
| GET /api/deployments/hardware-types | AdminAuth: controller.GetHardwareTypes | router/api-router.go:367 |
| GET /api/deployments/locations | AdminAuth: controller.GetLocations | router/api-router.go:368 |
| GET /api/deployments/available-replicas | AdminAuth: controller.GetAvailableReplicas | router/api-router.go:369 |
| POST /api/deployments/price-estimation | AdminAuth: controller.GetPriceEstimation | router/api-router.go:370 |
| GET /api/deployments/check-name | AdminAuth: controller.CheckClusterNameAvailability | router/api-router.go:371 |
| POST /api/deployments/ | AdminAuth: controller.CreateDeployment | router/api-router.go:372 |
| GET /api/deployments/:id | AdminAuth: controller.GetDeployment | router/api-router.go:374 |
| GET /api/deployments/:id/logs | AdminAuth: controller.GetDeploymentLogs | router/api-router.go:375 |
| GET /api/deployments/:id/containers | AdminAuth: controller.ListDeploymentContainers | router/api-router.go:376 |
| GET /api/deployments/:id/containers/:container_id | AdminAuth: controller.GetContainerDetails | router/api-router.go:377 |
| PUT /api/deployments/:id | AdminAuth: controller.UpdateDeployment | router/api-router.go:378 |
| PUT /api/deployments/:id/name | AdminAuth: controller.UpdateDeploymentName | router/api-router.go:379 |
| POST /api/deployments/:id/extend | AdminAuth: controller.ExtendDeployment | router/api-router.go:380 |
| DELETE /api/deployments/:id | AdminAuth: controller.DeleteDeployment | router/api-router.go:381 |
| GET /dashboard/billing/subscription | RouteTag('old_api') + CORS + TokenAuth: controller.GetSubscription | router/dashboard.go:18 |
| GET /v1/dashboard/billing/subscription | RouteTag('old_api') + CORS + TokenAuth: controller.GetSubscription | router/dashboard.go:19 |
| GET /dashboard/billing/usage | RouteTag('old_api') + CORS + TokenAuth: controller.GetUsage | router/dashboard.go:20 |
| GET /v1/dashboard/billing/usage | RouteTag('old_api') + CORS + TokenAuth: controller.GetUsage | router/dashboard.go:21 |
| GET /v1/models | RouteTag('relay') + TokenAuth: controller.ListModels (OpenAI/Anthropic/Gemini dispatch) | router/relay-router.go:23 |
| GET /v1/models/:model | RouteTag('relay') + TokenAuth: controller.RetrieveModel (Anthropic/OpenAI dispatch) | router/relay-router.go:34 |
| GET /v1beta/models | RouteTag('relay') + TokenAuth: controller.ListModels (Gemini) | router/relay-router.go:48 |
| GET /v1beta/openai/models | RouteTag('relay') + TokenAuth: controller.ListModels (OpenAI) | router/relay-router.go:57 |
| POST /pg/chat/completions | RouteTag('relay') + SystemPerformanceCheck + UserAuth + Distribute: controller.Playground | router/relay-router.go:67 |
| GET /v1/realtime | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIRealtime, WebSocket) | router/relay-router.go:78 |
| POST /v1/messages | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatClaude) | router/relay-router.go:88 |
| POST /v1/completions | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAI) | router/relay-router.go:93 |
| POST /v1/chat/completions | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAI) | router/relay-router.go:96 |
| POST /v1/responses | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIResponses) | router/relay-router.go:101 |
| POST /v1/responses/compact | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIResponsesCompaction) | router/relay-router.go:104 |
| POST /v1/alpha/search | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIAlphaSearch) | router/relay-router.go:109 |
| POST /v1/edits | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIImage) | router/relay-router.go:114 |
| POST /v1/images/generations | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIImage) | router/relay-router.go:117 |
| POST /v1/images/edits | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIImage) | router/relay-router.go:120 |
| POST /v1/embeddings | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatEmbedding) | router/relay-router.go:125 |
| POST /v1/audio/transcriptions | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIAudio) | router/relay-router.go:130 |
| POST /v1/audio/translations | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIAudio) | router/relay-router.go:133 |
| POST /v1/audio/speech | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIAudio) | router/relay-router.go:136 |
| POST /v1/rerank | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatRerank) | router/relay-router.go:141 |
| POST /v1/engines/:model/embeddings | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatGemini) | router/relay-router.go:146 |
| POST /v1/models/*path | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatGemini) | router/relay-router.go:149 |
| POST /v1/moderations | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAI) | router/relay-router.go:154 |
| POST /v1/images/variations | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:159 |
| GET /v1/files | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:160 |
| POST /v1/files | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:161 |
| DELETE /v1/files/:id | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:162 |
| GET /v1/files/:id | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:163 |
| GET /v1/files/:id/content | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:164 |
| POST /v1/fine-tunes | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:165 |
| GET /v1/fine-tunes | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:166 |
| GET /v1/fine-tunes/:id | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:167 |
| POST /v1/fine-tunes/:id/cancel | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:168 |
| GET /v1/fine-tunes/:id/events | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:169 |
| DELETE /v1/models/:model | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:170 |
| GET /mj/image/:id | RouteTag('relay') + SystemPerformanceCheck: relay.RelayMidjourneyImage (no TokenAuth) | router/relay-router.go:209 |
| POST /mj/submit/action | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:212 |
| POST /mj/submit/shorten | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:213 |
| POST /mj/submit/modal | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:214 |
| POST /mj/submit/imagine | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:215 |
| POST /mj/submit/change | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:216 |
| POST /mj/submit/simple-change | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:217 |
| POST /mj/submit/describe | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:218 |
| POST /mj/submit/blend | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:219 |
| POST /mj/submit/edits | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:220 |
| POST /mj/submit/video | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:221 |
| GET /mj/task/:id/fetch | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:223 |
| GET /mj/task/:id/image-seed | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:224 |
| POST /mj/task/list-by-condition | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:225 |
| POST /mj/insight-face/swap | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:226 |
| POST /mj/submit/upload-discord-images | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:227 |
| GET /:mode/mj/image/:id | RouteTag('relay') + SystemPerformanceCheck: relay.RelayMidjourneyImage (mode-prefixed MJ) | router/relay-router.go:178-181 |
| POST /:mode/mj/submit/* | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayMidjourney (mode-prefixed submit routes, all actions) | router/relay-router.go:178-181 |
| GET /:mode/mj/task/:id/fetch | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:178-181 |
| POST /suno/submit/:action | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayTask | router/relay-router.go:189 |
| POST /suno/fetch | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayTaskFetch | router/relay-router.go:190 |
| GET /suno/fetch/:id | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayTaskFetch | router/relay-router.go:191 |
| POST /v1beta/models/*path | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatGemini) | router/relay-router.go:202 |
| GET /v1/videos/:task_id/content | RouteTag('relay') + TokenOrUserAuth: controller.VideoProxy | router/video-router.go:16 |
| POST /v1/video/generations | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:23 |
| GET /v1/video/generations/:task_id | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTaskFetch | router/video-router.go:24 |
| POST /v1/videos/:video_id/remix | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:25 |
| POST /v1/videos | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTask (OpenAI-compatible video) | router/video-router.go:30 |
| GET /v1/videos/:task_id | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTaskFetch | router/video-router.go:31 |
| POST /kling/v1/videos/text2video | RouteTag('relay') + KlingRequestConvert + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:38 |
| POST /kling/v1/videos/image2video | RouteTag('relay') + KlingRequestConvert + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:39 |
| GET /kling/v1/videos/text2video/:task_id | RouteTag('relay') + KlingRequestConvert + TokenAuth + Distribute: controller.RelayTaskFetch | router/video-router.go:40 |
| GET /kling/v1/videos/image2video/:task_id | RouteTag('relay') + KlingRequestConvert + TokenAuth + Distribute: controller.RelayTaskFetch | router/video-router.go:41 |
| POST /jimeng/ | RouteTag('relay') + JimengRequestConvert + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:50 |
| GET / (web static) | gzip + GlobalWebRateLimit + Cache + static.Serve: embedded web/dist frontend | router/web-router.go:25-28 |
| NoRoute fallback | gzip + GlobalWebRateLimit + Cache; serves index.html (SPA) or controller.RelayNotFound for /v1,/api,/assets paths | router/web-router.go:29-37 |

## Persistent Entities

| Name | Detail | Evidence |
|---|---|---|
| User (users) | Core account entity. Fields: Id int, Username (unique;index), Password (not null), DisplayName (index), Role int, Status int, Email (index), GitHubId/DiscordId/OidcId/WeChatId/TelegramId (index each), AccessToken *string char(32) uniqueIndex, Quota int, UsedQuota int, RequestCount int, Group varchar(64), AffCode uniqueIndex, AffCount/AffQuota/AffHistoryQuota int, InviterId (index), DeletedAt (soft-delete index), LinuxDOId (index), Setting text, Remark, StripeCustomer (index), CreatedAt int64, LastLoginAt int64, AuthVersion bigint. No foreign-key relations (inviter/affiliation tracked by int ids). | model/user.go:79 |
| Token (tokens) | API access token. Fields: Id int, UserId (index), Key varchar(128) uniqueIndex, Status, Name (index), CreatedTime/AccessedTime/ExpiredTime int64, RemainQuota int, UnlimitedQuota bool, ModelLimitsEnabled bool, ModelLimits text, AllowIps *string, UsedQuota int, Group, CrossGroupRetry bool, AutoGroups text, DeletedAt (soft-delete index). Belongs-to User via UserId int (no FK constraint). | model/token.go:14 |
| Channel (channels) | Upstream provider channel. Fields: Id, Type, Key (not null), OpenAIOrganization *string, TestModel *string, Status, Name (index), Weight *uint, CreatedTime/TestTime int64, ResponseTime int, BaseURL *string, Other, Balance float64, BalanceUpdatedTime int64, Models, Group varchar(64), UsedQuota int64, ModelMapping text, StatusCodeMapping varchar(1024), Priority *int64, AutoBan *int, OtherInfo, Tag *string (index), Setting/ParamOverride/HeaderOverride text, Remark varchar(255), ChannelInfo json (embeds multi-key mode metadata), OtherSettings (column:settings). No soft-delete. | model/channel.go:23 |
| Ability (abilities) | Channel-model routing ability table. Composite primary key: Group varchar(64) + Model varchar(255) + ChannelId int (all primaryKey;autoIncrement:false). Extra: Enabled bool, Priority *int64 (index), Weight uint (index), Tag *string (index). Belongs-to Channel via ChannelId (joined as channels.type in AbilityWithChannel view). | model/ability.go:18 |
| Option (options) | Key/value system settings store. Fields: Key string primaryKey, Value string. No timestamps/soft-delete. | model/option.go:18 |
| Redemption (redemptions) | Invite/redeem codes. Fields: Id, UserId int, Key char(32) uniqueIndex, Status, Name (index), Quota int default 100, CreatedTime/RedeemedTime int64, UsedUserId int, DeletedAt (soft-delete index), ExpiredTime int64 (0=never). Belongs-to User via UserId/UsedUserId (int, no FK). | model/redemption.go:14 |
| Log (logs) | Request/consumption log. Fields: Id, UserId (index + composite idx_user_id_id), CreatedAt (index idx_created_at_id, idx_created_at_type), Type (index), Content, Username (index + idx index_username_model_name), TokenName (index), ModelName (index + index_username_model_name), Quota, PromptTokens, CompletionTokens, UseTime, IsStream bool, ChannelId (index), ChannelName (read-only ->), TokenId (index), Group (index), Ip (index), RequestId varchar(64) index, UpstreamRequestId varchar(128) index, Other. Written to separate LOG_DB; ClickHouse uses raw MergeTree DDL with TTL support. | model/log.go:59 |
| Midjourney (midjourneys) | Midjourney task log. Fields: Id, Code, UserId (index), Action varchar(40) index, MjId (index), Prompt, PromptEn, Description, State, SubmitTime/StartTime/FinishTime (index each), ImageUrl, VideoUrl, VideoUrls, Status varchar(20) index, Progress varchar(30) index, FailReason, ChannelId, Quota, Buttons, Properties. No soft-delete. | model/midjourney.go:3 |
| TopUp (top_ups) | Wallet recharge order. Fields: Id, UserId (index), Amount int64, Money float64, TradeNo varchar(255) unique;index, PaymentMethod varchar(50), PaymentProvider varchar(50), CreateTime/CompleteTime int64, Status string. Belongs-to User via UserId int. | model/topup.go:14 |
| QuotaData (quota_data) | Per-model usage histogram (billing aggregation). Fields: Id, UserID (index), Username (idx_qdt_model_user_name priority2), ModelName (priority1), CreatedAt (idx_qdt_created_at), UseGroup (index), TokenID (index), ChannelID (index), NodeName (index), TokenUsed, Count, Quota. No soft-delete. | model/usedata.go:13 |
| Task (tasks) | Async task (song/lyrics/video) record. Fields: ID int64 primary_key auto_increment, CreatedAt (index), UpdatedAt, TaskID varchar(191) index, Platform varchar(30) index, UserId (index), Group varchar(50), ChannelId (index), Quota, Action varchar(40) index, Status varchar(20) index, FailReason, SubmitTime/StartTime/FinishTime (index each), Progress varchar(20) index, Properties json (Scan/Value custom), PrivateData json (column:private_data, holds key/billing context), Data json.RawMessage. Belongs-to User/Channel by int id. | model/task.go:48 |
| Model (models) | Model metadata registry. Fields: Id, ModelName size:128 not null uniqueIndex:uk_model_name_delete_at priority1, Description text, Icon varchar(128), Tags varchar(255), VendorID (index), Endpoints text, Status, SyncOfficial, CreatedTime/UpdatedTime int64, DeletedAt (soft-delete index + uniqueIndex priority2), NameRule int. Non-persistent: BoundChannels []BoundChannel, EnableGroups, QuotaTypes, MatchedModels. Belongs-to Vendor via VendorID int. | model/model_meta.go:24 |
| Vendor (vendors) | Model vendor/provider registry. Fields: Id, Name size:128 not null uniqueIndex:uk_vendor_name_delete_at priority1, Description text, Icon varchar(128), Status, CreatedTime/UpdatedTime int64, DeletedAt (soft-delete index + uniqueIndex priority2). Has-many Models via VendorID. 3NF design note in file. | model/vendor_meta.go:15 |
| PrefillGroup (prefill_groups) | Prefill prompt groups. Fields: Id, Name size:64 not null uniqueIndex:uk_prefill_name (partial where deleted_at IS NULL), Type size:32 index not null, Items JSONValue (custom type json), Description varchar(255), CreatedTime/UpdatedTime int64, DeletedAt (soft-delete index). | model/prefill_group.go:76 |
| Setup (setups) | Single-row install/version marker. Fields: ID uint primaryKey, Version varchar(50) not null, InitializedAt bigint not null. No timestamps/soft-delete. | model/setup.go:3 |
| TwoFA (two_fas) | Per-user TOTP 2FA settings. Fields: Id primaryKey, UserId unique;not null;index, Secret varchar(255) not null (hidden), IsEnabled bool, FailedAttempts int, LockedUntil *time.Time, LastUsedAt *time.Time, CreatedAt/UpdatedAt time.Time, DeletedAt (soft-delete index). One-to-one with User via unique UserId. | model/twofa.go:14 |
| TwoFABackupCode (two_fa_backup_codes) | 2FA backup code usage records. Fields: Id primaryKey, UserId not null;index, CodeHash varchar(255) not null (hidden), IsUsed bool, UsedAt *time.Time, CreatedAt time.Time, DeletedAt (soft-delete index). Belongs-to User via UserId. | model/twofa.go:28 |
| Checkin (checkins) | Daily check-in record. Fields: Id primaryKey autoIncrement, UserId not null (uniqueIndex idx_user_checkin_date), CheckinDate varchar(10) not null (same uniqueIndex, format YYYY-MM-DD), QuotaAwarded int not null, CreatedAt int64. Composite unique (user,date). Explicit TableName 'checkins'. Belongs-to User via UserId. | model/checkin.go:14 |
| SubscriptionPlan (subscription_plans) | Subscription product plan. Fields: Id, Title varchar(128) not null, Subtitle varchar(255), PriceAmount decimal(10,6) not null, Currency varchar(8) default USD, DurationUnit varchar(16)/DurationValue int/CustomSeconds bigint, Enabled bool, SortOrder int, AllowBalancePay *bool, AllowWalletOverflow *bool, StripePriceId/CreemProductId/WaffoPancakeProductId varchar(128), MaxPurchasePerUser int, UpgradeGroup/DowngradeGroup varchar(64), TotalAmount bigint, QuotaResetPeriod varchar(16)/QuotaResetCustomSeconds bigint, CreatedAt/UpdatedAt int64. Migrated separately (manual SQLite DDL; decimal migration for other DBs). Has-many SubscriptionOrder/UserSubscription. | model/subscription.go:146 |
| SubscriptionOrder (subscription_orders) | Subscription payment order. Fields: Id, UserId (index), PlanId (index), Money float64, TradeNo varchar(255) unique;index, PaymentMethod varchar(50), PaymentProvider varchar(50), Status, CreateTime/CompleteTime int64, ProviderPayload text. Belongs-to User and SubscriptionPlan via int ids. | model/subscription.go:214 |
| UserSubscription (user_subscriptions) | Active user subscription instance. Fields: Id, UserId (index + idx_user_sub_active priority1), PlanId (index), AmountTotal/AmountUsed bigint, StartTime/EndTime int64 (EndTime index + idx_user_sub_active priority3), Status varchar(32) (index + priority2: active/expired/cancelled), Source varchar(32), LastResetTime/NextResetTime int64 (NextResetTime index), UpgradeGroup/PrevUserGroup/DowngradeGroup varchar(64), AllowWalletOverflow bool, CreatedAt/UpdatedAt int64. Belongs-to User and SubscriptionPlan. Has-many SubscriptionPreConsumeRecord. | model/subscription.go:253 |
| SubscriptionPreConsumeRecord (subscription_pre_consume_records) | Idempotent subscription pre-consume ledger. Fields: Id, RequestId varchar(64) uniqueIndex, UserId (index), UserSubscriptionId (index), PreConsumed bigint, Status varchar(32) index (consumed/refunded), CreatedAt int64, UpdatedAt int64 (index). Belongs-to UserSubscription. | model/subscription.go:1238 |
| CustomOAuthProvider (custom_oauth_providers) | Custom OAuth provider config. Fields: Id primaryKey, Name varchar(64) not null, Slug varchar(64) uniqueIndex not null, Icon varchar(128), Enabled bool, ClientId varchar(256), ClientSecret varchar(512) hidden, AuthorizationEndpoint/TokenEndpoint/UserInfoEndpoint varchar(512), Scopes varchar(256), UserIdField/UsernameField/DisplayNameField/EmailField varchar(128) JSONPath mappings, WellKnown varchar(512), AuthStyle int, AccessPolicy text, AccessDeniedMessage varchar(512), CreatedAt/UpdatedAt time.Time. Has-many UserOAuthBinding. | model/custom_oauth_provider.go:40 |
| UserOAuthBinding (user_oauth_bindings) | OAuth account binding. Fields: Id primaryKey, UserId (uniqueIndex ux_user_provider), ProviderId (uniqueIndex ux_user_provider + ux_provider_userid), ProviderUserId varchar(256) (uniqueIndex ux_provider_userid), CreatedAt time.Time. Enforces one binding per user per provider and one OAuth account per provider via composite unique indexes. Belongs-to User and CustomOAuthProvider. | model/user_oauth_binding.go:11 |
| PerfMetric (perf_metrics) | Aggregated relay performance metrics. Fields: Id primaryKey, ModelName size:128 (uniqueIndex idx_perf_model_group_bucket priority1), Group size:64 (priority2), BucketTs int64 (priority3 + index idx_perf_bucket_ts), RequestCount/SuccessCount/TotalLatencyMs/TtftSumMs/TtftCount/OutputTokens/GenerationMs int64. Upsert via OnConflict (model_name,group,bucket_ts). Explicit TableName 'perf_metrics'. | model/perf_metric.go:11 |
| SystemInstance (system_instances) | Cluster node registration/heartbeat. Fields: NodeName varchar(128) primaryKey, Info text, StartedAt/LastSeenAt/CreatedAt/UpdatedAt int64 (index each). Status derived (online/stale, 90s threshold). | model/system_instance.go:17 |
| SystemTask (system_tasks) | Background job task. Fields: ID int64 primary_key, TaskID varchar(64) uniqueIndex, Type varchar(64) index, Status varchar(32) index (pending/running/succeeded/failed), ActiveKey *string varchar(64) uniqueIndex, Payload/State/Result/Error text, LockedBy varchar(128) index, CreatedAt/UpdatedAt int64 (index each). | model/system_task.go:28 |
| SystemTaskLock (system_task_locks) | Distributed task lock keyed by task type. Fields: Type varchar(64) primaryKey, TaskID varchar(64) index, LockedBy varchar(128) index, LockedUntil int64 index, UpdatedAt int64 index. One row per task type. | model/system_task.go:43 |
| CasbinRule (casbin_rule) | Casbin policy rule storage (RBAC/ABAC). Fields: Id uint primaryKey, Ptype + V0..V5 each size:100, all part of composite index idx_casbin_rule and uniqueIndex idx_casbin_rule_unique (priorities 1-7). Explicit TableName 'casbin_rule'. | model/casbin_rule.go:3 |
| AuthzRole (authz_roles) | Authorization role catalog. Fields: Id uint primaryKey, Key size:64 uniqueIndex not null, Name size:100 not null, Description text, BuiltIn bool, Enabled bool, Sort int, CreatedAt/UpdatedAt int64 (autoCreateTime/autoUpdateTime). Explicit TableName 'authz_roles'. | model/authz_role.go:3 |
| UserSession (user_sessions) | Server-side session control plane for access JWTs. Fields: SID varchar(64) primaryKey, UserID (idx_user_sessions_user_status_expiry priority1 + idx_user_sessions_user_created priority1), Version bigint, UserAuthVersion bigint, Status varchar(16) (idx priority2 + idx_user_sessions_status_revoked priority1), RefreshHash char(64) not null (HMAC, hidden), PreviousRefreshHash varchar(64), PreviousValidUntil bigint, LoginMethod varchar(32), IP varchar(64), UserAgent text, CreatedAt (autoCreateTime + idx priority2), LastActiveAt bigint, ExpiresAt bigint (idx priority3 + idx_user_sessions_expires_at), RevokedAt bigint (idx priority2), RevokedReason varchar(64). Explicit TableName 'user_sessions'. Belongs-to User. | model/user_session.go:42 |
| AuthFlow (auth_flows) | One-time short-lived auth ceremony state (OAuth/2FA/passkey/telegram). Fields: Id int64 primaryKey, TokenHash char(64) not null uniqueIndex (HMAC, never persists raw token), Purpose varchar(32) not null (idx_auth_flow_purpose_expiry), Provider varchar(64), Intent varchar(16), UserId (index), SessionId varchar(64) index, Payload text, CreatedAt time.Time, ExpiresAt time.Time not null (idx_auth_flow_purpose_expiry), ConsumedAt *time.Time index. Explicit TableName 'auth_flows'. | model/auth_flow.go:39 |
| ExternalIdentityClaim (external_identity_claims) | Durable external identity ownership record (telegram). Fields: Id int64 primaryKey, Provider varchar(32) not null (uniqueIndex idx_external_identity_subject priority1 + idx_external_identity_user priority1), Subject varchar(128) not null (priority2), UserId not null index (idx_external_identity_user priority2), CreatedAt time.Time. Two unique indexes enforce single-owner for both provider-subject and user-provider-slot. Explicit TableName 'external_identity_claims'. Belongs-to User. | model/external_identity_claim.go:21 |
| PasskeyCredential (passkey_credentials) | WebAuthn passkey credential. Fields: ID int primaryKey, UserID uniqueIndex not null, CredentialID varchar(512) uniqueIndex not null (base64), PublicKey text not null (base64), AttestationType varchar(255), AAGUID varchar(512), SignCount uint32, CloneWarning/UserPresent/UserVerified/BackupEligible/BackupState bool, Transports text, Attachment varchar(32), LastUsedAt *time.Time, CreatedAt/UpdatedAt time.Time, DeletedAt (soft-delete index). One passkey per user via unique UserID. Belongs-to User. | model/passkey.go:22 |
| Migration approach (model/main.go) | Dual DB: main (SQL_DSN) + optional log (LOG_SQL_DSN). Backends: SQLite (glebarez), MySQL, PostgreSQL; log DB also ClickHouse. migrateDB() runs DB.AutoMigrate over 33 entities then separately migrates SubscriptionPlan (SQLite via manual CREATE TABLE + per-column ALTER ADD loop because SQLite lacks ALTER COLUMN; other DBs decimal(10,6) price_amount). migrateDBFast() runs same AutoMigrate concurrently via goroutines + errChan. Column migrations: tokens.model_limits varchar->text (migrateTokenModelLimitsToText), subscription_plans.price_amount ->decimal (migrateSubscriptionPlanPriceAmount). Post-migrate seeding: InitializeUserAuthVersions, InitializeExternalIdentityClaims, createRootAccountIfNeed (root/123456), CheckSetup. ClickHouse logs use raw MergeTree DDL partitioned by toYYYYMM with optional TTL (LOG_SQL_CLICKHOUSE_TTL_DAYS). No versioned migration framework; GORM AutoMigrate + hand-written ALTERs. | model/main.go:253 |

## Channel Types & Providers

| Name | Detail | Evidence |
|---|---|---|
| ChannelTypeUnknown | Channel type 0 (reserved/unknown); no dedicated adapter, falls back to openai.Adaptor | constant/channel.go:4 |
| ChannelTypeOpenAI | Channel type 1; maps to APITypeOpenAI -> relay/channel/openai.Adaptor | constant/channel.go:5 |
| ChannelTypeMidjourney | Channel type 2; Midjourney proxy channel, no Go adaptor (handled via mjproxy/RelayFormatMjProxy) | constant/channel.go:6 |
| ChannelTypeAzure | Channel type 3; Azure OpenAI, uses openai.Adaptor fallback with Azure base URL/headers | constant/channel.go:7 |
| ChannelTypeOllama | Channel type 4; maps to APITypeOllama -> relay/channel/ollama.Adaptor | constant/channel.go:8 |
| ChannelTypeMidjourneyPlus | Channel type 5; Midjourney Plus proxy channel, handled via mjproxy path | constant/channel.go:9 |
| ChannelTypeOpenAIMax | Channel type 6; OpenAI-Max reseller, openai.Adaptor fallback (base https://api.openaimax.com) | constant/channel.go:10 |
| ChannelTypeOhMyGPT | Channel type 7; OhMyGPT reseller, openai.Adaptor fallback (base https://api.ohmygpt.com) | constant/channel.go:11 |
| ChannelTypeCustom | Channel type 8; custom OpenAI-compatible endpoint, openai.Adaptor fallback | constant/channel.go:12 |
| ChannelTypeAILS | Channel type 9; AILS reseller, openai.Adaptor fallback | constant/channel.go:13 |
| ChannelTypeAIProxy | Channel type 10; AIProxy reseller, openai.Adaptor fallback | constant/channel.go:14 |
| ChannelTypePaLM | Channel type 11; maps to APITypePaLM -> relay/channel/palm.Adaptor | constant/channel.go:15 |
| ChannelTypeAPI2GPT | Channel type 12; API2GPT reseller, openai.Adaptor fallback | constant/channel.go:16 |
| ChannelTypeAIGC2D | Channel type 13; AIGC2D reseller, openai.Adaptor fallback | constant/channel.go:17 |
| ChannelTypeAnthropic | Channel type 14; maps to APITypeAnthropic -> relay/channel/claude.Adaptor | constant/channel.go:18 |
| ChannelTypeBaidu | Channel type 15; maps to APITypeBaidu -> relay/channel/baidu.Adaptor | constant/channel.go:19 |
| ChannelTypeZhipu | Channel type 16; maps to APITypeZhipu -> relay/channel/zhipu.Adaptor | constant/channel.go:20 |
| ChannelTypeAli | Channel type 17; maps to APITypeAli -> relay/channel/ali.Adaptor | constant/channel.go:21 |
| ChannelTypeXunfei | Channel type 18; maps to APITypeXunfei -> relay/channel/xunfei.Adaptor | constant/channel.go:22 |
| ChannelType360 | Channel type 19; 360 AI; package relay/channel/ai360 holds only constants (no Adaptor), openai fallback | constant/channel.go:23 |
| ChannelTypeOpenRouter | Channel type 20; maps to APITypeOpenRouter -> relay/channel/openai.Adaptor | constant/channel.go:24 |
| ChannelTypeAIProxyLibrary | Channel type 21; maps to APITypeAIProxyLibrary, which has no GetAdaptor case (returns nil) | constant/channel.go:25 |
| ChannelTypeFastGPT | Channel type 22; FastGPT channel, openai.Adaptor fallback | constant/channel.go:26 |
| ChannelTypeTencent | Channel type 23; maps to APITypeTencent -> relay/channel/tencent.DispatchAdaptor | constant/channel.go:27 |
| ChannelTypeGemini | Channel type 24; maps to APITypeGemini -> relay/channel/gemini.Adaptor | constant/channel.go:28 |
| ChannelTypeMoonshot | Channel type 25; maps to APITypeMoonshot -> relay/channel/moonshot.Adaptor (Claude API) | constant/channel.go:29 |
| ChannelTypeZhipu_v4 | Channel type 26; maps to APITypeZhipuV4 -> relay/channel/zhipu_4v.Adaptor | constant/channel.go:30 |
| ChannelTypePerplexity | Channel type 27; maps to APITypePerplexity -> relay/channel/perplexity.Adaptor | constant/channel.go:31 |
| ChannelTypeLingYiWanWu | Channel type 31; 01.AI/LingYiWanWu; package relay/channel/lingyiwanwu holds only constants (no Adaptor), openai fallback | constant/channel.go:32 |
| ChannelTypeAws | Channel type 33; maps to APITypeAws -> relay/channel/aws.Adaptor | constant/channel.go:33 |
| ChannelTypeCohere | Channel type 34; maps to APITypeCohere -> relay/channel/cohere.Adaptor | constant/channel.go:34 |
| ChannelTypeMiniMax | Channel type 35; maps to APITypeMiniMax -> relay/channel/minimax.Adaptor | constant/channel.go:35 |
| ChannelTypeSunoAPI | Channel type 36; Suno music, task-based -> relay/channel/task/suno.TaskAdaptor | constant/channel.go:36 |
| ChannelTypeDify | Channel type 37; maps to APITypeDify -> relay/channel/dify.Adaptor | constant/channel.go:37 |
| ChannelTypeJina | Channel type 38; maps to APITypeJina -> relay/channel/jina.Adaptor | constant/channel.go:38 |
| ChannelCloudflare | Channel type 39; maps to APITypeCloudflare -> relay/channel/cloudflare.Adaptor | constant/channel.go:39 |
| ChannelTypeSiliconFlow | Channel type 40; maps to APITypeSiliconFlow -> relay/channel/siliconflow.Adaptor | constant/channel.go:40 |
| ChannelTypeVertexAi | Channel type 41; maps to APITypeVertexAi -> relay/channel/vertex.Adaptor | constant/channel.go:41 |
| ChannelTypeMistral | Channel type 42; maps to APITypeMistral -> relay/channel/mistral.Adaptor | constant/channel.go:42 |
| ChannelTypeDeepSeek | Channel type 43; maps to APITypeDeepSeek -> relay/channel/deepseek.Adaptor | constant/channel.go:43 |
| ChannelTypeMokaAI | Channel type 44; maps to APITypeMokaAI -> relay/channel/mokaai.Adaptor | constant/channel.go:44 |
| ChannelTypeVolcEngine | Channel type 45; maps to APITypeVolcEngine -> relay/channel/volcengine.Adaptor | constant/channel.go:45 |
| ChannelTypeBaiduV2 | Channel type 46; maps to APITypeBaiduV2 -> relay/channel/baidu_v2.Adaptor | constant/channel.go:46 |
| ChannelTypeXinference | Channel type 47; maps to APITypeXinference -> relay/channel/openai.Adaptor | constant/channel.go:47 |
| ChannelTypeXai | Channel type 48; maps to APITypeXai -> relay/channel/xai.Adaptor | constant/channel.go:48 |
| ChannelTypeCoze | Channel type 49; maps to APITypeCoze -> relay/channel/coze.Adaptor | constant/channel.go:49 |
| ChannelTypeKling | Channel type 50; Kling video, task-based -> relay/channel/task/kling.TaskAdaptor | constant/channel.go:50 |
| ChannelTypeJimeng | Channel type 51; maps to APITypeJimeng -> relay/channel/jimeng.Adaptor (also task/jimeng) | constant/channel.go:51 |
| ChannelTypeVidu | Channel type 52; Vidu video, task-based -> relay/channel/task/vidu.TaskAdaptor | constant/channel.go:52 |
| ChannelTypeSubmodel | Channel type 53; maps to APITypeSubmodel -> relay/channel/submodel.Adaptor | constant/channel.go:53 |
| ChannelTypeDoubaoVideo | Channel type 54; Doubao video, task-based -> relay/channel/task/doubao.TaskAdaptor | constant/channel.go:54 |
| ChannelTypeSora | Channel type 55; Sora video, task-based -> relay/channel/task/sora.TaskAdaptor | constant/channel.go:55 |
| ChannelTypeReplicate | Channel type 56; maps to APITypeReplicate -> relay/channel/replicate.Adaptor | constant/channel.go:56 |
| ChannelTypeCodex | Channel type 57; ChatGPT subscription (Codex); maps to APITypeCodex -> relay/channel/codex.Adaptor | constant/channel.go:57 |
| ChannelTypeAdvancedCustom | Channel type 58; maps to APITypeAdvancedCustom -> relay/channel/advancedcustom.Adaptor | constant/channel.go:58 |
| ChannelTypeSub2API | Channel type 59; maps to APITypeSub2API -> relay/channel/sub2api.Adaptor | constant/channel.go:59 |
| ChannelTypeNewAPI | Channel type 60; New API gateway-to-gateway; maps to APITypeNewAPI -> relay/channel/newapi.Adaptor | constant/channel.go:60 |
| ChannelTypeDummy | Channel type 61; sentinel for count only, not a real channel | constant/channel.go:61 |
| APITypeOpenAI | API type 0 (iota); adapter openai.Adaptor | constant/api_type.go:4 |
| APITypeAnthropic | API type 1; adapter claude.Adaptor | constant/api_type.go:5 |
| APITypePaLM | API type 2; adapter palm.Adaptor | constant/api_type.go:6 |
| APITypeBaidu | API type 3; adapter baidu.Adaptor | constant/api_type.go:7 |
| APITypeZhipu | API type 4; adapter zhipu.Adaptor | constant/api_type.go:8 |
| APITypeAli | API type 5; adapter ali.Adaptor | constant/api_type.go:9 |
| APITypeXunfei | API type 6; adapter xunfei.Adaptor | constant/api_type.go:10 |
| APITypeAIProxyLibrary | API type 7; no GetAdaptor case (returns nil) | constant/api_type.go:11 |
| APITypeTencent | API type 8; adapter tencent.DispatchAdaptor | constant/api_type.go:12 |
| APITypeGemini | API type 9; adapter gemini.Adaptor | constant/api_type.go:13 |
| APITypeZhipuV4 | API type 10; adapter zhipu_4v.Adaptor | constant/api_type.go:14 |
| APITypeOllama | API type 11; adapter ollama.Adaptor | constant/api_type.go:15 |
| APITypePerplexity | API type 12; adapter perplexity.Adaptor | constant/api_type.go:16 |
| APITypeAws | API type 13; adapter aws.Adaptor | constant/api_type.go:17 |
| APITypeCohere | API type 14; adapter cohere.Adaptor | constant/api_type.go:18 |
| APITypeDify | API type 15; adapter dify.Adaptor | constant/api_type.go:19 |
| APITypeJina | API type 16; adapter jina.Adaptor | constant/api_type.go:20 |
| APITypeCloudflare | API type 17; adapter cloudflare.Adaptor | constant/api_type.go:21 |
| APITypeSiliconFlow | API type 18; adapter siliconflow.Adaptor | constant/api_type.go:22 |
| APITypeVertexAi | API type 19; adapter vertex.Adaptor | constant/api_type.go:23 |
| APITypeMistral | API type 20; adapter mistral.Adaptor | constant/api_type.go:24 |
| APITypeDeepSeek | API type 21; adapter deepseek.Adaptor | constant/api_type.go:25 |
| APITypeMokaAI | API type 22; adapter mokaai.Adaptor | constant/api_type.go:26 |
| APITypeVolcEngine | API type 23; adapter volcengine.Adaptor | constant/api_type.go:27 |
| APITypeBaiduV2 | API type 24; adapter baidu_v2.Adaptor | constant/api_type.go:28 |
| APITypeOpenRouter | API type 25; adapter openai.Adaptor | constant/api_type.go:29 |
| APITypeXinference | API type 26; adapter openai.Adaptor | constant/api_type.go:30 |
| APITypeXai | API type 27; adapter xai.Adaptor | constant/api_type.go:31 |
| APITypeCoze | API type 28; adapter coze.Adaptor | constant/api_type.go:32 |
| APITypeJimeng | API type 29; adapter jimeng.Adaptor | constant/api_type.go:33 |
| APITypeMoonshot | API type 30; adapter moonshot.Adaptor | constant/api_type.go:34 |
| APITypeSubmodel | API type 31; adapter submodel.Adaptor | constant/api_type.go:35 |
| APITypeMiniMax | API type 32; adapter minimax.Adaptor | constant/api_type.go:36 |
| APITypeReplicate | API type 33; adapter replicate.Adaptor | constant/api_type.go:37 |
| APITypeCodex | API type 34; adapter codex.Adaptor | constant/api_type.go:38 |
| APITypeAdvancedCustom | API type 35; adapter advancedcustom.Adaptor | constant/api_type.go:39 |
| APITypeSub2API | API type 36; adapter sub2api.Adaptor | constant/api_type.go:40 |
| APITypeNewAPI | API type 37; adapter newapi.Adaptor | constant/api_type.go:41 |
| APITypeDummy | API type 38; sentinel for count only | constant/api_type.go:42 |
| adapter/advancedcustom | Provider adapter package relay/channel/advancedcustom (Adaptor for APITypeAdvancedCustom) | relay/channel/advancedcustom |
| adapter/ai360 | Provider package relay/channel/ai360 (constants only, no Adaptor; 360 AI OpenAI-compatible) | relay/channel/ai360/constants.go |
| adapter/ali | Provider adapter package relay/channel/ali (Alibaba DashScope) | relay/channel/ali |
| adapter/aws | Provider adapter package relay/channel/aws (Amazon Bedrock) | relay/channel/aws |
| adapter/baidu | Provider adapter package relay/channel/baidu (Baidu Qianfan/Wenxin) | relay/channel/baidu |
| adapter/baidu_v2 | Provider adapter package relay/channel/baidu_v2 (Baidu v2) | relay/channel/baidu_v2 |
| adapter/claude | Provider adapter package relay/channel/claude (Anthropic Claude) | relay/channel/claude |
| adapter/cloudflare | Provider adapter package relay/channel/cloudflare (Cloudflare Workers AI) | relay/channel/cloudflare |
| adapter/codex | Provider adapter package relay/channel/codex (ChatGPT subscription/Codex) | relay/channel/codex |
| adapter/cohere | Provider adapter package relay/channel/cohere | relay/channel/cohere |
| adapter/coze | Provider adapter package relay/channel/coze (ByteDance Coze) | relay/channel/coze |
| adapter/deepseek | Provider adapter package relay/channel/deepseek | relay/channel/deepseek |
| adapter/dify | Provider adapter package relay/channel/dify | relay/channel/dify |
| adapter/gemini | Provider adapter package relay/channel/gemini (Google Gemini) | relay/channel/gemini |
| adapter/jimeng | Provider adapter package relay/channel/jimeng (ByteDance Jimeng) | relay/channel/jimeng |
| adapter/jina | Provider adapter package relay/channel/jina (Jina AI, rerank/embeddings) | relay/channel/jina |
| adapter/lingyiwanwu | Provider package relay/channel/lingyiwanwu (constants only, no Adaptor; 01.AI) | relay/channel/lingyiwanwu/constrants.go |
| adapter/minimax | Provider adapter package relay/channel/minimax | relay/channel/minimax |
| adapter/mistral | Provider adapter package relay/channel/mistral | relay/channel/mistral |
| adapter/mokaai | Provider adapter package relay/channel/mokaai | relay/channel/mokaai |
| adapter/moonshot | Provider adapter package relay/channel/moonshot (Moonshot/Kimi, Claude API) | relay/channel/moonshot |
| adapter/newapi | Provider adapter package relay/channel/newapi (New API gateway-to-gateway) | relay/channel/newapi |
| adapter/ollama | Provider adapter package relay/channel/ollama | relay/channel/ollama |
| adapter/openai | Provider adapter package relay/channel/openai (also fallback for OpenAI-compatible channels) | relay/channel/openai |
| adapter/openrouter | Provider package relay/channel/openrouter (constants/dto only; routed through openai.Adaptor) | relay/channel/openrouter/constant.go |
| adapter/palm | Provider adapter package relay/channel/palm (Google PaLM) | relay/channel/palm |
| adapter/perplexity | Provider adapter package relay/channel/perplexity | relay/channel/perplexity |
| adapter/replicate | Provider adapter package relay/channel/replicate | relay/channel/replicate |
| adapter/siliconflow | Provider adapter package relay/channel/siliconflow | relay/channel/siliconflow |
| adapter/sub2api | Provider adapter package relay/channel/sub2api | relay/channel/sub2api |
| adapter/submodel | Provider adapter package relay/channel/submodel (LLM SubModel) | relay/channel/submodel |
| adapter/tencent | Provider adapter package relay/channel/tencent (Tencent Hunyuan, DispatchAdaptor) | relay/channel/tencent |
| adapter/vertex | Provider adapter package relay/channel/vertex (Google Vertex AI) | relay/channel/vertex |
| adapter/volcengine | Provider adapter package relay/channel/volcengine (ByteDance Volcano Engine/Ark) | relay/channel/volcengine |
| adapter/xai | Provider adapter package relay/channel/xai (x.AI Grok) | relay/channel/xai |
| adapter/xinference | Provider package relay/channel/xinference (constants/dto only; routed through openai.Adaptor) | relay/channel/xinference/constant.go |
| adapter/xunfei | Provider adapter package relay/channel/xunfei (iFlytek Spark) | relay/channel/xunfei |
| adapter/zhipu | Provider adapter package relay/channel/zhipu (Zhipu BigModel GLM) | relay/channel/zhipu |
| adapter/zhipu_4v | Provider adapter package relay/channel/zhipu_4v (Zhipu v4) | relay/channel/zhipu_4v |
| adapter/task | Task adaptor parent package relay/channel/task defining TaskAdaptor sub-packages | relay/channel/task |
| adapter/task/ali | Task adaptor relay/channel/task/ali (Ali video/task) | relay/channel/task/ali |
| adapter/task/doubao | Task adaptor relay/channel/task/doubao (Doubao video) | relay/channel/task/doubao |
| adapter/task/gemini | Task adaptor relay/channel/task/gemini (Gemini video/task) | relay/channel/task/gemini |
| adapter/task/hailuo | Task adaptor relay/channel/task/hailuo (MiniMax Hailuo video) | relay/channel/task/hailuo |
| adapter/task/jimeng | Task adaptor relay/channel/task/jimeng (Jimeng task) | relay/channel/task/jimeng |
| adapter/task/kling | Task adaptor relay/channel/task/kling (Kling video) | relay/channel/task/kling |
| adapter/task/sora | Task adaptor relay/channel/task/sora (Sora video) | relay/channel/task/sora |
| adapter/task/suno | Task adaptor relay/channel/task/suno (Suno music) | relay/channel/task/suno |
| adapter/task/taskcommon | Shared task helpers package relay/channel/task/taskcommon | relay/channel/task/taskcommon |
| adapter/task/vertex | Task adaptor relay/channel/task/vertex (Vertex AI video/task) | relay/channel/task/vertex |
| adapter/task/vidu | Task adaptor relay/channel/task/vidu (Vidu video) | relay/channel/task/vidu |
| RelayFormatOpenAI | RelayFormat string "openai" | relaykit/types/relay_format.go:6 |
| RelayFormatClaude | RelayFormat string "claude" | relaykit/types/relay_format.go:7 |
| RelayFormatGemini | RelayFormat string "gemini" | relaykit/types/relay_format.go:8 |
| RelayFormatOpenAIResponses | RelayFormat string "openai_responses" | relaykit/types/relay_format.go:9 |
| RelayFormatOpenAIResponsesCompaction | RelayFormat string "openai_responses_compaction" | relaykit/types/relay_format.go:10 |
| RelayFormatOpenAIAlphaSearch | RelayFormat string "openai_alpha_search" | relaykit/types/relay_format.go:11 |
| RelayFormatOpenAIAudio | RelayFormat string "openai_audio" | relaykit/types/relay_format.go:12 |
| RelayFormatOpenAIImage | RelayFormat string "openai_image" | relaykit/types/relay_format.go:13 |
| RelayFormatOpenAIRealtime | RelayFormat string "openai_realtime" | relaykit/types/relay_format.go:14 |
| RelayFormatRerank | RelayFormat string "rerank" | relaykit/types/relay_format.go:15 |
| RelayFormatEmbedding | RelayFormat string "embedding" | relaykit/types/relay_format.go:16 |
| RelayFormatTask | RelayFormat string "task" | relaykit/types/relay_format.go:18 |
| RelayFormatMjProxy | RelayFormat string "mj_proxy" | relaykit/types/relay_format.go:19 |
| EndpointTypeOpenAI | EndpointType string "openai" | relaykit/types/endpoint_type.go:9 |
| EndpointTypeOpenAIResponse | EndpointType string "openai-response" | relaykit/types/endpoint_type.go:10 |
| EndpointTypeOpenAIResponseCompact | EndpointType string "openai-response-compact" | relaykit/types/endpoint_type.go:11 |
| EndpointTypeOpenAIAlphaSearch | EndpointType string "openai-alpha-search" | relaykit/types/endpoint_type.go:12 |
| EndpointTypeAnthropic | EndpointType string "anthropic" | relaykit/types/endpoint_type.go:13 |
| EndpointTypeGemini | EndpointType string "gemini" | relaykit/types/endpoint_type.go:14 |
| EndpointTypeJinaRerank | EndpointType string "jina-rerank" | relaykit/types/endpoint_type.go:15 |
| EndpointTypeImageGeneration | EndpointType string "image-generation" | relaykit/types/endpoint_type.go:16 |
| EndpointTypeEmbeddings | EndpointType string "embeddings" | relaykit/types/endpoint_type.go:17 |
| EndpointTypeOpenAIVideo | EndpointType string "openai-video" | relaykit/types/endpoint_type.go:18 |
| RelayModeUnknown | RelayMode 0 (iota) | relay/constant/relay_mode.go:8 |
| RelayModeChatCompletions | RelayMode 1 | relay/constant/relay_mode.go:9 |
| RelayModeCompletions | RelayMode 2 | relay/constant/relay_mode.go:10 |
| RelayModeEmbeddings | RelayMode 3 | relay/constant/relay_mode.go:11 |
| RelayModeModerations | RelayMode 4 | relay/constant/relay_mode.go:12 |
| RelayModeImagesGenerations | RelayMode 5 | relay/constant/relay_mode.go:13 |
| RelayModeImagesEdits | RelayMode 6 | relay/constant/relay_mode.go:14 |
| RelayModeEdits | RelayMode 7 | relay/constant/relay_mode.go:15 |
| RelayModeMidjourneyImagine | RelayMode 8 | relay/constant/relay_mode.go:17 |
| RelayModeMidjourneyDescribe | RelayMode 9 | relay/constant/relay_mode.go:18 |
| RelayModeMidjourneyBlend | RelayMode 10 | relay/constant/relay_mode.go:19 |
| RelayModeMidjourneyChange | RelayMode 11 | relay/constant/relay_mode.go:20 |
| RelayModeMidjourneySimpleChange | RelayMode 12 | relay/constant/relay_mode.go:21 |
| RelayModeMidjourneyNotify | RelayMode 13 | relay/constant/relay_mode.go:22 |
| RelayModeMidjourneyTaskFetch | RelayMode 14 | relay/constant/relay_mode.go:23 |
| RelayModeMidjourneyTaskImageSeed | RelayMode 15 | relay/constant/relay_mode.go:24 |
| RelayModeMidjourneyTaskFetchByCondition | RelayMode 16 | relay/constant/relay_mode.go:25 |
| RelayModeMidjourneyAction | RelayMode 17 | relay/constant/relay_mode.go:26 |
| RelayModeMidjourneyModal | RelayMode 18 | relay/constant/relay_mode.go:27 |
| RelayModeMidjourneyShorten | RelayMode 19 | relay/constant/relay_mode.go:28 |
| RelayModeSwapFace | RelayMode 20 | relay/constant/relay_mode.go:29 |
| RelayModeMidjourneyUpload | RelayMode 21 | relay/constant/relay_mode.go:30 |
| RelayModeMidjourneyVideo | RelayMode 22 | relay/constant/relay_mode.go:31 |
| RelayModeMidjourneyEdits | RelayMode 23 | relay/constant/relay_mode.go:32 |
| RelayModeAudioSpeech | RelayMode 24 (TTS) | relay/constant/relay_mode.go:34 |
| RelayModeAudioTranscription | RelayMode 25 (Whisper) | relay/constant/relay_mode.go:35 |
| RelayModeAudioTranslation | RelayMode 26 (Whisper) | relay/constant/relay_mode.go:36 |
| RelayModeSunoFetch | RelayMode 27 | relay/constant/relay_mode.go:38 |
| RelayModeSunoFetchByID | RelayMode 28 | relay/constant/relay_mode.go:39 |
| RelayModeSunoSubmit | RelayMode 29 | relay/constant/relay_mode.go:40 |
| RelayModeVideoFetchByID | RelayMode 30 | relay/constant/relay_mode.go:42 |
| RelayModeVideoSubmit | RelayMode 31 | relay/constant/relay_mode.go:43 |
| RelayModeRerank | RelayMode 32 | relay/constant/relay_mode.go:45 |
| RelayModeResponses | RelayMode 33 | relay/constant/relay_mode.go:47 |
| RelayModeRealtime | RelayMode 34 | relay/constant/relay_mode.go:49 |
| RelayModeGemini | RelayMode 35 | relay/constant/relay_mode.go:51 |
| RelayModeResponsesCompact | RelayMode 36 | relay/constant/relay_mode.go:53 |
| RelayModeAlphaSearch | RelayMode 37 | relay/constant/relay_mode.go:55 |
| TaskPlatformSuno | TaskPlatform string "suno" | constant/task.go:6 |
| TaskPlatformMidjourney | TaskPlatform string "mj" | constant/task.go:7 |

## Relay Formats, Modes & DTOs

| Name | Detail | Evidence |
|---|---|---|
| RelayFormat constants | 13 string constants on type RelayFormat: RelayFormatOpenAI="openai", RelayFormatClaude="claude", RelayFormatGemini="gemini", RelayFormatOpenAIResponses="openai_responses", RelayFormatOpenAIResponsesCompaction="openai_responses_compaction", RelayFormatOpenAIAlphaSearch="openai_alpha_search", RelayFormatOpenAIAudio="openai_audio", RelayFormatOpenAIImage="openai_image", RelayFormatOpenAIRealtime="openai_realtime", RelayFormatRerank="rerank", RelayFormatEmbedding="embedding", RelayFormatTask="task", RelayFormatMjProxy="mj_proxy" | relaykit/types/relay_format.go:3-19 |
| RelayMode constants | 38 iota constants (RelayModeUnknown=0): ChatCompletions, Completions, Embeddings, Moderations, ImagesGenerations, ImagesEdits, Edits, MidjourneyImagine, MidjourneyDescribe, MidjourneyBlend, MidjourneyChange, MidjourneySimpleChange, MidjourneyNotify, MidjourneyTaskFetch, MidjourneyTaskImageSeed, MidjourneyTaskFetchByCondition, MidjourneyAction, MidjourneyModal, MidjourneyShorten, SwapFace, MidjourneyUpload, MidjourneyVideo, MidjourneyEdits, AudioSpeech, AudioTranscription, AudioTranslation, SunoFetch, SunoFetchByID, SunoSubmit, VideoFetchByID, VideoSubmit, Rerank, Responses, Realtime, Gemini, ResponsesCompact, AlphaSearch | relay/constant/relay_mode.go:9-56 |
| GeneralOpenAIRequest | OpenAI Chat Completions request DTO. Fields (json tag): model, messages, prompt, prefix, suffix, stream, stream_options, max_tokens, max_completion_tokens, reasoning_effort, verbosity, temperature, top_p, top_k, stop, n, input, instruction, size, functions, frequency_penalty, presence_penalty, response_format, encoding_format, seed, parallel_tool_calls, tools, tool_choice, function_call, user, service_tier, logprobs, top_logprobs, dimensions, modalities, audio, safety_identifier, store, prompt_cache_key, prompt_cache_retention, logit_bias, metadata, prediction, extra_body, search_parameters, web_search_options, usage, reasoning, vl_high_resolution_images, enable_thinking, thinking_budget, chat_template_kwargs, enable_search, think, web_search, thinking(THINKING), search_domain_filter, search_recency_filter, return_images, return_related_questions, search_mode, reasoning_split | relaykit/dto/openai_request.go:28-109 |
| Message | OpenAI chat message. Fields: role, content(any), name, prefix, reasoning_content, reasoning, tool_calls, tool_call_id | relaykit/dto/openai_request.go:303-314 |
| MediaContent | Multimodal content part. Fields: type, text, image_url, input_audio, file, video_url, cache_control | relaykit/dto/openai_request.go:316-325 |
| MessageImageUrl / MessageInputAudio / MessageFile / MessageVideoUrl | Media payload structs. MessageImageUrl{url,detail,mime_type}; MessageInputAudio{data(base64),format}; MessageFile{filename,file_data,file_id}; MessageVideoUrl{url} | relaykit/dto/openai_request.go:426-449 |
| ContentType constants | ContentTypeText="text", ContentTypeImageURL="image_url", ContentTypeInputAudio="input_audio", ContentTypeFile="file", ContentTypeVideoUrl="video_url" | relaykit/dto/openai_request.go:451-458 |
| ToolCallRequest / FunctionRequest | ToolCallRequest{id,type,function,custom}; FunctionRequest{description,name,parameters,arguments} | relaykit/dto/openai_request.go:255-267 |
| StreamOptions | Streaming options: include_usage, include_obfuscation | relaykit/dto/openai_request.go:269-274 |
| ResponseFormat / FormatJsonSchema | ResponseFormat{type,json_schema}; FormatJsonSchema{description,name,schema,strict} | relaykit/dto/openai_request.go:14-24 |
| WebSearchOptions | Claude-style web search options on OpenAI request: search_context_size, user_location | relaykit/dto/openai_request.go:850-853 |
| Usage | Unified token usage. Fields: prompt_tokens, completion_tokens, total_tokens, prompt_cache_hit_tokens, usage_semantic, usage_source, billing_usage, prompt_tokens_details, completion_tokens_details, input_tokens, output_tokens, input_tokens_details, claude_cache_creation_5_m_tokens, claude_cache_creation_1_h_tokens, cost | relaykit/dto/openai_response.go:223-244 |
| InputTokenDetails | Prompt/input token breakdown: cached_tokens, cached_creation_tokens, cache_write_tokens, text_tokens, audio_tokens, image_tokens | relaykit/dto/openai_response.go:256-267 |
| OutputTokenDetails | Completion/output token breakdown: text_tokens, audio_tokens, image_tokens, reasoning_tokens | relaykit/dto/openai_response.go:286-291 |
| ChatCompletionsStreamResponse | Chat stream chunk. Fields: id, object, created, model, system_fingerprint, choices, usage | relaykit/dto/openai_response.go:142-150 |
| ChatCompletionsStreamResponseChoice | Stream choice: delta, logprobs, finish_reason, index | relaykit/dto/openai_response.go:81-86 |
| ChatCompletionsStreamResponseChoiceDelta | Stream delta: content, reasoning_content, reasoning, role, tool_calls | relaykit/dto/openai_response.go:88-94 |
| ToolCallResponse / FunctionResponse | ToolCallResponse{index,id,type,function}; FunctionResponse{description,name,parameters,arguments} | relaykit/dto/openai_response.go:122-140 |
| TextResponse / OpenAITextResponse / OpenAITextResponseChoice | Non-stream text completion response. OpenAITextResponse{id,model,object,created,choices,error,usage}; choice{index,message,finish_reason} | relaykit/dto/openai_response.go:25-48 |
| OpenAIEmbeddingResponse / OpenAIEmbeddingResponseItem | Embedding response: object, data[index/object/embedding], model, usage | relaykit/dto/openai_response.go:55-66 |
| CompletionsStreamResponse | Legacy completions stream: choices[{text,finish_reason}] | relaykit/dto/openai_response.go:216-221 |
| ImageRequest | Image generation request: model, prompt, n, size, quality, response_format, style, user, extra_fields, background, moderation, output_format, output_compression, partial_images, stream, images, mask, input_fidelity, watermark, watermark_enabled, user_id, image | relaykit/dto/openai_image.go:17-43 |
| ImageResponse / ImageData | ImageResponse{data,created,metadata}; ImageData{url,b64_json,revised_prompt} | relaykit/dto/openai_image.go:183-192 |
| AudioRequest | TTS request: model, input, voice, instructions, response_format, speed, stream_format, metadata, task_type, language, ref_audio, ref_text, x_vector_only_mode, max_new_tokens, initial_codec_chunk_frames | relaykit/dto/audio.go:11-30 |
| WhisperVerboseJSONResponse / Segment / AudioResponse | Whisper transcription response: task, language, duration, text, segments[id,seek,start,end,text,tokens,temperature,avg_logprob,compression_ratio,no_speech_prob]; AudioResponse{text} | relaykit/dto/audio.go:53-76 |
| EmbeddingRequest / EmbeddingResponse / EmbeddingOptions | EmbeddingRequest{model,input,encoding_format,dimensions,user,seed,temperature,top_p,frequency_penalty,presence_penalty}; EmbeddingResponse{object,data,model,usage}; EmbeddingOptions{seed,temperature,top_k,top_p,frequency_penalty,presence_penalty,num_predict,num_ctx} | relaykit/dto/embedding.go:10-87 |
| RerankRequest / RerankResponse | RerankRequest{documents,query,model,top_n,return_documents,max_chunk_per_doc,overlap_tokens}; RerankResponse{results[{document,index,relevance_score}],usage} | relaykit/dto/rerank.go:11-67 |
| OpenAIResponsesRequest | Responses API request. Fields: model, input, include, conversation, context_management, instructions, max_output_tokens, top_logprobs, metadata, moderation, parallel_tool_calls, frequency_penalty, presence_penalty, previous_response_id, reasoning, service_tier, store, prompt_cache_key, prompt_cache_options, prompt_cache_retention, safety_identifier, stream, stream_options, temperature, text, tool_choice, tools, top_p, truncation, user, max_tool_calls, prompt, client_metadata, enable_thinking, thinking_budget, preset | relaykit/dto/openai_request.go:856-908 |
| OpenAIResponsesResponse | Responses API response. Fields: id, object, created_at, status, error, incomplete_details, instructions, max_output_tokens, model, output, parallel_tool_calls, previous_response_id, reasoning, store, temperature, tool_choice, tools, top_p, truncation, usage, user, metadata | relaykit/dto/openai_response.go:293-316 |
| ResponsesOutput | Responses output item: type, id, status, role, content, quality, size, result, call_id, name, arguments | relaykit/dto/openai_response.go:327-339 |
| ResponsesOutputContent | Responses output content part: type, text, annotations | relaykit/dto/openai_response.go:354-358 |
| ResponsesStreamResponse | Responses stream event: type, response, delta, item, output_index, content_index, summary_index, item_id, part | relaykit/dto/openai_response.go:386-398 |
| Reasoning | Responses reasoning config: effort, summary, mode, context | relaykit/dto/openai_request.go:995-1000 |
| Input / MediaInput | Responses input item: Input{type,role,content}; MediaInput{type,text,file_url,image_url,detail}; supported content types input_text/input_image/input_file | relaykit/dto/openai_request.go:1002-1014 |
| IncompleteDetails / ResponsesReasoningSummaryPart | IncompleteDetails{reason}; ResponsesReasoningSummaryPart{type,text} | relaykit/dto/openai_response.go:323-363 |
| ClaudeRequest | Claude Messages request. Fields: model, prompt, system, messages, cache_control, inference_geo, max_tokens, max_tokens_to_sample, stop_sequences, temperature, top_p, top_k, stream, tools, context_management, output_config, output_format, container, tool_choice, thinking, mcp_servers, metadata, speed, service_tier | relaykit/dto/claude.go:205-236 |
| ClaudeMessage / ClaudeMediaMessage | ClaudeMessage{role,content}; ClaudeMediaMessage{type,text,model,source,usage,stop_reason,partial_json,role,thinking,signature,delta,cache_control,id,name,input,content,tool_use_id} | relaykit/dto/claude.go:17-124 |
| ClaudeMessageSource | Claude media source: type, media_type, data, url | relaykit/dto/claude.go:114-119 |
| ClaudeResponse | Claude Messages response: id, type, role, content, completion, stop_reason, model, error, usage, index, content_block, delta, message | relaykit/dto/claude.go:491-505 |
| ClaudeUsage / ClaudeCacheCreationUsage / ClaudeServerToolUse | ClaudeUsage{input_tokens,cache_creation_input_tokens,cache_read_input_tokens,output_tokens,cache_creation,claude_cache_creation_5_m_tokens,claude_cache_creation_1_h_tokens,server_tool_use,billing_usage}; ClaudeCacheCreationUsage{ephemeral_5m_input_tokens,ephemeral_1h_input_tokens}; ClaudeServerToolUse{web_search_requests} | relaykit/dto/claude.go:556-600 |
| Tool / InputSchema | Claude tool: Tool{name,description,input_schema}; InputSchema{type,properties,required} | relaykit/dto/claude.go:172-182 |
| ClaudeWebSearchTool / ClaudeWebSearchUserLocation / ClaudeToolChoice | ClaudeWebSearchTool{type,name,max_uses,user_location}; ClaudeWebSearchUserLocation{type,timezone,country,region,city}; ClaudeToolChoice{type,name,disable_parallel_tool_use} | relaykit/dto/claude.go:184-203 |
| Thinking / ClaudeMetadata | Claude Thinking{type,budget_tokens,display}; ClaudeMetadata{user_id} | relaykit/dto/claude.go:13-15,447-455 |
| GeminiChatRequest | Gemini GenerateContent request. Fields: requests(batch), contents, safetySettings, generationConfig, tools, toolConfig, systemInstruction, cachedContent | relaykit/dto/gemini.go:12-21 |
| GeminiChatContent / GeminiPart | GeminiChatContent{role,parts}; GeminiPart{text,thought,inlineData,functionCall,thoughtSignature,functionResponse,mediaResolution,videoMetadata,fileData,executableCode,codeExecutionResult} | relaykit/dto/gemini.go:270-315 |
| GeminiInlineData / GeminiFileData | GeminiInlineData{mimeType,data}; GeminiFileData{mimeType,fileUri} | relaykit/dto/gemini.go:205-208,265-268 |
| FunctionCall / GeminiFunctionResponse / GeminiPartExecutableCode / GeminiPartCodeExecutionResult | FunctionCall{name,args}; GeminiFunctionResponse{name,response,willContinue,scheduling,parts,id}; GeminiPartExecutableCode{language,code}; GeminiPartCodeExecutionResult{outcome,output} | relaykit/dto/gemini.go:241-263 |
| GeminiChatGenerationConfig | Gemini generation config: temperature, topP, topK, maxOutputTokens, candidateCount, stopSequences, responseMimeType, responseSchema, responseJsonSchema, presencePenalty, frequencyPenalty, responseLogprobs, logprobs, enableEnhancedCivicAnswers, mediaResolution, seed, responseModalities, thinkingConfig, speechConfig, imageConfig | relaykit/dto/gemini.go:330-351 |
| GeminiThinkingConfig | Gemini thinking: includeThoughts, thinkingBudget, thinkingLevel | relaykit/dto/gemini.go:163-168 |
| ToolConfig / FunctionCallingConfig / RetrievalConfig / LatLng | ToolConfig{functionCallingConfig,retrievalConfig,includeServerSideToolInvocations}; FunctionCallingConfig{mode,allowedFunctionNames}; RetrievalConfig{latLng,languageCode}; LatLng{latitude,longitude} | relaykit/dto/gemini.go:44-64 |
| GeminiChatTool | Gemini tool: googleSearch, googleSearchRetrieval, codeExecution, functionDeclarations, urlContext | relaykit/dto/gemini.go:322-328 |
| GeminiChatSafetySettings / GeminiChatSafetyRating / GeminiChatPromptFeedback | SafetySettings{category,threshold}; SafetyRating{category,probability}; PromptFeedback{safetyRatings,blockReason} | relaykit/dto/gemini.go:317-320,453-461 |
| GeminiChatResponse / GeminiChatCandidate / GeminiGroundingMetadata | GeminiChatResponse{candidates,promptFeedback,usageMetadata}; GeminiChatCandidate{content,finishReason,index,safetyRatings,groundingMetadata}; GeminiGroundingMetadata{webSearchQueries} | relaykit/dto/gemini.go:441-468 |
| GeminiUsageMetadata / GeminiPromptTokensDetails | GeminiUsageMetadata{promptTokenCount,toolUsePromptTokenCount,candidatesTokenCount,totalTokenCount,thoughtsTokenCount,cachedContentTokenCount,promptTokensDetails,toolUsePromptTokensDetails,candidatesTokensDetails,billing_usage}; GeminiPromptTokensDetails{modality,tokenCount} | relaykit/dto/gemini.go:506-522 |
| GeminiImageRequest / GeminiImageResponse / GeminiImageParameters | Imagen: GeminiImageRequest{instances[{prompt}],parameters}; GeminiImageParameters{sampleCount,aspectRatio,personGeneration,imageSize}; GeminiImageResponse{predictions[{mimeType,bytesBase64Encoded,raiFilteredReason,safetyAttributes}]} | relaykit/dto/gemini.go:525-550 |
| GeminiEmbeddingRequest / GeminiEmbeddingResponse / ContentEmbedding | GeminiEmbeddingRequest{model,content,taskType,title,outputDimensionality}; GeminiEmbeddingResponse{embedding{values[]}} | relaykit/dto/gemini.go:553-626 |
| OpenAIError / ClaudeError | Error DTOs: OpenAIError{message,type,param,code,metadata}; ClaudeError{type,message} | relaykit/types/error.go:13-24 |
| RealtimeEvent / RealtimeSession / RealtimeItem | Realtime DTO: RealtimeEvent{event_id,type,session,item,error,response,delta,audio}; RealtimeSession{modalities,instructions,voice,input_audio_format,output_audio_format,input_audio_transcription,turn_detection,tools,tool_choice,temperature}; RealtimeItem{id,type,status,role,content,name,tool_calls,call_id} | relaykit/dto/realtime.go:24-88 |

## Configuration & Settings

| Name | Detail | Evidence |
|---|---|---|
| PORT | Default 3000 (via --port flag). HTTP server listening port. | main.go |
| FRONTEND_BASE_URL | No default. Frontend base URL used by router to serve/configure frontend. | router/main.go |
| GIN_MODE | If set to "debug", gin runs in debug mode; otherwise release mode. | main.go |
| ENABLE_PPROF | "true" enables net/http/pprof listener on 0.0.0.0:8005 and system monitor. | main.go |
| DEBUG | "true" enables debug mode (verbose SQL/logging). Default false. | common/init.go |
| PYROSCOPE_URL | Default empty (disables pyroscope). Pyroscope server address. | common/pyro.go |
| PYROSCOPE_APP_NAME | Default "new-api". Pyroscope application name. | common/pyro.go |
| PYROSCOPE_BASIC_AUTH_USER | Default empty. Pyroscope basic auth username. | common/pyro.go |
| PYROSCOPE_BASIC_AUTH_PASSWORD | Default empty. Pyroscope basic auth password. | common/pyro.go |
| PYROSCOPE_MUTEX_RATE | Default 5. Go runtime mutex profile fraction. | common/pyro.go |
| PYROSCOPE_BLOCK_RATE | Default 5. Go runtime block profile rate. | common/pyro.go |
| HOSTNAME | Default "new-api". Hostname tag passed to pyroscope. | common/pyro.go |
| ERROR_LOG_ENABLED | Default false. Whether to record error logs. | common/init.go |
| SQL_DSN | Default empty (falls back to SQLite). Main database connection string (mysql/postgres/postgresql/sqlite). | model/main.go |
| LOG_SQL_DSN | Default empty (reuses main DB). Log database DSN; supports clickhouse:// for log-only. | model/main.go |
| LOG_SQL_CLICKHOUSE_TTL_DAYS | Default 0 (no auto-delete). ClickHouse log retention days. | model/main.go |
| SQLITE_PATH | Default "one-api.db?_busy_timeout=30000". SQLite database path. | common/database.go |
| SQL_MAX_IDLE_CONNS | Default 100. Database maximum idle connections. | model/main.go |
| SQL_MAX_OPEN_CONNS | Default 1000. Database maximum open connections. | model/main.go |
| SQL_MAX_LIFETIME | Default 60 (seconds). Database connection max lifetime. | model/main.go |
| SQL_SLOW_THRESHOLD_MS | Default 200 (ms). Slow query log threshold; 0 disables. | .env.example |
| REDIS_CONN_STRING | Default empty (disables Redis). Redis URL; if empty RedisEnabled=false. | common/redis.go |
| REDIS_POOL_SIZE | Default 10. Redis connection pool size. | common/redis.go |
| SYNC_FREQUENCY | Default 60 (seconds). Option map / channel cache sync frequency. | common/init.go |
| MEMORY_CACHE_ENABLED | "true" enables in-memory channel cache. Default false. | common/init.go |
| CHANNEL_UPDATE_FREQUENCY | Default empty (disabled). Channel auto-update frequency in seconds. | main.go |
| BATCH_UPDATE_ENABLED | "true" enables batch channel updater. Default false. | main.go |
| BATCH_UPDATE_INTERVAL | Default 5 (seconds). Batch update interval. | common/init.go |
| POLLING_INTERVAL | Default 0 (seconds). Request polling interval; 0 disables. | common/init.go |
| UPDATE_TASK | Default true. Whether the scheduled update task is enabled. | common/init.go |
| CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_ENABLED | Default true. Whether channel upstream model update task is enabled. | controller/system_task_handlers.go |
| RELAY_TIMEOUT | Default 0 (unlimited). Timeout for all relay requests in seconds. | common/init.go |
| RELAY_IDLE_CONN_TIMEOUT | Default 90 (seconds). Relay HTTP client idle keep-alive timeout; 0 = Go stdlib. | common/init.go |
| RELAY_MAX_IDLE_CONNS | Default 500. Relay HTTP transport max idle connections. | common/init.go |
| RELAY_MAX_IDLE_CONNS_PER_HOST | Default 100. Relay HTTP transport max idle connections per host. | common/init.go |
| STREAMING_TIMEOUT | Default 300 (seconds). Streaming mode no-response timeout. | common/init.go |
| SHUTDOWN_TIMEOUT_SECONDS | Default 120 (seconds). Graceful HTTP shutdown timeout. | main.go |
| TLS_INSECURE_SKIP_VERIFY | Default false. Skip TLS certificate verification for outbound requests. | common/init.go |
| TRUSTED_PROXIES | Default trusts 127.0.0.0/8, ::1, RFC1918, fc00::/7 (with warning); "none" = strict; explicit CIDR list replaces default. | .env.example |
| GEMINI_VISION_MAX_IMAGE_NUM | Default 16. Max image count for Gemini vision (documented in .env.example only). | .env.example |
| SESSION_SECRET | Default random UUID; literal "random_string" rejected at startup. Session signing secret. | common/init.go |
| CRYPTO_SECRET | Default falls back to SESSION_SECRET. Crypto secret for encryption. | common/init.go |
| SESSION_COOKIE_SECURE | Default false. true enables Secure cookie + strict refresh/logout OriginGuard (requires trusted URLs). | common/session_cookie.go |
| SESSION_COOKIE_TRUSTED_URL | Default empty. Comma-separated exact HTTPS origins (requires SESSION_COOKIE_SECURE=true). | common/session_cookie.go |
| USER_SESSION_ACTIVE_LIMIT | Default 50. Max active login sessions per user. | common/init.go |
| USER_SESSION_ISSUANCE_LIMIT | Default 100. Max sessions issued per user within window. | common/init.go |
| USER_SESSION_ISSUANCE_WINDOW_SECONDS | Default 86400. Session issuance counting window (clamped to revoked retention). | common/init.go |
| USER_SESSION_REVOKED_RETENTION_DAYS | Default 7. Revoked session audit retention days. | common/init.go |
| USER_SESSION_HOURLY_ALERT_THRESHOLD | Default 5000. Global hourly session issuance alert threshold (never rejects login). | common/init.go |
| GENERATE_DEFAULT_TOKEN | Default false. Whether to generate an initial access token. | common/init.go |
| COHERE_SAFETY_SETTING | Default "NONE". Cohere safety mode (NONE/CONTEXTUAL/STRICT). | common/init.go |
| GEMINI_SAFETY_SETTING | Default "BLOCK_NONE". Gemini safety setting env override. | common/init.go |
| GET_MEDIA_TOKEN | Default true. Whether to count image/media tokens. | common/init.go |
| GET_MEDIA_TOKEN_NOT_STREAM | Default false. Count media tokens for non-stream (stream=false) requests. | common/init.go |
| DIFY_DEBUG | Default true. Whether Dify channel outputs workflow/node info to client. | common/init.go |
| LINUX_DO_TOKEN_ENDPOINT | Default https://connect.linux.do/oauth2/token. LinuxDo OAuth token endpoint. | .env.example |
| LINUX_DO_USER_ENDPOINT | Default https://connect.linux.do/api/user. LinuxDo OAuth user endpoint. | .env.example |
| NODE_TYPE | Default "master" (anything != "slave" is master). Node role for multi-node deployments. | common/init.go |
| NODE_NAME | Default empty (falls back to hostname). Node name used in audit logs. | common/node_identity.go |
| TRUSTED_REDIRECT_DOMAINS | Default empty. Comma-separated trusted redirect domains (subdomain matching) for payment callback URL validation. | common/init.go |
| VERSION | Default "v0.0.0" (replaced at build). Overrides version string. | common/init.go |
| CHANNEL_TEST_FREQUENCY | Default empty. Auto channel test frequency in minutes (overrides monitor setting when set). | setting/operation_setting/monitor_setting.go |
| CHANNEL_TEST_ENABLED | Default empty. Boolean override for auto-test-channel-enabled. | setting/operation_setting/monitor_setting.go |
| UMAMI_WEBSITE_ID | Default empty. Umami analytics website ID injected into frontend HTML. | main.go |
| UMAMI_SCRIPT_URL | Default https://analytics.umami.is/script.js. Umami script URL. | main.go |
| GOOGLE_ANALYTICS_ID | Default empty. Google Analytics 4 measurement ID injected into frontend HTML. | main.go |
| GLOBAL_API_RATE_LIMIT_ENABLE | Default true. Enable global API rate limit. | common/init.go |
| GLOBAL_API_RATE_LIMIT | Default 360. Global API rate limit request count. | common/init.go |
| GLOBAL_API_RATE_LIMIT_DURATION | Default 180 (seconds). Global API rate limit window. | common/init.go |
| GLOBAL_WEB_RATE_LIMIT_ENABLE | Default true. Enable global web rate limit. | common/init.go |
| GLOBAL_WEB_RATE_LIMIT | Default 120. Global web rate limit request count. | common/init.go |
| GLOBAL_WEB_RATE_LIMIT_DURATION | Default 180 (seconds). Global web rate limit window. | common/init.go |
| CRITICAL_RATE_LIMIT_ENABLE | Default true. Enable critical (auth/sensitive) rate limit. | common/init.go |
| CRITICAL_RATE_LIMIT | Default 20. Critical rate limit request count. | common/init.go |
| CRITICAL_RATE_LIMIT_DURATION | Default 1200 (seconds). Critical rate limit window. | common/init.go |
| SEARCH_RATE_LIMIT_ENABLE | Default true. Enable per-user search rate limit. | common/init.go |
| SEARCH_RATE_LIMIT | Default 10. Search rate limit request count per user. | common/init.go |
| SEARCH_RATE_LIMIT_DURATION | Default 60 (seconds). Search rate limit window. | common/init.go |
| MAX_FILE_DOWNLOAD_MB | Default 64. Max file download size in MB. | common/init.go |
| STREAM_SCANNER_MAX_BUFFER_MB | Default 128. Max SSE stream scanner buffer in MB. | common/init.go |
| MAX_REQUEST_BODY_MB | Default 128. Max request body size (decompressed) in MB; prevents zip bombs. | common/init.go |
| ANONYMOUS_REQUEST_BODY_LIMIT_KB | Default 512. Anonymous request body limit in KB. | common/init.go |
| FORCE_STREAM_OPTION | Default true. Force return usage info by overriding request stream options. | common/init.go |
| CountToken | Default true. Master switch for token counting (env key is literally "CountToken"). | common/init.go |
| AZURE_DEFAULT_API_VERSION | Default "2025-04-01-preview". Azure OpenAI default API version. | common/init.go |
| NOTIFY_LIMIT_COUNT | Default 2. Notification rate limit count. | common/init.go |
| NOTIFICATION_LIMIT_DURATION_MINUTE | Default 10 (minutes). Notification rate limit window. | common/init.go |
| TASK_QUERY_LIMIT | Default 1000. Max tasks queried per polling run. | common/init.go |
| TASK_TIMEOUT_MINUTES | Default 1440. Async task timeout minutes before marked failed/refunded; 0 disables. | common/init.go |
| TASK_PRICE_PATCH | Default empty. Comma-separated task price patch strings (e.g., Sora). | common/init.go |
| SYNC_UPSTREAM_BASE | Default https://basellm.github.io/llm-metadata. Upstream model metadata sync base URL. | controller/model_sync.go |
| SYNC_HTTP_TIMEOUT_SECONDS | Default 10 (15 in some call sites). HTTP timeout for model sync requests. | controller/model_sync.go |
| SYNC_HTTP_RETRY | Default 3. HTTP retry attempts for model sync. | controller/model_sync.go |
| SYNC_HTTP_MAX_MB | Default 10. Max download size in MB for model sync. | controller/model_sync.go |
| SUBSCRIPTION_PLAN_CACHE_TTL | Default 300 (seconds). Subscription plan cache TTL. | model/subscription.go |
| SUBSCRIPTION_PLAN_INFO_CACHE_TTL | Default 120 (seconds). Subscription plan info cache TTL. | model/subscription.go |
| SUBSCRIPTION_PLAN_CACHE_CAP | Default 5000. Subscription plan cache capacity. | model/subscription.go |
| SUBSCRIPTION_PLAN_INFO_CACHE_CAP | Default 10000. Subscription plan info cache capacity. | model/subscription.go |
| SMTP_STARTTLS_ENABLE | Default false. Enable SMTP STARTTLS (alias SMTP_STARTTLS_ENABLED). | common/init.go |
| SMTP_INSECURE_SKIP_VERIFY | Default false. Skip SMTP TLS verify (alias SMTP_TLS_INSECURE_SKIP_VERIFY). | common/init.go |
| TEST_MYSQL_DSN | Test-only. MySQL DSN used in tests. | router/relay_router_test.go |
| TEST_POSTGRES_DSN | Test-only. PostgreSQL DSN used in tests. | router/relay_router_test.go |
| FileUploadPermission | DB option: role required for file upload (default 0 = guest). | model/option.go |
| FileDownloadPermission | DB option: role required for file download (default 0). | model/option.go |
| ImageUploadPermission | DB option: role required for image upload (default 0). | model/option.go |
| ImageDownloadPermission | DB option: role required for image download (default 0). | model/option.go |
| PasswordLoginEnabled | DB option: allow password login (default true). | model/option.go |
| PasswordRegisterEnabled | DB option: allow password registration (default true). | model/option.go |
| EmailVerificationEnabled | DB option: require email verification (default false). | model/option.go |
| GitHubOAuthEnabled | DB option: enable GitHub OAuth (default false). | model/option.go |
| LinuxDOOAuthEnabled | DB option: enable LinuxDo OAuth (default false). | model/option.go |
| TelegramOAuthEnabled | DB option: enable Telegram OAuth (default false). | model/option.go |
| WeChatAuthEnabled | DB option: enable WeChat auth (default false). | model/option.go |
| TurnstileCheckEnabled | DB option: enable Cloudflare Turnstile check (default false). | model/option.go |
| RegisterEnabled | DB option: allow user registration (default true). | model/option.go |
| AutomaticDisableChannelEnabled | DB option: auto-disable channels on failure (default false). | model/option.go |
| AutomaticEnableChannelEnabled | DB option: auto-enable channels on recovery (default false). | model/option.go |
| LogConsumeEnabled | DB option: log token consumption (default true). | model/option.go |
| DisplayInCurrencyEnabled | DB option: legacy display quota as currency (default true; syncs to general_setting.quota_display_type). | model/option.go |
| DisplayTokenStatEnabled | DB option: display token statistics (default true). | model/option.go |
| DrawingEnabled | DB option: enable drawing/image feature (default true). | model/option.go |
| TaskEnabled | DB option: enable async task feature (default true). | model/option.go |
| DataExportEnabled | DB option: enable dashboard data export (default true). | model/option.go |
| ChannelDisableThreshold | DB option: channel auto-disable failure threshold (default 5.0). | model/option.go |
| EmailDomainRestrictionEnabled | DB option: restrict registration to whitelisted email domains (default false). | model/option.go |
| EmailAliasRestrictionEnabled | DB option: restrict email alias (plus-addressing) (default false). | model/option.go |
| EmailDomainWhitelist | DB option: comma-separated allowed email domains (default gmail/163/126/qq/outlook/hotmail/icloud/yahoo/foxmail). | model/option.go |
| SMTPServer | DB option: SMTP server host (default empty). | model/option.go |
| SMTPFrom | DB option: SMTP From address (default empty). | model/option.go |
| SMTPPort | DB option: SMTP port (default 587). | model/option.go |
| SMTPAccount | DB option: SMTP account (default empty). | model/option.go |
| SMTPToken | DB option: SMTP token/password (default empty). | model/option.go |
| SMTPSSLEnabled | DB option: SMTP implicit SSL (default false). | model/option.go |
| SMTPStartTLSEnabled | DB option: SMTP STARTTLS (default false). | model/option.go |
| SMTPInsecureSkipVerify | DB option: SMTP TLS insecure skip verify (default false). | model/option.go |
| SMTPForceAuthLogin | DB option: force SMTP AUTH LOGIN (default false). | model/option.go |
| Notice | DB option: site notice text (default empty). | model/option.go |
| About | DB option: about text (default empty). | model/option.go |
| HomePageContent | DB option: homepage content (default empty). | model/option.go |
| Footer | DB option: site footer HTML (default empty). | model/option.go |
| SystemName | DB option: system display name (default "New API"). | model/option.go |
| Logo | DB option: site logo URL (default empty). | model/option.go |
| ServerAddress | DB option: public server address (default http://localhost:3000). | model/option.go |
| WorkerUrl | DB option: Cloudflare Worker proxy URL (default empty; enables worker when set). | model/option.go |
| WorkerValidKey | DB option: Cloudflare Worker valid key (default empty). | model/option.go |
| WorkerAllowHttpImageRequestEnabled | DB option: allow HTTP image requests through worker (default false). | model/option.go |
| PayAddress | DB option: payment (Epay) address (default empty). | model/option.go |
| CustomCallbackAddress | DB option: custom payment callback address (default empty). | model/option.go |
| EpayId | DB option: Epay merchant ID (default empty). | model/option.go |
| EpayKey | DB option: Epay merchant key (default empty). | model/option.go |
| Price | DB option: top-up price per unit (default 7.3). | model/option.go |
| USDExchangeRate | DB option: USD exchange rate (default 7.3). | model/option.go |
| MinTopUp | DB option: minimum top-up amount (default 1). | model/option.go |
| StripeMinTopUp | DB option: Stripe minimum top-up (default 1). | model/option.go |
| StripeApiSecret | DB option: Stripe API secret key (default empty). | model/option.go |
| StripeWebhookSecret | DB option: Stripe webhook secret (default empty). | model/option.go |
| StripePriceId | DB option: Stripe price ID (default empty). | model/option.go |
| StripeUnitPrice | DB option: Stripe unit price (default 8.0). | model/option.go |
| StripePromotionCodesEnabled | DB option: enable Stripe promotion codes (default false). | model/option.go |
| CreemApiKey | DB option: Creem API key (default empty). | model/option.go |
| CreemProducts | DB option: Creem products JSON (default "[]"). | model/option.go |
| CreemTestMode | DB option: Creem test mode (default false). | model/option.go |
| CreemWebhookSecret | DB option: Creem webhook secret (default empty). | model/option.go |
| WaffoEnabled | DB option: enable Waffo payment (default false). | model/option.go |
| WaffoApiKey | DB option: Waffo API key (default empty). | model/option.go |
| WaffoPrivateKey | DB option: Waffo private key (default empty). | model/option.go |
| WaffoPublicCert | DB option: Waffo public certificate (default empty). | model/option.go |
| WaffoSandboxPublicCert | DB option: Waffo sandbox public cert (default empty). | model/option.go |
| WaffoSandboxApiKey | DB option: Waffo sandbox API key (default empty). | model/option.go |
| WaffoSandboxPrivateKey | DB option: Waffo sandbox private key (default empty). | model/option.go |
| WaffoSandbox | DB option: Waffo sandbox mode (default false). | model/option.go |
| WaffoMerchantId | DB option: Waffo merchant ID (default empty). | model/option.go |
| WaffoNotifyUrl | DB option: Waffo notify URL (default empty). | model/option.go |
| WaffoReturnUrl | DB option: Waffo return URL (default empty). | model/option.go |
| WaffoSubscriptionReturnUrl | DB option: Waffo subscription return URL (default empty). | model/option.go |
| WaffoCurrency | DB option: Waffo currency (default empty). | model/option.go |
| WaffoUnitPrice | DB option: Waffo unit price (default 1.0). | model/option.go |
| WaffoMinTopUp | DB option: Waffo minimum top-up (default 1). | model/option.go |
| WaffoPayMethods | DB option: Waffo payment methods JSON (default from constant.DefaultWaffoPayMethods). | model/option.go |
| WaffoPancakeMerchantID | DB option: Waffo Pancake merchant ID (default empty). | model/option.go |
| WaffoPancakePrivateKey | DB option: Waffo Pancake private key (default empty). | model/option.go |
| WaffoPancakeReturnURL | DB option: Waffo Pancake return URL (default empty). | model/option.go |
| WaffoPancakeUnitPrice | DB option: Waffo Pancake unit price (default 1.0). | model/option.go |
| WaffoPancakeMinTopUp | DB option: Waffo Pancake min top-up (default 1). | model/option.go |
| WaffoPancakeStoreID | DB option: Waffo Pancake store ID (default empty). | model/option.go |
| WaffoPancakeProductID | DB option: Waffo Pancake product ID (default empty). | model/option.go |
| TopupGroupRatio | DB option: top-up group ratio JSON. | model/option.go |
| Chats | DB option: chat client config links JSON (Cherry Studio/AionUI/etc). | model/option.go |
| AutoGroups | DB option: auto group names JSON (default ["default"]). | model/option.go |
| DefaultUseAutoGroup | DB option: default to auto groups (default false). | model/option.go |
| MaxTokenAutoGroups | DB option: max tokens in auto groups (default 5). | model/option.go |
| PayMethods | DB option: Epay payment methods JSON (default alipay/wxpay/custom1). | model/option.go |
| GitHubClientId | DB option: GitHub OAuth client ID (default empty). | model/option.go |
| GitHubClientSecret | DB option: GitHub OAuth client secret (default empty). | model/option.go |
| TelegramBotToken | DB option: Telegram bot token (default empty). | model/option.go |
| TelegramBotName | DB option: Telegram bot name (default empty). | model/option.go |
| WeChatServerAddress | DB option: WeChat server address (default empty). | model/option.go |
| WeChatServerToken | DB option: WeChat server token (default empty). | model/option.go |
| WeChatAccountQRCodeImageURL | DB option: WeChat account QR code image URL (default empty). | model/option.go |
| TurnstileSiteKey | DB option: Cloudflare Turnstile site key (default empty). | model/option.go |
| TurnstileSecretKey | DB option: Cloudflare Turnstile secret key (default empty). | model/option.go |
| QuotaForNewUser | DB option: quota granted to new user (default 0). | model/option.go |
| QuotaForInviter | DB option: quota granted to inviter (default 0). | model/option.go |
| QuotaForInvitee | DB option: quota granted to invitee (default 0). | model/option.go |
| QuotaRemindThreshold | DB option: low-quota reminder threshold (default 1000). | model/option.go |
| PreConsumedQuota | DB option: pre-consumed quota (default 500). | model/option.go |
| ModelRequestRateLimitCount | DB option: model request rate limit count (default 0). | model/option.go |
| ModelRequestRateLimitDurationMinutes | DB option: model request rate limit window minutes (default 1). | model/option.go |
| ModelRequestRateLimitSuccessCount | DB option: model request rate limit success count (default 1000). | model/option.go |
| ModelRequestRateLimitGroup | DB option: per-group model rate limit JSON map. | model/option.go |
| ModelRatio | DB option: model quota ratio JSON map. | model/option.go |
| ModelPrice | DB option: model price JSON map. | model/option.go |
| CacheRatio | DB option: cache hit ratio JSON map. | model/option.go |
| CreateCacheRatio | DB option: cache creation ratio JSON map. | model/option.go |
| GroupRatio | DB option: group ratio JSON map. | model/option.go |
| GroupGroupRatio | DB option: group-to-group ratio JSON map. | model/option.go |
| UserUsableGroups | DB option: user usable groups JSON (default default/vip). | model/option.go |
| CompletionRatio | DB option: completion ratio JSON map. | model/option.go |
| ImageRatio | DB option: image ratio JSON map. | model/option.go |
| AudioRatio | DB option: audio ratio JSON map. | model/option.go |
| AudioCompletionRatio | DB option: audio completion ratio JSON map. | model/option.go |
| TopUpLink | DB option: top-up link URL (default empty). | model/option.go |
| QuotaPerUnit | DB option: quota per currency unit (default 500000.0). | model/option.go |
| RetryTimes | DB option: relay retry times (default 0). | model/option.go |
| DataExportInterval | DB option: data export interval minutes (default 5). | model/option.go |
| DataExportDefaultTime | DB option: data export default time bucket (default "hour"). | model/option.go |
| DefaultCollapseSidebar | DB option: default collapse sidebar (default false). | model/option.go |
| MjNotifyEnabled | DB option: Midjourney notify enabled (default false). | model/option.go |
| MjAccountFilterEnabled | DB option: Midjourney account filter enabled (default false). | model/option.go |
| MjModeClearEnabled | DB option: Midjourney mode clear enabled (default false). | model/option.go |
| MjForwardUrlEnabled | DB option: Midjourney forward URL enabled (default true). | model/option.go |
| MjActionCheckSuccessEnabled | DB option: Midjourney action check success enabled (default true). | model/option.go |
| CheckSensitiveEnabled | DB option: sensitive word checking enabled (default true). | model/option.go |
| DemoSiteEnabled | DB option: demo site mode (default false). | model/option.go |
| SelfUseModeEnabled | DB option: self-use mode (default false). | model/option.go |
| ModelRequestRateLimitEnabled | DB option: model request rate limit enabled (default false). | model/option.go |
| CheckSensitiveOnPromptEnabled | DB option: check sensitive words on prompt (default true). | model/option.go |
| StopOnSensitiveEnabled | DB option: stop generation on sensitive word instead of replace (default true). | model/option.go |
| SensitiveWords | DB option: sensitive word list (newline-separated; default ["test_sensitive"]). | model/option.go |
| StreamCacheQueueLength | DB option: stream cache queue length (default 0 = no cache). | model/option.go |
| AutomaticDisableKeywords | DB option: auto-disable channel keyword list (newline-separated). | model/option.go |
| AutomaticDisableStatusCodes | DB option: auto-disable status code ranges (default 401). | model/option.go |
| AutomaticRetryStatusCodes | DB option: auto-retry status code ranges (1xx/3xx/4xx/5xx except 400/408/504/524). | model/option.go |
| ExposeRatioEnabled | DB option: expose ratio data via API (default false). | model/option.go |
| LinuxDOClientId | DB option: LinuxDo OAuth client ID (default empty). | model/option.go |
| LinuxDOClientSecret | DB option: LinuxDo OAuth client secret (default empty). | model/option.go |
| LinuxDOMinimumTrustLevel | DB option: LinuxDo minimum trust level (default 0). | model/option.go |
| Theme | DB option (retired): legacy theme, migrated to "default" and deleted from OptionMap. | model/frontend_option_migration.go |
| fetch_setting | DB config group (fetch_setting.*): enable_ssrf_protection(true), allow_private_ip(false), domain_filter_mode, ip_filter_mode, domain_list, ip_list, allowed_ports(80/443/8080/8443), apply_ip_filter_for_domain(true). | setting/system_setting/fetch_setting.go |
| legal | DB config group (legal.*): user_agreement, privacy_policy. | setting/system_setting/legal.go |
| oidc | DB config group (oidc.*): enabled, display_name, client_id, client_secret, well_known, authorization_endpoint, token_endpoint, user_info_endpoint. | setting/system_setting/oidc.go |
| passkey | DB config group (passkey.*): enabled(false), rp_display_name, rp_id, origins, allow_insecure_origin(false), user_verification(preferred), attachment_preference. | setting/system_setting/passkey.go |
| discord | DB config group (discord.*): enabled, client_id, client_secret. | setting/system_setting/discord.go |
| general_setting | DB config group (general_setting.*): docs_link, ping_interval_enabled(false), ping_interval_seconds(60), quota_display_type(USD), custom_currency_symbol, custom_currency_exchange_rate(1.0). | setting/operation_setting/general_setting.go |
| payment_setting | DB config group (payment_setting.*): amount_options(10/20/50/100/200/500), amount_discount, compliance_confirmed, compliance_terms_version, compliance_confirmed_at/by/ip. | setting/operation_setting/payment_setting.go |
| quota_setting | DB config group (quota_setting.*): enable_free_model_pre_consume(true). | setting/operation_setting/quota_setting.go |
| token_setting | DB config group (token_setting.*): max_user_tokens(1000). | setting/operation_setting/token_setting.go |
| monitor_setting | DB config group (monitor_setting.*): auto_test_channel_enabled(false), auto_test_channel_minutes(10), channel_test_mode(scheduled_all). | setting/operation_setting/monitor_setting.go |
| checkin_setting | DB config group (checkin_setting.*): enabled(false), min_quota(1000), max_quota(10000). | setting/operation_setting/checkin_setting.go |
| channel_affinity_setting | DB config group (channel_affinity_setting.*): enabled(true), switch_on_success(true), keep_on_channel_disabled(false), max_entries(100000), default_ttl_seconds(3600), rules(codex/claude CLI pass-through). | setting/operation_setting/channel_affinity_setting.go |
| performance_setting | DB config group (performance_setting.*): disk_cache_enabled(false), disk_cache_threshold_mb(10), disk_cache_max_size_mb(1024), disk_cache_path, monitor_enabled(true), monitor_cpu_threshold(90), monitor_memory_threshold(90), monitor_disk_threshold(95). | setting/performance_setting/config.go |
| billing_setting | DB config group (billing_setting.*): billing_mode map, billing_expr map (tiered expression billing per model). | setting/billing_setting/tiered_billing.go |
| console_setting | DB config group (console_setting.*): api_info, uptime_kuma_groups, announcements, faq, api_info_enabled(true), uptime_kuma_enabled(true), announcements_enabled(true), faq_enabled(true). | setting/console_setting/config.go |
| perf_metrics_setting | DB config group (perf_metrics_setting.*): enabled(true), flush_interval(5), bucket_time(hour), retention_days(0). | setting/perf_metrics_setting/config.go |
| global | DB config group (global.*, OpenAI model settings): pass_through_request_enabled(false), thinking_model_blacklist, chat_completions_to_responses_policy. | setting/model_setting/global.go |
| gemini | DB config group (gemini.*): safety_settings, version_settings, supported_imagine_models, thinking_adapter_enabled(false), thinking_adapter_budget_tokens_percentage(0.6), function_call_thought_signature_enabled(true), remove_function_response_id_enabled(true). | setting/model_setting/gemini.go |
| claude | DB config group (claude.*): model_headers_settings, default_max_tokens(default 8192), thinking_adapter_enabled(true), thinking_adapter_budget_tokens_percentage(0.8). | setting/model_setting/claude.go |
| qwen | DB config group (qwen.*): sync_image_models list. | setting/model_setting/qwen.go |
| grok | DB config group (grok.*): violation_deduction_enabled(true), violation_deduction_amount(0.05). | setting/model_setting/grok.go |
| tool_price_setting | DB config group (tool_price_setting.prices): per-tool call prices $/1K calls with hardcoded fallbacks (web_search 10.0, file_search 2.5, image_generation 150.0, etc). | setting/operation_setting/tools.go |

## Frontend Routes, Settings Pages & Locales

| Name | Detail | Evidence |
|---|---|---|
| / | Route: home landing page (feature domain: home) \| auth: public | web/src/routes/index.tsx:23 |
| /about | Route: about page (feature domain: about) \| auth: public | web/src/routes/about/index.tsx:23 |
| /privacy-policy | Route: privacy policy (feature domain: legal) \| auth: public | web/src/routes/privacy-policy.tsx:23 |
| /user-agreement | Route: user agreement (feature domain: legal) \| auth: public | web/src/routes/user-agreement.tsx:23 |
| /setup | Route: setup wizard (feature domain: setup); redirects away once setup complete \| auth: public | web/src/routes/setup/index.tsx:23 |
| /sign-in | Route: sign-in (feature domain: auth) \| auth: public | web/src/routes/(auth)/sign-in.tsx |
| /sign-up | Route: sign-up (feature domain: auth) \| auth: public | web/src/routes/(auth)/sign-up.tsx |
| /register | Route: register (feature domain: auth) \| auth: public | web/src/routes/(auth)/register.tsx |
| /forgot-password | Route: forgot password (feature domain: auth) \| auth: public | web/src/routes/(auth)/forgot-password.tsx |
| /reset | Route: reset password (feature domain: auth) \| auth: public | web/src/routes/(auth)/reset.tsx |
| /user/reset | Route: user reset link (feature domain: auth) \| auth: public | web/src/routes/(auth)/user/reset.tsx |
| /otp | Route: one-time password (feature domain: auth) \| auth: public | web/src/routes/(auth)/otp.tsx |
| /oauth | Route: OAuth entry (feature domain: auth) \| auth: public | web/src/routes/(auth)/oauth.tsx |
| /oauth/$provider | Route: OAuth provider callback (feature domain: auth) \| auth: public | web/src/routes/oauth/$provider.tsx:246 |
| /401 | Route: 401 error page (feature domain: errors) \| auth: public | web/src/routes/(errors)/401.tsx |
| /403 | Route: 403 forbidden page (feature domain: errors) \| auth: public | web/src/routes/(errors)/403.tsx |
| /404 | Route: 404 not found page (feature domain: errors) \| auth: public | web/src/routes/(errors)/404.tsx |
| /500 | Route: 500 error page (feature domain: errors) \| auth: public | web/src/routes/(errors)/500.tsx |
| /503 | Route: 503 error page (feature domain: errors) \| auth: public | web/src/routes/(errors)/503.tsx |
| /pricing | Route: model pricing list (feature domain: pricing) \| auth: conditional (public by default; requireAuth enforced when module access configured) | web/src/routes/pricing/index.tsx:41 |
| /pricing/$modelId | Route: model pricing detail (feature domain: pricing) \| auth: conditional (same module-access policy as /pricing) | web/src/routes/pricing/$modelId/index.tsx |
| /rankings | Route: usage rankings leaderboard (feature domain: rankings) \| auth: conditional (public by default; requireAuth enforced when configured) | web/src/routes/rankings/index.tsx:35 |
| /_authenticated (layout) | Route layout: authenticated shell; redirects to /sign-in when no user/token \| auth: authenticated | web/src/routes/_authenticated/route.tsx:26 |
| /channels | Route: channel management (feature domain: channels) \| auth: admin (role >= ADMIN) | web/src/routes/_authenticated/channels/index.tsx:40 |
| /chat/$chatId | Route: chat conversation (feature domain: chat); redirects to /dashboard if no active chat \| auth: authenticated | web/src/routes/_authenticated/chat/$chatId.tsx:36 |
| /chat2link | Route: chat-to-link conversion (feature domain: chat) \| auth: authenticated | web/src/routes/_authenticated/chat2link.tsx:29 |
| /dashboard | Route: dashboard (redirects to default section) (feature domain: dashboard) \| auth: authenticated | web/src/routes/_authenticated/dashboard/index.tsx:24 |
| /dashboard/$section | Route: dashboard section (feature domain: dashboard) \| auth: authenticated | web/src/routes/_authenticated/dashboard/$section.tsx:28 |
| /errors/$error | Route: authenticated error detail page (feature domain: errors) \| auth: authenticated | web/src/routes/_authenticated/errors/$error.tsx:32 |
| /keys | Route: user API keys (feature domain: keys) \| auth: authenticated | web/src/routes/_authenticated/keys/index.tsx:36 |
| /models | Route: model list (feature domain: models) \| auth: admin (role >= ADMIN) | web/src/routes/_authenticated/models/index.tsx:29 |
| /models/$section | Route: model section (feature domain: models) \| auth: admin (role >= ADMIN) | web/src/routes/_authenticated/models/$section.tsx:47 |
| /playground | Route: API playground (feature domain: playground); redirects to /dashboard if no active chat \| auth: authenticated | web/src/routes/_authenticated/playground/index.tsx:26 |
| /profile | Route: user profile (feature domain: profile) \| auth: authenticated | web/src/routes/_authenticated/profile/index.tsx:23 |
| /redemption-codes | Route: redemption codes (feature domain: redemption-codes) \| auth: admin (role >= ADMIN) | web/src/routes/_authenticated/redemption-codes/index.tsx:38 |
| /subscriptions | Route: subscriptions (feature domain: subscriptions) \| auth: admin (role >= ADMIN) | web/src/routes/_authenticated/subscriptions/index.tsx:28 |
| /system-info | Route: system info (feature domain: system-info) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-info/index.tsx:29 |
| /system-settings (layout) | Route layout: system settings; redirects to /403 unless SUPER_ADMIN \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/route.tsx:26 |
| /system-settings | Route: system settings landing (feature domain: system-settings) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/index.tsx |
| /system-settings/auth | Route: system settings - auth page (feature domain: system-settings/auth) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/auth/index.tsx |
| /system-settings/auth/$section | Route: system settings - auth section (feature domain: system-settings/auth) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/auth/$section.tsx:27 |
| /system-settings/billing | Route: system settings - billing page (feature domain: system-settings/billing) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/billing/index.tsx |
| /system-settings/billing/$section | Route: system settings - billing section (feature domain: system-settings/billing) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/billing/$section.tsx:27 |
| /system-settings/content | Route: system settings - content page (feature domain: system-settings/content) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/content/index.tsx |
| /system-settings/content/$section | Route: system settings - content section (feature domain: system-settings/content) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/content/$section.tsx:27 |
| /system-settings/models | Route: system settings - models page (feature domain: system-settings/models) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/models/index.tsx |
| /system-settings/models/$section | Route: system settings - models section (feature domain: system-settings/models) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/models/$section.tsx:27 |
| /system-settings/operations | Route: system settings - operations page (feature domain: system-settings/operations) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/operations/index.tsx |
| /system-settings/operations/$section | Route: system settings - operations section (feature domain: system-settings/operations) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/operations/$section.tsx:27 |
| /system-settings/security | Route: system settings - security page (feature domain: system-settings/security) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/security/index.tsx |
| /system-settings/security/$section | Route: system settings - security section (feature domain: system-settings/security) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/security/$section.tsx:27 |
| /system-settings/site | Route: system settings - site page (feature domain: system-settings/site) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/site/index.tsx |
| /system-settings/site/$section | Route: system settings - site section (feature domain: system-settings/site) \| auth: admin (SUPER_ADMIN only) | web/src/routes/_authenticated/system-settings/site/$section.tsx:27 |
| /usage-logs | Route: usage logs (redirects to default section) (feature domain: usage-logs) \| auth: authenticated | web/src/routes/_authenticated/usage-logs/index.tsx:24 |
| /usage-logs/$section | Route: usage logs section (feature domain: usage-logs) \| auth: authenticated | web/src/routes/_authenticated/usage-logs/$section.tsx:53 |
| /users | Route: user management (feature domain: users) \| auth: admin (role >= ADMIN) | web/src/routes/_authenticated/users/index.tsx:45 |
| /wallet | Route: wallet (feature domain: wallet) \| auth: authenticated | web/src/routes/_authenticated/wallet/index.tsx:28 |
| __root | Route: root layout (TanStack Router root) \| auth: public | web/src/routes/__root.tsx |
| (auth) layout | Route layout: auth pages (pathless group) \| auth: public | web/src/routes/(auth)/route.tsx |
| System Settings: Auth | Settings page (feature: system-settings/auth); sections: basic-auth, oauth, passkey, bot-protection, custom-oauth | web/src/features/system-settings/auth/section-registry.tsx |
| System Settings: Billing | Settings page (feature: system-settings/billing); sections: quota, currency, model-pricing, group-pricing, payment, checkin | web/src/features/system-settings/billing/section-registry.tsx |
| System Settings: Content | Settings page (feature: system-settings/content); sections: dashboard, announcements, api-info, faq, uptime-kuma, chat, drawing | web/src/features/system-settings/content/section-registry.tsx |
| System Settings: Models | Settings page (feature: system-settings/models); sections: global, routing-reliability, gemini, claude, grok, channel-affinity, model-deployment | web/src/features/system-settings/models/section-registry.tsx |
| System Settings: Operations | Settings page (feature: system-settings/operations); sections: behavior, alerts, email, worker, logs, performance, update-checker | web/src/features/system-settings/operations/section-registry.tsx |
| System Settings: Security | Settings page (feature: system-settings/security); sections: rate-limit, sensitive-words, ssrf, token-limits | web/src/features/system-settings/security/section-registry.tsx |
| System Settings: Site | Settings page (feature: system-settings/site); sections: system-info, notice, header-navigation, sidebar-modules | web/src/features/system-settings/site/section-registry.tsx |
| NOTE: no web/src/pages/Setting/ | The directory web/src/pages/Setting/ does not exist in this repo; all settings pages live under web/src/features/system-settings/ (index.tsx + section-registry.tsx per feature). Supporting section components are split across features/system-settings/{general,integrations,maintenance,request-limits}. | web/src/features/system-settings/index.tsx |
| feature domain: about | Top-level frontend feature domain under web/src/features/ | web/src/features/about |
| feature domain: auth | Top-level frontend feature domain (sign-in, sign-up, forgot/reset, otp, passkey, oauth) | web/src/features/auth |
| feature domain: channels | Top-level frontend feature domain | web/src/features/channels |
| feature domain: chat | Top-level frontend feature domain | web/src/features/chat |
| feature domain: dashboard | Top-level frontend feature domain | web/src/features/dashboard |
| feature domain: errors | Top-level frontend feature domain | web/src/features/errors |
| feature domain: home | Top-level frontend feature domain | web/src/features/home |
| feature domain: keys | Top-level frontend feature domain | web/src/features/keys |
| feature domain: legal | Top-level frontend feature domain (privacy policy, user agreement) | web/src/features/legal |
| feature domain: models | Top-level frontend feature domain | web/src/features/models |
| feature domain: performance-metrics | Top-level frontend feature domain | web/src/features/performance-metrics |
| feature domain: playground | Top-level frontend feature domain | web/src/features/playground |
| feature domain: pricing | Top-level frontend feature domain | web/src/features/pricing |
| feature domain: profile | Top-level frontend feature domain | web/src/features/profile |
| feature domain: rankings | Top-level frontend feature domain | web/src/features/rankings |
| feature domain: redemption-codes | Top-level frontend feature domain | web/src/features/redemption-codes |
| feature domain: setup | Top-level frontend feature domain (setup wizard) | web/src/features/setup |
| feature domain: subscriptions | Top-level frontend feature domain | web/src/features/subscriptions |
| feature domain: system-info | Top-level frontend feature domain | web/src/features/system-info |
| feature domain: system-settings | Top-level frontend feature domain (admin settings; subdomains: auth, billing, content, general, integrations, maintenance, models, operations, request-limits, security, site) | web/src/features/system-settings |
| feature domain: usage-logs | Top-level frontend feature domain | web/src/features/usage-logs |
| feature domain: users | Top-level frontend feature domain | web/src/features/users |
| feature domain: wallet | Top-level frontend feature domain | web/src/features/wallet |
| locale: en | Locale file en.json \| 5266 top-level keys under 'translation' | web/src/i18n/locales/en.json |
| locale: fr | Locale file fr.json \| 5266 top-level keys under 'translation' | web/src/i18n/locales/fr.json |
| locale: ja | Locale file ja.json \| 5266 top-level keys under 'translation' | web/src/i18n/locales/ja.json |
| locale: ru | Locale file ru.json \| 5266 top-level keys under 'translation' | web/src/i18n/locales/ru.json |
| locale: vi | Locale file vi.json \| 5266 top-level keys under 'translation' | web/src/i18n/locales/vi.json |
| locale: zh-TW | Locale file zh-TW.json \| 5266 top-level keys under 'translation' | web/src/i18n/locales/zh-TW.json |
| locale: zh | Locale file zh.json \| 5266 top-level keys under 'translation' | web/src/i18n/locales/zh.json |
| _reports/_sync-report.json | Non-locale artifact under locales/ (i18n sync report), excluded from translation-key counts | web/src/i18n/locales/_reports/_sync-report.json |

## Billing, Security & Background Jobs

| Name | Detail | Evidence |
|---|---|---|
| QuotaPerUnit constant | Quota unit is 500 * 1000.0 quota points, documented as $0.002 per 1K tokens (so 1 quota point = $0.000002); used as the multiplier that converts prices/ratios into integer quota. | common/constants.go:22 |
| int32 quota saturation | All quota conversions centralize through QuotaFromFloat/QuotaRound/QuotaFromDecimal which clamp to int32 MinQuota/MaxQuota (overflow/underflow/NaN) and return a *QuotaClamp audit marker instead of wrapping a charge into a credit. | common/quota_math.go:21-200 |
| PreConsumeBilling stage | Pre-consume creates a BillingSession (NewBillingSession) and reserves the estimated quota from wallet or subscription funding source before the upstream request. | service/billing.go:23; service/billing_session.go:NewBillingSession |
| BillingSession lifecycle | BillingSession encapsulates pre-consume/settle/refund for one request: preConsume reserves token quota then funding; Settle applies actual-minus-preconsumed delta; Refund idempotently returns pre-consumed quota async via gopool. | service/billing_session.go:24-170 |
| SettleBilling stage | Post-consume settlement computes delta = actualQuota - preConsumedQuota and calls BillingSession.Settle (or falls back to legacy PostConsumeQuota), then sends quota-notify if threshold crossed. | service/billing.go:48-80 |
| Refund stage | BillingSession.Refund returns funding source and token quota, with retry-with-backoff only for transaction-based subscription refunds (RefundSubscriptionPreConsume via refundWithRetry). | service/billing_session.go:95-135; service/funding_source.go:130-140 |
| Task settlement (refund) | RefundTaskQuota refunds pre-consumed async-task quota to wallet or subscription, returns token quota, writes a refund task-billing log, then zeroes task.Quota. | service/task_billing.go:141-200 |
| Task settlement (delta recalc) | RecalculateTaskQuota settles the delta between actual and pre-consumed task quota (positive = consume, negative = refund); RecalculateTaskQuotaByTokens recomputes from totalTokens * modelRatio * groupRatio * otherMultiplier. | service/task_billing.go:205-330 |
| Funding source abstraction | FundingSource interface (Source/PreConsume/Settle/Refund) implemented by WalletFunding (atomic TryReserveUserQuota) and SubscriptionFunding (PreConsumeUserSubscription). | service/funding_source.go:14-135 |
| Billing preference fallback | NewBillingSession honors billing_preference (subscription_only/wallet_only/wallet_first/subscription_first) with wallet-overflow fallback controlled by UserActiveSubscriptionsAllowWalletOverflow. | service/billing_session.go:NewBillingSession |
| Trusted-quota bypass | Pre-consume skips reservation for wallet users with quota above GetTrustQuota (token unlimited or token_quota above threshold), unless ForcePreConsume for async tasks; subscriptions never bypass. | service/billing_session.go:shouldTrust |
| Atomic token pre-consume | PreConsumeTokenQuota uses TryReserveTokenQuota to check-and-decrement token remaining quota atomically, preventing concurrent over-consume. | service/quota.go:PreConsumeTokenQuota |
| Tiered expression billing | tiered_expr billing mode compiles a user-supplied expr-lang expression into a cached program; pre-consume estimates and settlement (ComputeTieredQuotaWithRequest) re-evaluate the same frozen BillingSnapshot. | setting/billing_setting/tiered_billing.go; pkg/billingexpr/settle.go; pkg/billingexpr/compile.go |
| Billing expression environment | Expression env exposes p, c, len, cr, cc, cc1h, img, img_o, ai, ao token vars plus tier(), header(), param(), has(), hour/minute/weekday/month/day and math helpers; coefficients are $/1M tokens. | pkg/billingexpr/run.go:60-120; pkg/billingexpr/settle.go:quotaConversion |
| Tiered token params builder | BuildTieredTokenParams normalizes GPT vs Claude usage semantics, subtracting cache/image/audio sub-categories from P/C only when the expression references them, and clamps negative remainders. | service/tiered_settle.go:28-90 |
| Prompt/completion tokens | Core billing dimensions P (prompt) and C (completion) with Len (input context length for tier conditions). | pkg/billingexpr/types.go:TokenParams; service/text_quota.go:textQuotaSummary |
| Cache-read variable | Cache read (hit) tokens exposed as cr; ratio from GetCacheRatio, applied to CachedTokens in text quota settlement. | pkg/billingexpr/types.go; setting/ratio_setting/cache_ratio.go:159 |
| Cache-create variables | Cache creation tokens split into cc (5-min TTL / generic) and cc1h (Claude 1-hour), with CacheCreation5mRatio and CacheCreation1hRatio; 1h multiplier fixed at 6/3.75. | pkg/billingexpr/types.go; relay/helper/price.go:30 |
| Image variables | Image input (img) and image output (img_o) tokens priced via GetImageRatio; text quota subtracts image tokens from base before applying image ratio. | pkg/billingexpr/types.go; setting/ratio_setting/model_ratio.go:653; service/text_quota.go |
| Audio variables | Audio input (ai) and audio output (ao) tokens priced via GetAudioRatio / GetAudioCompletionRatio; separate Gemini per-million audio input price path exists. | pkg/billingexpr/types.go; setting/ratio_setting/model_ratio.go:606; service/quota.go:calculateAudioQuota |
| Ratio sources | Ratio settings resolved per model: model ratio/price, completion ratio, cache ratio, cache-create ratio, image ratio, audio ratio, audio-completion ratio, plus group ratio and user-group special ratio. | setting/ratio_setting/model_ratio.go; setting/ratio_setting/group_ratio.go |
| ModelPriceHelper | Relay helper computes PriceData (pre-consume quota, ratios, free-model flag) for ratio mode, per-call mode, and tiered_expr mode; respects EnableFreeModelPreConsume. | relay/helper/price.go:ModelPriceHelper |
| Billing usage normalization | effectiveBillingUsage remaps provider-native usage (OpenAI/Anthropic/Gemini) into a common Usage with UsageSemantic tags for settlement and log recording. | service/billing_usage.go:effectiveBillingUsage |
| Tool-call surcharge | Billable tool calls (web_search, google_search, search-preview, Responses built-in tools) add a per-call surcharge quota via operation_setting tool prices. | service/text_quota.go:calculateTextToolCallSurcharge |
| Stripe payment | Stripe top-up and subscription payment with ApiSecret/WebhookSecret/PriceId/UnitPrice/PromotionCodes; hosted checkout plus webhook availability gate isStripeTopUpEnabled. | setting/payment_stripe.go; controller/topup_stripe.go; controller/payment_webhook_availability.go:14 |
| EPay payment | Chinese EasyPay (易支付) top-up via go-epay: GetEpayClient, RequestEpay purchase, EpayNotify callback with TradeStatus verification; payment methods include alipay/wxpay. | controller/topup.go:136-401; setting/operation_setting/payment_setting_old.go:20 |
| Creem payment | Creem top-up and subscription payment with ApiKey/Products/TestMode/WebhookSecret; product-based hosted checkout. | setting/payment_creem.go; controller/topup_creem.go; controller/payment_webhook_availability.go:31 |
| Waffo payment | Waffo global payment gateway (API/private key, public cert, merchant id, sandbox mode, per-method pay methods list) with top-up and subscription checkout plus pancake helper. | setting/payment_waffo.go; controller/topup_waffo.go; service/waffo_pancake.go |
| Waffo Pancake payment | Waffo Pancake hosted checkout variant enabled by MerchantID+PrivateKey+ProductID (no separate Enabled flag, matching Stripe/Creem). | setting/payment_waffo_pancake.go; controller/topup_waffo_pancake.go |
| Balance payment | Balance (wallet) funding source used as a top-up/subscription payment method (PaymentMethodBalance / PaymentProviderBalance); wallet credit also refundable/redemptable. | model/topup.go:32,41 |
| Redemption codes | Redemption (gift/redemption code) model with enabled/disabled/used statuses, used to credit user quota as an alternative funding path. | model/redemption.go; common/constants.go RedemptionCodeStatus |
| Payment method constants | Top-up payment methods are stripe, creem, waffo, waffo_pancake, balance; providers are epay, stripe, creem, waffo, waffo_pancake, balance. | model/topup.go:28-42 |
| OAuth GitHub | GitHub OAuth provider registered as "github"; token exchange at github.com/login/oauth/access_token and user info from the GitHub API; stores user.GitHubId. | oauth/github.go:20-181 |
| OAuth Discord | Discord OAuth provider registered as "discord"; token at discord.com/api/v10/oauth2/token and user info at /users/@me; stores user.DiscordId. | oauth/discord.go:19-175 |
| OAuth LinuxDO | LinuxDO OAuth provider registered as "linuxdo"; token at connect.linux.do/oauth2/token (configurable LINUX_DO_TOKEN_ENDPOINT) and user info at /api/user; enforces a minimum trust level. | oauth/linuxdo.go:21-198 |
| OAuth OIDC | OIDC (OpenID Connect) provider registered as "oidc" using configurable token/userinfo endpoints from system settings. | oauth/oidc.go:19-180 |
| OAuth generic/custom | GenericOAuthProvider supports DB-configured custom providers (slug, token/userinfo endpoints, auth style, access policy conditions) with LoadCustomProviders/RegisterCustom. | oauth/generic.go; oauth/registry.go:LoadCustomProviders |
| OAuth WeChat | WeChat OAuth login/bind implemented as a non-standard route /oauth/wechat (WeChatAuth) outside the provider registry, gated by WeChatAuthEnabled. | router/api-router.go:49; common/constants.go WeChatAuthEnabled |
| OAuth Telegram | Telegram OAuth login/bind via signed Telegram widget authorization (verifyTelegramAuthorization with 5-min max age) and bind flow tokens; gated by TelegramOAuthEnabled. | controller/telegram.go; router/api-router.go:51-53 |
| OAuth registry | Providers registered via init() into a global map with GetProvider/GetAllProviders and separate custom-provider tracking (customProviderSlugs). | oauth/registry.go |
| Rate limit: global API/web | GlobalWebRateLimit and GlobalAPIRateLimit use Redis fixed-window Lua (rateLimit:v2 namespace) or in-memory limiter keyed by client IP with 429 + Retry-After. | middleware/rate-limit.go:145-210 |
| Rate limit: critical | CriticalRateLimit guards sensitive endpoints (OAuth state, email bind) keyed by IP; UserCriticalRateLimit keys by authenticated user ID to resist proxy rotation. | middleware/rate-limit.go:CriticalRateLimit |
| Rate limit: upload/download/search | DownloadRateLimit, UploadRateLimit, and per-user SearchRateLimit middleware (search keyed by user ID). | middleware/rate-limit.go:DownloadRateLimit |
| Rate limit: model requests | ModelRequestRateLimit applies per-group total and success request limits (token bucket + success window) in Redis or memory. | middleware/model-rate-limit.go:ModelRequestRateLimit |
| Rate limit: email verification | EmailVerificationRateLimit caps 2 requests per 30s per IP with Redis fallback to in-memory. | middleware/email-verification-rate-limit.go |
| Turnstile CAPTCHA | TurnstileCheck verifies a Cloudflare Turnstile token against challenges.cloudflare.com/turnstile/v0/siteverify when TurnstileCheckEnabled. | middleware/turnstile-check.go |
| CORS | CORS middleware allows all origins, credentials, and GET/POST/PUT/DELETE/OPTIONS methods with wildcard headers. | middleware/cors.go:CORS |
| Trusted proxies | ConfigureTrustedProxies sets gin trusted proxies from TRUSTED_PROXIES env (defaults to loopback + RFC1918 + IPv6 ULA; "none" disables), affecting ClientIP resolution. | middleware/trusted_proxies.go |
| Session cookie origin guard | SessionCookieOriginGuard validates Origin/Referer against request host or SessionCookieTrustedURLs (constant-time compare) for cookie-authenticated refresh/logout when secure-cookie mode is on. | middleware/auth_origin.go |
| Secure verification proof | SecureVerificationRequired / RequireSecurityProof gate channel key disclosure behind X-Security-Proof validated via 2fa/passkey methods and proof scopes. | middleware/secure_verification.go |
| SSRF protection | SSRFProtection blocks private/reserved IPs (IANA v4/v6 lists), domain/IP allow/deny lists, port allowlist, and resolved-IP validation for outbound fetch clients. | common/ssrf_protection.go |
| SSRF-protected HTTP client | newProtectedFetchHTTPClient wraps an http.Transport with a protectedFetchDialer that validates host/port and resolved IPs before dialing, plus redirect checking; configurable via fetch_setting. | service/protected_fetch_client.go |
| Auth middleware | TryUserAuth, UserAuth, AdminAuth, RootAuth, TokenAuth, TokenAuthReadOnly, TokenOrUserAuth, WssAuth, and RequirePermission (authz) cover session and API-token authentication. | middleware/auth.go:78-260 |
| Distributor middleware | Distribute selects/validates a channel for the request (token model limits, group access, channel affinity, random satisfied channel) before relay. | middleware/distributor.go:Distribute |
| Request body limit | RequestBodyLimit and body_cleanup middleware bound request body size and clean up stored bodies. | middleware/request_body_limit.go; middleware/body_cleanup.go |
| Background job: scheduled system tasks | RegisterScheduledSystemTasks registers channel_test, model_update, midjourney_poll, async_task_poll handlers; StartSystemTaskRunner uses DB lease dedup across masters with run history. | controller/system_task_handlers.go:20-24; service/system_task.go:123 |
| Background job: log cleanup | log_cleanup system task deletes old logs; StartLogCleanupTask enqueues it and reports progress via SystemTaskProgressReporter. | service/system_task.go:78-168 |
| Background job: channel cache sync | model.SyncChannelCache periodically refreshes channel cache (with FixAbility panic retry) when memory cache is enabled. | main.go:model.SyncChannelCache |
| Background job: options sync | model.SyncOptions hot-reloads option config on a SyncFrequency interval. | main.go:go model.SyncOptions |
| Background job: authz policy sync | authz.StartPolicySync periodically reloads authorization policy across multi-node/master deployments. | main.go:go authz.StartPolicySync |
| Background job: quota data dashboard | model.UpdateQuotaData maintains the in-memory data dashboard (quota usage stats). | main.go:go model.UpdateQuotaData |
| Background job: channel auto update | controller.AutomaticallyUpdateChannels periodically updates channels when CHANNEL_UPDATE_FREQUENCY is set. | main.go:controller.AutomaticallyUpdateChannels |
| Background job: codex credential refresh | StartCodexCredentialAutoRefreshTask refreshes Codex OAuth credentials every 10 minutes when expiring within a day. | service/codex_credential_refresh_task.go:35; main.go |
| Background job: subscription quota reset | StartSubscriptionQuotaResetTask ticks every 1 minute, expiring and resetting due subscriptions (daily/weekly/monthly/custom) in batches of 300, with periodic cleanup. | service/subscription_reset_task.go:29 |
| Background job: system instance reporter | StartSystemInstanceReporter reports this process as a live system instance for multi-instance System Info. | service/system_instance.go:65 |
| Background job: auth artifact cleanup | StartAuthArtifactCleanup hourly removes expired dashboard sessions and one-time auth flows; master-instance only. | service/auth_cleanup.go:15 |
| Background job: batch updater | model.InitBatchUpdater batches DB updates when BATCH_UPDATE_ENABLED=true. | main.go:model.InitBatchUpdater |
