#!/usr/bin/env bash
set -Eeuo pipefail

# Read encrypted remote state through the HTTP backend after an S3 credential
# switch.  The response body is intentionally discarded; only an HTTP status
# is emitted.  Basic-auth material lives in a 0600 temporary curl config, not
# in a command argument.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
namespace="ai-native-paas-system"
credential_namespace="${STATE_SERVICE_NAMESPACE:-admin}"
local_port="${STATE_SERVICE_LOCAL_PORT:-18081}"
temporary_dir="$(mktemp -d -t state-service-s3-read.XXXXXX)"
port_forward_pid=""
trap 'if [[ -n "${port_forward_pid}" ]]; then kill "${port_forward_pid}" 2>/dev/null || true; wait "${port_forward_pid}" 2>/dev/null || true; fi; rm -rf "${temporary_dir}"' EXIT

: "${TF_HTTP_USERNAME:?TF_HTTP_USERNAME is required}"
: "${TF_HTTP_PASSWORD:?TF_HTTP_PASSWORD is required}"

curl_config="${temporary_dir}/curl.conf"
umask 077
printf 'user = "%s:%s"\n' "${TF_HTTP_USERNAME}" "${TF_HTTP_PASSWORD}" >"${curl_config}"

KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" port-forward service/state-service "${local_port}:8080" >"${temporary_dir}/port-forward.log" 2>&1 &
port_forward_pid="$!"
for _ in $(seq 1 30); do
  if curl --silent --show-error --fail --output /dev/null "http://127.0.0.1:${local_port}/healthz"; then
    break
  fi
  sleep 1
done

status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' --config "${curl_config}" "http://127.0.0.1:${local_port}/api/v1/state/${credential_namespace}")"
if [[ "${status}" != "200" ]]; then
  echo "state-service encrypted-state read failed: HTTP ${status}" >&2
  exit 1
fi

printf 'state-service encrypted S3 state read passed for namespace %s\n' "${credential_namespace}"
