#!/usr/bin/env bash
# Verify API_MATRIX route rows against the routes actually registered by the
# TokenRouter router. A Go test dumps router.SetUpRouter()'s route table
# (method + full path, one per line) to /tmp/tokenrouter_routes.txt; each
# matrix row is matched exactly against it with :param and *path placeholders
# normalized. Exact matching only — no suffix guessing.
set -euo pipefail
cd "$(dirname "$0")/.."

GOWORK=off go test ./router/ -run TestDumpRoutes -count=1 >/dev/null
DUMP=/tmp/tokenrouter_routes.txt
test -s "$DUMP" || { echo "route dump missing" >&2; exit 1; }
# gin keeps trailing slashes on "/" registrations; normalize them so exact
# matching works for both sides of the comparison.
sed -E 's|(.)/$|\1|' "$DUMP" > /tmp/tokenrouter_routes.norm.txt
DUMP=/tmp/tokenrouter_routes.norm.txt

grep -oE '^\| [0-9]+ \| [A-Z]+ [^|]+' docs/parity/API_MATRIX.md \
  | sed -E 's/^\| [0-9]+ \| //' | sed -E 's/ +$//' > /tmp/api_rows.txt
total=$(wc -l < /tmp/api_rows.txt | tr -d ' ')
echo "total rows: $total"

missing=0
found=0
special=0
: > /tmp/api_missing.txt
: > /tmp/api_found.txt
: > /tmp/api_ambig.txt
: > /tmp/api_special.txt
while IFS= read -r row; do
  method="${row%% *}"
  path="${row#* }"
  # gin redirects trailing-slash variants to the same route; normalize.
  path="${path%/}"
  if [ -z "$path" ]; then path="/"; fi
  # The static web root is served by main.go's embedded SPA (classified as an
  # equivalent in scripts/update-api-matrix.sh); it is not a SetUpRouter route.
  if [ "$path" = "/ (web static)" ]; then
    echo "$row" >> /tmp/api_special.txt
    special=$((special+1))
    continue
  fi
  pat=$(printf '%s' "$path" | sed -E 's/:[a-zA-Z0-9_]+/[^\/]+/g; s/\*path/.*/g')
  hits=$(grep -cE "^${method} ${pat}$" "$DUMP" || true)
  if [ "$hits" -gt 0 ]; then
    echo "FOUND ${method} ${path}" >> /tmp/api_found.txt
    found=$((found+1))
  else
    echo "$row" >> /tmp/api_missing.txt
    missing=$((missing+1))
  fi
done < /tmp/api_rows.txt

echo "found: $found"
echo "special (web static): $special"
echo "missing: $missing"
