#!/usr/bin/env bash
# tokenrouter-acceptance.sh — autonomous acceptance gate for TokenRouter.
# Exits non-zero on any failure. Run from the repository root.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARTIFACTS="$REPO_ROOT/.artifacts/tokenrouter-acceptance"
# Test-only value: strong enough to exercise production-mode startup validation,
# never used for a deployed instance or persisted outside the temporary smoke DB.
ACCEPTANCE_SESSION_SECRET="71d94f2ac8e603b57c1ae0469fdb3285e47ac9106bd25f83"
cd "$REPO_ROOT"

log() { echo -e "\n==> $*"; }
pass() { echo "   [PASS] $*"; }

failures=0
check() {
  local desc="$1"
  local status
  shift
  if "$@"; then
    pass "$desc"
  else
    status=$?
    echo "   [FAIL] $desc (exit $status)"
    failures=$((failures+1))
  fi
  # A failed check is recorded and the remaining independent checks still run.
  return 0
}

check_logged() {
  local desc="$1"
  local output_file="$2"
  local status
  shift 2
  # This function, rather than check itself, owns the pipeline. pipefail makes
  # the command's failure visible even when tee successfully writes the log.
  if "$@" 2>&1 | tee "$output_file"; then
    pass "$desc"
  else
    status=$?
    echo "   [FAIL] $desc (exit $status)"
    failures=$((failures+1))
  fi
  return 0
}

run_in_dir() {
  local directory="$1"
  shift
  (cd "$directory" && "$@")
}

report_and_exit() {
  echo ""
  echo "======================================================"
  if [ "$failures" -eq 0 ]; then
    echo "ACCEPTANCE: ALL CHECKS PASSED"
    echo "======================================================"
    exit 0
  fi
  echo "ACCEPTANCE: $failures CHECK(S) FAILED"
  echo "======================================================"
  # Always use a stable non-zero status, including if more than 255 checks fail.
  exit 1
}

SMOKE_PID=""
SMOKE_TMP_DIR=""
SMOKE_TMP_CREATED=false
SMOKE_BINARY=""
SMOKE_DB=""

best_effort_smoke_cleanup() {
  local original_status=$?
  trap - EXIT
  if [ -n "$SMOKE_PID" ]; then
    kill "$SMOKE_PID" 2>/dev/null || true
    wait "$SMOKE_PID" 2>/dev/null || true
  fi
  if [ "$SMOKE_TMP_CREATED" = true ] && [ -n "$SMOKE_TMP_DIR" ] && [ -d "$SMOKE_TMP_DIR" ]; then
    rm -rf -- "$SMOKE_TMP_DIR" 2>/dev/null || true
  fi
  exit "$original_status"
}
trap best_effort_smoke_cleanup EXIT

# Fast regression hook used by test-tokenrouter-acceptance.sh. It exercises the
# same aggregation paths as install, logged frontend, protocol, and cleanup
# failures without running the full acceptance suite.
if [ "${1:-}" = "--self-test-failures" ]; then
  log "Acceptance failure aggregation self-test"
  check "injected install failure" sh -c 'exit 11'
  check_logged "injected piped frontend failure" /dev/null sh -c 'exit 12'
  check "injected protocol-module failure" run_in_dir "$REPO_ROOT/protocolkit" sh -c 'exit 13'
  check "injected smoke-cleanup failure" sh -c 'exit 14'
  report_and_exit
fi

check "acceptance artifact directory" mkdir -p "$ARTIFACTS"

verify_go_format() {
  local unformatted
  local status=0
  unformatted="$(node scripts/list-source-files.mjs . --go | xargs -0 gofmt -l)" || status=$?
  if [ "$status" -ne 0 ]; then
    return "$status"
  fi
  if [ -n "$unformatted" ]; then
    echo "The following Go files need gofmt:"
    echo "$unformatted"
    return 1
  fi
}

log "Release metadata and scanner regressions"
check "Go source formatting" verify_go_format
check "repository layout verifier self-test" node scripts/verify-repository-layout.mjs --self-test
check "repository layout and exact test inventory" node scripts/verify-repository-layout.mjs
check "Go test manifest verifier self-test" node scripts/verify-go-test-manifest.mjs --self-test
check "Go test manifest runner regression" node scripts/test-go-test-manifest-runner.mjs
check "repository secret scanner self-test" bash scripts/scan-repository-secrets.sh --self-test

# ---------------------------------------------------------------- Frontend
# web/dist is embedded by the Go package and is not tracked, so a clean checkout
# must produce it before any Go vet/build/test command compiles web/embed.go.
log "Frontend"
if [ -d web ] && [ -f web/package.json ] && [ -f web/package-lock.json ]; then
  if command -v npm >/dev/null 2>&1; then
    check_logged "frontend install (npm ci)" "$ARTIFACTS/install.log" \
      run_in_dir "$REPO_ROOT/web" npm ci --no-audit --no-fund
    # The verifier imports the frontend's pinned TypeScript parser. Keep this
    # after the frozen install so a genuinely clean checkout has that runtime.
    check "translation report verifier self-test" node scripts/check-translations.mjs --self-test
    check_logged "frontend lint" "$ARTIFACTS/lint.log" \
      run_in_dir "$REPO_ROOT/web" npm run lint
    check_logged "frontend test" "$ARTIFACTS/frontend-test.log" \
      run_in_dir "$REPO_ROOT/web" npm run test
    check_logged "frontend typecheck" "$ARTIFACTS/typecheck.log" \
      run_in_dir "$REPO_ROOT/web" npm run typecheck
    check_logged "frontend build" "$ARTIFACTS/build.log" \
      run_in_dir "$REPO_ROOT/web" npm run build
  else
    check "npm is available" sh -c 'exit 127'
  fi
else
  check "frontend package and npm lock are present" sh -c 'exit 2'
fi
check "translation completeness" node scripts/check-translations.mjs

# ---------------------------------------------------------------- Backend
log "Backend: go vet"
check "go vet" go vet ./...

log "Backend: go build"
check "go build" go build ./...

log "Backend: go test (all packages)"
check_logged "go test" "$ARTIFACTS/go-test.log" go test ./...

log "Backend: race tests (all internal packages)"
# Match CI's bounded allowance for the full router integration package under
# race instrumentation; the default 10m is insufficient on hosted runners.
check_logged "race: security+routing+accounting+async" "$ARTIFACTS/go-race.log" \
  go test -race -timeout 20m ./internal/...

# ------------------------------------------------------ Independent module
log "Protocol conversion module (GOWORK=off)"
check "protocolkit vet" run_in_dir "$REPO_ROOT/protocolkit" env GOWORK=off go vet ./...
check "protocolkit build" run_in_dir "$REPO_ROOT/protocolkit" env GOWORK=off go build ./...
check "protocolkit test" run_in_dir "$REPO_ROOT/protocolkit" env GOWORK=off go test ./...

log "API parity evidence"
check "exact API route matrix" bash scripts/verify-api-matrix.sh

# ---------------------------------------------------------------- Databases
create_smoke_workspace() {
  local temp_base="${TMPDIR:-/tmp}"
  temp_base="${temp_base%/}"
  if SMOKE_TMP_DIR=$(mktemp -d "$temp_base/tokenrouter-acceptance.XXXXXX"); then
    SMOKE_TMP_CREATED=true
    SMOKE_BINARY="$SMOKE_TMP_DIR/tokenrouter-smoke"
    SMOKE_DB="$SMOKE_TMP_DIR/test.db"
    return 0
  else
    return $?
  fi
}

start_smoke_server() {
  # Run outside the repository so godotenv cannot load a developer's ignored
  # .env, and explicitly shadow the external stores that would otherwise take
  # precedence over this disposable SQLite database.
  (
    cd "$SMOKE_TMP_DIR"
    SESSION_SECRET="$ACCEPTANCE_SESSION_SECRET" \
      SQL_DSN="" \
      LOG_SQL_DSN="" \
      REDIS_CONN_STRING="" \
      SQLITE_PATH="$SMOKE_DB" \
      JIMENG_RECOVERY_DIR="$SMOKE_TMP_DIR/jimeng-recovery" \
      PORT=38999 \
      "$SMOKE_BINARY"
  ) >"$ARTIFACTS/smoke.log" 2>&1 &
  SMOKE_PID=$!
}

probe_smoke_server() {
  curl -sf http://localhost:38999/api/status >"$ARTIFACTS/status.json" 2>/dev/null
}

stop_smoke_server() {
  local pid="$SMOKE_PID"
  local kill_status=0
  local wait_status=0
  if [ -z "$pid" ]; then
    return 0
  fi

  if kill -0 "$pid" 2>/dev/null; then
    if kill "$pid" 2>/dev/null; then
      kill_status=0
    else
      kill_status=$?
      # The process may have exited between kill -0 and kill.
      if ! kill -0 "$pid" 2>/dev/null; then
        kill_status=0
      fi
    fi
  fi

  if wait "$pid" 2>/dev/null; then
    wait_status=0
  else
    wait_status=$?
  fi
  SMOKE_PID=""

  if [ "$kill_status" -ne 0 ]; then
    return "$kill_status"
  fi
  case "$wait_status" in
    0|143) return 0 ;;
    *) return "$wait_status" ;;
  esac
}

remove_smoke_workspace() {
  local directory="$SMOKE_TMP_DIR"
  if [ "$SMOKE_TMP_CREATED" != true ]; then
    return 0
  fi
  if [ -z "$directory" ]; then
    return 1
  fi
  if [ -d "$directory" ] && ! rm -rf -- "$directory"; then
    return 1
  fi
  SMOKE_TMP_CREATED=false
  SMOKE_TMP_DIR=""
  SMOKE_BINARY=""
  SMOKE_DB=""
}

log "Databases: SQLite empty migration + status"
check "create temporary smoke workspace" create_smoke_workspace
if [ "$SMOKE_TMP_CREATED" = true ]; then
  check_logged "build temporary smoke binary" "$ARTIFACTS/smoke-build.log" \
    go build -o "$SMOKE_BINARY" ./cmd/tokenrouter
  if [ -x "$SMOKE_BINARY" ]; then
    check "start temporary smoke binary" start_smoke_server
    sleep 3
    check "SQLite migration + /api/status" probe_smoke_server
    check "stop temporary smoke binary" stop_smoke_server
  else
    echo "   [SKIP] SQLite migration smoke (temporary binary was not built)"
  fi
  check "remove temporary smoke workspace" remove_smoke_workspace
fi

# ---------------------------------------------------------------- Secrets
log "Secret leakage scan"
check "tracked and non-ignored source secret scan" bash scripts/scan-repository-secrets.sh

# ---------------------------------------------------------------- Deployment
if command -v docker >/dev/null 2>&1; then
  log "Deployment: docker compose config"
  check "docker compose config" env SESSION_SECRET="$ACCEPTANCE_SESSION_SECRET" \
    docker compose config --quiet
  if docker info >/dev/null 2>&1; then
    check_logged "Dockerfile build + isolated production-mode start" "$ARTIFACTS/docker-build.log" \
      env TOKENROUTER_DOCKER_TEST_STATUS_FILE="$ARTIFACTS/docker-status.json" \
      bash scripts/test-docker-image.sh
  else
    echo "   [SKIP] Dockerfile build/start probe (Docker daemon is unavailable)"
  fi
else
  log "Deployment"
  echo "   [SKIP] Docker checks (Docker CLI is unavailable)"
fi

# ---------------------------------------------------------------- Summary
report_and_exit
