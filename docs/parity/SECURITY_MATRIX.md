# Security Matrix

| # | Capability | Status | Notes |
|---|---|---|---|
| 1 | Bearer token auth (relay) | PASS | smoke tested |
| 2 | Password hashing (bcrypt) | PASS | `common.PasswordHash` |
| 3 | Access JWT (HS256) | PASS | `common.GenerateJWT` |
| 4 | Refresh rotation + replay detection | IN_PROGRESS | `service.RefreshSession` |
| 5 | Session revocation | IN_PROGRESS | `service.RevokeSession` |
| 6 | Role guards (admin/root) | IN_PROGRESS | `AdminAuth`/`RootAuth` |
| 7 | IP allow-list on tokens | IN_PROGRESS | `middleware.TokenAuth` |
| 8 | Setup wizard (no default password) | PASS | `POST /api/setup` |
| 9 | Request body limit (16 MiB) | PASS | `io.LimitReader` in relay parse |
| 10 | Panic recovery (no stack leak) | PASS | `middleware.Recovery` |
| 11 | Secret masking / no secrets in logs | PASS | secret-leakage scan in `scripts/tokenrouter-acceptance.sh` (API key/AWS/GitHub/private-key patterns) passes |
| 12 | SSRF protection | PASS | `common/ssrf.go` (SafeDialContext + ValidateURL); `go test ./common/...` |
| 13 | Rate limiting (global/user/critical) | PASS | `middleware/rate_limit.go` wired + `go test ./common/...` |
| 14 | Turnstile bot protection | NOT_STARTED | |
| 15 | TOTP 2FA / backup codes | PASS | `service/twofa.go` + two-step login; `go test ./service/...` |
| 16 | Passkeys/WebAuthn | PASS | `service/passkey.go` (go-webauthn); registration/login + challenge + storage tested |
| 17 | OAuth providers | PASS | `service/oauth.go` (code-flow + bind) mock-server tested; live BLOCKED_EXTERNAL |
| 18 | Casbin RBAC | PASS | `service/authz.go` (enforcer + policies) enforced in AdminAuth; `go test ./service/...` |
| 19 | Trusted proxy / origin guard | PASS | `middleware/proxy.go` (OriginGuard on refresh/logout); `go test ./middleware/...` |
| 20 | CORS | PASS | `RelayCORS` + `DashboardCORS` |
