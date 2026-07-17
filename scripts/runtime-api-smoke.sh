#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-$ROOT/.verification/iteration-4/runtime-api-smoke}"
mkdir -p "$OUT"
LOG="$OUT/runtime-api-process.log"
JSONL="$OUT/runtime-api-smoke.jsonl"
RESULT="$OUT/runtime-api-smoke-result.txt"
PID=""
cleanup() {
  if [[ -n "$PID" ]]; then
    kill "$PID" >/dev/null 2>&1 || true
    wait "$PID" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

cd "$ROOT"
GO_BIN="${GO_BIN:-}"
if [[ -z "$GO_BIN" ]]; then
  GO_BIN="$(command -v go || true)"
fi
if [[ -z "$GO_BIN" && -x /opt/homebrew/bin/go ]]; then
  GO_BIN=/opt/homebrew/bin/go
fi
if [[ -z "$GO_BIN" ]]; then
  echo "go binary not found; set GO_BIN" >&2
  exit 127
fi
"$GO_BIN" build -trimpath -o ./bin/runtime-api ./cmd/runtime-api
port="$(python3 - <<'PY'
import socket
s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()
PY
)"
repo="$OUT/gitops"
rm -rf "$repo"
: > "$JSONL"
RUNTIME_API_ADDR="127.0.0.1:$port" \
RUNTIME_DEV_ALLOW_ARTIFACTS=true \
RUNTIME_GITOPS_ROOT="$repo" \
./bin/runtime-api >"$LOG" 2>&1 &
PID=$!

ready=0
for _ in $(seq 1 100); do
  if curl -fsS "http://127.0.0.1:$port/healthz" > "$OUT/health.json" 2>/dev/null; then ready=1; break; fi
  if ! kill -0 "$PID" >/dev/null 2>&1; then break; fi
  sleep 0.05
done
if [[ "$ready" -ne 1 ]]; then
  cat "$LOG" >&2
  exit 1
fi

tenant="tenant-smoke"
headers=(-H 'Content-Type: application/json' -H 'X-Principal-ID: user-smoke' -H "X-Tenant-ID: $tenant")
create="$(curl -fsS -X POST "${headers[@]}" -H 'Idempotency-Key: smoke-create' \
  --data '{"project_id":"project-smoke","name":"booking"}' \
  "http://127.0.0.1:$port/v1/organizations/$tenant/applications")"
printf '%s\n' "$create" >> "$JSONL"
read -r app env_id < <(python3 -c 'import json,sys; x=json.load(sys.stdin); print(x["application"]["ID"], x["default_environment"]["ID"])' <<<"$create")

digest="sha256:$(printf 'a%.0s' {1..64})"
deploy_body="$(cat <<JSON
{"artifact":{"artifact_id":"art-smoke","repository":"registry.test/tenants/$tenant/apps/$app","digest":"$digest","media_type":"application/vnd.oci.image.manifest.v1+json"},"configuration":{"region":"eu1","isolation":"sandboxed","unit":"u1","processes":{"web":{"port":8080,"minReplicas":1,"maxReplicas":3,"healthPath":"/health","startupTimeoutSeconds":60,"readinessTimeoutSeconds":30}},"attachment_snapshot_ref":"none","rollout_timeout_seconds":300,"egress_profile":"public-default"}}
JSON
)"
deploy="$(curl -fsS -X POST "${headers[@]}" -H 'Idempotency-Key: smoke-deploy' \
  --data "$deploy_body" \
  "http://127.0.0.1:$port/v1/organizations/$tenant/applications/$app/environments/$env_id/deployments")"
printf '%s\n' "$deploy" >> "$JSONL"
dep_id="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["deployment_id"])' <<<"$deploy")"
status="$(curl -fsS "${headers[@]}" "http://127.0.0.1:$port/v1/organizations/$tenant/deployments/$dep_id")"
printf '%s\n' "$status" >> "$JSONL"
wrong_code="$(curl -sS -o "$OUT/cross-tenant.json" -w '%{http_code}' \
  -H 'X-Principal-ID: user-smoke' -H 'X-Tenant-ID: tenant-other' \
  "http://127.0.0.1:$port/v1/organizations/$tenant/deployments/$dep_id")"

python3 - "$OUT/health.json" "$create" "$deploy" "$status" "$wrong_code" "$repo" "$RESULT" <<'PY'
import json, pathlib, sys
health_path, create_raw, deploy_raw, status_raw, wrong_code, repo_path, result_path = sys.argv[1:]
health=json.loads(pathlib.Path(health_path).read_text())
create=json.loads(create_raw); deploy=json.loads(deploy_raw); status=json.loads(status_raw)
repo=pathlib.Path(repo_path)
release_indexes=list((repo/".platform"/"releases").glob("*.json"))
runtime_sim_cell=False
runtime_sim_hostname=False
if release_indexes:
    index=json.loads(release_indexes[0].read_text())
    runtime_sim_cell=index.get("cell_id")=="runtime-sim-k8s" and str(index.get("path","")).startswith("cells/runtime-sim-k8s/")
    paasapp=repo/index["path"]/ "paasapp.yaml"
    if paasapp.exists():
        manifest=json.loads(paasapp.read_text())
        runtime_sim_hostname=str(manifest.get("spec",{}).get("route",{}).get("generatedHostname","")).endswith(".sim.runtime.internal")
checks={
  "health": health.get("status")=="ok",
  "tenant-derived": create.get("application",{}).get("TenantID")=="tenant-smoke",
  "gitops-committed": deploy.get("phase")=="GIT_COMMITTED",
  "runtime-sim-cell": runtime_sim_cell,
  "runtime-sim-hostname": runtime_sim_hostname,
  "status-readable": status.get("deployment_id")==deploy.get("deployment_id"),
  "cross-tenant-denied": wrong_code=="403",
}
lines=[f"{k}: {'PASS' if v else 'FAIL'}" for k,v in checks.items()]
lines.append("RESULT: "+("PASS" if all(checks.values()) else "FAIL"))
lines += [f"application_id={create['application']['ID']}",f"environment_id={create['default_environment']['ID']}",f"deployment_id={deploy['deployment_id']}"]
pathlib.Path(result_path).write_text("\n".join(lines)+"\n")
print("\n".join(lines))
raise SystemExit(0 if all(checks.values()) else 1)
PY
