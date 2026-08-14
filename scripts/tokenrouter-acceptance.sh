#!/usr/bin/env bash
# tokenrouter-acceptance.sh — autonomous acceptance gate for TokenRouter.
# Exits non-zero on any failure. Run from the repository root.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARTIFACTS="$REPO_ROOT/.artifacts/tokenrouter-acceptance"
cd "$REPO_ROOT"

mkdir -p "$ARTIFACTS"
log() { echo -e "\n==> $*"; }
pass() { echo "   [PASS] $*"; }

failures=0
check() {
  local desc="$1"; shift
  if "$@"; then
    pass "$desc"
  else
    echo "   [FAIL] $desc"
    failures=$((failures+1))
  fi
}

# ---------------------------------------------------------------- Backend
log "Backend: go vet"
check "go vet" go vet ./...

log "Backend: go build"
check "go build" go build ./...

log "Backend: go test (all packages)"
check "go test" go test ./... 2>&1 | tee "$ARTIFACTS/go-test.log"

log "Backend: targeted race tests (quota math, routing)"
check "race: common+service" go test -race ./common/... ./service/... 2>&1 | tee "$ARTIFACTS/go-race.log"

# ------------------------------------------------------ Independent module
log "Protocol conversion module (GOWORK=off)"
(
  cd protocolkit
  check "protocolkit vet" env GOWORK=off go vet ./...
  check "protocolkit build" env GOWORK=off go build ./...
  check "protocolkit test" env GOWORK=off go test ./...
)

# ---------------------------------------------------------------- Frontend
if [ -d web ] && [ -f web/package.json ]; then
  log "Frontend"
  PM=""
  if command -v bun >/dev/null 2>&1; then
    PM="bun"; INSTALL="bun install --frozen-lockfile || bun install"
    TC="bun run typecheck"; BD="bun run build"
  elif command -v npm >/dev/null 2>&1; then
    PM="npm"; INSTALL="npm install --no-audit --no-fund"; TC="npm run typecheck"; BD="npm run build"
  fi
  if [ -n "$PM" ]; then
    (cd web && eval "$INSTALL" >/dev/null 2>&1)
    check "frontend typecheck" bash -c "cd web && $TC 2>&1 | tee $ARTIFACTS/typecheck.log"
    check "frontend build" bash -c "cd web && $BD 2>&1 | tee $ARTIFACTS/build.log"
    check "translation completeness" bash -c "node scripts/check-translations.mjs"
  else
    echo "   [SKIP] no JS package manager (bun/npm) available"
  fi
fi

# ---------------------------------------------------------------- Databases
log "Databases: SQLite empty migration + status"
SQLITE_PATH="$ARTIFACTS/test.db" PORT=38999 "$REPO_ROOT"/tokenrouter >"$ARTIFACTS/smoke.log" 2>&1 &
SRV=$!
sleep 3
if curl -sf http://localhost:38999/api/status >"$ARTIFACTS/status.json" 2>/dev/null; then
  pass "SQLite migration + /api/status"
else
  echo "   [FAIL] SQLite migration + /api/status"
  failures=$((failures+1))
fi
kill "$SRV" 2>/dev/null || true

# ---------------------------------------------------------------- Secrets
log "Secret leakage scan"
SECRET_HITS=""
scan_patterns() {
  local pat="$1"; local desc="$2"
  if grep -rInE "$pat" . \
      --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=dist \
      --exclude-dir=.artifacts --exclude=.env.example \
      2>/dev/null | grep -vE '(sk-mock|sk-test|change-me|your-|xxx|EXAMPLE|example|placeholder)' ; then
    echo "   [FAIL] possible $desc found"
    failures=$((failures+1))
  else
    pass "no $desc found"
  fi
}
scan_patterns 'sk-[A-Za-z0-9]{24,}' 'API key'
scan_patterns 'AKIA[0-9A-Z]{16}' 'AWS access key'
scan_patterns 'ghp_[A-Za-z0-9]{36}' 'GitHub token'
scan_patterns '-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----' 'private key'

# ---------------------------------------------------------------- Deployment
if command -v docker >/dev/null 2>&1; then
  log "Deployment: docker compose config"
  check "docker compose config" docker compose config >/dev/null
fi

# ---------------------------------------------------------------- Summary
echo ""
echo "======================================================"
if [ "$failures" -eq 0 ]; then
  echo "ACCEPTANCE: ALL CHECKS PASSED"
else
  echo "ACCEPTANCE: $failures CHECK(S) FAILED"
fi
echo "======================================================"
exit "$failures"
