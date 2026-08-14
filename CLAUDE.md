# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

TokenRouter is an AI API gateway compiled into a single Go binary: an OpenAI-compatible relay data plane (`/v1/*`, `/v1beta/*`) that sits between clients and upstream LLM providers, plus a dashboard/control-plane API (`/api/*`) and an embedded React SPA.

It is an independent, MIT-licensed reimplementation of an AGPL-licensed reference gateway, built for behavioral parity. Parity state and decisions live in `docs/parity/` (`STATUS.md`, `KNOWN_DEVIATIONS.md`, `API_MATRIX.md`, `PROVENANCE.md`). Behaviors that look wrong may be deliberate parity choices — e.g. Chinese relay error strings, structured-501 placeholder endpoints, `QuotaPerUnit = 500000` — check `KNOWN_DEVIATIONS.md` before "fixing" them. Never copy code from the reference project; TokenRouter is written from scratch.

## Commands

Backend (Go 1.26, repo root). CI (`.github/workflows/ci.yml`) runs exactly these:

```bash
go build ./...          # requires web/dist to exist — see gotcha below
go vet ./...
go test ./...           # does NOT include protocolkit/ (separate module)
go test ./service/                          # one package
go test ./controller/ -run TestRegister     # one test
go test -race ./common/... ./service/...    # race pass for quota/routing code
```

`protocolkit/` is a separate Go module (consumed via a `replace` directive) and must also build standalone:

```bash
cd protocolkit && GOWORK=off go vet ./... && GOWORK=off go build ./... && GOWORK=off go test ./...
```

Frontend (`web/`, bun + Rsbuild + TypeScript):

```bash
cd web && bun install
bun run typecheck
bun run build           # outputs web/dist, which gets embedded into the Go binary
```

Repo-level checks:

```bash
scripts/tokenrouter-acceptance.sh    # full acceptance gate: vet+build+test+race+protocolkit+frontend
node scripts/check-translations.mjs  # all locale files must have identical key sets (run from repo root)
scripts/update-api-matrix.sh         # re-classify docs/parity/API_MATRIX.md from the actual router
scripts/verify-api-matrix.sh         # verify every API_MATRIX row against the real route table
```

Run locally: `go run .` — listens on `:3000`, zero-config SQLite (`tokenrouter.db`). Config via env vars / `.env` (see `.env.example`). The initial root account is created through the setup wizard (`POST /api/setup`); there is no default password. Outbound relay traffic goes through an SSRF guard (`common.SafeDialContext`); set `SSRF_DISABLE=true` only when local channels point at loopback upstreams.

**Build gotcha:** `web/embed.go` does `//go:embed dist` and `web/dist/` is gitignored, so on a fresh checkout `go build ./...` fails until you run `cd web && bun install && bun run build`.

## Architecture

Layering (top to bottom):

- `router/` — all route registration in `SetUpRouter()`: public + user `/api`, admin `/api` (AdminAuth), relay `/v1` + `/v1beta` (TokenAuth)
- `controller/` — HTTP handlers
- `service/` — business logic: auth flows, channel selection, billing/quota, subscriptions, background jobs, Casbin authz
- `model/` — GORM entities; `AllModels` in `model/main.go` is the AutoMigrate list — add new entities there
- `setting/` — DB-backed options table cached in memory, hot-reloaded via `Sync()` for multi-node deployments
- `relay/` — relay engine: lifecycle in `relay/controller.go`, adapter registry in `relay/adaptor.go`, provider adapters in `relay/channel/{openai,claude,gemini}`
- `protocolkit/` — pure, standalone module: OpenAI/Responses/Claude/Gemini DTOs plus wire-format conversion and usage normalization; it must never import the root module
- `middleware/` — dashboard auth (JWT + session), relay auth (bearer token), rate limits, CORS, origin guard, step-up secure verification
- `common/` — JSON wrapper (jsoniter; all business code routes through it), quota saturation math, Redis with in-memory fallback, SSRF guard, env helpers
- `pkg/billingexpr/` — expr-lang based price expressions

### Relay request lifecycle (`relay/controller.go`, `relayAndSettle`)

1. `TokenAuth` resolves the bearer API key to a `Token` and its owning `User`.
2. URL path maps to a `RelayMode` (`constant/`); per-request state travels in `RelayInfo`.
3. Channel selection: `Ability` rows (group, model, channel, priority, weight) chosen priority-first then weighted-random, with retries that exclude already-failed channels; served from a cache (`service.InitAbilityCache`).
4. Pre-consume: user quota is reserved before the upstream call so concurrent requests cannot overspend; token quota is checked.
5. The adapter converts the OpenAI request to the provider wire format and back (`relay.GetAdaptor`: Anthropic → claude, Gemini/Vertex → gemini, everything else → openai adapter). The conversion functions themselves live in `protocolkit`.
6. Settlement: actual usage is priced via the model-price registry (USD per 1M tokens), converted to quota with saturation clamps, settled against user + token, and a consumption log is written to `LOG_DB`.

Native (non-OpenAI-format) surfaces pass through without conversion: `/v1/messages` (Claude, `relay/claude_messages.go`), `/v1beta/...` and `/v1/models/*path` (Gemini generateContent, `relay/gemini_native.go`), `/v1/realtime` (WebSocket, `relay/realtime.go`). Some responses also adapt their shape to caller headers (e.g. `x-api-key` + `anthropic-version` gets Claude-shaped model listings).

### Control plane

Dashboard auth uses short-lived access JWTs plus server-side sessions with refresh-token rotation and replay detection (`service/auth_flow.go`). Roles are User/Admin/Root, layered with Casbin (`service/authz.go`). Sensitive account/admin actions require step-up verification (`POST /api/verify`, `middleware/secure_verification.go`). 2FA (TOTP + backup codes), passkeys (WebAuthn), and OAuth (GitHub/Discord/Telegram/WeChat/LinuxDO/custom) are all in `service/`.

### Persistence

GORM v2. Primary DB selected by env: SQLite (default), or MySQL/PostgreSQL via `SQL_DSN` prefix. Optional separate log DB via `LOG_SQL_DSN`, which may be `clickhouse://` — `model/clickhouse.go` hand-builds MergeTree DDL instead of AutoMigrate. Row locking uses `clause.Locking{Strength: "UPDATE"}` and is skipped on SQLite. Redis (`REDIS_CONN_STRING`) backs shared cache/rate limiting when configured, with an in-memory fallback otherwise.

### Frontend (`web/`)

React 19 + Rsbuild + axios + i18next. Views live in `web/src/views/`; the shared axios instance (`web/src/api.ts`) targets same-origin `/api` with cookie credentials. No dev-server proxy is configured, so verify UI changes against the Go binary serving a fresh `bun run build`. `main.go` serves the embedded SPA with index.html fallback and analytics injection; `/v1`, `/api`, and `/assets` paths never fall back to the SPA (they return the structured relay 404). All locale files in `web/src/i18n/locales/` must keep identical key sets — verify with `node scripts/check-translations.mjs`.

## Parity workflow

When adding, changing, or removing routes, update the machine-maintained matrix: run `scripts/update-api-matrix.sh`, then `scripts/verify-api-matrix.sh` (it dumps the real route table via `go test ./router/ -run TestDumpRoutes` and exact-matches every matrix row — no suffix guessing). Intentional differences from the reference get a numbered entry in `docs/parity/KNOWN_DEVIATIONS.md`; iteration status goes in `docs/parity/STATUS.md`.

Relay-facing error messages are intentionally Chinese (parity with the reference wire contract) — do not translate them.
