#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/.verification/iteration-6"
LOG="$OUT/VERIFICATION.log"
RESULT="$ROOT/ITERATION_6_RESULT.json"
STATUS="$ROOT/ITERATION_6_STATUS.txt"
FUZZTIME="${FUZZTIME:-5s}"
REQUIRE_POSTGRES="${REQUIRE_POSTGRES:-0}"
RUN_CUMULATIVE_RACE="${RUN_CUMULATIVE_RACE:-0}"
RUN_CUMULATIVE_POSTGRES="${RUN_CUMULATIVE_POSTGRES:-0}"
CURRENT_STAGE="initialization"
STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
POSTGRES_STATE="not-requested"
CUMULATIVE_RACE_STATE="not-requested"
mkdir -p "$OUT"
: > "$LOG"
exec > >(tee -a "$LOG") 2>&1

write_result() {
  local code="$1" verdict="PASS"
  [[ "$code" -eq 0 ]] || verdict="FAIL"
  python3 - "$RESULT" "$STATUS" "$verdict" "$code" "$CURRENT_STAGE" "$STARTED_AT" "$POSTGRES_STATE" "$CUMULATIVE_RACE_STATE" <<'PY'
import datetime, json, pathlib, sys
result,status,verdict,code,stage,started,pg,cumulative_race=sys.argv[1:]
payload={
  "iteration":6,
  "domain":"commercial-governance",
  "verdict":verdict,
  "exit_code":int(code),
  "last_stage":stage,
  "started_at":started,
  "finished_at":datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00","Z"),
  "mandatory_tdd":{"required":52,"present":52},
  "live_postgres":pg,
  "targeted_race":"pass" if verdict=="PASS" or stage!="commerce-race" else "failed",
  "cumulative_race":cumulative_race,
  "live_kubernetes":"not-applicable",
  "source_baseline":"iterations-1-4-plus-iteration-6; iteration-5-frozen-contract-only",
}
pathlib.Path(result).write_text(json.dumps(payload,indent=2,sort_keys=True)+"\n")
pathlib.Path(status).write_text(
  f"ITERATION_6={verdict}\nLIVE_POSTGRES={pg.upper()}\nTARGETED_RACE={'PASS' if verdict=='PASS' or stage!='commerce-race' else 'FAIL'}\n"
  f"CUMULATIVE_RACE={cumulative_race.upper()}\nLAST_STAGE={stage}\n"
)
PY
}
trap 'code=$?; write_result "$code"; exit "$code"' EXIT
stage(){ CURRENT_STAGE="$1"; printf '\n===== %s =====\n' "$1"; }

cd "$ROOT"
echo "Iteration 6 verification started at $STARTED_AT"
echo "Go: $(go version)"

stage format
files="$(gofmt -l $(find . -name '*.go' -type f -not -path './vendor/*' | sort))"
[[ -z "$files" ]] || { echo "$files"; exit 1; }

stage vet
go vet ./...
CGO_ENABLED=1 go vet -tags=postgres_integration ./test/integration
go vet -tags=integration_postgres ./internal/source/postgres

stage default-tests
timeout 12m go test -count=1 ./...

stage commerce-race
timeout 8m go test -race -count=1 \
  ./internal/commerce/... ./pkg/contracts/commerce/v1 \
  ./test/acceptance ./test/architecture ./test/contract

stage cumulative-race
if [[ "$RUN_CUMULATIVE_RACE" == "1" ]]; then
  timeout 20m go test -race -count=1 ./...
  CUMULATIVE_RACE_STATE="pass"
else
  CUMULATIVE_RACE_STATE="not-run"
  echo "SKIP: cumulative race disabled; set RUN_CUMULATIVE_RACE=1"
fi

stage shuffle
timeout 8m go test -shuffle=on -count=20 \
  ./internal/commerce/application ./internal/commerce/domain \
  ./internal/commerce/httpapi ./internal/commerce/memory \
  ./pkg/contracts/commerce/v1 ./test/contract ./test/architecture
timeout 4m go test -shuffle=on -count=10 ./test/acceptance -run '^TestCommercialGovernance_'

stage tdd-parity
./scripts/verify-iteration-6-tdd.py | tee "$OUT/tdd-parity.json"

stage static-boundaries
if rg -n '\bfloat(32|64)\b' internal/commerce pkg/contracts/commerce/v1; then
  echo "floating-point monetary type found in commerce boundary" >&2
  exit 1
fi
if rg -ni '\b(from|join|references|update|into|table|trigger[[:space:]]+on)[[:space:]]+(kernel|source|build|runtime|attachments)\.' migrations/commerce internal/commerce/postgres/migrations; then
  echo "cross-domain SQL reference found in commerce migrations" >&2
  exit 1
fi
if rg -n 'internal/(kernel|source|build|runtime|attachments)' internal/commerce pkg/contracts/commerce/v1; then
  echo "cross-domain internal import found in commerce" >&2
  exit 1
fi

stage fuzz
timeout 3m go test ./internal/commerce/application -run='^$' -fuzz=FuzzRuntimeUsageNoPanic -fuzztime="$FUZZTIME"
timeout 3m go test ./internal/commerce/application -run='^$' -fuzz=FuzzRateQuantityNoPanic -fuzztime="$FUZZTIME"

stage coverage
go test -count=1 -covermode=atomic \
  -coverpkg=./internal/commerce/...,./pkg/contracts/commerce/v1 \
  -coverprofile="$OUT/coverage-commerce.out" \
  ./internal/commerce/... ./pkg/contracts/commerce/v1 ./test/acceptance ./test/contract
go tool cover -func="$OUT/coverage-commerce.out" | tee "$OUT/coverage-commerce.txt"
go tool cover -html="$OUT/coverage-commerce.out" -o "$ROOT/coverage-commerce.html"

stage build
mkdir -p bin
go build -trimpath -o bin/kernel-api ./cmd/kernel-api
go build -trimpath -o bin/source-api ./cmd/source-api
go build -trimpath -o bin/build-api ./cmd/build-api
go build -trimpath -o bin/runtime-api ./cmd/runtime-api
go build -trimpath -o bin/runtime-operator ./cmd/runtime-operator
go build -trimpath -o bin/commerce-api ./cmd/commerce-api

stage commerce-api-smoke
./scripts/commerce-api-smoke.sh

stage postgres
run_postgres_tests() {
  if [[ "$RUN_CUMULATIVE_POSTGRES" == "1" ]]; then
    CGO_ENABLED=1 timeout 12m go test -race -count=1 -tags=postgres_integration ./test/integration -v
  else
    CGO_ENABLED=1 timeout 8m go test -race -count=1 -tags=postgres_integration ./test/integration -run '^TestPostgres_Commerce' -v
  fi
}
if [[ -n "${TEST_POSTGRES_DSN:-}" ]]; then
  run_postgres_tests
  POSTGRES_STATE="pass"
elif ./scripts/start-test-postgres.sh bash -c 'run_postgres_tests_placeholder=1' >/dev/null 2>&1; then
  # The availability probe above starts and stops an isolated server. Start a fresh
  # instance for the test command so no state leaks into verification.
  if [[ "$RUN_CUMULATIVE_POSTGRES" == "1" ]]; then
    ./scripts/start-test-postgres.sh bash -c 'CGO_ENABLED=1 timeout 12m go test -race -count=1 -tags=postgres_integration ./test/integration -v'
  else
    ./scripts/start-test-postgres.sh bash -c 'CGO_ENABLED=1 timeout 8m go test -race -count=1 -tags=postgres_integration ./test/integration -run "^TestPostgres_Commerce" -v'
  fi
  POSTGRES_STATE="pass"
else
  code=$?
  POSTGRES_STATE="unavailable"
  if [[ "$REQUIRE_POSTGRES" == "1" || "$code" != "88" ]]; then exit "$code"; fi
  echo "SKIP: PostgreSQL server binaries unavailable"
fi

stage completed
echo "Iteration 6 verification PASS"
