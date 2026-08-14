# Acceptance Summary

The automated acceptance gate is `scripts/tokenrouter-acceptance.sh`. It exits
non-zero on any failure and covers:

- Backend: `go vet`, `go build`, `go test ./...`, targeted `go test -race`.
- Protocol module: independent `GOWORK=off` vet/build/test.
- Frontend: `bun install`, `typecheck`, `build` (when `bun` is available).
- Databases: SQLite empty-migration smoke test.

## Latest run (2026-08-14)

| Check | Result |
|---|---|
| `go vet ./...` | PASS |
| `go build ./...` | PASS |
| `go test ./...` | PASS |
| `go test -race ./common/... ./service/...` | PASS |
| protocolkit `GOWORK=off go vet/build/test` | PASS |
| SQLite empty migration | PASS (verified manually) |
| Frontend build | PENDING (frontend foundation in progress) |
| MySQL/PostgreSQL live migration | PENDING (requires running services) |
| Docker build / compose | PENDING |

## How to run

```bash
scripts/tokenrouter-acceptance.sh
```
