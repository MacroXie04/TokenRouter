# Acceptance Evidence

Last reconciled: **2026-09-06**.

The frozen 2026-09-06 source snapshot passed the complete gate both in the
assembled working tree and from a clean export containing only tracked and
non-ignored source paths. Both runs ended with `ACCEPTANCE: ALL CHECKS PASSED`.
The matching content fingerprint and exact Git base are recorded in
`PROVENANCE.md`. Server-database manifests are recorded separately below and
are not conflated with the local release gate.

The first clean-export attempt exposed that the translation verifier's
self-test ran before its pinned TypeScript dependency was installed. The gate
now runs that check after `npm ci`, and its lightweight regression asserts that
the clean-checkout ordering cannot silently regress. A later race replay under
heavy package contention exposed a test that measured the relay's synchronous
durable refund work as part of its provider-I/O deadline. The regression now
observes outbound request-context cancellation directly while still requiring
the user, token, reservation, and log refund state to be terminal before the
handler returns. Focused normal and race stress runs passed after that repair;
the successful complete runs below were performed only after both fixes.

The local release gate is `scripts/tokenrouter-acceptance.sh`. It records every
independent failure and returns a stable non-zero status when any check fails.
It does not reuse the ignored root binary or a developer database: the startup
smoke builds a temporary binary, starts it outside the checkout with external
database variables cleared, uses a disposable SQLite database, and removes the
workspace on exit.

## Automated release path

The gate covers:

- npm's frozen install plus frontend lint, the complete Vitest suite,
  typecheck, build, and translation completeness. The current direct Vitest
  run passes 98 files / 751 tests; all seven locales have 2,644 keys and cover
  all 2,536 statically discoverable runtime keys.
- `gofmt` over tracked and non-ignored untracked Go source, `go vet`, `go
  build`, the complete Go suite, and targeted common/middleware/relay/router/
  service race tests.
- Standalone `protocolkit` vet, build, and test with `GOWORK=off`.
- Exact API route-matrix regeneration checks.
- An isolated SQLite migration/startup and `/api/status` probe.
- A source-only secret scan that reports locations without echoing candidate
  credentials, distinguishes "no match" from scanner/read failures, and has a
  deterministic regression test for both failure-closed behavior and
  match-level fixture handling.
- Docker Compose configuration when the Docker CLI is present. When a Docker
  daemon is reachable, it also builds the real multi-stage `Dockerfile` under
  a unique tag, starts the image in production mode with explicit empty
  external-store settings and ephemeral SQLite storage, probes `/api/status`,
  then removes the container and image on every exit. Local runs explicitly
  report a skip when Docker is unavailable; CI always runs this build/start
  gate on an Ubuntu runner.

The CI external-datastore job does not rely on `go test -run` returning zero.
Its manifest runner consumes `go test -json` and requires one `run`, one `pass`,
no `skip`, and no `fail` event for every named MySQL, PostgreSQL, ClickHouse,
and Redis test. The verifier's missing/skipped/fail/malformed-event cases have
their own deterministic self-test.

## Current acceptance results

Evidence refreshed on the frozen source snapshot on 2026-09-06:

| Boundary | Result | Evidence |
|---|---|---|
| Frontend tests | PASS | `npm test -- --run`: 98 files / 751 tests; current typecheck and lint also pass. |
| Frontend translations | PASS | Verifier self-test and live scan: 7 equal 2,644-key locales; 2,536 runtime keys across 110 source files (2,019 direct and 517 statically resolved); no missing, extra, invalid, untranslated, or interpolation-mismatched values. |
| API inventory | PASS | Deterministic regeneration and verification: 354 matrix rows, with 342 operative PASS and 12 pinned-reference placeholders. All 352 unique reference HTTP routes match exactly; the target inventory contains 390 routes. |
| Provider inventory | PASS | 206 operative deterministic provider contracts and 5 pinned-reference placeholders; no local implementation row remains open. |
| Channel notifications | PASS | Focused and race tests prove `channel_test` sends only when `notify:true`, uses the enabled-root account's configured email/webhook/Bark/Gotify delivery path, and isolates delivery failure from the task result. |
| Channel balance refresh | PASS | Focused and race tests cover all fixed provider contracts, configured endpoints, numeric/response/SSRF bounds, stale-write fencing, paging, cancellation, overlap, auto-ban, polling pace, schedule validation, and distributed periodic-job leasing. |
| Dependency integrity/advisories | PASS (locally bounded) | `go mod verify` passed and `npm audit --offline` reported zero known vulnerabilities for the full lockfile. No Go vulnerability scanner or locally cached vulnerability database was available, so this is not represented as a fresh network-backed advisory check. |
| Complete assembled-snapshot acceptance | PASS | The gate exited 0 with `ACCEPTANCE: ALL CHECKS PASSED`, including the full race suite and real Docker image probe. |
| Clean-export reproduction | PASS | The same gate exited 0 after exporting only the frozen snapshot's tracked and non-ignored paths into a new directory, initializing a fresh index, and rebuilding generated/ignored dependencies and assets there. |

## Complete gate and server-database results

The local rows below passed on the frozen 2026-09-06 snapshot. MySQL,
PostgreSQL, and ClickHouse also passed their current exact manifests against
disposable local servers. The current Redis selector is statically verified but
was not run because no local service, server binary, or cached image was
available:

| Boundary | Result | Evidence |
|---|---|---|
| npm frontend | PASS | Frozen install, lint, 98 test files / 751 tests, typecheck, build, and translation checks passed in both complete gates. |
| Go backend | PASS | `go vet ./...`, `go build ./...`, `go test ./...`, and the security/routing/accounting/async-task race suite passed. |
| protocolkit | PASS | Independent `GOWORK=off` vet/build/test passed. |
| SQLite | PASS | Empty migration, isolated process start, and `/api/status` passed without inheriting `.env`, SQL, log-store, or Redis settings. |
| MySQL 9.5 | PASS | The current nine-test manifest passed migration/restart, relational retention, relay accounting and channel-key concurrency, scheduler cadence, audit-outbox leasing/delivery, subscription usage epochs, provider-neutral TaskOperation accounting, and Jimeng atomic lifecycle against a new loopback-only Homebrew server and disposable data directory. |
| PostgreSQL 17 | PASS | The current nine-test manifest passed migration/restart, relational retention, relay accounting and channel-key concurrency, scheduler cadence, audit-outbox leasing/delivery, subscription usage epochs, provider-neutral TaskOperation accounting, and Jimeng atomic lifecycle. |
| ClickHouse 24.8 | PASS | Both current manifest tests passed DDL/migration, raw insertion, stable retry deduplication, content readback, and configurable retention/TTL lifecycle. |
| Redis 7 | NOT RUN LOCALLY | The exact lifecycle selector and fail-closed manifest wiring pass static/self-test verification; no local Redis service or cached image was available, and no registry pull was initiated for this check. |
| Docker Compose | PASS | Configuration validation passed with a non-deployment test session secret. |
| Dockerfile image | PASS | The real multi-stage image completed its frozen frontend install/build and CGO-disabled Go build, started with `GIN_MODE=release`, explicit empty external-store settings, and isolated SQLite, returned a successful `/api/status`, then removed its disposable container and image. |

Database PASS means those exact named boundaries ran; it is not evidence of a
live model-provider or payment-provider transaction.

The 2026-09-05 server run covered six MySQL tests, six PostgreSQL tests, and one
ClickHouse test. On 2026-09-06, all nine current tests passed independently on
MySQL and PostgreSQL, and both current ClickHouse tests passed. The Redis
selector, CI wiring, and manifest parser are compile/static-verified; only its
live local execution remains unavailable without pulling or provisioning
additional infrastructure.

## Remaining external verification

All local signature, state, replay, amount-binding, SSRF, response-bound, and
failure-path tests are retained. The remaining external blocks require
credentials and systems that were not available to this audit:

- EPay wallet checkout/webhook: a configured merchant, reachable callback,
  and external network.
- Stripe wallet checkout/webhook: a sandbox/live account, API and webhook
  secrets, price configuration, reachable callback, and network.
- Stripe subscription checkout/webhook: the Stripe requirements above plus a
  subscription product/price configuration and callback delivery.
- Built-in/custom OAuth ceremonies: registered GitHub, Discord, OIDC,
  LinuxDO, or operator-SSO clients, exact redirect URIs, secrets, and network.
- WeChat login: a live application, secret, callback configuration, and
  network.
- Cloudflare Turnstile: a valid site/secret pair and a live `siteverify`
  exchange.

These remain `BLOCKED_EXTERNAL`; deterministic mocks are not presented as live
third-party proof.

## Run locally

```bash
scripts/tokenrouter-acceptance.sh
```

To repeat a disposable server-database gate, set the corresponding test DSN
and use the exact manifest runner, for example:

```bash
TOKENROUTER_TEST_SQL_DSN=<isolated-mysql-or-postgresql-dsn> \
TOKENROUTER_TEST_ALLOW_SCHEMA_RESET=1 \
TOKENROUTER_CHANNEL_TYPE_CATALOG=reference-v1 \
bash scripts/run-go-test-manifest.sh \
  ./model:TestDatabaseMatrixExternalMigration \
  ./model:TestRelationalLogRetentionExternalDatabaseLifecycle \
  ./service:TestRelayAccountingExternalDatabaseConcurrency \
  ./service:TestChannelMultiKeyExternalDatabaseConcurrency \
  ./service:TestSystemTaskExternalDatabaseLeaseAndCadence \
  ./service:TestAuditOutboxExternalDatabaseLeaseAndDelivery \
  ./service:TestSubscriptionUsageEpochExternalDatabaseLifecycle \
  ./service:TestTaskOperationAccountingExternalDatabaseLifecycle \
  ./relay:TestJimengExternalDatabaseAtomicLifecycle
```

The current ClickHouse manifest contains
`TestClickHouseExternalLogLifecycle` and
`TestClickHouseLogRetentionExternalDatabaseLifecycle`; the Redis manifest
contains `TestRedisExternalSharedStoreLifecycle`.
