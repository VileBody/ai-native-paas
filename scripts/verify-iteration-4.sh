#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/.verification/iteration-4"
LOG="$OUT/VERIFICATION.log"
RESULT="$ROOT/ITERATION_4_RESULT.json"
STATUS="$ROOT/ITERATION_4_STATUS.txt"
FUZZTIME="${FUZZTIME:-3s}"
REQUIRE_POSTGRES="${REQUIRE_POSTGRES:-0}"
CURRENT_STAGE="initialization"
STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
POSTGRES_STATE="not-requested"
mkdir -p "$OUT"
: > "$LOG"
exec > >(tee -a "$LOG") 2>&1

write_result() {
  local code="$1" verdict="PASS"
  [[ "$code" -eq 0 ]] || verdict="FAIL"
  python3 - "$RESULT" "$STATUS" "$verdict" "$code" "$CURRENT_STAGE" "$STARTED_AT" "$POSTGRES_STATE" <<'PY'
import datetime, json, pathlib, sys
result,status,verdict,code,stage,started,pg=sys.argv[1:]
payload={
  "iteration":4,
  "domain":"runtime-delivery",
  "verdict":verdict,
  "scope":"without-live-kubernetes",
  "exit_code":int(code),
  "last_stage":stage,
  "started_at":started,
  "finished_at":datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00","Z"),
  "live_postgres":pg,
  "live_kubernetes":"not-run",
}
pathlib.Path(result).write_text(json.dumps(payload,indent=2,sort_keys=True)+"\n")
pathlib.Path(status).write_text(f"ITERATION_4={verdict}\nSCOPE=WITHOUT_LIVE_KUBERNETES\nLIVE_POSTGRES={pg}\nLIVE_KUBERNETES=NOT_RUN\nLAST_STAGE={stage}\n")
PY
}
trap 'code=$?; write_result "$code"; exit "$code"' EXIT
stage(){ CURRENT_STAGE="$1"; printf '\n===== %s =====\n' "$1"; }

cd "$ROOT"
echo "Iteration 4 verification started at $STARTED_AT"
echo "Go: $(go version)"

stage format
files="$(gofmt -l $(find . -name '*.go' -type f -not -path './vendor/*' | sort))"
[[ -z "$files" ]] || { echo "$files"; exit 1; }

stage vet
go vet ./...
CGO_ENABLED=1 go vet -tags=postgres_integration ./test/integration
go vet -tags=integration_postgres ./internal/source/postgres

stage default-tests
timeout 8m go test -count=1 ./...

stage iteration4-race
timeout 5m go test -race -count=1 ./internal/runtime/... ./pkg/contracts/runtime/v1 ./test/contract ./test/architecture
timeout 2m go test -race -count=1 ./test/acceptance -run '^TestRuntimeDelivery_'

stage shuffle
timeout 5m go test -shuffle=on -count=10 ./internal/runtime/application ./internal/runtime/domain ./internal/runtime/httpapi ./internal/runtime/kubeapi ./internal/runtime/operator ./pkg/contracts/runtime/v1 ./test/contract ./test/architecture
timeout 3m go test -shuffle=on -count=3 ./internal/runtime/gitops
timeout 3m go test -shuffle=on -count=5 ./test/acceptance -run '^TestRuntimeDelivery_'

stage tdd-parity
./scripts/verify-iteration-4-tdd.py --matrix docs/iteration-4/TDD_MATRIX.md --json "$OUT/tdd-parity.json"

stage manifests
go test -count=1 ./test/contract -run 'Test(Argo|Delete_Argo|PaaSAppCRD|RuntimeOperatorRBAC)' -v

stage fuzz
timeout 2m go test ./pkg/contracts/runtime/v1 -run='^$' -fuzz=FuzzPaaSAppValidateNeverPanics -fuzztime="$FUZZTIME"
timeout 2m go test ./internal/runtime/gitops -run='^$' -fuzz=FuzzGitOpsPathValidationCannotEscape -fuzztime="$FUZZTIME"
timeout 2m go test ./internal/runtime/domain -run='^$' -fuzz=FuzzRuntimeObjectNamesRemainDNSBounded -fuzztime="$FUZZTIME"

stage build
mkdir -p bin
go build -trimpath -o bin/kernel-api ./cmd/kernel-api
go build -trimpath -o bin/source-api ./cmd/source-api
go build -trimpath -o bin/build-api ./cmd/build-api
go build -trimpath -o bin/runtime-api ./cmd/runtime-api
go build -trimpath -o bin/runtime-operator ./cmd/runtime-operator

stage runtime-api-smoke
./scripts/runtime-api-smoke.sh "$OUT/runtime-api-smoke"

stage postgres
if [[ -n "${TEST_POSTGRES_DSN:-}" ]]; then
  POSTGRES_STATE="pass"
  CGO_ENABLED=1 timeout 5m go test -race -count=1 -tags=postgres_integration ./test/integration -run '^TestPostgres_Runtime' -v
elif ./scripts/start-test-postgres.sh bash -c 'CGO_ENABLED=1 go test -race -count=1 -tags=postgres_integration ./test/integration -run "^TestPostgres_Runtime" -v'; then
  POSTGRES_STATE="pass"
else
  code=$?
  POSTGRES_STATE="unavailable"
  if [[ "$REQUIRE_POSTGRES" == "1" || "$code" != "88" ]]; then exit "$code"; fi
  echo "SKIP: PostgreSQL server binaries unavailable"
fi

stage completed
echo "Iteration 4 verification PASS within no-live-Kubernetes boundary"
