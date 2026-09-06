# External Validation

Last reconciled: 2026-09-06

This document separates live external proof from deterministic local or
disposable-infrastructure evidence. A mocked signature, callback, or provider
response is not described as a live third-party result.

## Credentialed third-party systems

The following remain **BLOCKED_MISSING_CREDENTIALS**. No authorized credentials,
paid requests, production records, or externally reachable callbacks were used.

| Boundary | Live status | Local evidence already available |
|---|---|---|
| AI providers, including OpenAI, Anthropic, Gemini, and Jimeng | BLOCKED_MISSING_CREDENTIALS | Provider-specific mock servers exercise supported authentication, paths, conversion, streams, bounds, usage, task recovery, and billing. No operative deterministic provider row remains open; this block is solely the absence of a credentialed live exchange. |
| Built-in and custom OAuth providers | BLOCKED_MISSING_CREDENTIALS | State, redirect, token/userinfo, binding, ownership, discovery, and failure behavior use local fixtures. |
| WeChat login | BLOCKED_MISSING_CREDENTIALS | The code-to-openid exchange, binding, and failure paths use a local server. |
| Stripe wallet and subscription checkout/webhooks | BLOCKED_MISSING_CREDENTIALS | Signed webhook fixtures, immutable economic binding, durable pending orders, reconciliation leases/fencing, fulfillment, expiry, and reversal/manual-review policy are tested locally. |
| EPay wallet checkout/webhook | BLOCKED_MISSING_CREDENTIALS | Request signing, callback verification, exact order binding, replay/idempotency, and settlement are covered locally. |
| Creem, classic Waffo, and Waffo Pancake payments | BLOCKED_MISSING_CREDENTIALS | Fixed-endpoint clients, exact signatures and wire contracts, bounded failure handling, immutable wallet/subscription economics, replay-safe signed webhook settlement, and root configuration workflows use deterministic local transports and fixtures. |
| Cloudflare Turnstile | BLOCKED_MISSING_CREDENTIALS | Local success/failure fixtures cover the target integration boundary. |
| SMTP delivery | BLOCKED_MISSING_CREDENTIALS | An injectable mailer proves message construction and application behavior; it does not prove deliverability or reputation. |

EPay subscription checkout/callback plus Creem, classic Waffo, and Waffo
Pancake payment endpoints are implemented and locally verified. Credentialed
checkout and callback delivery remain external blocks; deterministic fixtures
are not presented as live-provider proof.

## Live disposable infrastructure

These checks used real server implementations with synthetic data. Current
2026-09-06 results and explicitly dated historical evidence are distinguished:

| System | Result | Exercised boundary |
|---|---|---|
| MySQL 9.5 | PASS (current snapshot) | The exact expanded nine-test manifest passed full migration/restart, relational retention, relay accounting and channel-key concurrency, scheduler cadence, audit-outbox leasing/delivery, subscription usage epochs, provider-neutral TaskOperation accounting, and Jimeng atomic lifecycle against a newly initialized loopback-only Homebrew server and disposable data directory. |
| PostgreSQL 17 | PASS (current snapshot) | The exact expanded nine-test manifest passed full migration/restart, relational retention, relay accounting and channel-key concurrency, scheduler cadence, audit-outbox leasing/delivery, subscription usage epochs, provider-neutral TaskOperation accounting, and Jimeng atomic lifecycle. |
| ClickHouse 24.8 | PASS (current snapshot) | Both current manifest tests passed configurable TTL addition/removal, DDL/migration, raw log insertion, stable audit retry deduplication, content readback, and retention lifecycle. |
| Redis 7 | NOT RUN LOCALLY | Its exact shared-store atomicity/expiry selector and manifest wiring are statically verified, but no local service or cached image was available. |
| Docker | PASS (current snapshot) | Both complete acceptance runs built the multi-stage frontend/backend image, started it in release mode with isolated SQLite, probed `/api/status`, and cleaned up. |

The database manifest runner requires exactly one run and pass event for every
named test and rejects missing, skipped, failed, or malformed events.

The CI manifest contains nine MySQL tests, nine PostgreSQL tests, two ClickHouse
tests, and one Redis test. All selectors, manifest wiring, normal/race
compilation, vet, and workflow YAML are verified. The full MySQL, PostgreSQL,
and ClickHouse manifests ran on the current snapshot; Redis live execution
remains unavailable without additional local infrastructure.

## Reference comparison

The reference checkout at
`/Users/hongzhe/Code/new-api@ccd535ef8e50cf6e5846a59278c40b7ff59d1b7d`
was inspected read-only. TokenRouter was compared through route/entity/provider
inventories, DTO and wire-contract analysis, and target-side behavioral tests.
A complete side-by-side golden runtime suite has not been executed. The strict
matrices therefore distinguish demonstrated local behavior, consciously
retained schema differences, pinned-reference placeholders, and live external
blocks instead of treating source-shape identity as observable parity.

Local provider servers and synthetic databases cover deterministic success,
failure, retry, streaming, ownership, accounting, and recovery behavior without
contacting a real vendor. These tests are strong implementation evidence but do
not establish account validity, regional availability, current vendor behavior,
deliverability, or production operational readiness.

## Requirements to clear the blocks

Clearing a live block requires explicit authorization, isolated sandbox
accounts, least-privilege credentials, exact registered callback URLs, bounded
test spend, a cleanup plan, and recorded redacted results. Production
credentials or data should not be introduced merely to turn a matrix row green.
