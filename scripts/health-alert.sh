#!/usr/bin/env bash
set -Eeuo pipefail

# 轻量生产探测：供 cron/systemd timer/1Panel 计划任务调用。
# 失败时向 ALERT_WEBHOOK_URL 发 JSON（未配置 webhook 仍以非零退出，便于 1Panel 告警）。
BASE_URL="${BASE_URL:-http://127.0.0.1:${APP_PORT:-18180}}"
METRICS_TOKEN="${METRICS_TOKEN:-}"
ALERT_WEBHOOK_URL="${ALERT_WEBHOOK_URL:-}"
MESSAGE=""

if ! curl --fail --silent --show-error --max-time 10 "$BASE_URL/health/ready" >/dev/null; then
  MESSAGE="eino readiness probe failed: $BASE_URL/health/ready"
elif [[ -n "$METRICS_TOKEN" ]] && ! curl --fail --silent --show-error --max-time 10 -H "Authorization: Bearer $METRICS_TOKEN" "$BASE_URL/metrics" | grep -q 'requests'; then
  MESSAGE="eino metrics probe failed: $BASE_URL/metrics"
fi

if [[ -z "$MESSAGE" ]]; then
  echo "[health-alert] ok"
  exit 0
fi
echo "[health-alert] $MESSAGE" >&2
if [[ -n "$ALERT_WEBHOOK_URL" ]]; then
  payload="{\"text\":\"$MESSAGE\"}"
  curl --fail --silent --show-error --max-time 10 -H 'Content-Type: application/json' -d "$payload" "$ALERT_WEBHOOK_URL" >/dev/null
fi
exit 1
