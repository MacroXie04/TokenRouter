# TokenRouter Architecture

TokenRouter is an AI API gateway: a relay data plane that sits between clients
and upstream AI providers, and a SaaS administration control plane. This
document describes the independent implementation's architecture.

## Layering

```
web/ (React 19 + Rsbuild)  →  embedded dist served by the Go binary
            │
            ▼
router/      HTTP routing (API, dashboard, relay, web)
controller/  Request handlers
service/     Business logic (auth, routing, billing, channels, logs)
model/       GORM entities + migration (SQLite / MySQL / PostgreSQL)
setting/     Database-backed options with hot-reload cache
relay/       Relay engine (request lifecycle, adapter registry)
  relay/common/      Shared types (Meta, Adaptor, tokenization, errors)
  relay/channel/     Provider adapters (openai, claude, gemini)
protocolkit/ Independent protocol-conversion module (separate go.mod)
middleware/  Auth, CORS, request IDs, recovery, rate limiting
common/      JSON wrapper, quota math, crypto, Redis fallback, env
constant/    Channel types, relay formats/modes
```

## Data Plane

The relay data plane implements the OpenAI-compatible API (`/v1/*`) plus
Midjourney/Suno/video task routes. A request flows through:

1. **Token auth** — bearer API key resolves to a `Token` and its owning `User`.
2. **Relay-mode resolution** — the URL path maps to a `RelayMode`.
3. **Channel selection** — `Ability` rows (group, model, channel, priority,
   weight) are selected priority-first then weighted-random, with retries that
   exclude failed channels.
4. **Protocol conversion** — the adapter converts the OpenAI request to the
   provider wire format (OpenAI, Claude, Gemini) and converts the response back.
5. **Billing settlement** — prompt/completion tokens are priced via the model
   price registry, converted to quota with saturation protection, and settled
   against the user and token.
6. **Usage logging** — a consumption log is written to the log database.

## Control Plane

The dashboard API (`/api/*`) provides user, token, channel, ability, log, and
option management. Auth uses short-lived access JWTs plus server-side sessions
with refresh-token rotation and replay detection. Roles are User / Admin / Root.

## Protocol Conversion Module

`protocolkit/` is a separate Go module with no dependency on the root module. It
holds the OpenAI/Responses/Claude/Gemini DTOs and pure conversion + usage
normalization functions, and builds standalone with `GOWORK=off go build ./...`.

## Persistence

GORM v2 with SQLite, MySQL (>=5.7.8) and PostgreSQL (>=9.6) support; an optional
separate log database (which may be ClickHouse). Row locking uses
`clause.Locking{Strength:"UPDATE"}` (skipped for SQLite). Redis is used for the
shared cache/rate-limiter when configured, with an in-memory fallback.
