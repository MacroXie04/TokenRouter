# TokenRouter — Project Scan and Parity Report

Generated: 2026-09-06

## Outcome

The current TokenRouter working tree implements every operative HTTP route and
provider-contract row in the pinned reference inventory: 342 operative route
rows and 206 operative provider contracts pass their strict deterministic
evidence rules. The other 12 API and five provider rows are genuine reference
placeholders. All 352 unique reference HTTP routes match the target exactly.
Recent work also replaced the former compact-console gaps with dedicated home,
pricing/performance, dashboard, model/deployment, playground, profile,
subscription, usage-log, user, and channel workflows.

All locally achievable operative software boundaries are complete. Nine exact
database-schema rows are retained as tested `INTENTIONAL_DEVIATION` choices for
non-destructive compatibility, security, or durability; no frontend or local
implementation row remains open. This does **not** establish live external
parity: no credentialed provider, OAuth, payment, WeChat, Turnstile, or
SMTP-delivery exchange was made. The frozen 2026-09-06 snapshot passed the
complete acceptance gate in the assembled working tree and again from a clean
export; its exact content fingerprint is recorded in `PROVENANCE.md`.

The current worktree is also not reproducible from revision
`a88784d16269d86f1c37298b5a185e6cfe7fccd0` alone: it contains a large set of
modified and non-ignored untracked source files. Clean-export acceptance proves
that the recorded source set is self-contained, but those files must still be
deliberately reviewed and included in a future changeset before a release is
cut.

## Strict parity snapshot

| Inventory | PASS | IN_PROGRESS | NOT_STARTED | BLOCKED_EXTERNAL | INTENTIONAL_DEVIATION | REFERENCE_PLACEHOLDER | Total |
|---|---:|---:|---:|---:|---:|---:|---:|
| HTTP API routes | 342 | 0 | 0 | 0 | 0 | 12 | 354 |
| Provider/channel contracts | 206 | 0 | 0 | 0 | 0 | 5 | 211 |
| Database entities/migration | 26 | 0 | 0 | 0 | 9 | 0 | 35 |
| Billing boundaries | 98 | 0 | 0 | 3 | 0 | 0 | 101 |
| Security controls | 57 | 0 | 0 | 4 | 0 | 0 | 61 |
| Frontend workflows | 103 | 0 | 0 | 0 | 0 | 0 | 103 |

`PARITY_MATRIX.csv` is an implementation-milestone ledger rather than the
strict completeness score. It currently records 147 PASS and 2
BLOCKED_EXTERNAL rows.

## High-risk boundaries verified

- Security-sensitive identifiers and credentials use checked, unbiased secure
  entropy. Injected entropy failures return before credential issuance or
  persistence.
- Native Gemini and Claude entry points dispatch only to a semantically valid
  provider contract. Unsupported native combinations fail before network I/O;
  Azure, custom, and explicitly compatible channels retain their own URL,
  authentication, model-mapping, and conversion rules.
- Ordinary, realtime, subscription, Stripe, Jimeng, and OpenAI/Sora video accounting use durable
  reservations, exact settlement/refund rules, database-time leases, and stale
  worker fencing. Ambiguous outcomes are retained for recovery or audited manual
  review rather than guessed.
- Jimeng submission, polling, restart recovery, encrypted emergency evidence,
  ownership, terminal monotonicity, and root-only manual resolution are
  exercised on SQLite and the critical lifecycle is exercised on MySQL and
  PostgreSQL.
- OpenAI/Sora JSON and multipart create/remix, polling, content, owner/platform
  fencing, encrypted accepted-ID crash recovery, operator retry, and exact-once
  terminal reversal are exercised locally; the provider-neutral TaskOperation
  lifecycle also passed the recorded 2026-09-05 MySQL/PostgreSQL manifests.
- Legacy import is read-only at the source, remaps ownership identifiers, uses
  one target transaction, rejects conflicting keys, and rolls back on failure.
- Channel list/search responses expose no upstream credentials or sensitive
  configuration. Root-only credential disclosure requires a fresh scoped 2FA
  or passkey proof; the frontend keeps the result isolated, read-only, and
  automatically clears it after 45 seconds.
- Provider balance refresh implements the reference OpenAI, AIProxy, API2GPT,
  AIGC2D, SiliconFlow, DeepSeek, OpenRouter, and Moonshot contracts plus a
  bounded configured endpoint. Conditional persistence rejects stale channel
  state, and the serialized paged sweep observes cancellation, configured
  pacing, and per-channel auto-ban policy. When `CHANNEL_UPDATE_FREQUENCY` is
  configured, periodic refresh runs under the distributed job lease.
- Channel-test tasks honor `notify:true` by sending the completion notice to an
  enabled root through the configured email, webhook, Bark, or Gotify delivery
  path; `notify:false` and delivery-failure isolation are tested.
- Epay wallet and subscription responses preserve their complete bounded
  signature field set; the browser creates no automatic navigation and exposes
  only an explicit native POST action. Hosted checkout providers remain safe
  GET links, and manual recovery of an existing pending order is not coupled to
  whether new payments are currently enabled.
- Tool-price configuration is lexically and numerically bounded, published as
  immutable request snapshots, and settled with exact decimal arithmetic across
  Responses, Chat, Claude, Gemini, image generation, and alpha search. Accepted
  work remains durably recoverable when model, tool, or combined quota exceeds
  the accounting domain.
- Root log cleanup is an authorized, rate-limited durable system task with
  bounded target time, batch size, progress exposure, deduplication, and fenced
  lease ownership.
- Public announcements are atomically validated, newest-first, and optional in
  `/api/status`; the browser preserves multiline Markdown while rejecting
  unsafe controls and renders a persistent unread Notice/timeline experience.
- The standalone `protocolkit` module vets, tests, and builds with
  `GOWORK=off`.

## Frontend and localization evidence

The direct current-tree frontend suite passes **98 files / 751 tests**. The
public shell applies status branding and module policy, composes the live
catalog/performance/ranking resources, isolates operator content, and presents
safe Notice/announcement state. The authenticated shell enforces role-aware
global and personal sidebar visibility, responsive keyboard navigation,
persistent language preference, and session-safe logout. Dashboard tests cover
overview, model, flow, and administrator-only user analytics; profile tests
cover identity, verified email, access tokens, 2FA, passkeys, sessions,
bindings, account deletion, notifications/privacy, language, and sidebar
preferences. Current-session labels, server Unix timestamps, and the complete
refresh-cookie recovery/rotation/logout lifecycle are rendered and tested.
System settings include tool pricing, SMTP, update checks, and durable log
cleanup while explaining the immutable SSRF and absent worker-trust boundaries.
Channel tests cover the full editor, granular permissions,
bulk/tag/copy/balance/update tools, multi-key/Codex/Ollama workflows, and
step-up key disclosure.

The translation verifier self-test and live scan pass. All seven locales have
identical **2,644-key** sets and cover all **2,536 runtime keys** found in 110
source files: 2,019 direct literals and 517 statically resolved keys. There are
no missing, extra, invalid, untranslated, or interpolation-mismatched entries.
The pinned reference has 5,266 English keys, but reference-only strings for
dormant, unsupported, or intentionally immutable UI are not missing reachable
target behavior; all seven locale rows are PASS.

## Acceptance evidence

The complete `scripts/tokenrouter-acceptance.sh` gate exited 0 twice on the
frozen 2026-09-06 snapshot—first in the assembled working tree, then in a clean
export containing only tracked and non-ignored source paths. Both runs ended
with `ACCEPTANCE: ALL CHECKS PASSED` and covered:

- frontend frozen install, lint, 98 files / 751 tests,
  typecheck, production build, and translation verification;
- Go formatting, vet, build, the complete test suite, and the targeted race
  suite;
- standalone `protocolkit` vet/build/test;
- exact API regeneration: 342 PASS and 12 reference placeholders, with all 352
  unique reference HTTP routes matched exactly;
- isolated SQLite migration/startup and `/api/status`, fail-closed source secret
  scanning, Compose validation, and an isolated production Docker image probe.

`go mod verify` also passed, and a cache-bounded `npm audit --offline` reported
zero known vulnerabilities for the complete lockfile. No locally installed Go
vulnerability scanner/database was available, so this report does not relabel
that absence as a current network-backed Go advisory pass.

The available current database manifests also passed exactly once per required
test, with no skip or failure:

- MySQL 9.5: all nine current migration, relational-retention, accounting,
  channel-key, scheduler, audit-outbox, subscription, TaskOperation, and Jimeng
  lifecycle tests;
- PostgreSQL 17: all nine current migration, relational-retention, accounting,
  channel-key, scheduler, audit-outbox, subscription, TaskOperation, and Jimeng
  lifecycle tests;
- ClickHouse 24.8: both current migration/insertion/deduplication/readback and
  retention/TTL lifecycle tests.

The same current nine-test manifest passed independently against a newly
initialized loopback-only Homebrew MySQL 9.5 server and the disposable
PostgreSQL 17 server. The Redis selector and manifest wiring are
compile/static-verified, but a live run was not made because no local service,
server binary, or cached image was available. No image pull was initiated for
that optional local check.

See `ACCEPTANCE.md` for the precise interpretation of these results.

## Material external or release-authority work

1. Run credentialed sandbox validation for the three billing and four security
   external boundaries: payments, OAuth/identity, WeChat, and Turnstile.
   SMTP delivery and live-provider smoke tests remain optional additional proof
   under explicit authorization.
2. Review and commit the deliberately dirty source snapshot before release; no
   commit or publication was authorized during this audit.

## Final classification

- **IMPLEMENTED_BOUNDARY_RELEASE_GATE: PASS**
- **OPERATIVE_HTTP_ROUTE_PARITY: PASS**
- **DETERMINISTIC_PROVIDER_CONTRACT_PARITY: PASS**
- **SOFTWARE_PARITY: COMPLETE_WITH_INTENTIONAL_DEVIATIONS**
- **LIVE_EXTERNAL_PARITY: BLOCKED_MISSING_CREDENTIALS**
- **RELEASE_FROM_CURRENT_HEAD: BLOCKED_DIRTY_WORKTREE**

No commit, push, persistent or external deployment, paid-provider call, or
production-data operation was performed during this audit. The Docker proof
used an isolated disposable production-mode container and SQLite database.
