#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${ATTACHMENTS_API_BIN:-$ROOT/bin/attachments-api}"
if [[ ! -x "$BIN" ]]; then
  mkdir -p "$ROOT/bin"
  go build -trimpath -o "$BIN" "$ROOT/cmd/attachments-api"
fi
PORT="$(python3 - <<'PY'
import socket
s=socket.socket(); s.bind(('127.0.0.1',0)); print(s.getsockname()[1]); s.close()
PY
)"
BASE="http://127.0.0.1:$PORT"
LOG="${TMPDIR:-/tmp}/attachments-api-smoke-$PORT.log"
ATTACHMENTS_API_ADDR="127.0.0.1:$PORT" "$BIN" >"$LOG" 2>&1 &
PID=$!
cleanup() {
  kill "$PID" 2>/dev/null || true
  wait "$PID" 2>/dev/null || true
}
trap cleanup EXIT
for _ in $(seq 1 100); do
  if curl -fsS "$BASE/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.05
done
curl -fsS "$BASE/healthz" | grep -q '"status":"ok"'

headers=(-H 'X-Tenant-ID: tenant-local' -H 'X-Principal-ID: user-local' -H 'Content-Type: application/json')
SECRET='smoke-super-secret-value'
secret_response="$(curl -fsS -X POST "${headers[@]}" -H 'Idempotency-Key: smoke-secret-1' \
  --data "{\"name\":\"API_TOKEN\",\"scope\":\"runtime\",\"phase\":\"runtime\",\"value\":\"$SECRET\"}" \
  "$BASE/v1/organizations/tenant-local/applications/app-local/environments/env-local/secrets")"
if grep -Fq "$SECRET" <<<"$secret_response"; then echo 'secret leaked in API response' >&2; exit 1; fi

service_response="$(curl -fsS -X POST "${headers[@]}" -H 'Idempotency-Key: smoke-service-1' \
  --data '{"name":"main-db","plan_id":"pg-small"}' "$BASE/v1/organizations/tenant-local/services")"
service_id="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$service_response")"
[[ -n "$service_id" ]]

binding_response="$(curl -fsS -X POST "${headers[@]}" -H 'Idempotency-Key: smoke-bind-1' \
  --data "{\"instance_id\":\"$service_id\",\"capabilities\":[\"connect\"]}" \
  "$BASE/v1/organizations/tenant-local/applications/app-local/environments/env-local/bindings")"
if grep -Eq 'postgres://|DATABASE_URL|smoke-super-secret' <<<"$binding_response"; then echo 'credential leaked in binding response' >&2; exit 1; fi

domain_response="$(curl -fsS -X POST "${headers[@]}" -H 'Idempotency-Key: smoke-domain-1' \
  --data '{"preferred_name":"booking"}' \
  "$BASE/v1/organizations/tenant-local/applications/app-local/environments/env-local/domains")"
grep -q 'ACTIVE' <<<"$domain_response"

snapshot="$(curl -fsS "${headers[@]}" "$BASE/v1/organizations/tenant-local/applications/app-local/environments/env-local/attachment-snapshot")"
python3 -c '
import json, sys
v = json.load(sys.stdin)
assert v["snapshot_id"]
assert v["version"] >= 3
assert len(v.get("service_bindings", [])) == 1
assert len(v.get("active_domains", [])) == 1
raw = json.dumps(v)
assert "postgres://" not in raw
assert "smoke-super-secret" not in raw
' <<<"$snapshot"

status="$(curl -sS -o /tmp/attachments-cross-tenant-body-$$ -w '%{http_code}' -X POST \
  -H 'X-Tenant-ID: tenant-local' -H 'X-Principal-ID: user-local' -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: cross-tenant' --data '{"name":"evil","plan_id":"pg-small"}' \
  "$BASE/v1/organizations/tenant-other/services")"
[[ "$status" == "403" ]]
rm -f /tmp/attachments-cross-tenant-body-$$

echo 'attachments-api smoke: PASS'
