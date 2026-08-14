# API Matrix

Every HTTP route in the reference system, with TokenRouter implementation status.

> Status values: NOT_STARTED, IN_PROGRESS, PASS, REFERENCE_PLACEHOLDER, BLOCKED_EXTERNAL

| # | Method + Path | Handler | Middleware / Permission | Status | Target Evidence |
|---|---|---|---|---|---|
| 1 | GET /api/setup | Public: controller.GetSetup (no auth; group: API status/setup) | router/api-router.go:22 | NOT_STARTED |  |
| 2 | POST /api/setup | Public: controller.PostSetup + middleware.AnonymousRequestBodyLimit | router/api-router.go:23 | NOT_STARTED |  |
| 3 | GET /api/status | Public: controller.GetStatus (health/status endpoint) | router/api-router.go:24 | NOT_STARTED |  |
| 4 | GET /api/uptime/status | Public: controller.GetUptimeKumaStatus | router/api-router.go:25 | NOT_STARTED |  |
| 5 | GET /api/models | UserAuth: controller.DashboardListModels | router/api-router.go:26 | NOT_STARTED |  |
| 6 | GET /api/status/test | AdminAuth: controller.TestStatus | router/api-router.go:27 | NOT_STARTED |  |
| 7 | GET /api/notice | Public: controller.GetNotice | router/api-router.go:28 | NOT_STARTED |  |
| 8 | GET /api/user-agreement | Public: controller.GetUserAgreement | router/api-router.go:29 | NOT_STARTED |  |
| 9 | GET /api/privacy-policy | Public: controller.GetPrivacyPolicy | router/api-router.go:30 | NOT_STARTED |  |
| 10 | GET /api/about | Public: controller.GetAbout | router/api-router.go:31 | NOT_STARTED |  |
| 11 | GET /api/home_page_content | Public: controller.GetHomePageContent | router/api-router.go:33 | NOT_STARTED |  |
| 12 | GET /api/pricing | HeaderNavModuleAuth('pricing'): controller.GetPricing | router/api-router.go:34 | NOT_STARTED |  |
| 13 | GET /api/perf-metrics/summary | HeaderNavModulePublicOrUserAuth('pricing'): controller.GetPerfMetricsSummary | router/api-router.go:38 | NOT_STARTED |  |
| 14 | GET /api/perf-metrics | HeaderNavModulePublicOrUserAuth('pricing'): controller.GetPerfMetrics | router/api-router.go:39 | NOT_STARTED |  |
| 15 | GET /api/rankings | HeaderNavModuleAuth('rankings'): controller.GetRankings | router/api-router.go:41 | NOT_STARTED |  |
| 16 | GET /api/verification | EmailVerificationRateLimit + TurnstileCheck: controller.SendEmailVerification | router/api-router.go:42 | NOT_STARTED |  |
| 17 | GET /api/reset_password | CriticalRateLimit + TurnstileCheck: controller.SendPasswordResetEmail | router/api-router.go:43 | NOT_STARTED |  |
| 18 | POST /api/user/reset | CriticalRateLimit + AnonymousRequestBodyLimit: controller.ResetPassword | router/api-router.go:44 | NOT_STARTED |  |
| 19 | POST /api/oauth/state | CriticalRateLimit + DisableCache + TryUserAuth + AnonymousRequestBodyLimit: controller.GenerateOAuthCode | router/api-router.go:46 | NOT_STARTED |  |
| 20 | POST /api/oauth/email/bind | UserAuth + CriticalRateLimit: controller.EmailBind | router/api-router.go:47 | NOT_STARTED |  |
| 21 | GET /api/oauth/wechat | CriticalRateLimit + DisableCache: controller.WeChatAuth | router/api-router.go:49 | NOT_STARTED |  |
| 22 | POST /api/oauth/wechat/bind | UserAuth + CriticalRateLimit: controller.WeChatBind | router/api-router.go:50 | NOT_STARTED |  |
| 23 | GET /api/oauth/telegram/login | CriticalRateLimit + DisableCache: controller.TelegramLogin | router/api-router.go:51 | NOT_STARTED |  |
| 24 | POST /api/oauth/telegram/bind/start | UserAuth + CriticalRateLimit + DisableCache: controller.TelegramBindStart | router/api-router.go:52 | NOT_STARTED |  |
| 25 | GET /api/oauth/telegram/bind/:flow_token | CriticalRateLimit + DisableCache: controller.TelegramBind | router/api-router.go:53 | NOT_STARTED |  |
| 26 | GET /api/oauth/:provider | CriticalRateLimit + DisableCache + TryUserAuth: controller.HandleOAuth (GitHub/Discord/OIDC/LinuxDO) | router/api-router.go:55 | NOT_STARTED |  |
| 27 | GET /api/ratio_config | CriticalRateLimit: controller.GetRatioConfig | router/api-router.go:56 | NOT_STARTED |  |
| 28 | POST /api/stripe/webhook | AnonymousRequestBodyLimit: controller.StripeWebhook | router/api-router.go:58 | NOT_STARTED |  |
| 29 | POST /api/creem/webhook | AnonymousRequestBodyLimit: controller.CreemWebhook | router/api-router.go:59 | NOT_STARTED |  |
| 30 | POST /api/waffo/webhook | AnonymousRequestBodyLimit: controller.WaffoWebhook | router/api-router.go:60 | NOT_STARTED |  |
| 31 | POST /api/waffo-pancake/webhook/:env | AnonymousRequestBodyLimit: controller.WaffoPancakeWebhook | router/api-router.go:63 | NOT_STARTED |  |
| 32 | POST /api/verify | UserAuth + CriticalRateLimit + DisableCache: controller.UniversalVerify | router/api-router.go:66 | NOT_STARTED |  |
| 33 | POST /api/user/auth/refresh | SessionCookieOriginGuard + CriticalRateLimit + DisableCache: controller.RefreshAuth | router/api-router.go:70 | NOT_STARTED |  |
| 34 | POST /api/user/auth/logout | SessionCookieOriginGuard + CriticalRateLimit + DisableCache: controller.AuthLogout | router/api-router.go:71 | NOT_STARTED |  |
| 35 | POST /api/user/register | CriticalRateLimit + AnonymousRequestBodyLimit + TurnstileCheck: controller.Register | router/api-router.go:72 | NOT_STARTED |  |
| 36 | POST /api/user/login | CriticalRateLimit + DisableCache + AnonymousRequestBodyLimit + TurnstileCheck: controller.Login | router/api-router.go:73 | NOT_STARTED |  |
| 37 | POST /api/user/login/2fa | CriticalRateLimit + DisableCache + AnonymousRequestBodyLimit: controller.Verify2FALogin | router/api-router.go:74 | NOT_STARTED |  |
| 38 | POST /api/user/passkey/login/begin | CriticalRateLimit + DisableCache + AnonymousRequestBodyLimit: controller.PasskeyLoginBegin | router/api-router.go:75 | NOT_STARTED |  |
| 39 | POST /api/user/passkey/login/finish | CriticalRateLimit + DisableCache + AnonymousRequestBodyLimit: controller.PasskeyLoginFinish | router/api-router.go:76 | NOT_STARTED |  |
| 40 | POST /api/user/epay/notify | AnonymousRequestBodyLimit: controller.EpayNotify | router/api-router.go:78 | NOT_STARTED |  |
| 41 | GET /api/user/epay/notify | Public: controller.EpayNotify | router/api-router.go:79 | NOT_STARTED |  |
| 42 | GET /api/user/groups | Public: controller.GetUserGroups | router/api-router.go:80 | NOT_STARTED |  |
| 43 | GET /api/user/sessions | UserAuth + DisableCache: controller.GetLoginSessions | router/api-router.go:85 | NOT_STARTED |  |
| 44 | DELETE /api/user/sessions/:sid | UserAuth + DisableCache: controller.DeleteLoginSession | router/api-router.go:86 | NOT_STARTED |  |
| 45 | POST /api/user/sessions/revoke-others | UserAuth + DisableCache: controller.RevokeOtherLoginSessions | router/api-router.go:87 | NOT_STARTED |  |
| 46 | GET /api/user/self/groups | UserAuth: controller.GetUserGroups | router/api-router.go:88 | NOT_STARTED |  |
| 47 | GET /api/user/self | UserAuth: controller.GetSelf | router/api-router.go:89 | NOT_STARTED |  |
| 48 | GET /api/user/models | UserAuth: controller.GetUserModels | router/api-router.go:90 | NOT_STARTED |  |
| 49 | PUT /api/user/self | UserAuth + CriticalRateLimit + DisableCache: controller.UpdateSelf | router/api-router.go:91 | NOT_STARTED |  |
| 50 | DELETE /api/user/self | UserAuth: controller.DeleteSelf | router/api-router.go:92 | NOT_STARTED |  |
| 51 | GET /api/user/token | UserAuth + CriticalRateLimit + UserCriticalRateLimit('access-token') + DisableCache: controller.GenerateAccessToken | router/api-router.go:93 | NOT_STARTED |  |
| 52 | GET /api/user/passkey | UserAuth: controller.PasskeyStatus | router/api-router.go:94 | NOT_STARTED |  |
| 53 | POST /api/user/passkey/register/begin | UserAuth + DisableCache: controller.PasskeyRegisterBegin | router/api-router.go:95 | NOT_STARTED |  |
| 54 | POST /api/user/passkey/register/finish | UserAuth + DisableCache: controller.PasskeyRegisterFinish | router/api-router.go:96 | NOT_STARTED |  |
| 55 | POST /api/user/passkey/verify/begin | UserAuth + DisableCache: controller.PasskeyVerifyBegin | router/api-router.go:97 | NOT_STARTED |  |
| 56 | POST /api/user/passkey/verify/finish | UserAuth + DisableCache: controller.PasskeyVerifyFinish | router/api-router.go:98 | NOT_STARTED |  |
| 57 | DELETE /api/user/passkey | UserAuth + DisableCache: controller.PasskeyDelete | router/api-router.go:99 | NOT_STARTED |  |
| 58 | GET /api/user/aff | UserAuth: controller.GetAffCode | router/api-router.go:100 | NOT_STARTED |  |
| 59 | GET /api/user/topup/info | UserAuth: controller.GetTopUpInfo | router/api-router.go:101 | NOT_STARTED |  |
| 60 | GET /api/user/topup/self | UserAuth: controller.GetUserTopUps | router/api-router.go:102 | NOT_STARTED |  |
| 61 | POST /api/user/topup | UserAuth + CriticalRateLimit: controller.TopUp | router/api-router.go:103 | NOT_STARTED |  |
| 62 | POST /api/user/pay | UserAuth + CriticalRateLimit: controller.RequestEpay | router/api-router.go:104 | NOT_STARTED |  |
| 63 | POST /api/user/amount | UserAuth: controller.RequestAmount | router/api-router.go:105 | NOT_STARTED |  |
| 64 | POST /api/user/stripe/pay | UserAuth + CriticalRateLimit: controller.RequestStripePay | router/api-router.go:106 | NOT_STARTED |  |
| 65 | POST /api/user/stripe/amount | UserAuth: controller.RequestStripeAmount | router/api-router.go:107 | NOT_STARTED |  |
| 66 | POST /api/user/creem/pay | UserAuth + CriticalRateLimit: controller.RequestCreemPay | router/api-router.go:108 | NOT_STARTED |  |
| 67 | POST /api/user/waffo/amount | UserAuth: controller.RequestWaffoAmount | router/api-router.go:109 | NOT_STARTED |  |
| 68 | POST /api/user/waffo/pay | UserAuth + CriticalRateLimit: controller.RequestWaffoPay | router/api-router.go:110 | NOT_STARTED |  |
| 69 | POST /api/user/waffo-pancake/amount | UserAuth: controller.RequestWaffoPancakeAmount | router/api-router.go:111 | NOT_STARTED |  |
| 70 | POST /api/user/waffo-pancake/pay | UserAuth + CriticalRateLimit: controller.RequestWaffoPancakePay | router/api-router.go:112 | NOT_STARTED |  |
| 71 | POST /api/user/aff_transfer | UserAuth + UserCriticalRateLimit('aff-transfer'): controller.TransferAffQuota | router/api-router.go:113 | NOT_STARTED |  |
| 72 | PUT /api/user/setting | UserAuth: controller.UpdateUserSetting | router/api-router.go:114 | NOT_STARTED |  |
| 73 | GET /api/user/2fa/status | UserAuth: controller.Get2FAStatus | router/api-router.go:117 | NOT_STARTED |  |
| 74 | POST /api/user/2fa/setup | UserAuth + DisableCache: controller.Setup2FA | router/api-router.go:118 | NOT_STARTED |  |
| 75 | POST /api/user/2fa/enable | UserAuth + DisableCache: controller.Enable2FA | router/api-router.go:119 | NOT_STARTED |  |
| 76 | POST /api/user/2fa/disable | UserAuth + DisableCache: controller.Disable2FA | router/api-router.go:120 | NOT_STARTED |  |
| 77 | POST /api/user/2fa/backup_codes | UserAuth + DisableCache: controller.RegenerateBackupCodes | router/api-router.go:121 | NOT_STARTED |  |
| 78 | GET /api/user/checkin | UserAuth: controller.GetCheckinStatus | router/api-router.go:124 | NOT_STARTED |  |
| 79 | POST /api/user/checkin | UserAuth + TurnstileCheck: controller.DoCheckin | router/api-router.go:125 | NOT_STARTED |  |
| 80 | GET /api/user/oauth/bindings | UserAuth: controller.GetUserOAuthBindings | router/api-router.go:128 | NOT_STARTED |  |
| 81 | DELETE /api/user/oauth/bindings/:provider_id | UserAuth: controller.UnbindCustomOAuth | router/api-router.go:129 | NOT_STARTED |  |
| 82 | GET /api/user/ | AdminAuth: controller.GetAllUsers (admin user management) | router/api-router.go:135 | NOT_STARTED |  |
| 83 | GET /api/user/topup | AdminAuth: controller.GetAllTopUps | router/api-router.go:136 | NOT_STARTED |  |
| 84 | POST /api/user/topup/complete | AdminAuth: controller.AdminCompleteTopUp | router/api-router.go:137 | NOT_STARTED |  |
| 85 | GET /api/user/search | AdminAuth: controller.SearchUsers | router/api-router.go:138 | NOT_STARTED |  |
| 86 | GET /api/user/:id/oauth/bindings | AdminAuth: controller.GetUserOAuthBindingsByAdmin | router/api-router.go:139 | NOT_STARTED |  |
| 87 | DELETE /api/user/:id/oauth/bindings/:provider_id | AdminAuth: controller.UnbindCustomOAuthByAdmin | router/api-router.go:140 | NOT_STARTED |  |
| 88 | DELETE /api/user/:id/bindings/:binding_type | AdminAuth: controller.AdminClearUserBinding | router/api-router.go:141 | NOT_STARTED |  |
| 89 | GET /api/user/:id | AdminAuth: controller.GetUser | router/api-router.go:142 | NOT_STARTED |  |
| 90 | POST /api/user/ | AdminAuth: controller.CreateUser | router/api-router.go:143 | NOT_STARTED |  |
| 91 | POST /api/user/manage | AdminAuth: controller.ManageUser | router/api-router.go:144 | NOT_STARTED |  |
| 92 | PUT /api/user/ | AdminAuth: controller.UpdateUser | router/api-router.go:145 | NOT_STARTED |  |
| 93 | DELETE /api/user/:id | AdminAuth: controller.DeleteUser | router/api-router.go:146 | NOT_STARTED |  |
| 94 | DELETE /api/user/:id/reset_passkey | AdminAuth: controller.AdminResetPasskey | router/api-router.go:147 | NOT_STARTED |  |
| 95 | GET /api/user/2fa/stats | AdminAuth: controller.Admin2FAStats | router/api-router.go:150 | NOT_STARTED |  |
| 96 | DELETE /api/user/:id/2fa | AdminAuth: controller.AdminDisable2FA | router/api-router.go:151 | NOT_STARTED |  |
| 97 | GET /api/subscription/plans | UserAuth: controller.GetSubscriptionPlans | router/api-router.go:159 | NOT_STARTED |  |
| 98 | GET /api/subscription/self | UserAuth: controller.GetSubscriptionSelf | router/api-router.go:160 | NOT_STARTED |  |
| 99 | PUT /api/subscription/self/preference | UserAuth: controller.UpdateSubscriptionPreference | router/api-router.go:161 | NOT_STARTED |  |
| 100 | POST /api/subscription/balance/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestBalancePay | router/api-router.go:162 | NOT_STARTED |  |
| 101 | POST /api/subscription/epay/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestEpay | router/api-router.go:163 | NOT_STARTED |  |
| 102 | POST /api/subscription/stripe/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestStripePay | router/api-router.go:164 | NOT_STARTED |  |
| 103 | POST /api/subscription/creem/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestCreemPay | router/api-router.go:165 | NOT_STARTED |  |
| 104 | POST /api/subscription/waffo-pancake/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestWaffoPancakePay | router/api-router.go:166 | NOT_STARTED |  |
| 105 | GET /api/subscription/admin/plans | AdminAuth: controller.AdminListSubscriptionPlans | router/api-router.go:171 | NOT_STARTED |  |
| 106 | POST /api/subscription/admin/plans | AdminAuth: controller.AdminCreateSubscriptionPlan | router/api-router.go:172 | NOT_STARTED |  |
| 107 | PUT /api/subscription/admin/plans/:id | AdminAuth: controller.AdminUpdateSubscriptionPlan | router/api-router.go:173 | NOT_STARTED |  |
| 108 | PATCH /api/subscription/admin/plans/:id | AdminAuth: controller.AdminUpdateSubscriptionPlanStatus | router/api-router.go:174 | NOT_STARTED |  |
| 109 | POST /api/subscription/admin/bind | AdminAuth: controller.AdminBindSubscription | router/api-router.go:175 | NOT_STARTED |  |
| 110 | POST /api/subscription/admin/plans/:id/subscriptions/reset | AdminAuth: controller.AdminResetPlanSubscriptions | router/api-router.go:176 | NOT_STARTED |  |
| 111 | GET /api/subscription/admin/users/:id/subscriptions | AdminAuth: controller.AdminListUserSubscriptions | router/api-router.go:179 | NOT_STARTED |  |
| 112 | POST /api/subscription/admin/users/:id/subscriptions | AdminAuth: controller.AdminCreateUserSubscription | router/api-router.go:180 | NOT_STARTED |  |
| 113 | POST /api/subscription/admin/users/:id/subscriptions/reset | AdminAuth: controller.AdminResetUserSubscriptionsByPlan | router/api-router.go:181 | NOT_STARTED |  |
| 114 | POST /api/subscription/admin/user_subscriptions/:id/invalidate | AdminAuth: controller.AdminInvalidateUserSubscription | router/api-router.go:182 | NOT_STARTED |  |
| 115 | DELETE /api/subscription/admin/user_subscriptions/:id | AdminAuth: controller.AdminDeleteUserSubscription | router/api-router.go:183 | NOT_STARTED |  |
| 116 | POST /api/subscription/epay/notify | AnonymousRequestBodyLimit: controller.SubscriptionEpayNotify (no auth callback) | router/api-router.go:187 | NOT_STARTED |  |
| 117 | GET /api/subscription/epay/notify | Public: controller.SubscriptionEpayNotify | router/api-router.go:188 | NOT_STARTED |  |
| 118 | GET /api/subscription/epay/return | Public: controller.SubscriptionEpayReturn | router/api-router.go:189 | NOT_STARTED |  |
| 119 | POST /api/subscription/epay/return | AnonymousRequestBodyLimit: controller.SubscriptionEpayReturn | router/api-router.go:190 | NOT_STARTED |  |
| 120 | GET /api/option/ | RootAuth: controller.GetOptions | router/api-router.go:194 | NOT_STARTED |  |
| 121 | PUT /api/option/ | RootAuth: controller.UpdateOption | router/api-router.go:195 | NOT_STARTED |  |
| 122 | POST /api/option/payment_compliance | RootAuth: controller.ConfirmPaymentCompliance | router/api-router.go:196 | NOT_STARTED |  |
| 123 | GET /api/option/channel_affinity_cache | RootAuth: controller.GetChannelAffinityCacheStats | router/api-router.go:197 | NOT_STARTED |  |
| 124 | DELETE /api/option/channel_affinity_cache | RootAuth: controller.ClearChannelAffinityCache | router/api-router.go:198 | NOT_STARTED |  |
| 125 | POST /api/option/rest_model_ratio | RootAuth: controller.ResetModelRatio | router/api-router.go:199 | NOT_STARTED |  |
| 126 | GET /api/option/waffo-pancake/catalog | RootAuth: controller.ListWaffoPancakeCatalog | router/api-router.go:200 | NOT_STARTED |  |
| 127 | POST /api/option/waffo-pancake/pair | RootAuth: controller.CreateWaffoPancakePair | router/api-router.go:201 | NOT_STARTED |  |
| 128 | POST /api/option/waffo-pancake/save | RootAuth: controller.SaveWaffoPancake | router/api-router.go:202 | NOT_STARTED |  |
| 129 | POST /api/option/waffo-pancake/subscription-product | RootAuth: controller.CreateWaffoPancakeSubscriptionProduct | router/api-router.go:203 | NOT_STARTED |  |
| 130 | GET /api/option/waffo-pancake/subscription-product-options | RootAuth: controller.ListWaffoPancakeSubscriptionProductOptions | router/api-router.go:204 | NOT_STARTED |  |
| 131 | POST /api/custom-oauth-provider/discovery | RootAuth: controller.FetchCustomOAuthDiscovery | router/api-router.go:211 | NOT_STARTED |  |
| 132 | GET /api/custom-oauth-provider/ | RootAuth: controller.GetCustomOAuthProviders | router/api-router.go:212 | NOT_STARTED |  |
| 133 | GET /api/custom-oauth-provider/:id | RootAuth: controller.GetCustomOAuthProvider | router/api-router.go:213 | NOT_STARTED |  |
| 134 | POST /api/custom-oauth-provider/ | RootAuth: controller.CreateCustomOAuthProvider | router/api-router.go:214 | NOT_STARTED |  |
| 135 | PUT /api/custom-oauth-provider/:id | RootAuth: controller.UpdateCustomOAuthProvider | router/api-router.go:215 | NOT_STARTED |  |
| 136 | DELETE /api/custom-oauth-provider/:id | RootAuth: controller.DeleteCustomOAuthProvider | router/api-router.go:216 | NOT_STARTED |  |
| 137 | GET /api/performance/stats | RootAuth: controller.GetPerformanceStats | router/api-router.go:221 | NOT_STARTED |  |
| 138 | DELETE /api/performance/disk_cache | RootAuth: controller.ClearDiskCache | router/api-router.go:222 | NOT_STARTED |  |
| 139 | POST /api/performance/reset_stats | RootAuth: controller.ResetPerformanceStats | router/api-router.go:223 | NOT_STARTED |  |
| 140 | POST /api/performance/gc | RootAuth: controller.ForceGC | router/api-router.go:224 | NOT_STARTED |  |
| 141 | GET /api/performance/logs | RootAuth: controller.GetLogFiles | router/api-router.go:225 | NOT_STARTED |  |
| 142 | DELETE /api/performance/logs | RootAuth: controller.CleanupLogFiles | router/api-router.go:226 | NOT_STARTED |  |
| 143 | GET /api/ratio_sync/channels | RootAuth: controller.GetSyncableChannels | router/api-router.go:231 | NOT_STARTED |  |
| 144 | POST /api/ratio_sync/fetch | RootAuth: controller.FetchUpstreamRatios | router/api-router.go:232 | NOT_STARTED |  |
| 145 | POST /api/channel/:id/key | AdminAuth + RootAuth + CriticalRateLimit + DisableCache + SecureVerificationRequired: controller.GetChannelKey | router/channel-router.go:23 | NOT_STARTED |  |
| 146 | GET /api/channel/ | AdminAuth + RequirePermission(ChannelRead): controller.GetAllChannels | router/channel-router.go:40 | NOT_STARTED |  |
| 147 | GET /api/channel/search | AdminAuth + RequirePermission(ChannelRead): controller.SearchChannels | router/channel-router.go:41 | NOT_STARTED |  |
| 148 | GET /api/channel/models | AdminAuth + RequirePermission(ChannelRead): controller.ChannelListModels | router/channel-router.go:42 | NOT_STARTED |  |
| 149 | GET /api/channel/models_enabled | AdminAuth + RequirePermission(ChannelRead): controller.EnabledListModels | router/channel-router.go:43 | NOT_STARTED |  |
| 150 | GET /api/channel/ops | AdminAuth + RequirePermission(ChannelRead): controller.GetChannelOps | router/channel-router.go:44 | NOT_STARTED |  |
| 151 | GET /api/channel/:id | AdminAuth + RequirePermission(ChannelRead): controller.GetChannel | router/channel-router.go:45 | NOT_STARTED |  |
| 152 | GET /api/channel/test | AdminAuth + RequirePermission(ChannelOperate): controller.TestAllChannels | router/channel-router.go:46 | NOT_STARTED |  |
| 153 | GET /api/channel/test/:id | AdminAuth + RequirePermission(ChannelOperate): controller.TestChannel | router/channel-router.go:47 | NOT_STARTED |  |
| 154 | GET /api/channel/update_balance | AdminAuth + RequirePermission(ChannelOperate): controller.UpdateAllChannelsBalance | router/channel-router.go:48 | NOT_STARTED |  |
| 155 | GET /api/channel/update_balance/:id | AdminAuth + RequirePermission(ChannelOperate): controller.UpdateChannelBalance | router/channel-router.go:49 | NOT_STARTED |  |
| 156 | POST /api/channel/ | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.AddChannel | router/channel-router.go:50 | NOT_STARTED |  |
| 157 | PUT /api/channel/ | AdminAuth + RequirePermission(ChannelWrite): controller.UpdateChannel | router/channel-router.go:51 | NOT_STARTED |  |
| 158 | POST /api/channel/status/batch | AdminAuth + RequirePermission(ChannelOperate): controller.BatchUpdateChannelStatus | router/channel-router.go:52 | NOT_STARTED |  |
| 159 | POST /api/channel/:id/status | AdminAuth + RequirePermission(ChannelOperate): controller.UpdateChannelStatus | router/channel-router.go:53 | NOT_STARTED |  |
| 160 | DELETE /api/channel/disabled | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.DeleteDisabledChannel | router/channel-router.go:54 | NOT_STARTED |  |
| 161 | POST /api/channel/tag/disabled | AdminAuth + RequirePermission(ChannelOperate): controller.DisableTagChannels | router/channel-router.go:55 | NOT_STARTED |  |
| 162 | POST /api/channel/tag/enabled | AdminAuth + RequirePermission(ChannelOperate): controller.EnableTagChannels | router/channel-router.go:56 | NOT_STARTED |  |
| 163 | PUT /api/channel/tag | AdminAuth + RequirePermission(ChannelWrite): controller.EditTagChannels | router/channel-router.go:57 | NOT_STARTED |  |
| 164 | DELETE /api/channel/:id | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.DeleteChannel | router/channel-router.go:58 | NOT_STARTED |  |
| 165 | POST /api/channel/batch | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.DeleteChannelBatch | router/channel-router.go:59 | NOT_STARTED |  |
| 166 | POST /api/channel/fix | AdminAuth + RequirePermission(ChannelOperate): controller.FixChannelsAbilities | router/channel-router.go:60 | NOT_STARTED |  |
| 167 | GET /api/channel/fetch_models/:id | AdminAuth + RequirePermission(ChannelOperate): controller.FetchUpstreamModels | router/channel-router.go:61 | NOT_STARTED |  |
| 168 | POST /api/channel/fetch_models | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.FetchModels | router/channel-router.go:62 | NOT_STARTED |  |
| 169 | POST /api/channel/:id/codex/refresh | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.RefreshCodexChannelCredential | router/channel-router.go:63 | NOT_STARTED |  |
| 170 | GET /api/channel/:id/codex/usage | AdminAuth + RequirePermission(ChannelRead): controller.GetCodexChannelUsage | router/channel-router.go:64 | NOT_STARTED |  |
| 171 | GET /api/channel/:id/codex/usage/reset-credits | AdminAuth + RequirePermission(ChannelRead): controller.GetCodexChannelRateLimitResetCredits | router/channel-router.go:65 | NOT_STARTED |  |
| 172 | POST /api/channel/:id/codex/usage/reset | AdminAuth + RequirePermission(ChannelOperate): controller.ResetCodexChannelUsage | router/channel-router.go:66 | NOT_STARTED |  |
| 173 | POST /api/channel/ollama/pull | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.OllamaPullModel | router/channel-router.go:67 | NOT_STARTED |  |
| 174 | POST /api/channel/ollama/pull/stream | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.OllamaPullModelStream | router/channel-router.go:68 | NOT_STARTED |  |
| 175 | DELETE /api/channel/ollama/delete | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.OllamaDeleteModel | router/channel-router.go:69 | NOT_STARTED |  |
| 176 | GET /api/channel/ollama/version/:id | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.OllamaVersion | router/channel-router.go:70 | NOT_STARTED |  |
| 177 | POST /api/channel/batch/tag | AdminAuth + RequirePermission(ChannelWrite): controller.BatchSetChannelTag | router/channel-router.go:71 | NOT_STARTED |  |
| 178 | GET /api/channel/tag/models | AdminAuth + RequirePermission(ChannelRead): controller.GetTagModels | router/channel-router.go:72 | NOT_STARTED |  |
| 179 | POST /api/channel/copy/:id | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.CopyChannel | router/channel-router.go:73 | NOT_STARTED |  |
| 180 | POST /api/channel/multi_key/manage | AdminAuth + RequirePermission(ChannelOperate): controller.ManageMultiKeys | router/channel-router.go:74 | NOT_STARTED |  |
| 181 | POST /api/channel/upstream_updates/apply | AdminAuth + RequirePermission(ChannelWrite): controller.ApplyChannelUpstreamModelUpdates | router/channel-router.go:75 | NOT_STARTED |  |
| 182 | POST /api/channel/upstream_updates/apply_all | AdminAuth + RequirePermission(ChannelWrite): controller.ApplyAllChannelUpstreamModelUpdates | router/channel-router.go:76 | NOT_STARTED |  |
| 183 | POST /api/channel/upstream_updates/detect | AdminAuth + RequirePermission(ChannelOperate): controller.DetectChannelUpstreamModelUpdates | router/channel-router.go:77 | NOT_STARTED |  |
| 184 | POST /api/channel/upstream_updates/detect_all | AdminAuth + RequirePermission(ChannelOperate): controller.DetectAllChannelUpstreamModelUpdates | router/channel-router.go:78 | NOT_STARTED |  |
| 185 | GET /api/authz/catalog | AdminAuth: controller.GetPermissionCatalog | router/authz-router.go:17 | NOT_STARTED |  |
| 186 | GET /api/token/ | UserAuth: controller.GetAllTokens | router/api-router.go:239 | NOT_STARTED |  |
| 187 | GET /api/token/search | UserAuth + SearchRateLimit: controller.SearchTokens | router/api-router.go:240 | NOT_STARTED |  |
| 188 | GET /api/token/auto-groups | UserAuth: controller.GetTokenAutoGroups | router/api-router.go:241 | NOT_STARTED |  |
| 189 | GET /api/token/:id | UserAuth: controller.GetToken | router/api-router.go:242 | NOT_STARTED |  |
| 190 | POST /api/token/:id/key | UserAuth + CriticalRateLimit + DisableCache: controller.GetTokenKey | router/api-router.go:243 | NOT_STARTED |  |
| 191 | POST /api/token/ | UserAuth: controller.AddToken | router/api-router.go:244 | NOT_STARTED |  |
| 192 | PUT /api/token/ | UserAuth: controller.UpdateToken | router/api-router.go:245 | NOT_STARTED |  |
| 193 | DELETE /api/token/:id | UserAuth: controller.DeleteToken | router/api-router.go:246 | NOT_STARTED |  |
| 194 | POST /api/token/batch | UserAuth: controller.DeleteTokenBatch | router/api-router.go:247 | NOT_STARTED |  |
| 195 | POST /api/token/batch/keys | UserAuth + CriticalRateLimit + DisableCache: controller.GetTokenKeysBatch | router/api-router.go:248 | NOT_STARTED |  |
| 196 | GET /api/usage/token/ | CORS + CriticalRateLimit + TokenAuthReadOnly: controller.GetTokenUsage | router/api-router.go:257 | NOT_STARTED |  |
| 197 | GET /api/redemption/ | AdminAuth: controller.GetAllRedemptions | router/api-router.go:264 | NOT_STARTED |  |
| 198 | GET /api/redemption/search | AdminAuth: controller.SearchRedemptions | router/api-router.go:265 | NOT_STARTED |  |
| 199 | GET /api/redemption/:id | AdminAuth: controller.GetRedemption | router/api-router.go:266 | NOT_STARTED |  |
| 200 | POST /api/redemption/ | AdminAuth: controller.AddRedemption | router/api-router.go:267 | NOT_STARTED |  |
| 201 | PUT /api/redemption/ | AdminAuth: controller.UpdateRedemption | router/api-router.go:268 | NOT_STARTED |  |
| 202 | DELETE /api/redemption/invalid | AdminAuth: controller.DeleteInvalidRedemption | router/api-router.go:269 | NOT_STARTED |  |
| 203 | DELETE /api/redemption/:id | AdminAuth: controller.DeleteRedemption | router/api-router.go:270 | NOT_STARTED |  |
| 204 | GET /api/log/ | AdminAuth: controller.GetAllLogs | router/api-router.go:273 | NOT_STARTED |  |
| 205 | GET /api/log/stat | AdminAuth: controller.GetLogsStat | router/api-router.go:274 | NOT_STARTED |  |
| 206 | GET /api/log/self/stat | UserAuth: controller.GetLogsSelfStat | router/api-router.go:275 | NOT_STARTED |  |
| 207 | GET /api/log/channel_affinity_usage_cache | AdminAuth: controller.GetChannelAffinityUsageCacheStats | router/api-router.go:276 | NOT_STARTED |  |
| 208 | GET /api/log/search | AdminAuth: controller.SearchAllLogs | router/api-router.go:277 | NOT_STARTED |  |
| 209 | GET /api/log/self | UserAuth: controller.GetUserLogs | router/api-router.go:278 | NOT_STARTED |  |
| 210 | GET /api/log/self/search | UserAuth + SearchRateLimit: controller.SearchUserLogs | router/api-router.go:279 | NOT_STARTED |  |
| 211 | GET /api/log/token | CORS + CriticalRateLimit + TokenAuthReadOnly: controller.GetLogByKey | router/api-router.go:306 | NOT_STARTED |  |
| 212 | POST /api/system-task/log-cleanup | RootAuth: controller.CreateLogCleanupSystemTask | router/api-router.go:284 | NOT_STARTED |  |
| 213 | GET /api/system-task/list | RootAuth: controller.ListSystemTasks | router/api-router.go:285 | NOT_STARTED |  |
| 214 | GET /api/system-task/current | RootAuth: controller.GetCurrentSystemTask | router/api-router.go:286 | NOT_STARTED |  |
| 215 | GET /api/system-task/:task_id | RootAuth: controller.GetSystemTask | router/api-router.go:287 | NOT_STARTED |  |
| 216 | GET /api/system-info/instances | RootAuth: controller.ListSystemInstances | router/api-router.go:292 | NOT_STARTED |  |
| 217 | DELETE /api/system-info/stale-instances | RootAuth: controller.DeleteStaleSystemInstances | router/api-router.go:293 | NOT_STARTED |  |
| 218 | DELETE /api/system-info/instances/:node_name | RootAuth: controller.DeleteStaleSystemInstance | router/api-router.go:294 | NOT_STARTED |  |
| 219 | GET /api/data/ | AdminAuth: controller.GetAllQuotaDates | router/api-router.go:298 | NOT_STARTED |  |
| 220 | GET /api/data/users | AdminAuth: controller.GetQuotaDatesByUser | router/api-router.go:299 | NOT_STARTED |  |
| 221 | GET /api/data/self | UserAuth: controller.GetUserQuotaDates | router/api-router.go:300 | NOT_STARTED |  |
| 222 | GET /api/data/flow | AdminAuth: controller.GetAllFlowQuotaDates | router/api-router.go:301 | NOT_STARTED |  |
| 223 | GET /api/data/flow/self | UserAuth: controller.GetUserFlowQuotaDates | router/api-router.go:302 | NOT_STARTED |  |
| 224 | GET /api/group/ | AdminAuth: controller.GetGroups | router/api-router.go:311 | NOT_STARTED |  |
| 225 | GET /api/prefill_group/ | AdminAuth: controller.GetPrefillGroups | router/api-router.go:317 | NOT_STARTED |  |
| 226 | POST /api/prefill_group/ | AdminAuth: controller.CreatePrefillGroup | router/api-router.go:318 | NOT_STARTED |  |
| 227 | PUT /api/prefill_group/ | AdminAuth: controller.UpdatePrefillGroup | router/api-router.go:319 | NOT_STARTED |  |
| 228 | DELETE /api/prefill_group/:id | AdminAuth: controller.DeletePrefillGroup | router/api-router.go:320 | NOT_STARTED |  |
| 229 | GET /api/mj/self | UserAuth: controller.GetUserMidjourney | router/api-router.go:324 | NOT_STARTED |  |
| 230 | GET /api/mj/ | AdminAuth: controller.GetAllMidjourney | router/api-router.go:325 | NOT_STARTED |  |
| 231 | GET /api/task/self | UserAuth: controller.GetUserTask | router/api-router.go:329 | NOT_STARTED |  |
| 232 | GET /api/task/ | AdminAuth: controller.GetAllTask | router/api-router.go:330 | NOT_STARTED |  |
| 233 | GET /api/vendors/ | AdminAuth: controller.GetAllVendors | router/api-router.go:336 | NOT_STARTED |  |
| 234 | GET /api/vendors/search | AdminAuth: controller.SearchVendors | router/api-router.go:337 | NOT_STARTED |  |
| 235 | GET /api/vendors/:id | AdminAuth: controller.GetVendorMeta | router/api-router.go:338 | NOT_STARTED |  |
| 236 | POST /api/vendors/ | AdminAuth: controller.CreateVendorMeta | router/api-router.go:339 | NOT_STARTED |  |
| 237 | PUT /api/vendors/ | AdminAuth: controller.UpdateVendorMeta | router/api-router.go:340 | NOT_STARTED |  |
| 238 | DELETE /api/vendors/:id | AdminAuth: controller.DeleteVendorMeta | router/api-router.go:341 | NOT_STARTED |  |
| 239 | GET /api/models/sync_upstream/preview | AdminAuth: controller.SyncUpstreamPreview | router/api-router.go:347 | NOT_STARTED |  |
| 240 | POST /api/models/sync_upstream | AdminAuth: controller.SyncUpstreamModels | router/api-router.go:348 | NOT_STARTED |  |
| 241 | GET /api/models/missing | AdminAuth: controller.GetMissingModels | router/api-router.go:349 | NOT_STARTED |  |
| 242 | GET /api/models/ | AdminAuth: controller.GetAllModelsMeta | router/api-router.go:350 | NOT_STARTED |  |
| 243 | GET /api/models/search | AdminAuth: controller.SearchModelsMeta | router/api-router.go:351 | NOT_STARTED |  |
| 244 | GET /api/models/:id | AdminAuth: controller.GetModelMeta | router/api-router.go:352 | NOT_STARTED |  |
| 245 | POST /api/models/ | AdminAuth: controller.CreateModelMeta | router/api-router.go:353 | NOT_STARTED |  |
| 246 | PUT /api/models/ | AdminAuth: controller.UpdateModelMeta | router/api-router.go:354 | NOT_STARTED |  |
| 247 | DELETE /api/models/:id | AdminAuth: controller.DeleteModelMeta | router/api-router.go:355 | NOT_STARTED |  |
| 248 | GET /api/deployments/settings | AdminAuth: controller.GetModelDeploymentSettings | router/api-router.go:362 | NOT_STARTED |  |
| 249 | POST /api/deployments/settings/test-connection | AdminAuth: controller.TestIoNetConnection | router/api-router.go:363 | NOT_STARTED |  |
| 250 | GET /api/deployments/ | AdminAuth: controller.GetAllDeployments | router/api-router.go:364 | NOT_STARTED |  |
| 251 | GET /api/deployments/search | AdminAuth: controller.SearchDeployments | router/api-router.go:365 | NOT_STARTED |  |
| 252 | POST /api/deployments/test-connection | AdminAuth: controller.TestIoNetConnection | router/api-router.go:366 | NOT_STARTED |  |
| 253 | GET /api/deployments/hardware-types | AdminAuth: controller.GetHardwareTypes | router/api-router.go:367 | NOT_STARTED |  |
| 254 | GET /api/deployments/locations | AdminAuth: controller.GetLocations | router/api-router.go:368 | NOT_STARTED |  |
| 255 | GET /api/deployments/available-replicas | AdminAuth: controller.GetAvailableReplicas | router/api-router.go:369 | NOT_STARTED |  |
| 256 | POST /api/deployments/price-estimation | AdminAuth: controller.GetPriceEstimation | router/api-router.go:370 | NOT_STARTED |  |
| 257 | GET /api/deployments/check-name | AdminAuth: controller.CheckClusterNameAvailability | router/api-router.go:371 | NOT_STARTED |  |
| 258 | POST /api/deployments/ | AdminAuth: controller.CreateDeployment | router/api-router.go:372 | NOT_STARTED |  |
| 259 | GET /api/deployments/:id | AdminAuth: controller.GetDeployment | router/api-router.go:374 | NOT_STARTED |  |
| 260 | GET /api/deployments/:id/logs | AdminAuth: controller.GetDeploymentLogs | router/api-router.go:375 | NOT_STARTED |  |
| 261 | GET /api/deployments/:id/containers | AdminAuth: controller.ListDeploymentContainers | router/api-router.go:376 | NOT_STARTED |  |
| 262 | GET /api/deployments/:id/containers/:container_id | AdminAuth: controller.GetContainerDetails | router/api-router.go:377 | NOT_STARTED |  |
| 263 | PUT /api/deployments/:id | AdminAuth: controller.UpdateDeployment | router/api-router.go:378 | NOT_STARTED |  |
| 264 | PUT /api/deployments/:id/name | AdminAuth: controller.UpdateDeploymentName | router/api-router.go:379 | NOT_STARTED |  |
| 265 | POST /api/deployments/:id/extend | AdminAuth: controller.ExtendDeployment | router/api-router.go:380 | NOT_STARTED |  |
| 266 | DELETE /api/deployments/:id | AdminAuth: controller.DeleteDeployment | router/api-router.go:381 | NOT_STARTED |  |
| 267 | GET /dashboard/billing/subscription | RouteTag('old_api') + CORS + TokenAuth: controller.GetSubscription | router/dashboard.go:18 | NOT_STARTED |  |
| 268 | GET /v1/dashboard/billing/subscription | RouteTag('old_api') + CORS + TokenAuth: controller.GetSubscription | router/dashboard.go:19 | NOT_STARTED |  |
| 269 | GET /dashboard/billing/usage | RouteTag('old_api') + CORS + TokenAuth: controller.GetUsage | router/dashboard.go:20 | NOT_STARTED |  |
| 270 | GET /v1/dashboard/billing/usage | RouteTag('old_api') + CORS + TokenAuth: controller.GetUsage | router/dashboard.go:21 | NOT_STARTED |  |
| 271 | GET /v1/models | RouteTag('relay') + TokenAuth: controller.ListModels (OpenAI/Anthropic/Gemini dispatch) | router/relay-router.go:23 | NOT_STARTED |  |
| 272 | GET /v1/models/:model | RouteTag('relay') + TokenAuth: controller.RetrieveModel (Anthropic/OpenAI dispatch) | router/relay-router.go:34 | NOT_STARTED |  |
| 273 | GET /v1beta/models | RouteTag('relay') + TokenAuth: controller.ListModels (Gemini) | router/relay-router.go:48 | NOT_STARTED |  |
| 274 | GET /v1beta/openai/models | RouteTag('relay') + TokenAuth: controller.ListModels (OpenAI) | router/relay-router.go:57 | NOT_STARTED |  |
| 275 | POST /pg/chat/completions | RouteTag('relay') + SystemPerformanceCheck + UserAuth + Distribute: controller.Playground | router/relay-router.go:67 | NOT_STARTED |  |
| 276 | GET /v1/realtime | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIRealtime, WebSocket) | router/relay-router.go:78 | NOT_STARTED |  |
| 277 | POST /v1/messages | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatClaude) | router/relay-router.go:88 | NOT_STARTED |  |
| 278 | POST /v1/completions | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAI) | router/relay-router.go:93 | NOT_STARTED |  |
| 279 | POST /v1/chat/completions | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAI) | router/relay-router.go:96 | NOT_STARTED |  |
| 280 | POST /v1/responses | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIResponses) | router/relay-router.go:101 | NOT_STARTED |  |
| 281 | POST /v1/responses/compact | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIResponsesCompaction) | router/relay-router.go:104 | NOT_STARTED |  |
| 282 | POST /v1/alpha/search | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIAlphaSearch) | router/relay-router.go:109 | NOT_STARTED |  |
| 283 | POST /v1/edits | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIImage) | router/relay-router.go:114 | NOT_STARTED |  |
| 284 | POST /v1/images/generations | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIImage) | router/relay-router.go:117 | NOT_STARTED |  |
| 285 | POST /v1/images/edits | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIImage) | router/relay-router.go:120 | NOT_STARTED |  |
| 286 | POST /v1/embeddings | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatEmbedding) | router/relay-router.go:125 | NOT_STARTED |  |
| 287 | POST /v1/audio/transcriptions | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIAudio) | router/relay-router.go:130 | NOT_STARTED |  |
| 288 | POST /v1/audio/translations | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIAudio) | router/relay-router.go:133 | NOT_STARTED |  |
| 289 | POST /v1/audio/speech | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIAudio) | router/relay-router.go:136 | NOT_STARTED |  |
| 290 | POST /v1/rerank | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatRerank) | router/relay-router.go:141 | NOT_STARTED |  |
| 291 | POST /v1/engines/:model/embeddings | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatGemini) | router/relay-router.go:146 | NOT_STARTED |  |
| 292 | POST /v1/models/*path | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatGemini) | router/relay-router.go:149 | NOT_STARTED |  |
| 293 | POST /v1/moderations | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAI) | router/relay-router.go:154 | NOT_STARTED |  |
| 294 | POST /v1/images/variations | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:159 | NOT_STARTED |  |
| 295 | GET /v1/files | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:160 | NOT_STARTED |  |
| 296 | POST /v1/files | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:161 | NOT_STARTED |  |
| 297 | DELETE /v1/files/:id | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:162 | NOT_STARTED |  |
| 298 | GET /v1/files/:id | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:163 | NOT_STARTED |  |
| 299 | GET /v1/files/:id/content | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:164 | NOT_STARTED |  |
| 300 | POST /v1/fine-tunes | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:165 | NOT_STARTED |  |
| 301 | GET /v1/fine-tunes | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:166 | NOT_STARTED |  |
| 302 | GET /v1/fine-tunes/:id | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:167 | NOT_STARTED |  |
| 303 | POST /v1/fine-tunes/:id/cancel | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:168 | NOT_STARTED |  |
| 304 | GET /v1/fine-tunes/:id/events | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:169 | NOT_STARTED |  |
| 305 | DELETE /v1/models/:model | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:170 | NOT_STARTED |  |
| 306 | GET /mj/image/:id | RouteTag('relay') + SystemPerformanceCheck: relay.RelayMidjourneyImage (no TokenAuth) | router/relay-router.go:209 | NOT_STARTED |  |
| 307 | POST /mj/submit/action | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:212 | NOT_STARTED |  |
| 308 | POST /mj/submit/shorten | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:213 | NOT_STARTED |  |
| 309 | POST /mj/submit/modal | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:214 | NOT_STARTED |  |
| 310 | POST /mj/submit/imagine | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:215 | NOT_STARTED |  |
| 311 | POST /mj/submit/change | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:216 | NOT_STARTED |  |
| 312 | POST /mj/submit/simple-change | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:217 | NOT_STARTED |  |
| 313 | POST /mj/submit/describe | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:218 | NOT_STARTED |  |
| 314 | POST /mj/submit/blend | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:219 | NOT_STARTED |  |
| 315 | POST /mj/submit/edits | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:220 | NOT_STARTED |  |
| 316 | POST /mj/submit/video | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:221 | NOT_STARTED |  |
| 317 | GET /mj/task/:id/fetch | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:223 | NOT_STARTED |  |
| 318 | GET /mj/task/:id/image-seed | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:224 | NOT_STARTED |  |
| 319 | POST /mj/task/list-by-condition | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:225 | NOT_STARTED |  |
| 320 | POST /mj/insight-face/swap | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:226 | NOT_STARTED |  |
| 321 | POST /mj/submit/upload-discord-images | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:227 | NOT_STARTED |  |
| 322 | GET /:mode/mj/image/:id | RouteTag('relay') + SystemPerformanceCheck: relay.RelayMidjourneyImage (mode-prefixed MJ) | router/relay-router.go:178-181 | NOT_STARTED |  |
| 323 | POST /:mode/mj/submit/* | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayMidjourney (mode-prefixed submit routes, all actions) | router/relay-router.go:178-181 | NOT_STARTED |  |
| 324 | GET /:mode/mj/task/:id/fetch | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:178-181 | NOT_STARTED |  |
| 325 | POST /suno/submit/:action | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayTask | router/relay-router.go:189 | NOT_STARTED |  |
| 326 | POST /suno/fetch | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayTaskFetch | router/relay-router.go:190 | NOT_STARTED |  |
| 327 | GET /suno/fetch/:id | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayTaskFetch | router/relay-router.go:191 | NOT_STARTED |  |
| 328 | POST /v1beta/models/*path | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatGemini) | router/relay-router.go:202 | NOT_STARTED |  |
| 329 | GET /v1/videos/:task_id/content | RouteTag('relay') + TokenOrUserAuth: controller.VideoProxy | router/video-router.go:16 | NOT_STARTED |  |
| 330 | POST /v1/video/generations | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:23 | NOT_STARTED |  |
| 331 | GET /v1/video/generations/:task_id | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTaskFetch | router/video-router.go:24 | NOT_STARTED |  |
| 332 | POST /v1/videos/:video_id/remix | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:25 | NOT_STARTED |  |
| 333 | POST /v1/videos | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTask (OpenAI-compatible video) | router/video-router.go:30 | NOT_STARTED |  |
| 334 | GET /v1/videos/:task_id | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTaskFetch | router/video-router.go:31 | NOT_STARTED |  |
| 335 | POST /kling/v1/videos/text2video | RouteTag('relay') + KlingRequestConvert + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:38 | NOT_STARTED |  |
| 336 | POST /kling/v1/videos/image2video | RouteTag('relay') + KlingRequestConvert + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:39 | NOT_STARTED |  |
| 337 | GET /kling/v1/videos/text2video/:task_id | RouteTag('relay') + KlingRequestConvert + TokenAuth + Distribute: controller.RelayTaskFetch | router/video-router.go:40 | NOT_STARTED |  |
| 338 | GET /kling/v1/videos/image2video/:task_id | RouteTag('relay') + KlingRequestConvert + TokenAuth + Distribute: controller.RelayTaskFetch | router/video-router.go:41 | NOT_STARTED |  |
| 339 | POST /jimeng/ | RouteTag('relay') + JimengRequestConvert + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:50 | NOT_STARTED |  |
| 340 | GET / (web static) | gzip + GlobalWebRateLimit + Cache + static.Serve: embedded web/dist frontend | router/web-router.go:25-28 | NOT_STARTED |  |
| 341 | NoRoute fallback | gzip + GlobalWebRateLimit + Cache; serves index.html (SPA) or controller.RelayNotFound for /v1,/api,/assets paths | router/web-router.go:29-37 | NOT_STARTED |  |
