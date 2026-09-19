#!/usr/bin/env bash
set -Eeuo pipefail

# Linux 生产备份：MySQL 一致性 dump + Milvus/etcd/MinIO/app-data 卷归档。
# 运行前在项目目录准备 .env.prod，并确认生产 Compose 正常运行。
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${COMPOSE_FILE:-$ROOT_DIR/docker-compose.prod.yml}"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.prod}"
BACKUP_ROOT="${BACKUP_ROOT:-$ROOT_DIR/data/backups}"
KEEP="${KEEP:-14}"
PROJECT="${COMPOSE_PROJECT_NAME:-eino-prod}"
MYSQL_SERVICE="mysql"

[[ -f "$COMPOSE_FILE" ]] || { echo "compose file not found: $COMPOSE_FILE" >&2; exit 1; }
[[ -f "$ENV_FILE" ]] || { echo "env file not found: $ENV_FILE" >&2; exit 1; }
mkdir -p "$BACKUP_ROOT"
set -a; # shellcheck disable=SC1090
source "$ENV_FILE"
set +a

: "${MYSQL_DATABASE:?MYSQL_DATABASE is required}"
DB_USER="${MYSQL_BACKUP_USER:-${MYSQL_USER:?MYSQL_USER is required}}"
DB_PASSWORD="${MYSQL_BACKUP_PASSWORD:-${MYSQL_PASSWORD:?MYSQL_PASSWORD is required}}"
STAMP="$(date -u +%Y%m%d-%H%M%S)"
TARGET="$BACKUP_ROOT/$STAMP"
mkdir -p "$TARGET"
cleanup() { [[ -f "$TARGET/manifest.txt" ]] || rm -rf "$TARGET"; }
trap cleanup EXIT

compose() { docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"; }
compose ps -q "$MYSQL_SERVICE" >/dev/null
MYSQL_CID="$(compose ps -q "$MYSQL_SERVICE")"
[[ -n "$MYSQL_CID" ]] || { echo "mysql service is not running" >&2; exit 1; }

echo "[backup] dumping MySQL $MYSQL_DATABASE"
docker exec -e "MYSQL_PWD=$DB_PASSWORD" "$MYSQL_CID" mysqldump \
  --single-transaction --quick --routines --events --no-tablespaces \
  --set-gtid-purged=OFF --default-character-set=utf8mb4 \
  -u "$DB_USER" "$MYSQL_DATABASE" > "$TARGET/mysql.sql"
[[ -s "$TARGET/mysql.sql" ]] || { echo "empty mysql dump" >&2; exit 1; }
INSERTS="$(grep -c 'INSERT INTO' "$TARGET/mysql.sql" || true)"
(( INSERTS > 0 )) || { echo "mysql dump contains no INSERT INTO statements" >&2; exit 1; }
ROW_COUNTS=""
while IFS= read -r table; do
  [[ -z "$table" ]] && continue
  count="$(docker exec -e "MYSQL_PWD=$DB_PASSWORD" "$MYSQL_CID" mysql -N -B -u "$DB_USER" -D "$MYSQL_DATABASE" -e "SELECT COUNT(*) FROM \`$table\`")"
  ROW_COUNTS+="$(printf '%s\t%s\n' "$table" "$count")"
done < <(docker exec -e "MYSQL_PWD=$DB_PASSWORD" "$MYSQL_CID" mysql -N -B -u "$DB_USER" -D "$MYSQL_DATABASE" -e "SELECT table_name FROM information_schema.tables WHERE table_schema='$MYSQL_DATABASE' AND table_type='BASE TABLE' ORDER BY table_name")
[[ -n "$ROW_COUNTS" ]] || { echo "database has no base tables" >&2; exit 1; }

echo "[backup] archiving persistent volumes"
declare -A VOLUMES=(
  [milvus-data]="${MILVUS_VOLUME_NAME:-eino-prod-milvus}"
  [etcd-data]="${ETCD_VOLUME_NAME:-eino-prod-etcd}"
  [minio-data]="${MINIO_VOLUME_NAME:-eino-prod-minio}"
  [app-data]="${APP_VOLUME_NAME:-eino-prod-app-data}"
)
HELPER_IMAGE="${BACKUP_HELPER_IMAGE:-alpine:3.22}"
for logical in "${!VOLUMES[@]}"; do
  volume="${VOLUMES[$logical]}"
  docker volume inspect "$volume" >/dev/null
  archive="$TARGET/${logical}.tgz"
  docker run --rm --pull=never -v "$volume:/data:ro" -v "$TARGET:/backup" \
    "$HELPER_IMAGE" tar czf "/backup/$(basename "$archive")" -C /data .
done

{
  echo "backup_time_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "database=$MYSQL_DATABASE"
  echo "insert_statements=$INSERTS"
  echo "[mysql_row_counts]"
  while IFS=$'\t' read -r table count; do
    printf '%s = %s\n' "$table" "$count"
  done <<< "$ROW_COUNTS"
  echo "[files]"
  for file in "$TARGET"/*; do
    name="$(basename "$file")"
    hash="$(sha256sum "$file" | awk '{print $1}')"
    bytes="$(stat -c '%s' "$file")"
    printf '%s sha256=%s bytes=%s\n' "$name" "$hash" "$bytes"
  done
} > "$TARGET/manifest.txt"

mapfile -t dirs < <(find "$BACKUP_ROOT" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | grep -E '^[0-9]{8}-[0-9]{6}$' | sort -r)
if (( KEEP > 0 && ${#dirs[@]} > KEEP )); then
  for old in "${dirs[@]:KEEP}"; do rm -rf "$BACKUP_ROOT/$old"; done
fi
trap - EXIT
echo "[backup] complete: $TARGET"
