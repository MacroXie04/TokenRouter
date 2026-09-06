# Development

Read [`CLAUDE.md`](../../CLAUDE.md) and the [ownership map](reorganization.md)
before moving packages. Use the frontend's frozen npm lockfile and Go 1.26.

```sh
# Build web/dist first: it is embedded but not tracked.
(cd web && npm ci --no-audit --no-fund && npm run lint && npm test && npm run typecheck && npm run build)
go vet ./...
go build ./...
go test ./...
go test -race ./internal/...
node scripts/verify-repository-layout.mjs --self-test
node scripts/verify-repository-layout.mjs
(cd protocolkit && GOWORK=off go vet ./... && GOWORK=off go build ./... && GOWORK=off go test ./...)
node scripts/check-translations.mjs
bash scripts/verify-api-matrix.sh
bash scripts/tokenrouter-acceptance.sh
```

Focused example: `go test ./internal/billing -run '^TestSettleUserQuotaNoDoubleCharge$'`. Verify
the exact test exists before using a selector; Go otherwise succeeds with no
tests run. External-store CI uses `scripts/run-go-test-manifest.sh` to require
every selected test to execute and pass without skipping.

The layout guard checks the complete source test inventory, both module
boundaries, the executable/embedding chain, retired directories, and every
external-store CI selector. Update `scripts/manifests/go-tests.json` only after
reviewing an intentional test move/addition/removal:
`node scripts/verify-repository-layout.mjs --update-manifest`.

`scripts/list-source-files.mjs` enumerates the actual tracked/non-ignored
snapshot, including new paths and excluding Git-confirmed worktree deletions.
This allows validation of uncommitted moves without staging them. Unreadable
paths and tracked files replaced with directories still fail scans.

Keep frontend behavior, API functions, validation and tests in the owning
feature. Bootstrap/global session coordination belongs in `app`; general
transport and UI belong in `shared`. Features must not depend on app composition.
Keep backend tests beside implementations; group only real integration tests.
Preserve transactional units rather than splitting commits across packages.

The active [API matrix](API_MATRIX.md) records current source/test associations.
Historical `docs/parity` evidence is retained unchanged. Regenerating route
evidence does not run live-provider or credential-gated integration tests.
