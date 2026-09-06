# Boundary cleanup

This follow-up starts from `07c00114f32e8075d3d00d042475cf4734008fa4` and
addresses the structural audit of the reorganized repository. It retains the
existing command, domain, provider, persistence and independent protocol-module
layout. Validation was performed on the working-tree snapshot before publication.
The later user-authorized commit and push do not deploy the application.

## Ownership changes

- HTTP handlers own ordinary relay decoding/validation and capture the
  authenticated request state once. Engine/task lifecycles consume explicit
  identity, model policy and authorized groups. Provider streaming and native
  protocol transport contracts remain in place.
- Payment clients live in `internal/payments/{stripe,creem,waffo}`. Application
  startup installs Stripe reconciliation and the named asynchronous recovery
  callbacks explicitly. Missing Stripe assembly is reported rather than
  silently succeeding. Task promotion/reconciliation ordering is retained.
- Provider correlation fingerprints belong to `platform/cryptoutil`. SMTP
  transport owns its configuration value and delivery validation; settings owns
  option decoding and whole-domain environment precedence. Every delivery
  reads a fresh immutable configuration snapshot.
- Router registration functions have separate files. Domain request DTOs live
  with their handlers; pagination is a shared HTTP utility. Deployment handlers
  have separate client, query and presentation files. Router tests retain their
  full middleware/database coverage, with explicit database/session/HTTP fixtures.
- App selects domain pages directly after removal of the old AdminConsole.
  Profile, channel and wallet panels have focused modules. Models metadata and
  deployment contracts/API calls have separate ownership. Pure Turnstile logic
  is separate from the widget; shared public content belongs to public documents.
- Styles are colocated with their owners. The stylesheet entry imports shared
  primitives, feature styles and the application shell. Shared forms, status
  labels and pagination remain in the shared stylesheet.
- Backend production imports and frontend production feature boundaries are
  checked automatically, with negative and allowed-case verifier tests.

## Preservation evidence

The seven router assembly function bodies match the baseline exactly, including
route order, middleware order and group inheritance. The three payment clients
match their previous bodies after accounting for package/export names and the
removed Stripe initializer. Deployment function bodies and moved DTO fields/tags
were compared against the baseline. Schema, migration registry and protocolkit
sources were not reorganized.

The CSS migration preserves the selector/declaration/important/media-condition
inventory and declaration order for each selector. Domain selectors were moved
alongside their features; shared selectors were retained in the shared layer.

The active API matrix was regenerated from the assembled route dump: 390 target
routes, 352 exact reference matches, 342 evidenced rows and 12 preserved reference
placeholders. Historical parity documents retain their original evidence.

## Validation

| Check | Result |
| --- | --- |
| Backend formatting, vet and build | Passed |
| Root-module tests | Passed; compiled discovery matches all 2,297 manifest selectors |
| Race tests over all internal packages | Passed |
| Independent protocolkit vet/build/tests | Passed; 23 tests remain inventoried separately |
| Frontend frozen install, lint, types, tests and production build | Passed; 99 test files / 748 tests, including 13 additional lint boundary self-check cases |
| Translation report | Passed; 7 locales, 2,644 keys each, 2,490 current runtime keys covered |
| Layout/import guard and self-tests | Passed; all 12 external-store CI selectors still resolve uniquely |
| Exact API matrix | Passed; route/match/placeholder totals preserved |
| Secret scan | Passed |

The root inventory increased from 2,287 to 2,297 tests. Original tests were
preserved across package moves; the SMTP hot-reload transport test was renamed
to describe its injected delivery snapshot, while persisted-option hot reload
is verified in settings. New regressions cover missing Stripe assembly, missing
or altered relay authorization, captured identity, recovery callback replacement
and ordering, SMTP resolver errors and the Jimeng error envelope.

Frontend tests changed from 751 to 748: removal of the obsolete AdminConsole's
11 tests is balanced by two migrated Prefill tests, three channel composition
tests, two settings UI tests and one settings API test. The remaining duplicate
cases are covered by existing subscription, system-settings and settings-tools
tests. Original route assertions and profile lifecycle tests were retained.

The complete `scripts/tokenrouter-acceptance.sh` gate exited successfully with
`ACCEPTANCE: ALL CHECKS PASSED`. It rebuilt the embedded frontend from the frozen
lockfile, reran the backend and frontend checks, verified SQLite empty-database
migration and `/api/status`, and built the Docker image and passed its isolated
production-mode status probe. No deployment was performed.

The final root test manifest has ten additional tests and no unexplained removal;
compiled discovery matches all 2,297 selectors. External MySQL/PostgreSQL/
ClickHouse/Redis matrices and credential-gated live-provider tests were not rerun
for this cleanup. Those environment-dependent cases can skip in ordinary local
tests; the historical CI results are not evidence for this working-tree snapshot.
Current logs are in `.artifacts/structure-boundaries/acceptance.log` and the
acceptance artifact directory; the preceding acceptance logs were preserved in
`.artifacts/structure-boundaries/baseline-acceptance`.

The validated working-tree source inventory contains 1,324 Git-scoped files
excluding `docs/` and root Markdown narratives. Its SHA-256 is
`449f135358c7753aa153d756f17205a0a8d8e3a766c1c955db6cb0a3fcfe92dd`.
The fingerprint uses sorted path, NUL, decimal byte length, NUL, and file contents,
matching the earlier reorganization's source-only fingerprint method. Ignored
dependencies, build outputs, binaries and validation artifacts are excluded.

The pre-publication staged review removed one extra trailing newline from each
of four frontend files. This whitespace-only cleanup was checked with frontend
lint and the staged whitespace check; it does not change the validated behavior.
The publication source inventory still contains 1,324 files, with SHA-256
`ff8acda8f08787ef7861f61ffa88ead7139444b57e898c70f459f9469142938a`.
