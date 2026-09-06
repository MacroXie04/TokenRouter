# TokenRouter Parity Status

Last updated: 2026-09-06

## Current classification

- **IMPLEMENTED_BOUNDARY_RELEASE_GATE: PASS**
- **OPERATIVE_HTTP_ROUTE_PARITY: PASS**
- **DETERMINISTIC_PROVIDER_CONTRACT_PARITY: PASS**
- **SOFTWARE_PARITY: COMPLETE_WITH_INTENTIONAL_DEVIATIONS**
- **LIVE_EXTERNAL_PARITY: BLOCKED_MISSING_CREDENTIALS**
- **RELEASE_FROM_CURRENT_HEAD: BLOCKED_DIRTY_WORKTREE**

All 342 operative reference HTTP route rows and all 206 operative
provider-contract rows now have target implementations and deterministic
evidence. The remaining 12 API rows and five provider rows are genuine
placeholders in the pinned reference, not missing target behavior. All 352
unique reference HTTP routes match exactly.

All locally achievable operative software boundaries are implemented. Nine
database rows retain explicit, tested `INTENTIONAL_DEVIATION` classifications
for non-destructive compatibility, security, or durability invariants; they are
not unfinished implementation work. Credentialed third-party validation remains
external. The frozen 2026-09-06 snapshot passed the complete acceptance gate in
the assembled tree and again from a clean export; `PROVENANCE.md` records the
exact Git base and source fingerprint.

## Current evidence totals

| Matrix | Current strict totals |
|---|---|
| `API_MATRIX.md` | 342 PASS / 12 REFERENCE_PLACEHOLDER (354) |
| `PROVIDER_MATRIX.md` | 206 PASS / 5 REFERENCE_PLACEHOLDER (211) |
| `BILLING_MATRIX.md` | 98 PASS / 3 BLOCKED_EXTERNAL (101) |
| `DATABASE_MATRIX.md` | 26 PASS / 9 INTENTIONAL_DEVIATION (35) |
| `SECURITY_MATRIX.md` | 57 PASS / 4 BLOCKED_EXTERNAL (61) |
| `FRONTEND_MATRIX.md` | 103 PASS (103) |
| `PARITY_MATRIX.csv` | 147 PASS / 2 BLOCKED_EXTERNAL (149 milestone rows) |

`PARITY_MATRIX.csv` is an implementation-milestone ledger. The six strict
matrices govern completeness and distinguish implemented behavior, consciously
retained schema differences, pinned-reference placeholders, and live external
verification rather than folding those categories into one incomplete count.

## Current locally verified evidence

- Exact API inventory regeneration reports 354 matrix rows: 342 implemented
  PASS and 12 pinned-reference placeholders. All 352 unique reference HTTP
  routes have exact target matches; the target exposes 390 routes in total,
  including documented extensions.
- The provider matrix has no locally actionable implementation gap: 206
  operative contracts pass deterministic wire, conversion, streaming, usage,
  billing, identifier, or routing tests; five reference rows are placeholders.
- The frontend Vitest suite passes **98 files / 751 tests**; current typecheck
  and lint checks also pass.
- The translation self-test and live checker pass: all seven locales contain
  the same **2,644 keys**, cover all **2,536 runtime keys** found across 110
  source files (2,019 direct and 517 statically resolved), and have no missing,
  extra, invalid, untranslated, or interpolation-mismatched entries.
- Dependency integrity verification passes for every Go module, and the
  cache-bounded full-lockfile `npm audit --offline` reports zero known
  vulnerabilities. No local Go vulnerability scanner/database was available,
  so no fresh network-backed Go advisory result is claimed.
- Focused Go tests cover recent channel credential redaction and step-up key
  disclosure, fine-grained channel permissions, provider-specific balance
  refresh, distributed scheduled and cancellable balance sweeps, dashboard
  query limits, saved profile preferences, notifications, and the expanded
  administration workflows.
- The frozen snapshot's exact nine-test manifests pass independently against
  disposable MySQL 9.5 and PostgreSQL 17 servers, and the two-test ClickHouse
  24.8 manifest passes against its disposable server. The Redis lifecycle
  selector is compile/static-verified, but no local service, server binary, or
  cached image was available for a current run.
- The complete acceptance command exited 0 with `ACCEPTANCE: ALL CHECKS PASSED`
  both in the assembled working tree and from an independently initialized
  clean export of the exact frozen source set. The gate covered frontend,
  translations, format/vet/build/tests/races, standalone protocolkit, route
  inventory, isolated SQLite startup, secret scanning, Compose, and a real
  multi-stage Docker image/startup probe.

## Implemented product surface

The authenticated shell now applies role-aware global and personal sidebar
policy, persistent language selection, responsive navigation, logout handling,
and a shared safe brand. Dedicated, bounded frontend domains cover the public
home, pricing and model performance, dashboard analytics, channel
administration, model metadata and deployments, playground, profile/security,
subscriptions, usage logs, users, relay keys, redemption codes, rankings,
wallet, system information, and system settings. The public shell applies
status branding/navigation, combines Notice and announcement timelines, and
keeps operator content isolated. Epay wallet and subscription checkout now
preserves and explicitly submits the complete signed form rather than reducing
it to an unusable URL.

Channel administration includes bulk/tag/copy/balance operations, staged
upstream-model updates, multi-key/Codex/Ollama tooling, a complete typed editor,
and independently enforced read/operate/write/sensitive-write permissions.
Ordinary channel responses omit credentials and sensitive configuration; root
credential disclosure requires a short-lived 2FA or passkey proof and the
secret is automatically cleared in the UI.

Profile settings persist language, personal navigation, notification delivery,
quota-warning, upstream-update, unset-price, and IP-log privacy preferences.
Session management identifies the current session, renders server Unix
timestamps consistently, and exercises refresh-cookie recovery, retry, expiry,
rotation, and logout without exposing refresh credentials.
The user-management surface includes lifecycle timestamps, permission matrices,
identity bindings, and per-user subscription operations. Dashboard sections
provide role-scoped overview, model, flow, and user analytics with bounded
filters and accessible charts and tables.

System settings expose all supported mutable domains, including bounded tool
pricing, SMTP, update checks, and durable usage-log cleanup. SSRF policy remains
an immutable safe-transport boundary, and the absent delegated worker
URL/shared-key trust plane is explained rather than simulated by inert inputs.

## Security and accounting posture

The final cross-cutting audit found no unresolved high- or medium-severity issue
inside the implemented and tested security/accounting scope. In particular:

- checked cryptographic entropy fails before credential or identifier mutation;
- native provider dispatch fails closed instead of leaking an incompatible
  request to an arbitrary upstream;
- quota holds and settlement are atomic and idempotent across ordinary,
  realtime, subscription, Stripe, Jimeng, and OpenAI/Sora video flows;
- database-time leases and fencing prevent stale-worker completion;
- ambiguous financial/provider outcomes remain durable for reconciliation or
  audited root-only manual resolution;
- external URLs and payloads are bounded and subject to SSRF controls.

OpenAI/Sora video additionally preserves distinct public/provider IDs, binds
encrypted provider IDs and credential snapshots to the exact task/accounting
coordinates, recovers accepted submissions through the primary database and a
bounded encrypted node-local journal, and reverses settled terminal failures
exactly once.

This assessment is limited to the boundaries represented by tests and review.
It does not claim exact schema identity for the nine intentional deviations or
convert the credential-dependent `BLOCKED_EXTERNAL` rows into local PASSes.

## Remaining external and release-authority work

1. Validate the three billing and four security external rows with explicitly
   authorized payment, OAuth/identity, WeChat, and Turnstile environments;
   separately run optional SMTP and live-provider smoke checks if authorized.
2. Review and place the intentionally dirty working-tree snapshot under version
   control before release; this audit was not authorized to commit or publish it.

## Scope and safety

The reference checkout remained read-only. Validation used synthetic data and
disposable local infrastructure. No commit, push, persistent or external
deployment, external message, paid-provider request, or production-data
mutation was performed. The Docker validation used an isolated disposable
production-mode container and SQLite database.
