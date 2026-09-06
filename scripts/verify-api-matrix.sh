#!/usr/bin/env bash
# Fail when the reference inventory, target route/handler dump, status, source
# evidence, or behavioral-test evidence is missing, duplicated, or stale.
set -euo pipefail
cd "$(dirname "$0")/.."

route_dump="$(mktemp "${TMPDIR:-/tmp}/tokenrouter-routes.XXXXXX")"
trap 'rm -f "$route_dump"' EXIT

TOKENROUTER_ROUTE_DUMP="$route_dump" GOWORK=off go test ./router/ -run '^TestDumpRoutes$' -count=1 >/dev/null
TOKENROUTER_ROUTE_DUMP="$route_dump" node scripts/api-matrix-evidence.mjs --check
