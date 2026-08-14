#!/usr/bin/env bash
# update-api-matrix.sh — machine-update API_MATRIX.md statuses from the actual
# router registrations. Classification:
#   PASS                  route registered (or documented equivalent/static)
#   REFERENCE_PLACEHOLDER task platforms (deviation #3) and not-ported
#                         external-platform features (deviation #10)
#   NOT_STARTED           everything else (open backlog for later iterations)
set -euo pipefail
cd "$(dirname "$0")/.."

MATRIX="docs/parity/API_MATRIX.md"

# --- classify helper: prints the status for "METHOD path" ---
classify() {
  local method="$1" path="$2"
  # 1. task platforms (deviation #3); the public Midjourney image proxy is a
  # complete implementation rather than a configured-provider placeholder.
  if [ "$method" = "GET" ] && [ "$path" = "/mj/image/:id" ] &&
      awk -v m="$method" -v p="$path" \
        '$1=="FOUND" && $2==m && $3==p {found=1} END {exit !found}' /tmp/api_found.txt 2>/dev/null; then
    echo "PASS|controller/midjourney_image.go + router/router.go"
    return
  fi
  if printf '%s' "$path" | grep -qE '^/(mj|suno|kling|jimeng)/|^/v1/video|^/v1/videos|^/:mode/mj'; then
    echo "REFERENCE_PLACEHOLDER|task platform placeholder (KNOWN_DEVIATIONS #3)"
    return
  fi
  # 1b. endpoints the reference itself leaves unimplemented (RelayNotImplemented);
  #     TokenRouter registers them with the identical structured 501.
  if printf '%s' "$path" | grep -qE '/v1/images/variations|/v1/files|/v1/fine-tunes'; then
    echo "REFERENCE_PLACEHOLDER|reference returns 'API not implemented' 501; TokenRouter matches"
    return
  fi
  if [ "$method" = "DELETE" ] && printf '%s' "$path" | grep -qE '/v1/models/'; then
    echo "REFERENCE_PLACEHOLDER|reference returns 'API not implemented' 501; TokenRouter matches"
    return
  fi
  # 2. not-ported external-platform features (deviation #10)
  if printf '%s' "$path" | grep -qE '/subscription/epay|/creem|/waffo|/vendors|/deployments|/performance|/ratio_sync|/codex|/ollama|/upstream_updates|/dashboard/billing|/user/topup/complete'; then
    echo "REFERENCE_PLACEHOLDER|feature not ported (KNOWN_DEVIATIONS #10)"
    return
  fi
  # 3. equivalents with different method/path in TokenRouter
  case "$path" in
    "/api/verification") echo "PASS|equivalent: POST /api/verification"; return ;;
    "/api/reset_password") echo "PASS|equivalent: POST /api/reset_password"; return ;;
    "/api/user/checkin") echo "PASS|equivalent: GET /api/user/checkin/status"; return ;;
    "/api/user/topup/self") echo "PASS|equivalent: GET /api/user/topup"; return ;;
    "/api/user/2fa/setup") echo "PASS|equivalent: POST /api/user/2fa/start (secret + pending record), then /api/user/2fa/enable"; return ;;
    "/ (web static)") echo "PASS|main.go serveEmbedded"; return ;;
  esac
  # 4. registered routes (from the verify pass; exact field match so a
  #    substring like /api/token can never match /api/token/search)
  if awk -v m="$method" -v p="$path" \
      '$1=="FOUND" && $2==m && $3==p {found=1} END {exit !found}' /tmp/api_found.txt 2>/dev/null; then
    echo "PASS|router/router.go"
    return
  fi
  echo "NOT_STARTED|"
}

# --- regenerate statuses ---
tmp="$(mktemp)"
while IFS= read -r line; do
  # The NoRoute fallback row has no METHOD prefix; classify it directly.
  if printf '%s' "$line" | grep -qE '^\| [0-9]+ \| NoRoute fallback'; then
    id=$(printf '%s' "$line" | sed -E 's/^\| ([0-9]+) \|.*/\1/')
    printf '| %s | NoRoute fallback | gzip + GlobalWebRateLimit + Cache; RelayNotFound for /v1,/api,/assets, else SPA | router/router.go + main.go serveEmbedded | PASS | controller.RelayNotFoundRoute + SPA fallback |\n' "$id" >> "$tmp"
    continue
  fi
  if ! printf '%s' "$line" | grep -qE '^\| [0-9]+ \| [A-Z]+ /'; then
    printf '%s\n' "$line" >> "$tmp"
    continue
  fi
  # Parse: id, method+path, description, evidence, status, target-notes
  id=$(printf '%s' "$line" | sed -E 's/^\| ([0-9]+) \|.*/\1/')
  mp=$(printf '%s' "$line" | sed -E 's/^\| [0-9]+ \| ([A-Z]+ [^|]*) \|.*/\1/')
  desc=$(printf '%s' "$line" | sed -E 's/^\| [0-9]+ \| [A-Z]+ [^|]* \| ([^|]*) \|.*/\1/')
  ev=$(printf '%s' "$line" | sed -E 's/^\| [0-9]+ \| [A-Z]+ [^|]* \| [^|]* \| ([^|]*) \|.*/\1/')
  method="${mp%% *}"
  path="${mp#* }"
  path="${path%/}"
  verdict=$(classify "$method" "$path")
  status="${verdict%%|*}"
  note="${verdict#*|}"
  printf '| %s | %s | %s | %s | %s | %s |\n' "$id" "$mp" "$desc" "$ev" "$status" "$note" >> "$tmp"
done < "$MATRIX"
mv "$tmp" "$MATRIX"

echo "statuses regenerated:"
grep -oE '\| (PASS|NOT_STARTED|IN_PROGRESS|REFERENCE_PLACEHOLDER|BLOCKED_EXTERNAL) \|' "$MATRIX" \
  | sort | uniq -c | sort -rn
