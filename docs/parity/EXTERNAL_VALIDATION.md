# External Validation

Status of validation against live external systems.

## Live providers (OpenAI, Anthropic, Gemini, ...)

**BLOCKED_MISSING_CREDENTIALS** — no live provider credentials are available in
this environment and `ALLOW_LIVE_EXTERNAL_TESTS=false`. Provider behavior was
validated against a local mock upstream (an OpenAI-compatible HTTP server) that
exercises non-stream and SSE-stream responses, usage extraction, and settlement.

## OAuth applications

**BLOCKED_MISSING_CREDENTIALS** — no OAuth client credentials configured.

## WeChat login server

**BLOCKED_MISSING_CREDENTIALS** — the code→openid exchange was validated against
a local mock server (`go test ./controller/ -run TestWeChat`); no real WeChat
login server address/token is available in this environment.

## Payments (Stripe, EPay, Creem, Waffo)

**BLOCKED_MISSING_CREDENTIALS** — no sandbox keys configured; no live calls made.

## Reference runtime comparison

The reference system is inspected read-only. Reference runtime execution is
permitted in isolated synthetic environments, but a complete side-by-side golden
suite has not yet been run. Protocol DTOs are reconciled against the reference's
OpenAPI/DTO inventory. Iteration 87 reconciled the model-ratio reset from the
reference handler and default-registry contracts, then verified TokenRouter's
observable response, database effects, live quota effect, authorization, and
multi-node reload behavior with fresh SQLite fixtures; no external service or
production data was involved. Iteration 88 reconciled both performance-metrics
response contracts, option defaults, bucket arithmetic, relay success/failure and
first-token timing hooks, durable additive upserts, retention, and active-group
filtering against the read-only reference source. Target behavior was exercised
with synthetic SQLite buckets and local mock OpenAI/Claude streams only.
Iteration 89 reconciled the rule-based channel-affinity settings, cache/clear
contracts, retry behavior, usage-cache counters, and option defaults against the
read-only reference. TokenRouter exercised the complete routing lifecycle with
synthetic SQLite channels and local mock upstreams. Redis behavior is implemented
through the configured shared go-redis client with atomic Lua updates and a
bounded in-memory fallback; no external Redis or provider credentials were used.
Iteration 90 reconciled the group and prefill-group controllers, JSON wire shape,
soft-delete uniqueness, timestamps, filters, and response envelopes against the
read-only reference. Validation used fresh and legacy synthetic SQLite schemas;
no production data or external service was involved.
Iteration 91 reconciled model metadata CRUD/search/enrichment, missing-model
calculation, sync preview/apply shapes, catalog URLs, and selective overwrite
behavior against the read-only reference. All upstream behavior was exercised
against local synthetic HTTP catalogs, including explicit disabled status,
failure envelopes, transport failures, bounded/cancelled requests, and no-op
sync. Fresh and deliberately duplicated legacy SQLite registries verified the
portable active-name migration and reference-preserving soft-delete recovery;
no paid provider, production data, or external metadata service was contacted.
Iteration 92 reconciled Midjourney and generic asynchronous task history paging,
filters, role/ownership boundaries, public DTOs, and image-forwarding behavior
against the read-only reference. Synthetic SQLite tasks covered own-user and
cross-user views, malformed legacy JSON, missing tables, and private-data
redaction. The image proxy used only a local HTTP server and exercised success,
upstream-error, MIME hardening, missing-task, response bounds, and SSRF rejection;
no task-provider credential, production record, or external image host was used.
Iteration 93 reconciled the dashboard playground's session/PAT boundary,
request-local token context, optional group selection, and delegation to the full
chat relay lifecycle. A local synthetic OpenAI server covered non-stream and SSE
responses, channel authentication/path setup, usage settlement, wallet deduction,
logs, and group authorization; no provider credential or external endpoint was
used.
Iteration 94 reconciled the official Jimeng submit/GetResult actions, v3 model
normalization, direct HMAC-SHA256 and gateway-bearer authentication, public/private
task IDs, status/result mapping, per-call duration billing, and ownership-scoped
polling against the read-only reference. Local HTTP providers validated signed
query/payload contracts, accepted-task persistence, terminal caching, definitive
failure refunds, no post-dispatch retries, atomic user/token accounting, and
concurrent terminal-state monotonicity. No Jimeng credential, paid endpoint,
production task, or external image URL was used; live Jimeng parity remains
BLOCKED_MISSING_CREDENTIALS.
