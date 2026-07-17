#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
namespace="ai-native-paas-system"
secret="workspace-manager-secrets"
temporary_dir="$(mktemp -d -t workspace-manager-secrets.XXXXXX)"
trap 'rm -rf "${temporary_dir}"' EXIT

: "${TWC_TOKEN:?TWC_TOKEN is required}"
: "${WORKSPACE_LOG_S3_ACCESS_KEY_FILE:?WORKSPACE_LOG_S3_ACCESS_KEY_FILE is required}"
: "${WORKSPACE_LOG_S3_SECRET_KEY_FILE:?WORKSPACE_LOG_S3_SECRET_KEY_FILE is required}"

for source in "${WORKSPACE_LOG_S3_ACCESS_KEY_FILE}" "${WORKSPACE_LOG_S3_SECRET_KEY_FILE}"; do
  if [[ ! -f "${source}" || -L "${source}" || ! -s "${source}" ]]; then
    echo "workspace-log S3 credential files must be nonempty regular files" >&2
    exit 1
  fi
done
tr -d '\r\n' <"${WORKSPACE_LOG_S3_ACCESS_KEY_FILE}" >"${temporary_dir}/log-s3-access-key"
tr -d '\r\n' <"${WORKSPACE_LOG_S3_SECRET_KEY_FILE}" >"${temporary_dir}/log-s3-secret-key"
printf '%s' "${TWC_TOKEN}" >"${temporary_dir}/timeweb-token"
chmod 600 "${temporary_dir}/timeweb-token" "${temporary_dir}/log-s3-access-key" "${temporary_dir}/log-s3-secret-key"

encryption_key="$(KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" get secret "${secret}" -o jsonpath='{.data.WORKSPACE_LOG_ENCRYPTION_KEY}' 2>/dev/null || true)"
if [[ -n "${encryption_key}" ]]; then
  printf '%s' "${encryption_key}" | base64 -D >"${temporary_dir}/log-encryption-key"
else
  openssl rand 32 >"${temporary_dir}/log-encryption-key"
fi
chmod 600 "${temporary_dir}/log-encryption-key"

KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" create secret generic "${secret}" \
  --from-file=TIMEWEB_TOKEN="${temporary_dir}/timeweb-token" \
  --from-file=WORKSPACE_LOG_S3_ACCESS_KEY="${temporary_dir}/log-s3-access-key" \
  --from-file=WORKSPACE_LOG_S3_SECRET_KEY="${temporary_dir}/log-s3-secret-key" \
  --from-file=WORKSPACE_LOG_ENCRYPTION_KEY="${temporary_dir}/log-encryption-key" \
  --dry-run=client -o yaml \
  | KUBECONFIG="${kubeconfig}" kubectl apply --server-side --field-manager=ai-native-paas-secret-sync -f - >/dev/null

unset encryption_key
printf 'workspace-manager Timeweb and encrypted-log credentials synchronized\n'
