#!/usr/bin/env bash
# Regression test for acceptance failure aggregation. It intentionally injects
# failures and verifies that every phase is recorded before the final non-zero.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GATE="$REPO_ROOT/scripts/tokenrouter-acceptance.sh"
OUTPUT=$(mktemp "${TMPDIR:-/tmp}/tokenrouter-acceptance-test.XXXXXX")
cleanup() { rm -f -- "$OUTPUT"; }
trap cleanup EXIT

status=0
if "$GATE" --self-test-failures >"$OUTPUT" 2>&1; then
  echo "acceptance self-test unexpectedly passed"
  cat "$OUTPUT"
  exit 1
else
  status=$?
fi

if [ "$status" -ne 1 ]; then
  echo "acceptance self-test returned $status; expected 1"
  cat "$OUTPUT"
  exit 1
fi

for expected in \
  "[FAIL] injected install failure" \
  "[FAIL] injected piped frontend failure" \
  "[FAIL] injected protocol-module failure" \
  "[FAIL] injected smoke-cleanup failure" \
  "ACCEPTANCE: 4 CHECK(S) FAILED"
do
  if ! grep -Fq "$expected" "$OUTPUT"; then
    echo "acceptance self-test missing: $expected"
    cat "$OUTPUT"
    exit 1
  fi
done

# The translation verifier imports TypeScript from web/node_modules. Its
# self-test must therefore remain after the clean checkout's frozen install.
install_line="$(grep -nF 'frontend install (npm ci)' "$GATE" | cut -d: -f1)"
translation_line="$(grep -nF 'translation report verifier self-test' "$GATE" | cut -d: -f1)"
if [[ ! "$install_line" =~ ^[0-9]+$ ]] || [[ ! "$translation_line" =~ ^[0-9]+$ ]] || \
    [ "$translation_line" -le "$install_line" ]; then
  echo "translation verifier self-test must run after the frozen frontend install"
  exit 1
fi

echo "Acceptance failure aggregation regression test passed."
