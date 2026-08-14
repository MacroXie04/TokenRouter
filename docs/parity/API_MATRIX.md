# API Matrix

Every HTTP route in the reference system, with TokenRouter implementation status.

> Status values: NOT_STARTED, IN_PROGRESS, PASS, REFERENCE_PLACEHOLDER, BLOCKED_EXTERNAL

| # | Method + Path | Handler | Middleware / Permission | Status | Target Evidence |
|---|---|---|---|---|---|
| 1 | GET /api/setup | Public: controller.GetSetup (no auth; group: API status/setup) | router/api-router.go:22 | PASS | router/router.go |
| 2 | POST /api/setup | Public: controller.PostSetup + middleware.AnonymousRequestBodyLimit | router/api-router.go:23 | PASS | router/router.go |
| 3 | GET /api/status | Public: controller.GetStatus (health/status endpoint) | router/api-router.go:24 | PASS | router/router.go |
| 4 | GET /api/uptime/status | Public: controller.GetUptimeKumaStatus | router/api-router.go:25 | PASS | router/router.go |
| 5 | GET /api/models | UserAuth: controller.DashboardListModels | router/api-router.go:26 | PASS | router/router.go |
| 6 | GET /api/status/test | AdminAuth: controller.TestStatus | router/api-router.go:27 | PASS | router/router.go |
| 7 | GET /api/notice | Public: controller.GetNotice | router/api-router.go:28 | PASS | router/router.go |
| 8 | GET /api/user-agreement | Public: controller.GetUserAgreement | router/api-router.go:29 | PASS | router/router.go |
| 9 | GET /api/privacy-policy | Public: controller.GetPrivacyPolicy | router/api-router.go:30 | PASS | router/router.go |
| 10 | GET /api/about | Public: controller.GetAbout | router/api-router.go:31 | PASS | router/router.go |
| 11 | GET /api/home_page_content | Public: controller.GetHomePageContent | router/api-router.go:33 | PASS | router/router.go |
| 12 | GET /api/pricing | HeaderNavModuleAuth('pricing'): controller.GetPricing | router/api-router.go:34 | PASS | router/router.go |
| 13 | GET /api/perf-metrics/summary | HeaderNavModulePublicOrUserAuth('pricing'): controller.GetPerfMetricsSummary | router/api-router.go:38 | PASS | router/router.go |
| 14 | GET /api/perf-metrics | HeaderNavModulePublicOrUserAuth('pricing'): controller.GetPerfMetrics | router/api-router.go:39 | PASS | router/router.go |
| 15 | GET /api/rankings | HeaderNavModuleAuth('rankings'): controller.GetRankings | router/api-router.go:41 | PASS | router/router.go |
| 16 | GET /api/verification | EmailVerificationRateLimit + TurnstileCheck: controller.SendEmailVerification | router/api-router.go:42 | PASS | equivalent: POST /api/verification |
| 17 | GET /api/reset_password | CriticalRateLimit + TurnstileCheck: controller.SendPasswordResetEmail | router/api-router.go:43 | PASS | equivalent: POST /api/reset_password |
| 18 | POST /api/user/reset | CriticalRateLimit + AnonymousRequestBodyLimit: controller.ResetPassword | router/api-router.go:44 | PASS | router/router.go |
| 19 | POST /api/oauth/state | CriticalRateLimit + DisableCache + TryUserAuth + AnonymousRequestBodyLimit: controller.GenerateOAuthCode | router/api-router.go:46 | PASS | router/router.go |
| 20 | POST /api/oauth/email/bind | UserAuth + CriticalRateLimit: controller.EmailBind | router/api-router.go:47 | PASS | router/router.go |
| 21 | GET /api/oauth/wechat | CriticalRateLimit + DisableCache: controller.WeChatAuth | router/api-router.go:49 | PASS | router/router.go |
| 22 | POST /api/oauth/wechat/bind | UserAuth + CriticalRateLimit: controller.WeChatBind | router/api-router.go:50 | PASS | router/router.go |
| 23 | GET /api/oauth/telegram/login | CriticalRateLimit + DisableCache: controller.TelegramLogin | router/api-router.go:51 | PASS | router/router.go |
| 24 | POST /api/oauth/telegram/bind/start | UserAuth + CriticalRateLimit + DisableCache: controller.TelegramBindStart | router/api-router.go:52 | PASS | router/router.go |
| 25 | GET /api/oauth/telegram/bind/:flow_token | CriticalRateLimit + DisableCache: controller.TelegramBind | router/api-router.go:53 | PASS | router/router.go |
| 26 | GET /api/oauth/:provider | CriticalRateLimit + DisableCache + TryUserAuth: controller.HandleOAuth (GitHub/Discord/OIDC/LinuxDO) | router/api-router.go:55 | PASS | router/router.go |
| 27 | GET /api/ratio_config | CriticalRateLimit: controller.GetRatioConfig | router/api-router.go:56 | PASS | router/router.go |
| 28 | POST /api/stripe/webhook | AnonymousRequestBodyLimit: controller.StripeWebhook | router/api-router.go:58 | PASS | router/router.go |
| 29 | POST /api/creem/webhook | AnonymousRequestBodyLimit: controller.CreemWebhook | router/api-router.go:59 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 30 | POST /api/waffo/webhook | AnonymousRequestBodyLimit: controller.WaffoWebhook | router/api-router.go:60 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 31 | POST /api/waffo-pancake/webhook/:env | AnonymousRequestBodyLimit: controller.WaffoPancakeWebhook | router/api-router.go:63 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 32 | POST /api/verify | UserAuth + CriticalRateLimit + DisableCache: controller.UniversalVerify | router/api-router.go:66 | PASS | router/router.go |
| 33 | POST /api/user/auth/refresh | SessionCookieOriginGuard + CriticalRateLimit + DisableCache: controller.RefreshAuth | router/api-router.go:70 | PASS | router/router.go |
| 34 | POST /api/user/auth/logout | SessionCookieOriginGuard + CriticalRateLimit + DisableCache: controller.AuthLogout | router/api-router.go:71 | PASS | router/router.go |
| 35 | POST /api/user/register | CriticalRateLimit + AnonymousRequestBodyLimit + TurnstileCheck: controller.Register | router/api-router.go:72 | PASS | router/router.go |
| 36 | POST /api/user/login | CriticalRateLimit + DisableCache + AnonymousRequestBodyLimit + TurnstileCheck: controller.Login | router/api-router.go:73 | PASS | router/router.go |
| 37 | POST /api/user/login/2fa | CriticalRateLimit + DisableCache + AnonymousRequestBodyLimit: controller.Verify2FALogin | router/api-router.go:74 | PASS | router/router.go |
| 38 | POST /api/user/passkey/login/begin | CriticalRateLimit + DisableCache + AnonymousRequestBodyLimit: controller.PasskeyLoginBegin | router/api-router.go:75 | PASS | router/router.go |
| 39 | POST /api/user/passkey/login/finish | CriticalRateLimit + DisableCache + AnonymousRequestBodyLimit: controller.PasskeyLoginFinish | router/api-router.go:76 | PASS | router/router.go |
| 40 | POST /api/user/epay/notify | AnonymousRequestBodyLimit: controller.EpayNotify | router/api-router.go:78 | PASS | router/router.go |
| 41 | GET /api/user/epay/notify | Public: controller.EpayNotify | router/api-router.go:79 | PASS | router/router.go |
| 42 | GET /api/user/groups | Public: controller.GetUserGroups | router/api-router.go:80 | PASS | router/router.go |
| 43 | GET /api/user/sessions | UserAuth + DisableCache: controller.GetLoginSessions | router/api-router.go:85 | PASS | router/router.go |
| 44 | DELETE /api/user/sessions/:sid | UserAuth + DisableCache: controller.DeleteLoginSession | router/api-router.go:86 | PASS | router/router.go |
| 45 | POST /api/user/sessions/revoke-others | UserAuth + DisableCache: controller.RevokeOtherLoginSessions | router/api-router.go:87 | PASS | router/router.go |
| 46 | GET /api/user/self/groups | UserAuth: controller.GetUserGroups | router/api-router.go:88 | PASS | router/router.go |
| 47 | GET /api/user/self | UserAuth: controller.GetSelf | router/api-router.go:89 | PASS | router/router.go |
| 48 | GET /api/user/models | UserAuth: controller.GetUserModels | router/api-router.go:90 | PASS | router/router.go |
| 49 | PUT /api/user/self | UserAuth + CriticalRateLimit + DisableCache: controller.UpdateSelf | router/api-router.go:91 | PASS | router/router.go |
| 50 | DELETE /api/user/self | UserAuth: controller.DeleteSelf | router/api-router.go:92 | PASS | router/router.go |
| 51 | GET /api/user/token | UserAuth + CriticalRateLimit + UserCriticalRateLimit('access-token') + DisableCache: controller.GenerateAccessToken | router/api-router.go:93 | PASS | router/router.go |
| 52 | GET /api/user/passkey | UserAuth: controller.PasskeyStatus | router/api-router.go:94 | PASS | router/router.go |
| 53 | POST /api/user/passkey/register/begin | UserAuth + DisableCache: controller.PasskeyRegisterBegin | router/api-router.go:95 | PASS | router/router.go |
| 54 | POST /api/user/passkey/register/finish | UserAuth + DisableCache: controller.PasskeyRegisterFinish | router/api-router.go:96 | PASS | router/router.go |
| 55 | POST /api/user/passkey/verify/begin | UserAuth + DisableCache: controller.PasskeyVerifyBegin | router/api-router.go:97 | PASS | router/router.go |
| 56 | POST /api/user/passkey/verify/finish | UserAuth + DisableCache: controller.PasskeyVerifyFinish | router/api-router.go:98 | PASS | router/router.go |
| 57 | DELETE /api/user/passkey | UserAuth + DisableCache: controller.PasskeyDelete | router/api-router.go:99 | PASS | router/router.go |
| 58 | GET /api/user/aff | UserAuth: controller.GetAffCode | router/api-router.go:100 | PASS | router/router.go |
| 59 | GET /api/user/topup/info | UserAuth: controller.GetTopUpInfo | router/api-router.go:101 | PASS | router/router.go |
| 60 | GET /api/user/topup/self | UserAuth: controller.GetUserTopUps | router/api-router.go:102 | PASS | equivalent: GET /api/user/topup |
| 61 | POST /api/user/topup | UserAuth + CriticalRateLimit: controller.TopUp | router/api-router.go:103 | PASS | router/router.go |
| 62 | POST /api/user/pay | UserAuth + CriticalRateLimit: controller.RequestEpay | router/api-router.go:104 | PASS | router/router.go |
| 63 | POST /api/user/amount | UserAuth: controller.RequestAmount | router/api-router.go:105 | PASS | router/router.go |
| 64 | POST /api/user/stripe/pay | UserAuth + CriticalRateLimit: controller.RequestStripePay | router/api-router.go:106 | PASS | router/router.go |
| 65 | POST /api/user/stripe/amount | UserAuth: controller.RequestStripeAmount | router/api-router.go:107 | PASS | router/router.go |
| 66 | POST /api/user/creem/pay | UserAuth + CriticalRateLimit: controller.RequestCreemPay | router/api-router.go:108 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 67 | POST /api/user/waffo/amount | UserAuth: controller.RequestWaffoAmount | router/api-router.go:109 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 68 | POST /api/user/waffo/pay | UserAuth + CriticalRateLimit: controller.RequestWaffoPay | router/api-router.go:110 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 69 | POST /api/user/waffo-pancake/amount | UserAuth: controller.RequestWaffoPancakeAmount | router/api-router.go:111 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 70 | POST /api/user/waffo-pancake/pay | UserAuth + CriticalRateLimit: controller.RequestWaffoPancakePay | router/api-router.go:112 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 71 | POST /api/user/aff_transfer | UserAuth + UserCriticalRateLimit('aff-transfer'): controller.TransferAffQuota | router/api-router.go:113 | PASS | router/router.go |
| 72 | PUT /api/user/setting | UserAuth: controller.UpdateUserSetting | router/api-router.go:114 | PASS | router/router.go |
| 73 | GET /api/user/2fa/status | UserAuth: controller.Get2FAStatus | router/api-router.go:117 | PASS | router/router.go |
| 74 | POST /api/user/2fa/setup | UserAuth + DisableCache: controller.Setup2FA | router/api-router.go:118 | PASS | equivalent: POST /api/user/2fa/start (secret + pending record), then /api/user/2fa/enable |
| 75 | POST /api/user/2fa/enable | UserAuth + DisableCache: controller.Enable2FA | router/api-router.go:119 | PASS | router/router.go |
| 76 | POST /api/user/2fa/disable | UserAuth + DisableCache: controller.Disable2FA | router/api-router.go:120 | PASS | router/router.go |
| 77 | POST /api/user/2fa/backup_codes | UserAuth + DisableCache: controller.RegenerateBackupCodes | router/api-router.go:121 | PASS | router/router.go |
| 78 | GET /api/user/checkin | UserAuth: controller.GetCheckinStatus | router/api-router.go:124 | PASS | equivalent: GET /api/user/checkin/status |
| 79 | POST /api/user/checkin | UserAuth + TurnstileCheck: controller.DoCheckin | router/api-router.go:125 | PASS | equivalent: GET /api/user/checkin/status |
| 80 | GET /api/user/oauth/bindings | UserAuth: controller.GetUserOAuthBindings | router/api-router.go:128 | PASS | router/router.go |
| 81 | DELETE /api/user/oauth/bindings/:provider_id | UserAuth: controller.UnbindCustomOAuth | router/api-router.go:129 | PASS | router/router.go |
| 82 | GET /api/user/ | AdminAuth: controller.GetAllUsers (admin user management) | router/api-router.go:135 | PASS | router/router.go |
| 83 | GET /api/user/topup | AdminAuth: controller.GetAllTopUps | router/api-router.go:136 | PASS | router/router.go |
| 84 | POST /api/user/topup/complete | AdminAuth: controller.AdminCompleteTopUp | router/api-router.go:137 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 85 | GET /api/user/search | AdminAuth: controller.SearchUsers | router/api-router.go:138 | PASS | router/router.go |
| 86 | GET /api/user/:id/oauth/bindings | AdminAuth: controller.GetUserOAuthBindingsByAdmin | router/api-router.go:139 | PASS | router/router.go |
| 87 | DELETE /api/user/:id/oauth/bindings/:provider_id | AdminAuth: controller.UnbindCustomOAuthByAdmin | router/api-router.go:140 | PASS | router/router.go |
| 88 | DELETE /api/user/:id/bindings/:binding_type | AdminAuth: controller.AdminClearUserBinding | router/api-router.go:141 | PASS | router/router.go |
| 89 | GET /api/user/:id | AdminAuth: controller.GetUser | router/api-router.go:142 | PASS | router/router.go |
| 90 | POST /api/user/ | AdminAuth: controller.CreateUser | router/api-router.go:143 | PASS | router/router.go |
| 91 | POST /api/user/manage | AdminAuth: controller.ManageUser | router/api-router.go:144 | PASS | router/router.go |
| 92 | PUT /api/user/ | AdminAuth: controller.UpdateUser | router/api-router.go:145 | PASS | router/router.go |
| 93 | DELETE /api/user/:id | AdminAuth: controller.DeleteUser | router/api-router.go:146 | PASS | router/router.go |
| 94 | DELETE /api/user/:id/reset_passkey | AdminAuth: controller.AdminResetPasskey | router/api-router.go:147 | PASS | router/router.go |
| 95 | GET /api/user/2fa/stats | AdminAuth: controller.Admin2FAStats | router/api-router.go:150 | PASS | router/router.go |
| 96 | DELETE /api/user/:id/2fa | AdminAuth: controller.AdminDisable2FA | router/api-router.go:151 | PASS | router/router.go |
| 97 | GET /api/subscription/plans | UserAuth: controller.GetSubscriptionPlans | router/api-router.go:159 | PASS | router/router.go |
| 98 | GET /api/subscription/self | UserAuth: controller.GetSubscriptionSelf | router/api-router.go:160 | PASS | router/router.go |
| 99 | PUT /api/subscription/self/preference | UserAuth: controller.UpdateSubscriptionPreference | router/api-router.go:161 | PASS | router/router.go |
| 100 | POST /api/subscription/balance/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestBalancePay | router/api-router.go:162 | PASS | router/router.go |
| 101 | POST /api/subscription/epay/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestEpay | router/api-router.go:163 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 102 | POST /api/subscription/stripe/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestStripePay | router/api-router.go:164 | PASS | router/router.go |
| 103 | POST /api/subscription/creem/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestCreemPay | router/api-router.go:165 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 104 | POST /api/subscription/waffo-pancake/pay | UserAuth + CriticalRateLimit: controller.SubscriptionRequestWaffoPancakePay | router/api-router.go:166 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 105 | GET /api/subscription/admin/plans | AdminAuth: controller.AdminListSubscriptionPlans | router/api-router.go:171 | PASS | router/router.go |
| 106 | POST /api/subscription/admin/plans | AdminAuth: controller.AdminCreateSubscriptionPlan | router/api-router.go:172 | PASS | router/router.go |
| 107 | PUT /api/subscription/admin/plans/:id | AdminAuth: controller.AdminUpdateSubscriptionPlan | router/api-router.go:173 | PASS | router/router.go |
| 108 | PATCH /api/subscription/admin/plans/:id | AdminAuth: controller.AdminUpdateSubscriptionPlanStatus | router/api-router.go:174 | PASS | router/router.go |
| 109 | POST /api/subscription/admin/bind | AdminAuth: controller.AdminBindSubscription | router/api-router.go:175 | PASS | router/router.go |
| 110 | POST /api/subscription/admin/plans/:id/subscriptions/reset | AdminAuth: controller.AdminResetPlanSubscriptions | router/api-router.go:176 | PASS | router/router.go |
| 111 | GET /api/subscription/admin/users/:id/subscriptions | AdminAuth: controller.AdminListUserSubscriptions | router/api-router.go:179 | PASS | router/router.go |
| 112 | POST /api/subscription/admin/users/:id/subscriptions | AdminAuth: controller.AdminCreateUserSubscription | router/api-router.go:180 | PASS | router/router.go |
| 113 | POST /api/subscription/admin/users/:id/subscriptions/reset | AdminAuth: controller.AdminResetUserSubscriptionsByPlan | router/api-router.go:181 | PASS | router/router.go |
| 114 | POST /api/subscription/admin/user_subscriptions/:id/invalidate | AdminAuth: controller.AdminInvalidateUserSubscription | router/api-router.go:182 | PASS | router/router.go |
| 115 | DELETE /api/subscription/admin/user_subscriptions/:id | AdminAuth: controller.AdminDeleteUserSubscription | router/api-router.go:183 | PASS | router/router.go |
| 116 | POST /api/subscription/epay/notify | AnonymousRequestBodyLimit: controller.SubscriptionEpayNotify (no auth callback) | router/api-router.go:187 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 117 | GET /api/subscription/epay/notify | Public: controller.SubscriptionEpayNotify | router/api-router.go:188 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 118 | GET /api/subscription/epay/return | Public: controller.SubscriptionEpayReturn | router/api-router.go:189 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 119 | POST /api/subscription/epay/return | AnonymousRequestBodyLimit: controller.SubscriptionEpayReturn | router/api-router.go:190 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 120 | GET /api/option/ | RootAuth: controller.GetOptions | router/api-router.go:194 | PASS | router/router.go |
| 121 | PUT /api/option/ | RootAuth: controller.UpdateOption | router/api-router.go:195 | PASS | router/router.go |
| 122 | POST /api/option/payment_compliance | RootAuth: controller.ConfirmPaymentCompliance | router/api-router.go:196 | PASS | router/router.go |
| 123 | GET /api/option/channel_affinity_cache | RootAuth: controller.GetChannelAffinityCacheStats | router/api-router.go:197 | PASS | router/router.go |
| 124 | DELETE /api/option/channel_affinity_cache | RootAuth: controller.ClearChannelAffinityCache | router/api-router.go:198 | PASS | router/router.go |
| 125 | POST /api/option/rest_model_ratio | RootAuth: controller.ResetModelRatio | router/api-router.go:199 | PASS | router/router.go |
| 126 | GET /api/option/waffo-pancake/catalog | RootAuth: controller.ListWaffoPancakeCatalog | router/api-router.go:200 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 127 | POST /api/option/waffo-pancake/pair | RootAuth: controller.CreateWaffoPancakePair | router/api-router.go:201 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 128 | POST /api/option/waffo-pancake/save | RootAuth: controller.SaveWaffoPancake | router/api-router.go:202 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 129 | POST /api/option/waffo-pancake/subscription-product | RootAuth: controller.CreateWaffoPancakeSubscriptionProduct | router/api-router.go:203 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 130 | GET /api/option/waffo-pancake/subscription-product-options | RootAuth: controller.ListWaffoPancakeSubscriptionProductOptions | router/api-router.go:204 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 131 | POST /api/custom-oauth-provider/discovery | RootAuth: controller.FetchCustomOAuthDiscovery | router/api-router.go:211 | PASS | router/router.go |
| 132 | GET /api/custom-oauth-provider/ | RootAuth: controller.GetCustomOAuthProviders | router/api-router.go:212 | PASS | router/router.go |
| 133 | GET /api/custom-oauth-provider/:id | RootAuth: controller.GetCustomOAuthProvider | router/api-router.go:213 | PASS | router/router.go |
| 134 | POST /api/custom-oauth-provider/ | RootAuth: controller.CreateCustomOAuthProvider | router/api-router.go:214 | PASS | router/router.go |
| 135 | PUT /api/custom-oauth-provider/:id | RootAuth: controller.UpdateCustomOAuthProvider | router/api-router.go:215 | PASS | router/router.go |
| 136 | DELETE /api/custom-oauth-provider/:id | RootAuth: controller.DeleteCustomOAuthProvider | router/api-router.go:216 | PASS | router/router.go |
| 137 | GET /api/performance/stats | RootAuth: controller.GetPerformanceStats | router/api-router.go:221 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 138 | DELETE /api/performance/disk_cache | RootAuth: controller.ClearDiskCache | router/api-router.go:222 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 139 | POST /api/performance/reset_stats | RootAuth: controller.ResetPerformanceStats | router/api-router.go:223 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 140 | POST /api/performance/gc | RootAuth: controller.ForceGC | router/api-router.go:224 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 141 | GET /api/performance/logs | RootAuth: controller.GetLogFiles | router/api-router.go:225 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 142 | DELETE /api/performance/logs | RootAuth: controller.CleanupLogFiles | router/api-router.go:226 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 143 | GET /api/ratio_sync/channels | RootAuth: controller.GetSyncableChannels | router/api-router.go:231 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 144 | POST /api/ratio_sync/fetch | RootAuth: controller.FetchUpstreamRatios | router/api-router.go:232 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 145 | POST /api/channel/:id/key | AdminAuth + RootAuth + CriticalRateLimit + DisableCache + SecureVerificationRequired: controller.GetChannelKey | router/channel-router.go:23 | PASS | router/router.go |
| 146 | GET /api/channel/ | AdminAuth + RequirePermission(ChannelRead): controller.GetAllChannels | router/channel-router.go:40 | PASS | router/router.go |
| 147 | GET /api/channel/search | AdminAuth + RequirePermission(ChannelRead): controller.SearchChannels | router/channel-router.go:41 | PASS | router/router.go |
| 148 | GET /api/channel/models | AdminAuth + RequirePermission(ChannelRead): controller.ChannelListModels | router/channel-router.go:42 | PASS | router/router.go |
| 149 | GET /api/channel/models_enabled | AdminAuth + RequirePermission(ChannelRead): controller.EnabledListModels | router/channel-router.go:43 | PASS | router/router.go |
| 150 | GET /api/channel/ops | AdminAuth + RequirePermission(ChannelRead): controller.GetChannelOps | router/channel-router.go:44 | PASS | router/router.go |
| 151 | GET /api/channel/:id | AdminAuth + RequirePermission(ChannelRead): controller.GetChannel | router/channel-router.go:45 | PASS | router/router.go |
| 152 | GET /api/channel/test | AdminAuth + RequirePermission(ChannelOperate): controller.TestAllChannels | router/channel-router.go:46 | PASS | router/router.go |
| 153 | GET /api/channel/test/:id | AdminAuth + RequirePermission(ChannelOperate): controller.TestChannel | router/channel-router.go:47 | PASS | router/router.go |
| 154 | GET /api/channel/update_balance | AdminAuth + RequirePermission(ChannelOperate): controller.UpdateAllChannelsBalance | router/channel-router.go:48 | PASS | router/router.go |
| 155 | GET /api/channel/update_balance/:id | AdminAuth + RequirePermission(ChannelOperate): controller.UpdateChannelBalance | router/channel-router.go:49 | PASS | router/router.go |
| 156 | POST /api/channel/ | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.AddChannel | router/channel-router.go:50 | PASS | router/router.go |
| 157 | PUT /api/channel/ | AdminAuth + RequirePermission(ChannelWrite): controller.UpdateChannel | router/channel-router.go:51 | PASS | router/router.go |
| 158 | POST /api/channel/status/batch | AdminAuth + RequirePermission(ChannelOperate): controller.BatchUpdateChannelStatus | router/channel-router.go:52 | PASS | router/router.go |
| 159 | POST /api/channel/:id/status | AdminAuth + RequirePermission(ChannelOperate): controller.UpdateChannelStatus | router/channel-router.go:53 | PASS | router/router.go |
| 160 | DELETE /api/channel/disabled | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.DeleteDisabledChannel | router/channel-router.go:54 | PASS | router/router.go |
| 161 | POST /api/channel/tag/disabled | AdminAuth + RequirePermission(ChannelOperate): controller.DisableTagChannels | router/channel-router.go:55 | PASS | router/router.go |
| 162 | POST /api/channel/tag/enabled | AdminAuth + RequirePermission(ChannelOperate): controller.EnableTagChannels | router/channel-router.go:56 | PASS | router/router.go |
| 163 | PUT /api/channel/tag | AdminAuth + RequirePermission(ChannelWrite): controller.EditTagChannels | router/channel-router.go:57 | PASS | router/router.go |
| 164 | DELETE /api/channel/:id | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.DeleteChannel | router/channel-router.go:58 | PASS | router/router.go |
| 165 | POST /api/channel/batch | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.DeleteChannelBatch | router/channel-router.go:59 | PASS | router/router.go |
| 166 | POST /api/channel/fix | AdminAuth + RequirePermission(ChannelOperate): controller.FixChannelsAbilities | router/channel-router.go:60 | PASS | router/router.go |
| 167 | GET /api/channel/fetch_models/:id | AdminAuth + RequirePermission(ChannelOperate): controller.FetchUpstreamModels | router/channel-router.go:61 | PASS | router/router.go |
| 168 | POST /api/channel/fetch_models | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.FetchModels | router/channel-router.go:62 | PASS | router/router.go |
| 169 | POST /api/channel/:id/codex/refresh | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.RefreshCodexChannelCredential | router/channel-router.go:63 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 170 | GET /api/channel/:id/codex/usage | AdminAuth + RequirePermission(ChannelRead): controller.GetCodexChannelUsage | router/channel-router.go:64 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 171 | GET /api/channel/:id/codex/usage/reset-credits | AdminAuth + RequirePermission(ChannelRead): controller.GetCodexChannelRateLimitResetCredits | router/channel-router.go:65 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 172 | POST /api/channel/:id/codex/usage/reset | AdminAuth + RequirePermission(ChannelOperate): controller.ResetCodexChannelUsage | router/channel-router.go:66 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 173 | POST /api/channel/ollama/pull | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.OllamaPullModel | router/channel-router.go:67 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 174 | POST /api/channel/ollama/pull/stream | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.OllamaPullModelStream | router/channel-router.go:68 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 175 | DELETE /api/channel/ollama/delete | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.OllamaDeleteModel | router/channel-router.go:69 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 176 | GET /api/channel/ollama/version/:id | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.OllamaVersion | router/channel-router.go:70 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 177 | POST /api/channel/batch/tag | AdminAuth + RequirePermission(ChannelWrite): controller.BatchSetChannelTag | router/channel-router.go:71 | PASS | router/router.go |
| 178 | GET /api/channel/tag/models | AdminAuth + RequirePermission(ChannelRead): controller.GetTagModels | router/channel-router.go:72 | PASS | router/router.go |
| 179 | POST /api/channel/copy/:id | AdminAuth + RequirePermission(ChannelSensitiveWrite): controller.CopyChannel | router/channel-router.go:73 | PASS | router/router.go |
| 180 | POST /api/channel/multi_key/manage | AdminAuth + RequirePermission(ChannelOperate): controller.ManageMultiKeys | router/channel-router.go:74 | PASS | router/router.go |
| 181 | POST /api/channel/upstream_updates/apply | AdminAuth + RequirePermission(ChannelWrite): controller.ApplyChannelUpstreamModelUpdates | router/channel-router.go:75 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 182 | POST /api/channel/upstream_updates/apply_all | AdminAuth + RequirePermission(ChannelWrite): controller.ApplyAllChannelUpstreamModelUpdates | router/channel-router.go:76 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 183 | POST /api/channel/upstream_updates/detect | AdminAuth + RequirePermission(ChannelOperate): controller.DetectChannelUpstreamModelUpdates | router/channel-router.go:77 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 184 | POST /api/channel/upstream_updates/detect_all | AdminAuth + RequirePermission(ChannelOperate): controller.DetectAllChannelUpstreamModelUpdates | router/channel-router.go:78 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 185 | GET /api/authz/catalog | AdminAuth: controller.GetPermissionCatalog | router/authz-router.go:17 | PASS | router/router.go |
| 186 | GET /api/token/ | UserAuth: controller.GetAllTokens | router/api-router.go:239 | PASS | router/router.go |
| 187 | GET /api/token/search | UserAuth + SearchRateLimit: controller.SearchTokens | router/api-router.go:240 | PASS | router/router.go |
| 188 | GET /api/token/auto-groups | UserAuth: controller.GetTokenAutoGroups | router/api-router.go:241 | PASS | router/router.go |
| 189 | GET /api/token/:id | UserAuth: controller.GetToken | router/api-router.go:242 | PASS | router/router.go |
| 190 | POST /api/token/:id/key | UserAuth + CriticalRateLimit + DisableCache: controller.GetTokenKey | router/api-router.go:243 | PASS | router/router.go |
| 191 | POST /api/token/ | UserAuth: controller.AddToken | router/api-router.go:244 | PASS | router/router.go |
| 192 | PUT /api/token/ | UserAuth: controller.UpdateToken | router/api-router.go:245 | PASS | router/router.go |
| 193 | DELETE /api/token/:id | UserAuth: controller.DeleteToken | router/api-router.go:246 | PASS | router/router.go |
| 194 | POST /api/token/batch | UserAuth: controller.DeleteTokenBatch | router/api-router.go:247 | PASS | router/router.go |
| 195 | POST /api/token/batch/keys | UserAuth + CriticalRateLimit + DisableCache: controller.GetTokenKeysBatch | router/api-router.go:248 | PASS | router/router.go |
| 196 | GET /api/usage/token/ | CORS + CriticalRateLimit + TokenAuthReadOnly: controller.GetTokenUsage | router/api-router.go:257 | PASS | router/router.go |
| 197 | GET /api/redemption/ | AdminAuth: controller.GetAllRedemptions | router/api-router.go:264 | PASS | router/router.go |
| 198 | GET /api/redemption/search | AdminAuth: controller.SearchRedemptions | router/api-router.go:265 | PASS | router/router.go |
| 199 | GET /api/redemption/:id | AdminAuth: controller.GetRedemption | router/api-router.go:266 | PASS | router/router.go |
| 200 | POST /api/redemption/ | AdminAuth: controller.AddRedemption | router/api-router.go:267 | PASS | router/router.go |
| 201 | PUT /api/redemption/ | AdminAuth: controller.UpdateRedemption | router/api-router.go:268 | PASS | router/router.go |
| 202 | DELETE /api/redemption/invalid | AdminAuth: controller.DeleteInvalidRedemption | router/api-router.go:269 | PASS | router/router.go |
| 203 | DELETE /api/redemption/:id | AdminAuth: controller.DeleteRedemption | router/api-router.go:270 | PASS | router/router.go |
| 204 | GET /api/log/ | AdminAuth: controller.GetAllLogs | router/api-router.go:273 | PASS | router/router.go |
| 205 | GET /api/log/stat | AdminAuth: controller.GetLogsStat | router/api-router.go:274 | PASS | router/router.go |
| 206 | GET /api/log/self/stat | UserAuth: controller.GetLogsSelfStat | router/api-router.go:275 | PASS | router/router.go |
| 207 | GET /api/log/channel_affinity_usage_cache | AdminAuth: controller.GetChannelAffinityUsageCacheStats | router/api-router.go:276 | PASS | router/router.go |
| 208 | GET /api/log/search | AdminAuth: controller.SearchAllLogs | router/api-router.go:277 | PASS | router/router.go |
| 209 | GET /api/log/self | UserAuth: controller.GetUserLogs | router/api-router.go:278 | PASS | router/router.go |
| 210 | GET /api/log/self/search | UserAuth + SearchRateLimit: controller.SearchUserLogs | router/api-router.go:279 | PASS | router/router.go |
| 211 | GET /api/log/token | CORS + CriticalRateLimit + TokenAuthReadOnly: controller.GetLogByKey | router/api-router.go:306 | PASS | router/router.go |
| 212 | POST /api/system-task/log-cleanup | RootAuth: controller.CreateLogCleanupSystemTask | router/api-router.go:284 | PASS | router/router.go |
| 213 | GET /api/system-task/list | RootAuth: controller.ListSystemTasks | router/api-router.go:285 | PASS | router/router.go |
| 214 | GET /api/system-task/current | RootAuth: controller.GetCurrentSystemTask | router/api-router.go:286 | PASS | router/router.go |
| 215 | GET /api/system-task/:task_id | RootAuth: controller.GetSystemTask | router/api-router.go:287 | PASS | router/router.go |
| 216 | GET /api/system-info/instances | RootAuth: controller.ListSystemInstances | router/api-router.go:292 | PASS | router/router.go |
| 217 | DELETE /api/system-info/stale-instances | RootAuth: controller.DeleteStaleSystemInstances | router/api-router.go:293 | PASS | router/router.go |
| 218 | DELETE /api/system-info/instances/:node_name | RootAuth: controller.DeleteStaleSystemInstance | router/api-router.go:294 | PASS | router/router.go |
| 219 | GET /api/data/ | AdminAuth: controller.GetAllQuotaDates | router/api-router.go:298 | PASS | router/router.go |
| 220 | GET /api/data/users | AdminAuth: controller.GetQuotaDatesByUser | router/api-router.go:299 | PASS | router/router.go |
| 221 | GET /api/data/self | UserAuth: controller.GetUserQuotaDates | router/api-router.go:300 | PASS | router/router.go |
| 222 | GET /api/data/flow | AdminAuth: controller.GetAllFlowQuotaDates | router/api-router.go:301 | PASS | router/router.go |
| 223 | GET /api/data/flow/self | UserAuth: controller.GetUserFlowQuotaDates | router/api-router.go:302 | PASS | router/router.go |
| 224 | GET /api/group/ | AdminAuth: controller.GetGroups | router/api-router.go:311 | PASS | router/router.go |
| 225 | GET /api/prefill_group/ | AdminAuth: controller.GetPrefillGroups | router/api-router.go:317 | PASS | router/router.go |
| 226 | POST /api/prefill_group/ | AdminAuth: controller.CreatePrefillGroup | router/api-router.go:318 | PASS | router/router.go |
| 227 | PUT /api/prefill_group/ | AdminAuth: controller.UpdatePrefillGroup | router/api-router.go:319 | PASS | router/router.go |
| 228 | DELETE /api/prefill_group/:id | AdminAuth: controller.DeletePrefillGroup | router/api-router.go:320 | PASS | router/router.go |
| 229 | GET /api/mj/self | UserAuth: controller.GetUserMidjourney | router/api-router.go:324 | PASS | router/router.go |
| 230 | GET /api/mj/ | AdminAuth: controller.GetAllMidjourney | router/api-router.go:325 | PASS | router/router.go |
| 231 | GET /api/task/self | UserAuth: controller.GetUserTask | router/api-router.go:329 | PASS | router/router.go |
| 232 | GET /api/task/ | AdminAuth: controller.GetAllTask | router/api-router.go:330 | PASS | router/router.go |
| 233 | GET /api/vendors/ | AdminAuth: controller.GetAllVendors | router/api-router.go:336 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 234 | GET /api/vendors/search | AdminAuth: controller.SearchVendors | router/api-router.go:337 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 235 | GET /api/vendors/:id | AdminAuth: controller.GetVendorMeta | router/api-router.go:338 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 236 | POST /api/vendors/ | AdminAuth: controller.CreateVendorMeta | router/api-router.go:339 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 237 | PUT /api/vendors/ | AdminAuth: controller.UpdateVendorMeta | router/api-router.go:340 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 238 | DELETE /api/vendors/:id | AdminAuth: controller.DeleteVendorMeta | router/api-router.go:341 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 239 | GET /api/models/sync_upstream/preview | AdminAuth: controller.SyncUpstreamPreview | router/api-router.go:347 | PASS | router/router.go |
| 240 | POST /api/models/sync_upstream | AdminAuth: controller.SyncUpstreamModels | router/api-router.go:348 | PASS | router/router.go |
| 241 | GET /api/models/missing | AdminAuth: controller.GetMissingModels | router/api-router.go:349 | PASS | router/router.go |
| 242 | GET /api/models/ | AdminAuth: controller.GetAllModelsMeta | router/api-router.go:350 | PASS | router/router.go |
| 243 | GET /api/models/search | AdminAuth: controller.SearchModelsMeta | router/api-router.go:351 | PASS | router/router.go |
| 244 | GET /api/models/:id | AdminAuth: controller.GetModelMeta | router/api-router.go:352 | PASS | router/router.go |
| 245 | POST /api/models/ | AdminAuth: controller.CreateModelMeta | router/api-router.go:353 | PASS | router/router.go |
| 246 | PUT /api/models/ | AdminAuth: controller.UpdateModelMeta | router/api-router.go:354 | PASS | router/router.go |
| 247 | DELETE /api/models/:id | AdminAuth: controller.DeleteModelMeta | router/api-router.go:355 | PASS | router/router.go |
| 248 | GET /api/deployments/settings | AdminAuth: controller.GetModelDeploymentSettings | router/api-router.go:362 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 249 | POST /api/deployments/settings/test-connection | AdminAuth: controller.TestIoNetConnection | router/api-router.go:363 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 250 | GET /api/deployments/ | AdminAuth: controller.GetAllDeployments | router/api-router.go:364 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 251 | GET /api/deployments/search | AdminAuth: controller.SearchDeployments | router/api-router.go:365 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 252 | POST /api/deployments/test-connection | AdminAuth: controller.TestIoNetConnection | router/api-router.go:366 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 253 | GET /api/deployments/hardware-types | AdminAuth: controller.GetHardwareTypes | router/api-router.go:367 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 254 | GET /api/deployments/locations | AdminAuth: controller.GetLocations | router/api-router.go:368 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 255 | GET /api/deployments/available-replicas | AdminAuth: controller.GetAvailableReplicas | router/api-router.go:369 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 256 | POST /api/deployments/price-estimation | AdminAuth: controller.GetPriceEstimation | router/api-router.go:370 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 257 | GET /api/deployments/check-name | AdminAuth: controller.CheckClusterNameAvailability | router/api-router.go:371 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 258 | POST /api/deployments/ | AdminAuth: controller.CreateDeployment | router/api-router.go:372 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 259 | GET /api/deployments/:id | AdminAuth: controller.GetDeployment | router/api-router.go:374 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 260 | GET /api/deployments/:id/logs | AdminAuth: controller.GetDeploymentLogs | router/api-router.go:375 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 261 | GET /api/deployments/:id/containers | AdminAuth: controller.ListDeploymentContainers | router/api-router.go:376 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 262 | GET /api/deployments/:id/containers/:container_id | AdminAuth: controller.GetContainerDetails | router/api-router.go:377 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 263 | PUT /api/deployments/:id | AdminAuth: controller.UpdateDeployment | router/api-router.go:378 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 264 | PUT /api/deployments/:id/name | AdminAuth: controller.UpdateDeploymentName | router/api-router.go:379 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 265 | POST /api/deployments/:id/extend | AdminAuth: controller.ExtendDeployment | router/api-router.go:380 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 266 | DELETE /api/deployments/:id | AdminAuth: controller.DeleteDeployment | router/api-router.go:381 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 267 | GET /dashboard/billing/subscription | RouteTag('old_api') + CORS + TokenAuth: controller.GetSubscription | router/dashboard.go:18 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 268 | GET /v1/dashboard/billing/subscription | RouteTag('old_api') + CORS + TokenAuth: controller.GetSubscription | router/dashboard.go:19 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 269 | GET /dashboard/billing/usage | RouteTag('old_api') + CORS + TokenAuth: controller.GetUsage | router/dashboard.go:20 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 270 | GET /v1/dashboard/billing/usage | RouteTag('old_api') + CORS + TokenAuth: controller.GetUsage | router/dashboard.go:21 | REFERENCE_PLACEHOLDER | feature not ported (KNOWN_DEVIATIONS #10) |
| 271 | GET /v1/models | RouteTag('relay') + TokenAuth: controller.ListModels (OpenAI/Anthropic/Gemini dispatch) | router/relay-router.go:23 | PASS | router/router.go |
| 272 | GET /v1/models/:model | RouteTag('relay') + TokenAuth: controller.RetrieveModel (Anthropic/OpenAI dispatch) | router/relay-router.go:34 | PASS | router/router.go |
| 273 | GET /v1beta/models | RouteTag('relay') + TokenAuth: controller.ListModels (Gemini) | router/relay-router.go:48 | PASS | router/router.go |
| 274 | GET /v1beta/openai/models | RouteTag('relay') + TokenAuth: controller.ListModels (OpenAI) | router/relay-router.go:57 | PASS | router/router.go |
| 275 | POST /pg/chat/completions | RouteTag('relay') + SystemPerformanceCheck + UserAuth + Distribute: controller.Playground | router/relay-router.go:67 | PASS | router/router.go |
| 276 | GET /v1/realtime | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIRealtime, WebSocket) | router/relay-router.go:78 | PASS | router/router.go |
| 277 | POST /v1/messages | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatClaude) | router/relay-router.go:88 | PASS | router/router.go |
| 278 | POST /v1/completions | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAI) | router/relay-router.go:93 | PASS | router/router.go |
| 279 | POST /v1/chat/completions | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAI) | router/relay-router.go:96 | PASS | router/router.go |
| 280 | POST /v1/responses | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIResponses) | router/relay-router.go:101 | PASS | router/router.go |
| 281 | POST /v1/responses/compact | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIResponsesCompaction) | router/relay-router.go:104 | PASS | router/router.go |
| 282 | POST /v1/alpha/search | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIAlphaSearch) | router/relay-router.go:109 | PASS | router/router.go |
| 283 | POST /v1/edits | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIImage) | router/relay-router.go:114 | PASS | router/router.go |
| 284 | POST /v1/images/generations | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIImage) | router/relay-router.go:117 | PASS | router/router.go |
| 285 | POST /v1/images/edits | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIImage) | router/relay-router.go:120 | PASS | router/router.go |
| 286 | POST /v1/embeddings | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatEmbedding) | router/relay-router.go:125 | PASS | router/router.go |
| 287 | POST /v1/audio/transcriptions | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIAudio) | router/relay-router.go:130 | PASS | router/router.go |
| 288 | POST /v1/audio/translations | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIAudio) | router/relay-router.go:133 | PASS | router/router.go |
| 289 | POST /v1/audio/speech | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAIAudio) | router/relay-router.go:136 | PASS | router/router.go |
| 290 | POST /v1/rerank | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatRerank) | router/relay-router.go:141 | PASS | router/router.go |
| 291 | POST /v1/engines/:model/embeddings | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatGemini) | router/relay-router.go:146 | PASS | router/router.go |
| 292 | POST /v1/models/*path | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatGemini) | router/relay-router.go:149 | PASS | router/router.go |
| 293 | POST /v1/moderations | TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatOpenAI) | router/relay-router.go:154 | PASS | router/router.go |
| 294 | POST /v1/images/variations | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:159 | REFERENCE_PLACEHOLDER | reference returns 'API not implemented' 501; TokenRouter matches |
| 295 | GET /v1/files | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:160 | REFERENCE_PLACEHOLDER | reference returns 'API not implemented' 501; TokenRouter matches |
| 296 | POST /v1/files | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:161 | REFERENCE_PLACEHOLDER | reference returns 'API not implemented' 501; TokenRouter matches |
| 297 | DELETE /v1/files/:id | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:162 | REFERENCE_PLACEHOLDER | reference returns 'API not implemented' 501; TokenRouter matches |
| 298 | GET /v1/files/:id | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:163 | REFERENCE_PLACEHOLDER | reference returns 'API not implemented' 501; TokenRouter matches |
| 299 | GET /v1/files/:id/content | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:164 | REFERENCE_PLACEHOLDER | reference returns 'API not implemented' 501; TokenRouter matches |
| 300 | POST /v1/fine-tunes | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:165 | REFERENCE_PLACEHOLDER | reference returns 'API not implemented' 501; TokenRouter matches |
| 301 | GET /v1/fine-tunes | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:166 | REFERENCE_PLACEHOLDER | reference returns 'API not implemented' 501; TokenRouter matches |
| 302 | GET /v1/fine-tunes/:id | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:167 | REFERENCE_PLACEHOLDER | reference returns 'API not implemented' 501; TokenRouter matches |
| 303 | POST /v1/fine-tunes/:id/cancel | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:168 | REFERENCE_PLACEHOLDER | reference returns 'API not implemented' 501; TokenRouter matches |
| 304 | GET /v1/fine-tunes/:id/events | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:169 | REFERENCE_PLACEHOLDER | reference returns 'API not implemented' 501; TokenRouter matches |
| 305 | DELETE /v1/models/:model | TokenAuth + ModelRequestRateLimit + Distribute: controller.RelayNotImplemented | router/relay-router.go:170 | REFERENCE_PLACEHOLDER | reference returns 'API not implemented' 501; TokenRouter matches |
| 306 | GET /mj/image/:id | RouteTag('relay') + SystemPerformanceCheck: relay.RelayMidjourneyImage (no TokenAuth) | router/relay-router.go:209 | PASS | controller/midjourney_image.go + router/router.go |
| 307 | POST /mj/submit/action | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:212 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 308 | POST /mj/submit/shorten | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:213 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 309 | POST /mj/submit/modal | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:214 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 310 | POST /mj/submit/imagine | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:215 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 311 | POST /mj/submit/change | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:216 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 312 | POST /mj/submit/simple-change | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:217 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 313 | POST /mj/submit/describe | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:218 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 314 | POST /mj/submit/blend | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:219 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 315 | POST /mj/submit/edits | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:220 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 316 | POST /mj/submit/video | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:221 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 317 | GET /mj/task/:id/fetch | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:223 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 318 | GET /mj/task/:id/image-seed | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:224 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 319 | POST /mj/task/list-by-condition | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:225 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 320 | POST /mj/insight-face/swap | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:226 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 321 | POST /mj/submit/upload-discord-images | TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:227 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 322 | GET /:mode/mj/image/:id | RouteTag('relay') + SystemPerformanceCheck: relay.RelayMidjourneyImage (mode-prefixed MJ) | router/relay-router.go:178-181 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 323 | POST /:mode/mj/submit/* | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayMidjourney (mode-prefixed submit routes, all actions) | router/relay-router.go:178-181 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 324 | GET /:mode/mj/task/:id/fetch | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayMidjourney | router/relay-router.go:178-181 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 325 | POST /suno/submit/:action | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayTask | router/relay-router.go:189 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 326 | POST /suno/fetch | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayTaskFetch | router/relay-router.go:190 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 327 | GET /suno/fetch/:id | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + Distribute: controller.RelayTaskFetch | router/relay-router.go:191 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 328 | POST /v1beta/models/*path | RouteTag('relay') + SystemPerformanceCheck + TokenAuth + ModelRequestRateLimit + Distribute: controller.Relay (RelayFormatGemini) | router/relay-router.go:202 | PASS | router/router.go |
| 329 | GET /v1/videos/:task_id/content | RouteTag('relay') + TokenOrUserAuth: controller.VideoProxy | router/video-router.go:16 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 330 | POST /v1/video/generations | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:23 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 331 | GET /v1/video/generations/:task_id | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTaskFetch | router/video-router.go:24 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 332 | POST /v1/videos/:video_id/remix | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:25 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 333 | POST /v1/videos | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTask (OpenAI-compatible video) | router/video-router.go:30 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 334 | GET /v1/videos/:task_id | RouteTag('relay') + TokenAuth + Distribute: controller.RelayTaskFetch | router/video-router.go:31 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 335 | POST /kling/v1/videos/text2video | RouteTag('relay') + KlingRequestConvert + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:38 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 336 | POST /kling/v1/videos/image2video | RouteTag('relay') + KlingRequestConvert + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:39 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 337 | GET /kling/v1/videos/text2video/:task_id | RouteTag('relay') + KlingRequestConvert + TokenAuth + Distribute: controller.RelayTaskFetch | router/video-router.go:40 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 338 | GET /kling/v1/videos/image2video/:task_id | RouteTag('relay') + KlingRequestConvert + TokenAuth + Distribute: controller.RelayTaskFetch | router/video-router.go:41 | REFERENCE_PLACEHOLDER | task platform placeholder (KNOWN_DEVIATIONS #3) |
| 339 | POST /jimeng/ | RouteTag('relay') + JimengRequestConvert + TokenAuth + Distribute: controller.RelayTask | router/video-router.go:50 | PASS | router/router.go |
| 340 | GET / (web static) | gzip + GlobalWebRateLimit + Cache + static.Serve: embedded web/dist frontend | router/web-router.go:25-28 | PASS | main.go serveEmbedded |
| 341 | NoRoute fallback | gzip + GlobalWebRateLimit + Cache; RelayNotFound for /v1,/api,/assets, else SPA | router/router.go + main.go serveEmbedded | PASS | controller.RelayNotFoundRoute + SPA fallback |
