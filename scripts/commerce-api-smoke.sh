#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${COMMERCE_API_BIN:-$ROOT/bin/commerce-api}"
LOG="${COMMERCE_API_SMOKE_LOG:-$ROOT/.verification/iteration-6/commerce-api-process.log}"
mkdir -p "$(dirname "$LOG")"

if [[ ! -x "$BIN" ]]; then
  echo "commerce-api binary is missing: $BIN" >&2
  exit 2
fi

PORT="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"
BASE="http://127.0.0.1:$PORT"
ADMIN=(-H 'X-Principal-ID: admin-smoke' -H 'X-Principal-Role: platform-admin')
TENANT=(-H 'X-Principal-ID: user-smoke' -H 'X-Tenant-ID: tenant-smoke')
PID=""
cleanup() {
  if [[ -n "$PID" ]] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

COMMERCE_API_ADDR="127.0.0.1:$PORT" \
COMMERCE_DEV_ALLOW_ALL_OWNERSHIP=true \
"$BIN" >"$LOG" 2>&1 &
PID=$!

for _ in $(seq 1 100); do
  if curl --silent --show-error --fail "$BASE/healthz" >/tmp/commerce-health.$$.json 2>/dev/null; then
    break
  fi
  if ! kill -0 "$PID" 2>/dev/null; then
    cat "$LOG" >&2
    exit 1
  fi
  sleep 0.05
done
jq -e '.status == "ok"' </tmp/commerce-health.$$.json >/dev/null
rm -f /tmp/commerce-health.$$.json

curl --silent --show-error --fail -X POST "$BASE/v1/admin/plan-definitions" \
  "${ADMIN[@]}" -H 'Content-Type: application/json' \
  --data '{"id":"developer","name":"Developer"}' | jq -e '.ID == "developer"' >/dev/null

curl --silent --show-error --fail -X POST "$BASE/v1/admin/plan-versions" \
  "${ADMIN[@]}" -H 'Content-Type: application/json' \
  --data '{
    "id":"developer-v1",
    "definition_id":"developer",
    "policy_version":"policy-smoke-v1",
    "number":1,
    "effective_from":"2026-07-01T00:00:00Z",
    "spec":{
      "currency":"EUR",
      "features":{"deploy":true},
      "quotas":{"runtime.units":2},
      "prices":{"runtime.unit_seconds":{"minor_units":1,"per_quantity":3600}},
      "included":{},
      "charge_user_build_failures":true
    }
  }' | jq -e '.ID == "developer-v1" and .State == "DRAFT"' >/dev/null

curl --silent --show-error --fail -X POST "$BASE/v1/admin/plan-versions/developer-v1/activate" \
  "${ADMIN[@]}" | jq -e '.State == "ACTIVE"' >/dev/null

curl --silent --show-error --fail -X POST "$BASE/v1/admin/subscriptions" \
  "${ADMIN[@]}" -H 'Content-Type: application/json' \
  --data '{
    "id":"sub-smoke",
    "tenant_id":"tenant-smoke",
    "plan_version_id":"developer-v1",
    "period_id":"period-smoke",
    "state":"ACTIVE",
    "period_start":"2026-07-01T00:00:00Z",
    "period_end":"2026-08-01T00:00:00Z"
  }' | jq -e '.subscription.ID == "sub-smoke" and .billing_period.ID == "period-smoke"' >/dev/null

curl --silent --show-error --fail -X POST "$BASE/v1/organizations/tenant-smoke/entitlements/check" \
  "${TENANT[@]}" -H 'Content-Type: application/json' \
  --data '{"feature":"deploy","resource":"runtime.units","quantity":1,"at":"2026-07-13T12:00:00Z"}' \
  | jq -e '.allowed == true and .remaining == 2' >/dev/null

RESERVATION="$(curl --silent --show-error --fail -X POST "$BASE/v1/organizations/tenant-smoke/quota-reservations" \
  "${TENANT[@]}" -H 'Content-Type: application/json' -H 'Idempotency-Key: quota-smoke-1' \
  --data '{"resource":"runtime.units","quantity":1,"expires_at":"2026-07-13T13:00:00Z","at":"2026-07-13T12:00:00Z"}')"
RESERVATION_ID="$(jq -er '.id' <<<"$RESERVATION")"

curl --silent --show-error --fail -X POST "$BASE/v1/organizations/tenant-smoke/quota-reservations/$RESERVATION_ID/commit" \
  "${TENANT[@]}" | jq -e '.status == "committed"' >/dev/null

USAGE='{"period_id":"period-smoke","resource_type":"application","resource_id":"app-smoke","meter":"runtime.unit_seconds","kind":"STANDARD","quantity":3600,"occurred_at":"2026-07-13T12:00:00Z","window_start":"2026-07-13T11:00:00Z","window_end":"2026-07-13T12:00:00Z"}'
for _ in 1 2; do
  curl --silent --show-error --fail -X POST "$BASE/v1/organizations/tenant-smoke/usage" \
    "${TENANT[@]}" -H 'Content-Type: application/json' -H 'Idempotency-Key: usage-smoke-1' \
    --data "$USAGE" | jq -e '.status == "recorded"' >/dev/null
done

curl --silent --show-error --fail "$BASE/v1/organizations/tenant-smoke/billing-periods/period-smoke/invoice-preview" \
  "${TENANT[@]}" | jq -e '.total_minor_units == 1 and (.lines | length) == 1' >/dev/null

curl --silent --show-error --fail -X POST "$BASE/v1/organizations/tenant-smoke/commercial-state/suspend" \
  "${TENANT[@]}" | jq -e '.action == "suspend" and .retain_managed_services == true' >/dev/null

STATUS="$(curl --silent --output /tmp/commerce-cross-tenant.$$.json --write-out '%{http_code}' \
  -X POST "$BASE/v1/organizations/tenant-other/entitlements/check" \
  "${TENANT[@]}" -H 'Content-Type: application/json' --data '{"feature":"deploy"}')"
[[ "$STATUS" == "403" ]]
jq -e '.error.code == "PERMISSION_DENIED"' </tmp/commerce-cross-tenant.$$.json >/dev/null
rm -f /tmp/commerce-cross-tenant.$$.json

CACHE_CONTROL="$(curl --silent --head "$BASE/healthz" | tr -d '\r' | awk -F': ' 'tolower($1)=="cache-control"{print $2}')"
[[ "$CACHE_CONTROL" == "no-store" ]]

echo "commerce-api process smoke: PASS"
