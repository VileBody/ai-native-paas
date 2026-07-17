#!/usr/bin/env bash
set -Eeuo pipefail

# Switch the state-service S3 user without materializing credentials in shell
# arguments, Terraform output, Git, or command logs.  The caller supplies
# private file paths created from the dedicated Timeweb S3 user.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
namespace="ai-native-paas-system"
secret="state-service-secrets"
temporary_dir="$(mktemp -d -t state-service-s3.XXXXXX)"
trap 'rm -rf "${temporary_dir}"' EXIT

: "${STATE_S3_ACCESS_KEY_FILE:?STATE_S3_ACCESS_KEY_FILE is required}"
: "${STATE_S3_SECRET_KEY_FILE:?STATE_S3_SECRET_KEY_FILE is required}"

for source in "${STATE_S3_ACCESS_KEY_FILE}" "${STATE_S3_SECRET_KEY_FILE}"; do
  if [[ ! -f "${source}" || -L "${source}" || ! -s "${source}" ]]; then
    echo "state S3 credential files must be nonempty regular files" >&2
    exit 1
  fi
done

tr -d '\r\n' <"${STATE_S3_ACCESS_KEY_FILE}" >"${temporary_dir}/access-key"
tr -d '\r\n' <"${STATE_S3_SECRET_KEY_FILE}" >"${temporary_dir}/secret-key"
test -s "${temporary_dir}/access-key"
test -s "${temporary_dir}/secret-key"
chmod 600 "${temporary_dir}/access-key" "${temporary_dir}/secret-key"

patch="${temporary_dir}/patch.json"
jq -n \
  --rawfile access_key "${temporary_dir}/access-key" \
  --rawfile secret_key "${temporary_dir}/secret-key" \
  '{data:{STATE_S3_ACCESS_KEY:($access_key | @base64),STATE_S3_SECRET_KEY:($secret_key | @base64)}}' \
  >"${patch}"
chmod 600 "${patch}"

KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" get secret "${secret}" >/dev/null
KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" patch secret "${secret}" \
  --type merge --patch-file "${patch}" >/dev/null
KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" rollout restart deployment/state-service >/dev/null
KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" rollout status deployment/state-service --timeout=180s >/dev/null

printf 'state-service S3 credentials synchronized and deployment restarted\n'
