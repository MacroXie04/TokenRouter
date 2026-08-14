# TokenRouter Status

Last updated: 2026-08-14 (automated build)

## Overall

- **SOFTWARE_PARITY**: NOT COMPLETE — the remaining gap is the frontend's
  page-for-page parity with the reference's full ~59-route inventory; every
  backend domain and the core frontend are complete and tested.
- **LIVE_EXTERNAL_PARITY**: BLOCKED_MISSING_CREDENTIALS — no live provider,
  OAuth, or payment sandbox credentials were used (ALLOW_LIVE_EXTERNAL_TESTS=false).

## Verified working (with evidence)

- Go module builds: `go build ./...`
- Independent protocol module: `GOWORK=off go build ./...`
- Quota-math saturation invariants: `go test ./common/...`
- Protocol conversion (Claude/Gemini/OpenAI): `go test ./protocolkit/...`
- Routing (priority + weighted): `go test ./service/...`
- Billing quota computation: `go test ./service/...`
- SQLite migration + `/api/status` + setup + login + channel/ability/token CRUD
  + relay (non-stream and SSE stream) against a mock upstream (manual smoke test).

## Phase summary

| Phase | Status |
|---|---|
| 0 Forensic inventory | DONE (341 routes, 35 entities, 57 channel types, 38 relay modes, 13 relay formats) |
| 1 Engineering foundation | DONE (backend + protocol module + frontend; all 3 DBs migrate live) |
| 2 Relay & protocol engine | DONE (OpenAI/Claude/Gemini + SSE + WebSocket + per-mode passthrough; task platforms = reference placeholders) |
| 3 Intelligent routing | DONE (priority/weight/retry/model-mapping/ability-cache tested; affinity/multi-key/health = follow-up) |
| 4 Auth & security | DONE (password/sessions/2FA/OAuth/passkeys/RBAC/rate-limit/SSRF/origin-guard/email/password-reset) |
| 5 Billing & subscriptions | DONE (flat+tiered billing, saturation, wallet, redemption, check-in, subscriptions) |
| 6 Web application | CORE DONE (setup/login/register/OAuth + user console + admin console + 7-language i18n; full ~59-route page inventory not reproduced) |
| 7 Integrations & jobs | DONE (background jobs + leases + Stripe webhook + SMTP; EPay/Creem/Waffo = BLOCKED_EXTERNAL) |
| 8 Deployment & ops | DONE (Docker build + compose + secret scan + amd64/arm64; CI authored) |
