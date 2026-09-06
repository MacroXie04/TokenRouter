#!/usr/bin/env bash
# Scan exactly the tracked and non-ignored untracked source files. Match output
# is reduced to file/line locations so a discovered credential is not repeated
# into CI logs. Exit codes above grep's ordinary "no match" status fail closed.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

scan_pattern() {
  local scan_root="$1"
  local pattern="$2"
  local description="$3"
  local allowed_path="${4:-}"
  local allowed_value="${5:-}"
  local file_list="$6"
  local scratch="$7"
  local found=false
  local scan_failed=false

  while IFS= read -r -d '' relative_path; do
    if [ "$relative_path" = ".env.example" ] || [ -L "$scan_root/$relative_path" ]; then
      continue
    fi
    if [ ! -f "$scan_root/$relative_path" ]; then
      echo "secret scan could not read listed source file: $relative_path" >&2
      scan_failed=true
      continue
    fi

    : >"$scratch.matches"
    : >"$scratch.errors"
    grep_status=0
    LC_ALL=C grep -InEo -- "$pattern" "$scan_root/$relative_path" \
      >"$scratch.matches" 2>"$scratch.errors" || grep_status=$?
    case "$grep_status" in
      0)
        while IFS= read -r hit; do
          line_number="${hit%%:*}"
          matched_value="${hit#*:}"
          if [ "$relative_path" = "$allowed_path" ] && [ "$matched_value" = "$allowed_value" ]; then
            continue
          fi
          echo "possible $description in $relative_path:$line_number" >&2
          found=true
        done <"$scratch.matches"
        ;;
      1) ;;
      *)
        echo "secret scan failed while reading $relative_path (grep exit $grep_status)" >&2
        scan_failed=true
        ;;
    esac
  done <"$file_list"

  [ "$found" = false ] && [ "$scan_failed" = false ]
}

scan_repository() {
  local scan_root="$1"
  local scan_tmp
  local result=0
  local known_aws_fixture="AKIA""ABCDEFGHIJKLMNOP"

  if [ ! -d "$scan_root" ]; then
    echo "secret scan root is not a directory: $scan_root" >&2
    return 1
  fi
  scan_tmp="$(mktemp -d "${TMPDIR:-/tmp}/tokenrouter-secret-scan.XXXXXX")"
  if ! node "$REPO_ROOT/scripts/list-source-files.mjs" "$scan_root" >"$scan_tmp/files"; then
    echo "secret scan could not enumerate repository files" >&2
    rm -rf -- "$scan_tmp"
    return 1
  fi

  scan_pattern "$scan_root" 'sk-[A-Za-z0-9]{24,}' 'API key' '' '' "$scan_tmp/files" "$scan_tmp/api" || result=1
  scan_pattern "$scan_root" 'AKIA[0-9A-Z]{16}' 'AWS access key' \
    'internal/platform/logging/logger_test.go' "$known_aws_fixture" "$scan_tmp/files" "$scan_tmp/aws" || result=1
  scan_pattern "$scan_root" 'ghp_[A-Za-z0-9]{36}' 'GitHub token' '' '' "$scan_tmp/files" "$scan_tmp/github" || result=1
  scan_pattern "$scan_root" '-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----' 'private key' '' '' "$scan_tmp/files" "$scan_tmp/private-key" || result=1

  rm -rf -- "$scan_tmp"
  return "$result"
}

run_self_test() {
  local test_root
  local injected_token
  local known_aws_fixture="AKIA""ABCDEFGHIJKLMNOP"
  local result=0
  test_root="$(mktemp -d "${TMPDIR:-/tmp}/tokenrouter-secret-scan-test.XXXXXX")"
  git -C "$test_root" init -q
  mkdir -p "$test_root/internal/platform/logging"
  printf '%s\n' 'ordinary source text' >"$test_root/clean.txt"
  printf '%s\n' "$known_aws_fixture" >"$test_root/internal/platform/logging/logger_test.go"
  if ! scan_repository "$test_root"; then
    echo "secret scanner rejected its exact public test fixture" >&2
    result=1
  fi

  injected_token="sk-$(printf '%024d' 0)"
  printf 'example placeholder followed by a real-looking token: %s\n' "$injected_token" >"$test_root/mixed.txt"
  if scan_repository "$test_root" >/dev/null 2>&1; then
    echo "secret scanner missed a token on a line containing placeholder language" >&2
    result=1
  fi

  printf '%s\n' 'tracked then made unreadable as a source file' >"$test_root/io-target"
  git -C "$test_root" add io-target
  rm -f -- "$test_root/io-target"
  mkdir "$test_root/io-target"
  if scan_repository "$test_root" >/dev/null 2>&1; then
    echo "secret scanner treated an unreadable listed source path as clean" >&2
    result=1
  fi

  rm -rf -- "$test_root"
  if [ "$result" -ne 0 ]; then
    return "$result"
  fi
  echo "Repository secret scanner self-test passed."
}

if [ "${1:-}" = "--self-test" ]; then
  run_self_test
  exit $?
fi
if [ "$#" -ne 0 ]; then
  echo "usage: $0 [--self-test]" >&2
  exit 2
fi
scan_repository "$REPO_ROOT"
