#!/usr/bin/env bash
set -Eeuo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.prod}"
COMPOSE_FILE="${COMPOSE_FILE:-$ROOT_DIR/docker-compose.prod.yml}"
VERSION="${1:-${VERSION:-}}"
[[ -n "$VERSION" ]] || { echo "usage: prod-rollback.sh <immutable-version>" >&2; exit 2; }
APP_IMAGE_REPOSITORY="${APP_IMAGE_REPOSITORY:-my-eino-app}"
export APP_IMAGE="$APP_IMAGE_REPOSITORY:$VERSION"
echo "[prod-rollback] switching to $APP_IMAGE"
exec docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" up -d --no-build app
