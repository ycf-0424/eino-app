#!/usr/bin/env bash
set -Eeuo pipefail

# Linux 恢复演练：先校验 manifest/SHA256，再将 SQL 导入临时库并逐表比对。
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${COMPOSE_FILE:-$ROOT_DIR/docker-compose.prod.yml}"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.prod}"
BACKUP_ROOT="${BACKUP_ROOT:-$ROOT_DIR/data/backups}"
BACKUP_DIR="${BACKUP_DIR:-}"
RESTORE_DATABASE="${RESTORE_DATABASE:-eino_restore_check}"
KEEP_TEMP_DATABASE="${KEEP_TEMP_DATABASE:-false}"
set -a; # shellcheck disable=SC1090
source "$ENV_FILE"
set +a
DB_USER="${MYSQL_BACKUP_USER:-${MYSQL_USER:?MYSQL_USER is required}}"
DB_PASSWORD="${MYSQL_BACKUP_PASSWORD:-${MYSQL_PASSWORD:?MYSQL_PASSWORD is required}}"
compose() { docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"; }
MYSQL_CID="$(compose ps -q mysql)"
[[ -n "$MYSQL_CID" ]] || { echo "mysql service is not running" >&2; exit 1; }

if [[ -z "$BACKUP_DIR" ]]; then
  while IFS= read -r dir; do
    [[ -f "$dir/manifest.txt" ]] && { BACKUP_DIR="$dir"; break; }
  done < <(find "$BACKUP_ROOT" -mindepth 1 -maxdepth 1 -type d | sort -r)
fi
[[ -n "$BACKUP_DIR" && -f "$BACKUP_DIR/manifest.txt" ]] || { echo "complete backup not found" >&2; exit 1; }
SQL="$BACKUP_DIR/mysql.sql"
[[ -s "$SQL" ]] || { echo "mysql.sql missing or empty" >&2; exit 1; }

echo "[restore-check] verifying checksums"
while read -r name rest; do
  hash="${rest#sha256=}"
  hash="${hash%% *}"
  [[ -f "$BACKUP_DIR/$name" ]] || { echo "missing artifact: $name" >&2; exit 1; }
  actual="$(sha256sum "$BACKUP_DIR/$name" | awk '{print $1}')"
  [[ "$actual" == "$hash" ]] || { echo "checksum mismatch: $name" >&2; exit 1; }
done < <(awk '/^\[files\]/{ok=1; next} ok && NF && $1 !~ /^\[/{print $1, $2}' "$BACKUP_DIR/manifest.txt")
grep -q 'INSERT INTO' "$SQL" || { echo "dump has no INSERT INTO statements" >&2; exit 1; }
[[ "$RESTORE_DATABASE" =~ ^[A-Za-z0-9_]{1,64}$ ]] || { echo "invalid restore database" >&2; exit 1; }

sql() { docker exec -e "MYSQL_PWD=$DB_PASSWORD" "$MYSQL_CID" mysql -N -B -u "$DB_USER" -e "$1"; }
sql "DROP DATABASE IF EXISTS \`$RESTORE_DATABASE\`; CREATE DATABASE \`$RESTORE_DATABASE\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
docker cp "$SQL" "$MYSQL_CID:/tmp/eino-restore-check.sql"
docker exec -e "MYSQL_PWD=$DB_PASSWORD" "$MYSQL_CID" sh -c "mysql -u '$DB_USER' -D '$RESTORE_DATABASE' < /tmp/eino-restore-check.sql"
TABLES="$(sql "SELECT table_name FROM information_schema.tables WHERE table_schema='$RESTORE_DATABASE' AND table_type='BASE TABLE'")"
[[ -n "$TABLES" ]] || { echo "restored database has no tables" >&2; exit 1; }
EXPECTED_SECTION="$(awk '/^\[mysql_row_counts\]/{ok=1; next} /^\[/{if(ok) exit} ok && NF{print}' "$BACKUP_DIR/manifest.txt")"
[[ -n "$EXPECTED_SECTION" ]] || { echo "manifest has no mysql row counts" >&2; exit 1; }
while IFS=$'\t' read -r table expected; do
  [[ -z "$table" ]] && continue
  count="$(sql "SELECT COUNT(*) FROM \`$RESTORE_DATABASE\`.\`$table\`")"
  [[ "$count" == "$expected" ]] || { echo "row count mismatch: $table expected=$expected actual=$count" >&2; exit 1; }
  echo "[restore-check] $table rows=$count"
done < <(printf '%s\n' "$EXPECTED_SECTION" | awk -F' = ' 'NF==2 {print $1 "\t" $2}')

if [[ "$KEEP_TEMP_DATABASE" != "true" ]]; then
  sql "DROP DATABASE IF EXISTS \`$RESTORE_DATABASE\`;"
fi
docker exec "$MYSQL_CID" rm -f /tmp/eino-restore-check.sql >/dev/null 2>&1 || true
echo "[restore-check] complete: $BACKUP_DIR"
