#!/usr/bin/env bash
set -Eeuo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.prod}"
COMPOSE_FILE="${COMPOSE_FILE:-$ROOT_DIR/docker-compose.prod.yml}"
VERSION="${1:-${VERSION:-}}"
[[ -f "$ENV_FILE" ]] || { echo "env file not found: $ENV_FILE" >&2; exit 1; }
[[ -f "$COMPOSE_FILE" ]] || { echo "compose file not found: $COMPOSE_FILE" >&2; exit 1; }
set -a; # shellcheck disable=SC1090
source "$ENV_FILE"
set +a
if [[ -n "$VERSION" ]]; then export APP_IMAGE="${APP_IMAGE_REPOSITORY:-my-eino-app}:$VERSION"; fi
: "${APP_IMAGE:?APP_IMAGE or VERSION is required}"
echo "[prod-deploy] image=$APP_IMAGE"
exec docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" up -d --no-build
