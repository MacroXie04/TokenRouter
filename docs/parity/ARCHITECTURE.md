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
  relay/channel/     Provider adapters/profiles (openai, claude, gemini, codex, jimeng, sora, ...)
protocolkit/ Independent protocol-conversion module (separate go.mod)
middleware/  Auth, CORS, request IDs, recovery, rate limiting
common/      JSON wrapper, quota math, crypto, Redis fallback, env
constant/    Channel types, relay formats/modes
```

## Data Plane

The relay data plane implements the OpenAI-compatible and native relay APIs
(`/v1/*`), plus bounded durable task lifecycles for the reference asynchronous
media families, including Jimeng, Midjourney, Suno, Kling, Gemini/Vertex Veo,
and the six OpenAI/Sora video routes. Provider-specific codecs share the
provider-neutral reservation and recovery machinery where their wire contracts
permit it. An implemented relay request flows through:

1. **Token auth** — bearer API key resolves to a `Token` and its owning `User`.
2. **Relay-mode resolution** — the URL path maps to a `RelayMode`.
3. **Channel selection** — an eligible settings-driven affinity hit may select
   a preferred channel first. Otherwise `Ability` rows (group, model, channel,
   priority, weight) are selected priority-first then weighted-random, with
   retries that exclude failed channels.
4. **Provider-safe dispatch** — the registered provider profile selects the
   native or explicitly compatible wire contract, URL, authentication, model
   mapping, and conversion. Unsupported native combinations fail before network
   I/O.
5. **Durable accounting** — funding and, for limited tokens, a token hold are
   committed before dispatch. Settlement or refund is exact and idempotent;
   ambiguous outcomes remain in the operation journal for recovery or audited
   manual review.
6. **Usage and audit logging** — consumption and security events are written to
   the configured log sink. A primary-database outbox preserves a checked
   audit event's delivery payload while a separate sink is unavailable or an
   outcome is ambiguous, then atomically replaces delivered payloads with
   content-free receipts.

## Control Plane

The dashboard API (`/api/*`) provides user, token, channel, ability, log, and
option management. Auth uses short-lived access JWTs plus server-side sessions
with refresh-token rotation and replay detection. Roles are User / Admin / Root.

## Protocol Conversion Module

`protocolkit/` is a separate Go module with no dependency on the root module. It
holds the OpenAI/Responses/Claude/Gemini DTOs and pure conversion + usage
normalization functions, and builds standalone with `GOWORK=off go build ./...`.

## Persistence

GORM v2 supports SQLite, MySQL, and PostgreSQL. The aggregate target migration
currently creates the 34 reference entity tables plus five target-only
durability tables for the audit outbox, Jimeng operations, provider-neutral task
operations, relay reservations, and reservation-review events. `LOG_SQL_DSN`
optionally selects a separate log sink, which may use ClickHouse. Row locking
uses `clause.Locking{Strength:"UPDATE"}` where the
connected dialect supports it; critical state machines additionally use
compare-and-swap transitions, database time, bounded leases, and fencing. Redis
is used for shared cache/rate-limiter state when configured, with a bounded
in-memory fallback where supported.

The primary database clock is authoritative for `created_at` emitted by checked
audit-log writes and for every audit-outbox lifecycle transition (enqueue,
scan, claim, retry, and delivery completion), as well as instance liveness,
periodic leases, and retention cutoffs. Before a node's monotonic heartbeat
upsert, a node-scoped delete repairs its legacy row only when the statement-time
database clock finds it more than 90 seconds in the future; a concurrent newer
legitimate row survives that predicate. Configured log-sink cleanup reads
`LOG_RETENTION_DAYS` (default 30, bounded from 0 through 36500) and is performed
by the fenced `log_cleanup` task, which physically removes rows from relational
or ClickHouse log sinks. A value of `0` disables scheduled retention only; a
root operator can still run manual cleanup. A valid configured horizon whose
cutoff is at or before the Unix epoch succeeds as a no-op without enqueueing an
invalid task. The optional native ClickHouse TTL uses ClickHouse's own server
clock, so it defaults off. An invalid or out-of-range
`LOG_SQL_CLICKHOUSE_TTL_DAYS` also disables that independent TTL;
leaving it at `0` avoids introducing a second clock authority.

Delivered audit-outbox rows retain only content-free SHA-256 receipts. Before
serving, startup drains the indexed legacy delivered-payload scan in 500-row
batches; each leased periodic audit-delivery pass also scrubs at most one such
batch, even when another delivery in that pass fails. This limits raw payloads
left by an older binary, but receipt-only replay is not compatible with old
workers; mixed-version deployments must use the coordinated/drained rollout
described in Known Deviations item 25.

## Recovery and secret handling

Security-sensitive credentials and identifiers are issued only from checked
cryptographic entropy. Provider and payment credentials are never placed in
public task DTOs or audit payloads. Ordinary, realtime, Stripe, subscription,
audit-outbox, Jimeng, and OpenAI/Sora video workflows retain enough durable state to reconcile a
restart or ambiguous commit without redispatching a possibly accepted request
or inventing an accounting outcome.
