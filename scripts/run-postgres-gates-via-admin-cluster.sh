#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
namespace="${PAAS_ADMIN_NAMESPACE:-ai-native-paas-system}"
secret_name="${PAAS_POSTGRES_SECRET:-control-plane-postgres}"
runner_name="${PAAS_POSTGRES_RUNNER_NAME:-postgres-test-runner}"
test_database="${PAAS_POSTGRES_TEST_DATABASE:-ai_native_paas_integration_test}"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
runner_image="${PAAS_POSTGRES_RUNNER_IMAGE:-ai-native-paas-registry.registry.twcstorage.ru/mirror/postgres@sha256:0a8a1e76503c091f0feb387d51b10fcd746c2d61cf6cdd6e8356973a45e40a0f}"

export KUBECONFIG="${kubeconfig}"

if [[ ! "${test_database}" =~ ^[a-z0-9_]+_test$ ]]; then
  echo "PAAS_POSTGRES_TEST_DATABASE must be a lowercase *_test database name" >&2
  exit 1
fi

decode_secret() {
  kubectl -n "${namespace}" get secret "${secret_name}" \
    -o "jsonpath={.data.$1}" | base64 -d
}

integration_bin="$(mktemp)"
pivot_bin="$(mktemp)"

cleanup() {
  kubectl -n "${namespace}" delete pod "${runner_name}" \
    --ignore-not-found --wait=false >/dev/null 2>&1 || true
  rm -f "${integration_bin}" "${pivot_bin}"
}
trap cleanup EXIT

cd "${repo_root}"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -trimpath \
  -tags=postgres_integration -o "${integration_bin}" ./test/integration
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -trimpath \
  -tags=postgres_integration -o "${pivot_bin}" ./test/pivot

kubectl -n "${namespace}" delete pod "${runner_name}" \
  --ignore-not-found --wait=true >/dev/null
kubectl -n "${namespace}" run "${runner_name}" \
  --image="${runner_image}" \
  --restart=Never \
  --overrides='{"spec":{"imagePullSecrets":[{"name":"craas-ai-native-paas-registry"}]}}' \
  --command -- sleep 3600 >/dev/null
kubectl -n "${namespace}" wait --for=condition=Ready \
  "pod/${runner_name}" --timeout=180s >/dev/null
kubectl -n "${namespace}" cp "${integration_bin}" \
  "${runner_name}:/tmp/postgres-integration.test"
kubectl -n "${namespace}" cp "${pivot_bin}" \
  "${runner_name}:/tmp/postgres-pivot.test"

export PGUSER_RUNNER="$(decode_secret PGUSER)"
export PGPASSWORD_RUNNER="$(decode_secret PGPASSWORD)"
export PGHOST_RUNNER="$(decode_secret PGHOST)"
export PGPORT_RUNNER="$(decode_secret PGPORT)"
production_database="$(decode_secret PGDATABASE)"
if [[ "${test_database}" == "${production_database}" ]]; then
  echo "refusing to run destructive integration tests against the production database" >&2
  exit 1
fi
export PGDATABASE_RUNNER="${test_database}"
export PGSSLMODE_RUNNER="$(decode_secret PGSSLMODE)"

database_exists="$(kubectl -n "${namespace}" exec "${runner_name}" -- \
  env PGPASSWORD="${PGPASSWORD_RUNNER}" PGSSLMODE="${PGSSLMODE_RUNNER}" \
  psql -h "${PGHOST_RUNNER}" -p "${PGPORT_RUNNER}" -U "${PGUSER_RUNNER}" \
  -d "${production_database}" -Atc "SELECT 1 FROM pg_database WHERE datname='${test_database}'")"
if [[ "${database_exists}" != "1" ]]; then
  echo "integration test database is missing; provision it through infra/stacks/admin" >&2
  exit 1
fi
export TEST_POSTGRES_DSN
TEST_POSTGRES_DSN="$(python3 - <<'PY'
import os
import urllib.parse

user = urllib.parse.quote(os.environ["PGUSER_RUNNER"], safe="")
password = urllib.parse.quote(os.environ["PGPASSWORD_RUNNER"], safe="")
host = os.environ["PGHOST_RUNNER"]
port = os.environ["PGPORT_RUNNER"]
database = urllib.parse.quote(os.environ["PGDATABASE_RUNNER"], safe="")
sslmode = urllib.parse.quote(os.environ["PGSSLMODE_RUNNER"], safe="")
print(f"postgres://{user}:{password}@{host}:{port}/{database}?sslmode={sslmode}")
PY
)"

integration_flags=(-test.count=1 -test.timeout=15m)
if [[ -n "${PAAS_POSTGRES_INTEGRATION_RUN:-}" ]]; then
  integration_flags+=(-test.run "${PAAS_POSTGRES_INTEGRATION_RUN}")
fi
kubectl -n "${namespace}" exec "${runner_name}" -- \
  env TEST_POSTGRES_DSN="${TEST_POSTGRES_DSN}" \
  /tmp/postgres-integration.test "${integration_flags[@]}"
kubectl -n "${namespace}" exec "${runner_name}" -- \
  env TEST_POSTGRES_DSN="${TEST_POSTGRES_DSN}" \
  /tmp/postgres-pivot.test -test.count=1 -test.timeout=5m \
  -test.run '^TestKernel_OperationCheckpointSurvivesDatabaseFailover$'

echo 'POSTGRES_GATES=PASS integration=green k14=green'
