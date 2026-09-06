# Repository reorganization

This is the active migration record for the source reorganization started on
2026-09-06 from `180b0408bbd3b7218aa37e2add33457dde6b173e`. The working tree
was clean. The reorganization phase did not commit, push or deploy; publication
requires a separate request. No reference-repository edits, live production
data, or paid provider calls were part of the reorganization or validation.

## Bounded phases

1. Inventory packages, private-symbol dependencies, routes, and test selectors;
   establish ownership and this migration record.
2. Move frontend pages and adjacent tests into features, orchestration into
   `app`, and reusable infrastructure into `shared`; remove migrated shims.
3. Split large frontend modules at existing state, validation, transport, and
   presentation boundaries. Validate lint, tests, types, build, and translations.
4. Extract backend business packages from leaf domains toward authentication
   and billing. Keep transactions and durable accounting units together.
5. Separate relay contracts, explicit provider implementations, engine, and
   task-family lifecycles. Do not combine provider state machines.
6. Reorganize infrastructure and persistence; place process startup in
   `cmd/tokenrouter` with composition and lifecycle in `internal/app`.
7. Update active commands, CI selectors, exact manifests, and documentation.
   Run the complete acceptance gate and a source-only clean-checkout build.

Each phase uses mechanical moves first, then narrowly scoped decomposition.
HTTP contracts, provider identifiers, environment variables, schema and stored
values remain unchanged. Unit tests move with implementations. Historical
`docs/parity` reports and fingerprints remain historical evidence, not evidence
that the reorganized source has passed validation.

## Ownership and dependency direction

| Area | Responsibility | Permitted direction |
| --- | --- | --- |
| `cmd/tokenrouter`, `internal/app` | Process entry, initialization order, lifecycle, assembly | Composition may depend on all required runtime domains |
| `internal/httpapi` | Route registration, middleware, handlers by domain | Handlers depend on business packages; business packages do not import handlers |
| `internal/auth`, `users`, `channels`, `catalog`, `billing`, `operations` | Cohesive business behavior | Explicit lower-level dependencies; no reverse import of app/router |
| `internal/relay/contract` | Adapter contracts and shared wire/request state | No engine or task lifecycle dependency |
| `internal/relay/providers` | Explicit provider protocols | Contracts, protocolkit, and required lower-level infrastructure |
| `internal/relay/engine`, `tasks` | Request orchestration and durable task-family lifecycles | Providers, contracts, business services, persistence |
| `internal/settings`, `store`, `platform` | Runtime settings, persistence, domain-independent infrastructure | No app or HTTP handler dependency |
| `protocolkit` | Independent protocol module | Must never import the root module |
| `web/src/app` | Bootstrap, routes, session coordination, layouts | Features and shared infrastructure |
| `web/src/features` | Domain pages, behavior, API functions, adjacent tests | Shared infrastructure; cross-feature dependencies must be explicit |
| `web/src/shared` | General UI, HTTP transport, reusable utilities | Must not depend on app composition |

The initial Go graph is `app -> router -> controller -> service -> model ->
common/constant`, with middleware also consuming services. Settings consume
models; services consume settings. Relay orchestration consumes services,
middleware and adapters; adapters consume relay common contracts and selected
settings/model infrastructure. This means persistence must remain below business
packages, and shared relay contracts must not import orchestration. File-level
private-symbol graphs determine the final boundaries rather than directory names
alone. Shared transactional entities may remain together where splitting would
introduce cycles or obscure atomicity.

## Concurrent work

Frontend work owns `web/**`; HTTP extraction owns handler/router files; the
primary agent owns backend business extraction, shared infrastructure, startup,
scripts, CI, and active documentation. Global import rewrites are coordinated
with the owners. All edits share one working tree; no resets or source restores
are used.

## Implemented phases

| Phase | Result |
| --- | --- |
| 1. Inventory and ownership | Read repository instructions, confirmed a clean baseline, mapped private-symbol and package dependencies, and assigned concurrent ownership before global moves. |
| 2. Frontend organization | Removed `views` and `lib`; pages, API functions, validation and adjacent tests now belong to features. Application bootstrap/routing/layout/session coordination moved to `app`; general UI/transport/utilities moved to `shared`. |
| 3. Frontend decomposition | Extracted dashboard panels/charts, model editors/deployment sections, profile/session panels, wallet presentation/checkout controls, login validation, channel editor state, and application bootstrap/session coordination. |
| 4. Business packages | Extracted users, channels, catalog, operations, auth, and billing from service. Credential-affecting mutations remain in auth; quota mutations remain in billing. Shared row-lock helpers live below business packages. |
| 5. Relay | Separated contract, 37 explicit provider packages, engine, policy/custom configuration, and durable tasks. Engine and tasks do not import each other. Provider-specific recovery and review state machines remain explicit. |
| 6. Infrastructure and startup | Removed legacy infrastructure/model/settings roots. Focused platform packages replace common/constant; stable provider metadata and quota arithmetic belong to their domains. Startup is `cmd/tokenrouter` → `internal/app`. Store connections, dialect helpers, entity registry, and schema families have separate files. |
| 7. Validation integration | Updated Docker/CI/build commands, exact CI selectors, source enumeration, secret-scan fixture paths, active route evidence, documentation and the complete test inventory guard. |

Large backend files were split without changing their packages: session access,
creation, refresh, revocation, password login/change and 2FA login; OAuth
configuration, validation, transport, profile decoding and identity transactions;
pricing registry/calculation; durable reservation creation, settlement, refunds,
recovery and records; accounting review queries, retry, events, projections and
explicit async/video shapes; and database migration schema families.

### Deliberate boundaries

- Store remains one entity/migration package because its ordered migration
  registry, GORM hooks, connection handles and cross-entity invariants are
  coupled. File decomposition improves navigation without creating cyclic
  entity packages or changing tables, columns or migration ordering.
- Durable task families remain together where private journal, lease, fenced
  recovery and operator-review helpers form a connected lifecycle. They are
  separate from synchronous dispatch and provider protocol adapters; similar
  provider state machines were not merged.
- Shared accounting transactions remain in billing. Cross-domain callers use
  existing operation boundaries, not piecemeal debit/credit APIs.
- Cohesive provider protocols, subscription settlement and complex UI state
  machines may still be long. This reorganization does not redesign their
  behavior merely to meet a line-count target.
- `protocolkit`, dependency manifests/locks, `web/embed.go`, and historical
  `docs/parity` files are unchanged. Ignored `web/dist` is rebuilt, not tracked.

## Validation ledger

- Initial inventory: clean working tree; repository `CLAUDE.md` read; no
  applicable `AGENTS.md`; root package dependency graph resolved using the
  existing dependency cache.
- Phase tests: frontend lint/typecheck/build and all 98 test files / 751 tests
  passed; 7 locales × 2,644 keys and all 2,536 statically resolved runtime keys
  passed the regenerated translation report check. Domain, HTTP, store, relay
  and focused concurrency suites passed after their respective package moves.
- Preservation audit: all original 745 service, 429 controller/router, and 735
  relay tests were located after migration. Two user-notification transport
  tests were added. The complete current inventory records 2,287 root tests and
  23 standalone protocolkit tests. Twelve unique external-store CI selectors
  (21 workflow occurrences) resolve exactly once.
- Declaration audit: 13,158 original Go declarations became 13,163 declarations
  after extraction/test-fixture changes. No unexplained declaration loss,
  production body/constant drift, build-tag changes, schema tags, DDL or
  `TableName` changes were found. Only three duplicate role constants were
  intentionally consolidated. All 14,805 frontend production text-literal
  occurrences match the baseline exactly.
- Current route evidence: 390 target routes, 352/352 exact reference HTTP-route
  matches, with 342 evidenced rows and 12 preserved reference-placeholder rows.
  This evidence is in `docs/development/API_MATRIX.md`, not the historical matrix.
- Final acceptance, isolated external-store checks and clean-source build
  results are recorded separately below. Previous parity
  acceptance does not cover these moves.

## New validation scope (2026-09-06)

Validation was performed on an uncommitted source snapshot based on the baseline
named above, not on the assumption that the baseline's acceptance evidence
transfers to the new layout. Subsequent publication is a separate step.
The reorganization validation snapshot and its clean export had the same SHA-256:

`84fcb8c56500efd88a2d7d248ad96ebf0a051fcfd08c66bc6bf9c2778ab2665b`

The fingerprint covers the 1,220 Git-scoped files in that snapshot from
`scripts/list-source-files.mjs`, excluding `docs/` and root Markdown documents
so validation narratives can be completed after a run. Files are sorted by path;
each hash input is UTF-8 path, NUL, decimal byte length, NUL, then file bytes.
Ignored build outputs, dependency directories, local configuration, binaries and
run artifacts are not inputs. The clean-export-only verification binary is
also excluded. Historical fingerprints remain untouched.

| Check | Reorganization validation result |
| --- | --- |
| Source-only clean build | PASS: copied only current non-ignored source into a new temporary directory; verified `.env`, `web/node_modules`, and `web/dist` were absent; installed locked npm dependencies; rebuilt assets; built all root packages and `cmd/tokenrouter` with the new version symbol; independently vetted, built and tested protocolkit. |
| Exact test inventory | PASS: 2,287 runnable root tests + 23 protocolkit tests; `TestMain` is a lifecycle hook, not a test; all 12 unique CI selectors resolve exactly once; stale/missing/duplicate/legacy-path verifier self-tests pass. |
| Compiled test discovery | PASS: `go test -json -list '^Test' ./...` matches every manifest entry and per-package count: 2,287 tests across 73 root packages plus 23 in standalone protocolkit, with no missing, extra or duplicate tests. |
| External MySQL 8.4 | PASS: all 9 exact CI tests ran and passed, no skips. |
| External PostgreSQL 17 | PASS: all 9 exact CI tests ran and passed, no skips. |
| External ClickHouse 24.8 | PASS: both exact CI tests ran and passed, no skips. |
| External Redis 7 | PASS: the exact shared-store lifecycle test ran and passed, no skips. |
| Complete acceptance gate | PASS: frozen frontend install, lint, all 751 tests, typecheck, production build, translations, formatting, Go vet/build/all tests, race checks over every internal package, standalone protocolkit vet/build/tests, exact route matrix, isolated SQLite migration/status, secret scan, Compose validation, and the real multi-stage Docker build plus production-mode status probe. |

External-store tests used fresh disposable containers, random loopback-only
ports, and temporary in-memory database storage. An initial disk-backed launch
stopped before tests because Docker had no available space. All audit-created
containers and anonymous volumes were removed; only the MySQL/Redis image tags
downloaded for this run were removed afterward. Existing images, containers,
volumes and build cache were not deleted.
The subsequent application image build and isolated startup succeeded despite
the remaining Docker disk pressure. Its disposable image/container and the
SQLite smoke workspace were also cleaned up.

The final manifest refinement excludes `TestMain` and shares fail-closed source
enumeration with formatting/secret scanning. It was rechecked independently
with its self-tests, the clean source export, and compiled discovery; the initial
2,288 declaration count included that one lifecycle hook. No runnable test was
removed. The implementation source was the same snapshot covered by the full
acceptance and concurrency run.

Local generated logs are under `.artifacts/reorganization/` (acceptance, clean
build, preservation audit, external-store event logs and cleanup records) and
`.artifacts/tokenrouter-acceptance/`. They are intentionally ignored. These
checks cover the reorganized implementation, deterministic mock-provider
behavior, embedded build and disposable stores; they do not claim live-provider,
production-data or credential-gated external parity.

## Publication and CI follow-up

The reorganization was published as `6626fdc` after a separate user request.
Its [first GitHub CI run](https://github.com/MacroXie04/TokenRouter/actions/runs/34009061448)
exposed a cold-cache test-runner issue: all nine MySQL tests passed, but three
dependency-download diagnostics were merged into the JSON event log and rejected
by its strict verifier. The runner now leaves Go diagnostics on stderr and
verifies only stdout events, while retaining command/pipeline exit checks and
strict missing, skipped, failed, duplicate and malformed-event rejection.
Runner regression coverage is included in both CI and local acceptance. This
follow-up changes validation scripts, not application behavior; the snapshot
fingerprint above remains evidence for the original reorganization validation.
