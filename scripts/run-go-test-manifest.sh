#!/usr/bin/env bash
# Run an exact set of top-level Go tests and prove from go test -json that each
# expected test ran once, passed once, and was never skipped.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERIFIER="$REPO_ROOT/scripts/verify-go-test-manifest.mjs"
cd "$REPO_ROOT"

if [ "$#" -eq 0 ]; then
  echo "usage: $0 <package:test-name> [...]" >&2
  exit 2
fi
if ! command -v node >/dev/null 2>&1; then
  echo "node is required to verify the Go test event manifest" >&2
  exit 2
fi

packages=()
tests=()
expected=()
package_count=0
for spec in "$@"; do
  case "$spec" in
    *:*) package_name="${spec%%:*}"; test_name="${spec#*:}" ;;
    *) echo "invalid test manifest entry $spec; use <package>:<test-name>" >&2; exit 2 ;;
  esac
  if [ -z "$package_name" ] || [[ ! "$test_name" =~ ^Test[A-Za-z0-9_]+$ ]]; then
    echo "invalid test manifest entry $spec" >&2
    exit 2
  fi

  import_path="$(go list -f '{{.ImportPath}}' "$package_name")"
  expected+=("$import_path:$test_name")
  tests+=("$test_name")

  package_seen=false
  if [ "$package_count" -gt 0 ]; then
    for existing_package in "${packages[@]}"; do
      if [ "$existing_package" = "$package_name" ]; then
        package_seen=true
        break
      fi
    done
  fi
  if [ "$package_seen" = false ]; then
    packages+=("$package_name")
    package_count=$((package_count + 1))
  fi
done

test_pattern="$(IFS='|'; echo "^(${tests[*]})$")"
json_log="$(mktemp "${TMPDIR:-/tmp}/tokenrouter-go-test-events.XXXXXX")"
cleanup() { rm -f -- "$json_log"; }
trap cleanup EXIT

set +e
go test -json -p 1 -count=1 -run "$test_pattern" "${packages[@]}" 2>&1 | tee "$json_log"
pipeline_status=("${PIPESTATUS[@]}")
set -e
go_status="${pipeline_status[0]}"
tee_status="${pipeline_status[1]}"

verify_status=0
node "$VERIFIER" "$json_log" "${expected[@]}" || verify_status=$?
if [ "$go_status" -ne 0 ] || [ "$tee_status" -ne 0 ] || [ "$verify_status" -ne 0 ]; then
  echo "Go test manifest gate failed (go=$go_status, tee=$tee_status, verify=$verify_status)" >&2
  exit 1
fi
