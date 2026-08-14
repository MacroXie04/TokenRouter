# Security Matrix

| # | Capability | Status | Notes |
|---|---|---|---|
| 1 | Bearer token auth (relay) | PASS | smoke tested |
| 2 | Password hashing (bcrypt) | PASS | `common.PasswordHash` |
| 3 | Access JWT (HS256) | PASS | `common.GenerateJWT` |
| 4 | Refresh rotation + replay detection | PASS | `service.RefreshSession`; `go test ./service/ -run TestLoginAndRefreshRotation` |
| 5 | Session revocation | PASS | `service.RevokeSession`; `go test ./service/ -run TestRevokeSession` |
| 6 | Role guards (admin/root) | PASS | `middleware/auth.go` AdminAuth/RootAuth; `go test ./middleware/ -run TestRoleGuards` (401/403/200 matrix; also closed a gin Next-chain bypass that ran handlers before the role check) |
| 7 | IP allow-list on tokens | PASS | `middleware.TokenAuth`; `go test ./middleware/ -run TestIPInList` |
| 8 | Setup wizard (no default password) | PASS | `POST /api/setup` |
| 9 | Request body limit (16 MiB) | PASS | `io.LimitReader` in relay parse |
| 10 | Panic recovery (no stack leak) | PASS | `middleware.Recovery` |
| 11 | Secret masking / no secrets in logs | PASS | secret-leakage scan in `scripts/tokenrouter-acceptance.sh` (API key/AWS/GitHub/private-key patterns) passes |
| 12 | SSRF protection | PASS | `common/ssrf.go` (SafeDialContext + ValidateURL); `go test ./common/...` |
| 13 | Rate limiting (global/user/critical) | PASS | `middleware/rate_limit.go` wired + `go test ./common/...` |
| 14 | Turnstile bot protection | BLOCKED_EXTERNAL | `service/turnstile.go` offline siteverify + fail-open/fail-closed tested; live Cloudflare needs secret |
| 15 | TOTP 2FA / backup codes | PASS | `service/twofa.go` + two-step login; `go test ./service/...` |
| 16 | Passkeys/WebAuthn | PASS | `service/passkey.go` (go-webauthn); registration/login + challenge + storage tested |
| 17 | OAuth providers | PASS | `service/oauth.go` (code-flow + bind) mock-server tested; live BLOCKED_EXTERNAL |
| 18 | Casbin RBAC | PASS | `service/authz.go` (enforcer + policies) enforced in AdminAuth; `go test ./service/...` |
| 19 | Trusted proxy / origin guard | PASS | `middleware/proxy.go` (OriginGuard on refresh/logout); `go test ./middleware/...` |
| 20 | CORS | PASS | `RelayCORS` + `DashboardCORS` |
| 21 | Telegram login verification | PASS | `service/telegram.go` HMAC data-check-string; `go test ./service/...` |
| 22 | WeChat login verification | PASS | `service/wechat.go` server-mediated code→openid exchange; `go test ./controller/ -run TestWeChat` (live server BLOCKED_EXTERNAL) |
| 23 | Channel key masking + step-up disclosure | PASS | `controller/channel.go` masks keys in list/get; `POST /api/channel/:id/key` requires RootAuth + 2FA/passkey security proof; `go test ./controller/ -run TestChannelKey` |
| 24 | Security-proof step-up tokens | PASS | `service/security_proof.go` HMAC-JWT proofs (5 min TTL) bound to session/user/auth-version; `go test ./service/ -run TestIssueAndVerifySecurityProof` |
| 25 | Universal 2FA step-up (POST /api/verify) | PASS | `controller/secure_verify.go` TOTP-verified proof issuance for allowed scopes; `go test ./controller/ -run TestUniversalVerify` |
| 26 | Passkey step-up verify flow | PASS | `controller/passkey.go` verify begin/finish with session-bound single-use flows; `go test ./controller/ -run TestPasskeyVerify` |
| 27 | Passkey register/delete 2FA gates | PASS | `controller/passkey.go` + `controller/self.go`; `go test ./controller/ -run TestPasskeyRegisterGate\|TestPasskeyDelete` |
| 28 | Admin passkey reset | PASS | `controller/admin_security.go` AdminResetPasskey: role guard (root or strictly higher), deletes passkeys + revokes all sessions, audit log; `go test ./controller/ -run TestAdminResetPasskey` |
| 29 | Admin force-disable 2FA | PASS | `controller/admin_security.go` AdminDisable2FA: same-level-or-higher target rejected, disables 2FA + revokes all sessions, audit log; `go test ./controller/ -run TestAdminDisable2FA` |
| 30 | OAuth state ceremony | PASS | `controller/oauth.go` GenerateOAuthCode: known-provider + intent validation, session-bound bind flows, single-use 10-min tokens; `go test ./controller/ -run TestGenerateOAuthState` |
| 31 | Email bind verification | PASS | `service/verification.go` VerifyAndBindEmail: email-keyed single-use codes, taken-email rejection; `go test ./controller/ -run TestEmailBind` |
| 32 | Anonymous request-body limit | PASS | `middleware/request_body_limit.go`: 512 KB default via ANONYMOUS_REQUEST_BODY_LIMIT_KB on all anonymous POST endpoints (413 oversize / 400 unreadable); `go test ./middleware/ -run TestAnonymousRequestBodyLimit` |
| 33 | TurnstileCheck middleware | PASS | `middleware/turnstile_check.go`: query-param token verified against siteverify; wired on verification/reset-password/register/login/checkin; `go test ./middleware/ -run TestTurnstileCheck` + `go test ./controller/ -run TestRegisterTurnstileGate` (live siteverify BLOCKED_EXTERNAL) |
| 34 | Email verification rate limit | PASS | `middleware/email_verification_rate_limit.go`: 2/30s per IP via shared KV store with memory fallback; `go test ./middleware/ -run TestEmailVerificationRateLimit` |
| 35 | Turnstile option-store config | PASS | `setting/setting.go` TurnstileCheckEnabled/SiteKey/SecretKey keys; `service/turnstile.go` option-first with env fallback; /api/status exposes turnstile_check + turnstile_site_key |
| 36 | Token key masking + disclosure | PASS | `controller/token_group.go` masks keys in list/search/detail; full keys only via rate-limited owner-scoped POST /:id/key and POST /batch/keys (<=100 ids); `go test ./controller/ -run TestTokenGetAndKeyDisclosure` |
| 37 | Dashboard access tokens (PAT) | PASS | `service/access_token.go` issues 28-32 char base64 PATs (replaces old on regeneration); `middleware/auth.go` accepts them as bearer fallback across UserAuth/AdminAuth/RootAuth; `go test ./controller/ -run TestGenerateAccessToken\|TestAccessTokenAuthenticatesAdminRoutes` |
| 38 | Admin user manage role guards | PASS | `controller/admin_user.go` ManageUser: Root protections (disable/delete/demote), same-level-or-higher rejection, root-only promote, session revocation on demote, audit logs; `go test ./controller/ -run TestAdminManageUser` |
| 39 | Admin user creation isolation + role/permission guards | PASS | `controller/admin_user.go` CreateUser: AdminAuth, equal/higher-role rejection, four-field persistence whitelist, bcrypt/defaults, transactional root-only permission provisioning with rollback/reload, actor audit; `go test ./controller/ -run TestAdminCreateUser` |
| 40 | Root-only option and compliance controls | PASS | `/api/option/` moved from AdminAuth to RootAuth; PAT-auth context marker and explicit PAT denial for compliance; sensitive Token/Secret/Key options omitted; compliance keys blocked from generic writes; positive affiliate bonuses require current terms; `go test ./controller/ -run 'TestPaymentCompliance|TestRootOption|TestOptionAndCompliance'` |
| 41 | Root-only model-pricing reset and validated live option writes | PASS | POST `/api/option/rest_model_ratio` is RootAuth (anonymous 401, common/admin 403); generic `ModelPrice`/`GroupRatio` edits reject malformed, negative, non-finite, empty-name, and non-positive-ratio values before persistence or cache publication; management logs contain only `option.reset_ratio`/the option key; `go test ./controller/ -run 'TestResetModelRatio|TestPricingOptionsWriteThrough'` |
| 42 | Affinity-key privacy and cache-control authorization | PASS | Raw affinity values are SHA-256-keyed and never exposed; logs/cache telemetry use only 8-hex fingerprints. Cache GET/DELETE are RootAuth, usage counters are AdminAuth, malformed rules are rejected before persistence/publication, and outbound pass-through is an explicit header allow-list; `go test ./controller/ -run TestChannelAffinity` + `go test ./service/ -run TestRuleBasedChannelAffinityRoutingAndPrivacy` |
| 43 | Jimeng task credential, request-resource, and ownership hardening | PASS | `relay/channel/jimeng` signs direct `access_key|secret_key` credentials with HMAC-SHA256 and uses bearer mode only for `sk-` gateway keys; outbound transport uses `SafeDialContext`, validates HTTP(S) image/base URLs, caps images at ~4.7 MiB and provider responses at 1 MiB, and never exposes upstream task IDs or channel keys. `/jimeng/` applies per-IP global limiting and validates only the cheap Action selector before `TokenAuth`; its body is allocated/decoded only after authentication and is capped at 16 MiB. Fetches are owner-scoped and terminal status writes are monotonic under concurrency. `go test ./relay/channel/jimeng ./controller/ ./relay/ -run 'TestJimeng|TestPersistJimeng' -count=1` + affected race suite. |
