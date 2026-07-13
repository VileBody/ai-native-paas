#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/.verification/iteration-3"
LOG="$OUT/VERIFICATION.log"
RESULT="$ROOT/ITERATION_3_RESULT.json"
STATUS="$ROOT/ITERATION_3_STATUS.txt"
FUZZTIME="${FUZZTIME:-5s}"
CURRENT_STAGE="initialization"
SMOKE_PID=""
STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
mkdir -p "$OUT"
: > "$LOG"
exec > >(tee -a "$LOG") 2>&1

write_result() {
  local exit_code="$1"
  local verdict="PASS"
  if [[ "$exit_code" -ne 0 ]]; then verdict="FAIL"; fi
  python3 - "$RESULT" "$STATUS" "$verdict" "$exit_code" "$CURRENT_STAGE" "$STARTED_AT" <<'PY'
import json, pathlib, sys
result, status, verdict, exit_code, stage, started = sys.argv[1:]
payload = {
    "iteration": 3,
    "domain": "build-and-artifact",
    "verdict": verdict,
    "exit_code": int(exit_code),
    "last_stage": stage,
    "started_at": started,
    "finished_at": __import__("datetime").datetime.now(__import__("datetime").timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
    "live_postgres_requested": bool(__import__("os").environ.get("TEST_POSTGRES_DSN")),
}
pathlib.Path(result).write_text(json.dumps(payload, indent=2) + "\n")
pathlib.Path(status).write_text(f"ITERATION_3={verdict}\nEXIT_CODE={exit_code}\nLAST_STAGE={stage}\n")
PY
}
on_exit() {
  local code=$?
  if [[ -n "$SMOKE_PID" ]]; then
    kill "$SMOKE_PID" 2>/dev/null || true
    wait "$SMOKE_PID" 2>/dev/null || true
  fi
  write_result "$code"
  exit "$code"
}
trap on_exit EXIT

stage() {
  CURRENT_STAGE="$1"
  printf '\n===== %s =====\n' "$CURRENT_STAGE"
}

cd "$ROOT"
echo "Iteration 3 verification started at $STARTED_AT"
echo "Go: $(go version)"

stage "format"
files="$(gofmt -l $(find . -name '*.go' -type f -not -path './vendor/*' | sort))"
if [[ -n "$files" ]]; then
  echo "Unformatted Go files:"
  echo "$files"
  exit 1
fi

stage "vet"
go vet ./...
CGO_ENABLED=1 go vet -tags=postgres_integration ./test/integration
go vet -tags=integration_postgres ./internal/source/postgres

stage "default-tests"
go test -count=1 ./...

stage "race-build"
go test -race -count=1 ./internal/build/... ./pkg/contracts/build/v1 ./test/acceptance ./test/architecture

stage "race-kernel-source"
go test -race -count=1 ./internal/kernel/... ./adapters/... ./contracts/... ./internal/source/... ./pkg/contracts/source/v1 ./test/contract

stage "shuffle-fast"
go test -shuffle=on -count=20 \
  ./internal/build/application \
  ./internal/build/cache \
  ./internal/build/detect \
  ./internal/build/dockerfilevm \
  ./internal/build/domain \
  ./internal/build/httpapi \
  ./internal/build/kpack \
  ./internal/build/logs \
  ./internal/build/memory \
  ./internal/build/postgres \
  ./internal/build/sbom \
  ./internal/build/scanner \
  ./internal/build/signer \
  ./pkg/contracts/build/v1 \
  ./test/architecture

stage "shuffle-real-fixtures"
go test -shuffle=on -count=3 \
  ./internal/build/localoci \
  ./internal/build/registry \
  ./internal/build/source \
  ./test/acceptance

stage "fuzz-kernel"
go test ./internal/kernel -run='^$' -fuzz=FuzzOperationTransitionNeverMutatesOnRejectedTransition -fuzztime="$FUZZTIME"

stage "fuzz-source"
go test ./internal/source/workspace -run='^$' -fuzz=FuzzPatchPathCannotEscape -fuzztime="$FUZZTIME"

stage "fuzz-build"
go test ./internal/build/domain -run='^$' -fuzz=FuzzBuildConfig_PathNormalizationCannotEscape -fuzztime="$FUZZTIME"

stage "tdd-name-parity"
python3 - <<'PY'
import pathlib, re, sys
spec = pathlib.Path("docs/tdd/03-build-and-artifact.md").read_text()
expected = re.findall(r"(?m)^Test[A-Za-z0-9_]+$", spec)
actual = set()
for path in pathlib.Path(".").rglob("*_test.go"):
    actual.update(re.findall(r"(?m)^func (Test[A-Za-z0-9_]+)\s*\(", path.read_text(errors="ignore")))
missing = [name for name in expected if name not in actual]
if missing:
    print("Missing TDD tests:", *missing, sep="\n")
    sys.exit(1)
print(f"TDD names present: {len(expected)}/{len(expected)}")
PY

stage "production-secret-literal-scan"
if grep -R -n -E 'build-secret-must-not-leak|runtime-secret-must-never-enter-build|runtime-super-secret|ultra-secret|top-secret|host-secret' \
  internal/build cmd/build-api pkg/contracts/build migrations/build \
  --include='*.go' --include='*.sql' --exclude='*_test.go'; then
  echo "Fixture secret literal found in production files"
  exit 1
fi

stage "coverage"
go test -count=1 -covermode=atomic -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | tail -1
go tool cover -html=coverage.out -o coverage.html

stage "binary-build"
make build

stage "build-api-smoke"
port="$(python3 - <<'PY'
import socket
s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()
PY
)"
smoke_log="$OUT/build-api-smoke.log"
LISTEN_ADDR="127.0.0.1:$port" ./bin/build-api >"$smoke_log" 2>&1 &
SMOKE_PID=$!
ready=0
for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:$port/healthz" > "$OUT/build-api-health.json"; then ready=1; break; fi
  if ! kill -0 "$SMOKE_PID" 2>/dev/null; then break; fi
  sleep 0.1
done
if [[ "$ready" -ne 1 ]]; then
  cat "$smoke_log"
  exit 1
fi
grep -q '"status":"ok"' "$OUT/build-api-health.json"
kill "$SMOKE_PID" 2>/dev/null || true
wait "$SMOKE_PID" 2>/dev/null || true
SMOKE_PID=""

stage "live-postgres"
if [[ -n "${TEST_POSTGRES_DSN:-}" ]]; then
  CGO_ENABLED=1 go test -race -count=1 -tags=postgres_integration ./test/integration -v
  go test -race -count=1 -tags=integration_postgres ./internal/source/postgres -v
else
  echo "SKIP: TEST_POSTGRES_DSN is not set"
fi

stage "completed"
echo "Iteration 3 verification PASS"
