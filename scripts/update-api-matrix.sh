#!/usr/bin/env bash
# Rebuild API_MATRIX target verdicts from the exact Gin route/handler dump and
# behavioral-test evidence. Route registration by itself never produces PASS.
set -euo pipefail
cd "$(dirname "$0")/.."

route_dump="$(mktemp "${TMPDIR:-/tmp}/tokenrouter-routes.XXXXXX")"
trap 'rm -f "$route_dump"' EXIT

TOKENROUTER_ROUTE_DUMP="$route_dump" GOWORK=off go test ./internal/httpapi/router/ -run '^TestDumpRoutes$' -count=1 >/dev/null
TOKENROUTER_ROUTE_DUMP="$route_dump" node scripts/api-matrix-evidence.mjs
