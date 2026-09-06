# Repository instructions

TokenRouter is an independently implemented, MIT-licensed AI gateway with an
embedded React dashboard. Never copy reference implementation code. Compatibility
choices (Chinese relay errors, structured 501 placeholders, the 500000 quota unit)
are intentional; consult `docs/parity/KNOWN_DEVIATIONS.md` before changing behavior.

## Navigation and ownership

- `cmd/tokenrouter`: executable entry point; `internal/app`: initialization,
  dependency assembly, HTTP server and embedded SPA lifecycle.
- `internal/httpapi`: router, middleware, request context, DTOs and domain
  handlers. Handler unit tests are adjacent; assembled cross-domain HTTP tests
  live with the router.
- `internal/auth`, `users`, `channels`, `catalog`, `billing`, `operations`:
  business logic with explicit downward dependencies.
- `internal/relay/{contract,providers,engine,tasks,policy,customconfig}`: shared
  contracts, explicit provider protocols, dispatch, durable task lifecycles,
  request policy and advanced-provider configuration.
- `internal/store`: entities, connections, migration registry and schema
  migrations; `internal/settings`: validated database-backed runtime snapshots.
- `internal/platform`: focused infrastructure subpackages, not a catch-all.
  `internal/testutil` contains reusable test fixtures only.
- `protocolkit`: independent Go module; never import the root module from it.
- `web/src/app`: bootstrap, routes, layout, session coordination;
  `features`: business pages, behavior, APIs and adjacent tests;
  `shared`: general transport/utilities/UI; `i18n` and global `styles.css`.

See `docs/architecture/README.md` and `docs/development/reorganization.md`.

## Commands

Use Go 1.26 and the frozen npm lockfile. Build frontend assets first:

```sh
(cd web && npm ci --no-audit --no-fund && npm run lint && npm test && npm run typecheck && npm run build)
go vet ./...
go build ./...
go test ./...
go test -race -timeout 20m ./internal/...
node scripts/verify-repository-layout.mjs
go run ./cmd/tokenrouter
(cd protocolkit && GOWORK=off go vet ./... && GOWORK=off go build ./... && GOWORK=off go test ./...)
bash scripts/tokenrouter-acceptance.sh
```

`web/embed.go` embeds ignored `web/dist`; preserve this relationship and never
commit generated assets. Local startup defaults to port 3000 and SQLite. `.env`
does not override environment variables. Root account creation uses the setup
wizard (`POST /api/setup`); there is no default password.

`node scripts/check-translations.mjs` checks all locales. Current route evidence
lives in `docs/development/API_MATRIX.md`: regenerate with
`bash scripts/update-api-matrix.sh`, verify with
`bash scripts/verify-api-matrix.sh`. Update exact external-store CI selectors
when tests move; the manifest runner rejects missing/skipped tests.
The repository layout guard also requires all top-level Go tests to match
`scripts/manifests/go-tests.json`, separately for root and protocolkit modules.
After reviewing an intentional addition or move, refresh that inventory with
`node scripts/verify-repository-layout.mjs --update-manifest`.

## Change discipline

Inspect the working tree and coordinate around other edits. A Go subdirectory
is a new package: inspect private symbols and dependency direction first. Do not
force boundaries with unnecessary exports, split transactions, introduce generic
catch-all packages, or merge similar-looking provider state machines.

Preserve routes, payloads/statuses, authentication, provider IDs, configuration
names, database names/values, transaction boundaries, idempotency, cancellation
and durable recovery. Run race checks for auth, accounting and recovery changes.
The ordered `AllModels` registry drives migrations; preserve GORM hooks,
database-clock fences, and separate primary/log-store behavior.

No production data or paid provider calls in local validation. Outbound calls
use the SSRF-safe transport; `SSRF_DISABLE=true` is only for controlled local
test upstreams. Runtime guidance is in `docs/operations`.

Historical reports and fingerprints in `docs/parity` describe recorded snapshots.
Do not rewrite them for source moves or imply they validate reorganized code.
Active development and validation documents live in `docs/development`.
