#!/usr/bin/env bash
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT="$ROOT/.verification/iteration-2"
rm -rf "$OUT"; mkdir -p "$OUT"
summary="$OUT/status.tsv"
: > "$summary"
run_step() {
  local name=$1; shift
  local log="$OUT/${name}.log"
  local start end rc
  start=$(date +%s)
  (cd "$ROOT" && "$@") >"$log" 2>&1
  rc=$?
  end=$(date +%s)
  if [[ $rc -eq 0 ]]; then printf '%s\tPASS\t%s\n' "$name" "$((end-start))" >> "$summary"; else printf '%s\tFAIL(%s)\t%s\n' "$name" "$rc" "$((end-start))" >> "$summary"; fi
  return $rc
}
mandatory_failed=0
run_step gofmt bash -lc 'files=$(gofmt -l pkg/contracts/source/v1 internal/source cmd/source-api test/architecture/domain_boundaries_test.go test/acceptance/source_control_acceptance_test.go); [[ -z "$files" ]] || { echo "$files"; exit 1; }' || mandatory_failed=1
run_step vet go vet ./... || mandatory_failed=1
run_step test-default bash -lc 'go test -json -count=1 ./... | tee .verification/iteration-2/test-default.json' || mandatory_failed=1
run_step test-race go test -race -count=1 ./... || mandatory_failed=1
run_step test-shuffle go test -shuffle=on -count=20 ./internal/source/... ./pkg/contracts/source/... ./test/... || mandatory_failed=1
run_step fuzz-workspace go test -run='^$' -fuzz=FuzzPatchPathCannotEscape -fuzztime=15s ./internal/source/workspace || mandatory_failed=1
run_step coverage go test -count=1 -covermode=atomic -coverprofile=.verification/iteration-2/coverage.out ./... || mandatory_failed=1
if [[ -f "$OUT/coverage.out" ]]; then (cd "$ROOT" && go tool cover -func="$OUT/coverage.out") > "$OUT/coverage.txt" 2>&1; (cd "$ROOT" && go tool cover -html="$OUT/coverage.out" -o "$OUT/coverage.html") >> "$OUT/coverage-html.log" 2>&1; fi
run_step build go build -trimpath -o .verification/iteration-2/source-api ./cmd/source-api || mandatory_failed=1
run_step api-smoke bash -lc 'LISTEN_ADDR=127.0.0.1:18081 .verification/iteration-2/source-api >.verification/iteration-2/source-api-process.log 2>&1 & pid=$!; trap "kill $pid 2>/dev/null || true" EXIT; for i in $(seq 1 50); do curl -fsS http://127.0.0.1:18081/healthz >.verification/iteration-2/health.json && break; sleep .1; done; grep -q '"status":"ok"' .verification/iteration-2/health.json' || mandatory_failed=1
# Detect every PostgreSQL-related build tag already present in the repository, including Iteration 1.
tags=$(grep -RhoE '^//go:build [^[:space:]]*postgres[^[:space:]]*' --include='*_test.go' . 2>/dev/null | sed 's#^//go:build ##' | tr '|' '\n' | tr '&' '\n' | tr -d '()!' | xargs -n1 | grep postgres | sort -u | paste -sd, -)
[[ -n "$tags" ]] || tags=integration_postgres
if command -v pg_config >/dev/null 2>&1 || find /usr/lib/postgresql -name initdb -type f -print -quit 2>/dev/null | grep -q .; then
  run_step postgres-live ./scripts/start-test-postgres.sh bash -lc "go vet -tags='$tags' ./... && go test -race -count=1 -tags='$tags' ./..." || mandatory_failed=1
else
  printf '%s\tSKIP(no server binaries)\t0\n' postgres-live >> "$summary"
  touch "$OUT/postgres-not-run"
fi
run_step production-secret-scan bash -lc '! grep -RInE "super-secret-token|ephemeral-only|admin-secret|do-not-persist" --include="*.go" --exclude="*_test.go" internal/source cmd/source-api' || mandatory_failed=1
python3 "$ROOT/scripts/write-iteration-2-report.py" "$ROOT" "$OUT" "$mandatory_failed"
if [[ $mandatory_failed -eq 0 ]]; then echo PASS > "$OUT/FINAL_STATUS"; exit 0; else echo FAIL > "$OUT/FINAL_STATUS"; exit 1; fi
