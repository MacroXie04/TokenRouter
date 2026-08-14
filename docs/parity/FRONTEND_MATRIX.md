# Frontend Matrix

Frontend routes, settings pages, and locale keys in the reference system.

| # | Route / Page / Locale | Detail | Status | Target Evidence |
|---|---|---|---|---|
| 1 | / | Route: home landing page (feature domain: home) \| auth: public | NOT_STARTED |  |
| 2 | /about | Route: about page (feature domain: about) \| auth: public | NOT_STARTED |  |
| 3 | /privacy-policy | Route: privacy policy (feature domain: legal) \| auth: public | NOT_STARTED |  |
| 4 | /user-agreement | Route: user agreement (feature domain: legal) \| auth: public | NOT_STARTED |  |
| 5 | /setup | Route: setup wizard (feature domain: setup); redirects away once setup complete \| auth: public | NOT_STARTED |  |
| 6 | /sign-in | Route: sign-in (feature domain: auth) \| auth: public | NOT_STARTED |  |
| 7 | /sign-up | Route: sign-up (feature domain: auth) \| auth: public | NOT_STARTED |  |
| 8 | /register | Route: register (feature domain: auth) \| auth: public | NOT_STARTED |  |
| 9 | /forgot-password | Route: forgot password (feature domain: auth) \| auth: public | NOT_STARTED |  |
| 10 | /reset | Route: reset password (feature domain: auth) \| auth: public | NOT_STARTED |  |
| 11 | /user/reset | Route: user reset link (feature domain: auth) \| auth: public | NOT_STARTED |  |
| 12 | /otp | Route: one-time password (feature domain: auth) \| auth: public | NOT_STARTED |  |
| 13 | /oauth | Route: OAuth entry (feature domain: auth) \| auth: public | NOT_STARTED |  |
| 14 | /oauth/$provider | Route: OAuth provider callback (feature domain: auth) \| auth: public | NOT_STARTED |  |
| 15 | /401 | Route: 401 error page (feature domain: errors) \| auth: public | NOT_STARTED |  |
| 16 | /403 | Route: 403 forbidden page (feature domain: errors) \| auth: public | NOT_STARTED |  |
| 17 | /404 | Route: 404 not found page (feature domain: errors) \| auth: public | NOT_STARTED |  |
| 18 | /500 | Route: 500 error page (feature domain: errors) \| auth: public | NOT_STARTED |  |
| 19 | /503 | Route: 503 error page (feature domain: errors) \| auth: public | NOT_STARTED |  |
| 20 | /pricing | Route: model pricing list (feature domain: pricing) \| auth: conditional (public by default; requireAuth enforced when module access configured) | NOT_STARTED |  |
| 21 | /pricing/$modelId | Route: model pricing detail (feature domain: pricing) \| auth: conditional (same module-access policy as /pricing) | NOT_STARTED |  |
| 22 | /rankings | Route: usage rankings leaderboard (feature domain: rankings) \| auth: conditional (public by default; requireAuth enforced when configured) | NOT_STARTED |  |
| 23 | /_authenticated (layout) | Route layout: authenticated shell; redirects to /sign-in when no user/token \| auth: authenticated | NOT_STARTED |  |
| 24 | /channels | Route: channel management (feature domain: channels) \| auth: admin (role >= ADMIN) | NOT_STARTED |  |
| 25 | /chat/$chatId | Route: chat conversation (feature domain: chat); redirects to /dashboard if no active chat \| auth: authenticated | NOT_STARTED |  |
| 26 | /chat2link | Route: chat-to-link conversion (feature domain: chat) \| auth: authenticated | NOT_STARTED |  |
| 27 | /dashboard | Route: dashboard (redirects to default section) (feature domain: dashboard) \| auth: authenticated | NOT_STARTED |  |
| 28 | /dashboard/$section | Route: dashboard section (feature domain: dashboard) \| auth: authenticated | NOT_STARTED |  |
| 29 | /errors/$error | Route: authenticated error detail page (feature domain: errors) \| auth: authenticated | NOT_STARTED |  |
| 30 | /keys | Route: user API keys (feature domain: keys) \| auth: authenticated | NOT_STARTED |  |
| 31 | /models | Route: model list (feature domain: models) \| auth: admin (role >= ADMIN) | NOT_STARTED |  |
| 32 | /models/$section | Route: model section (feature domain: models) \| auth: admin (role >= ADMIN) | NOT_STARTED |  |
| 33 | /playground | Route: API playground (feature domain: playground); redirects to /dashboard if no active chat \| auth: authenticated | NOT_STARTED |  |
| 34 | /profile | Route: user profile (feature domain: profile) \| auth: authenticated | NOT_STARTED |  |
| 35 | /redemption-codes | Route: redemption codes (feature domain: redemption-codes) \| auth: admin (role >= ADMIN) | NOT_STARTED |  |
| 36 | /subscriptions | Route: subscriptions (feature domain: subscriptions) \| auth: admin (role >= ADMIN) | NOT_STARTED |  |
| 37 | /system-info | Route: system info (feature domain: system-info) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 38 | /system-settings (layout) | Route layout: system settings; redirects to /403 unless SUPER_ADMIN \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 39 | /system-settings | Route: system settings landing (feature domain: system-settings) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 40 | /system-settings/auth | Route: system settings - auth page (feature domain: system-settings/auth) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 41 | /system-settings/auth/$section | Route: system settings - auth section (feature domain: system-settings/auth) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 42 | /system-settings/billing | Route: system settings - billing page (feature domain: system-settings/billing) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 43 | /system-settings/billing/$section | Route: system settings - billing section (feature domain: system-settings/billing) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 44 | /system-settings/content | Route: system settings - content page (feature domain: system-settings/content) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 45 | /system-settings/content/$section | Route: system settings - content section (feature domain: system-settings/content) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 46 | /system-settings/models | Route: system settings - models page (feature domain: system-settings/models) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 47 | /system-settings/models/$section | Route: system settings - models section (feature domain: system-settings/models) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 48 | /system-settings/operations | Route: system settings - operations page (feature domain: system-settings/operations) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 49 | /system-settings/operations/$section | Route: system settings - operations section (feature domain: system-settings/operations) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 50 | /system-settings/security | Route: system settings - security page (feature domain: system-settings/security) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 51 | /system-settings/security/$section | Route: system settings - security section (feature domain: system-settings/security) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 52 | /system-settings/site | Route: system settings - site page (feature domain: system-settings/site) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 53 | /system-settings/site/$section | Route: system settings - site section (feature domain: system-settings/site) \| auth: admin (SUPER_ADMIN only) | NOT_STARTED |  |
| 54 | /usage-logs | Route: usage logs (redirects to default section) (feature domain: usage-logs) \| auth: authenticated | NOT_STARTED |  |
| 55 | /usage-logs/$section | Route: usage logs section (feature domain: usage-logs) \| auth: authenticated | NOT_STARTED |  |
| 56 | /users | Route: user management (feature domain: users) \| auth: admin (role >= ADMIN) | NOT_STARTED |  |
| 57 | /wallet | Route: wallet (feature domain: wallet) \| auth: authenticated | NOT_STARTED |  |
| 58 | __root | Route: root layout (TanStack Router root) \| auth: public | NOT_STARTED |  |
| 59 | (auth) layout | Route layout: auth pages (pathless group) \| auth: public | NOT_STARTED |  |
| 60 | System Settings: Auth | Settings page (feature: system-settings/auth); sections: basic-auth, oauth, passkey, bot-protection, custom-oauth | NOT_STARTED |  |
| 61 | System Settings: Billing | Settings page (feature: system-settings/billing); sections: quota, currency, model-pricing, group-pricing, payment, checkin | NOT_STARTED |  |
| 62 | System Settings: Content | Settings page (feature: system-settings/content); sections: dashboard, announcements, api-info, faq, uptime-kuma, chat, drawing | NOT_STARTED |  |
| 63 | System Settings: Models | Settings page (feature: system-settings/models); sections: global, routing-reliability, gemini, claude, grok, channel-affinity, model-deployment | NOT_STARTED |  |
| 64 | System Settings: Operations | Settings page (feature: system-settings/operations); sections: behavior, alerts, email, worker, logs, performance, update-checker | NOT_STARTED |  |
| 65 | System Settings: Security | Settings page (feature: system-settings/security); sections: rate-limit, sensitive-words, ssrf, token-limits | NOT_STARTED |  |
| 66 | System Settings: Site | Settings page (feature: system-settings/site); sections: system-info, notice, header-navigation, sidebar-modules | NOT_STARTED |  |
| 67 | NOTE: no web/src/pages/Setting/ | The directory web/src/pages/Setting/ does not exist in this repo; all settings pages live under web/src/features/system-settings/ (index.tsx + section-registry.tsx per feature). Supporting section components are split across features/system-settings/{general,integrations,maintenance,request-limits}. | NOT_STARTED |  |
| 68 | feature domain: about | Top-level frontend feature domain under web/src/features/ | NOT_STARTED |  |
| 69 | feature domain: auth | Top-level frontend feature domain (sign-in, sign-up, forgot/reset, otp, passkey, oauth) | NOT_STARTED |  |
| 70 | feature domain: channels | Top-level frontend feature domain | NOT_STARTED |  |
| 71 | feature domain: chat | Top-level frontend feature domain | NOT_STARTED |  |
| 72 | feature domain: dashboard | Top-level frontend feature domain | NOT_STARTED |  |
| 73 | feature domain: errors | Top-level frontend feature domain | NOT_STARTED |  |
| 74 | feature domain: home | Top-level frontend feature domain | NOT_STARTED |  |
| 75 | feature domain: keys | Top-level frontend feature domain | NOT_STARTED |  |
| 76 | feature domain: legal | Top-level frontend feature domain (privacy policy, user agreement) | NOT_STARTED |  |
| 77 | feature domain: models | Top-level frontend feature domain | NOT_STARTED |  |
| 78 | feature domain: performance-metrics | Top-level frontend feature domain | NOT_STARTED |  |
| 79 | feature domain: playground | Top-level frontend feature domain | NOT_STARTED |  |
| 80 | feature domain: pricing | Top-level frontend feature domain | NOT_STARTED |  |
| 81 | feature domain: profile | Top-level frontend feature domain | NOT_STARTED |  |
| 82 | feature domain: rankings | Top-level frontend feature domain | NOT_STARTED |  |
| 83 | feature domain: redemption-codes | Top-level frontend feature domain | NOT_STARTED |  |
| 84 | feature domain: setup | Top-level frontend feature domain (setup wizard) | NOT_STARTED |  |
| 85 | feature domain: subscriptions | Top-level frontend feature domain | NOT_STARTED |  |
| 86 | feature domain: system-info | Top-level frontend feature domain | NOT_STARTED |  |
| 87 | feature domain: system-settings | Top-level frontend feature domain (admin settings; subdomains: auth, billing, content, general, integrations, maintenance, models, operations, request-limits, security, site) | NOT_STARTED |  |
| 88 | feature domain: usage-logs | Top-level frontend feature domain | NOT_STARTED |  |
| 89 | feature domain: users | Top-level frontend feature domain | NOT_STARTED |  |
| 90 | feature domain: wallet | Top-level frontend feature domain | NOT_STARTED |  |
| 91 | locale: en | Locale file en.json \| 5266 top-level keys under 'translation' | NOT_STARTED |  |
| 92 | locale: fr | Locale file fr.json \| 5266 top-level keys under 'translation' | NOT_STARTED |  |
| 93 | locale: ja | Locale file ja.json \| 5266 top-level keys under 'translation' | NOT_STARTED |  |
| 94 | locale: ru | Locale file ru.json \| 5266 top-level keys under 'translation' | NOT_STARTED |  |
| 95 | locale: vi | Locale file vi.json \| 5266 top-level keys under 'translation' | NOT_STARTED |  |
| 96 | locale: zh-TW | Locale file zh-TW.json \| 5266 top-level keys under 'translation' | NOT_STARTED |  |
| 97 | locale: zh | Locale file zh.json \| 5266 top-level keys under 'translation' | NOT_STARTED |  |
| 98 | _reports/_sync-report.json | Non-locale artifact under locales/ (i18n sync report), excluded from translation-key counts | NOT_STARTED |  |
| 99 | TokenRouter payment-compliance control | Root-only settings workflow: current v1 status/timestamp, explicit acknowledgement checkbox, dedicated session-auth confirmation action; compliance metadata excluded from generic editing and ordinary admins never fetch/root-render options | PASS | `web/src/views/AdminConsole.tsx` + `web/src/App.tsx`; `npm run typecheck` + production build in acceptance |
| 100 | TokenRouter model-pricing reset control | Root-only settings action describes the immediate live-billing effect, requires explicit browser confirmation, disables while running, reports success/failure, and refreshes persisted options after completion | PASS | `web/src/views/AdminConsole.tsx`; `npm --prefix web run typecheck` + production build in acceptance |
| 101 | TokenRouter model performance workflow | Public home view fetches the reference performance summary and renders 24-hour average latency, success rate and token throughput per model; root grouped settings expose the reference `perf_metrics_setting.*` enabled/flush/bucket/retention controls | PASS | `web/src/views/HomeView.tsx` + `web/src/lib/settings-groups.ts`; `npm --prefix web run typecheck` + production build in acceptance |
| 102 | TokenRouter channel-affinity operations workflow | Root grouped settings expose all six `channel_affinity_setting.*` controls with a multiline validated rules editor; live cache card shows enabled/entry/capacity/algorithm/rule counts and confirms per-rule/all clears with busy/success/error states; ordinary admins cannot fetch or render the root-only controls | PASS | `web/src/views/AdminConsole.tsx` + `web/src/lib/settings-groups.ts`; `npm --prefix web run typecheck && npm --prefix web run build` |
| 103 | TokenRouter prefill-group management workflow | Admin console lists and type-filters reusable groups, creates/edits model/tag lists or endpoint JSON, confirms deletion, supports cancel-edit, and exposes busy/success/error/empty states with labeled controls | PASS | `web/src/views/AdminConsole.tsx` + `web/src/styles.css`; `npm --prefix web run typecheck && npm --prefix web run build` |
