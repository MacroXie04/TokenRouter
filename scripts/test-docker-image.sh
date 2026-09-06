#!/usr/bin/env bash
# Build the real multi-stage Dockerfile, start the resulting image with only an
# isolated SQLite database and explicit production-safe test settings, verify
# /api/status, and remove the disposable container and image on every exit.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE_TAG="${TOKENROUTER_DOCKER_TEST_TAG:-tokenrouter-acceptance:local-$$-$(date +%s)}"
CONTAINER_NAME="tokenrouter-acceptance-$$-$(date +%s)"
STATUS_FILE="${TOKENROUTER_DOCKER_TEST_STATUS_FILE:-}"
CREATED_IMAGE=false
CREATED_CONTAINER=false
CREATED_STATUS_FILE=false

cleanup() {
  local original_status=$?
  trap - EXIT
  if [ "$CREATED_CONTAINER" = true ]; then
    docker container rm --force "$CONTAINER_NAME" >/dev/null 2>&1 || true
  fi
  if [ "$CREATED_IMAGE" = true ]; then
    docker image rm --force "$IMAGE_TAG" >/dev/null 2>&1 || true
  fi
  if [ "$CREATED_STATUS_FILE" = true ]; then
    rm -f -- "$STATUS_FILE" 2>/dev/null || true
  fi
  exit "$original_status"
}
trap cleanup EXIT

if docker image inspect "$IMAGE_TAG" >/dev/null 2>&1; then
  echo "refusing to overwrite existing Docker image: $IMAGE_TAG" >&2
  exit 2
fi
if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
  echo "refusing to replace existing Docker container: $CONTAINER_NAME" >&2
  exit 2
fi
# From this point the unique names are reserved by this process. Mark them for
# cleanup before creation so an interrupt between Docker returning and the next
# shell statement cannot leave a completed image or container behind.
CREATED_IMAGE=true
CREATED_CONTAINER=true
if [ -z "$STATUS_FILE" ]; then
  STATUS_FILE="$(mktemp "${TMPDIR:-/tmp}/tokenrouter-docker-status.XXXXXX")"
  CREATED_STATUS_FILE=true
fi

cd "$REPO_ROOT"
docker build --tag "$IMAGE_TAG" --build-arg VERSION=acceptance .
docker image inspect "$IMAGE_TAG" >/dev/null

docker run --detach \
  --name "$CONTAINER_NAME" \
  --publish 127.0.0.1::3000 \
  --env GIN_MODE=release \
  --env SESSION_SECRET=71d94f2ac8e603b57c1ae0469fdb3285e47ac9106bd25f83 \
  --env SQL_DSN= \
  --env LOG_SQL_DSN= \
  --env REDIS_CONN_STRING= \
  --env SQLITE_PATH=/tmp/tokenrouter-docker-smoke.db \
  --env JIMENG_RECOVERY_DIR=/tmp/tokenrouter-jimeng-recovery \
  --env TOKENROUTER_CHANNEL_TYPE_CATALOG=reference-v1 \
  --env PORT=3000 \
  "$IMAGE_TAG" >/dev/null

published_address="$(docker port "$CONTAINER_NAME" 3000/tcp)"
published_port="${published_address##*:}"
if [[ ! "$published_port" =~ ^[0-9]+$ ]]; then
  echo "could not determine disposable container port from: $published_address" >&2
  exit 1
fi

for attempt in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$published_port/api/status" >"$STATUS_FILE" 2>/dev/null \
      && grep -Eq '"success"[[:space:]]*:[[:space:]]*true' "$STATUS_FILE"; then
    echo "Docker image built and isolated production-mode status probe passed."
    exit 0
  fi
  if [ "$(docker inspect --format '{{.State.Running}}' "$CONTAINER_NAME" 2>/dev/null || true)" != true ]; then
    break
  fi
  sleep 1
done

echo "Docker container did not become healthy; final logs follow:" >&2
docker logs --tail 100 "$CONTAINER_NAME" >&2 || true
exit 1
