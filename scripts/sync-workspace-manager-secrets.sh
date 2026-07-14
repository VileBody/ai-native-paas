#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
namespace="ai-native-paas-system"
secret="workspace-manager-secrets"

: "${TWC_TOKEN:?TWC_TOKEN is required}"
: "${TF_HTTP_USERNAME:?TF_HTTP_USERNAME is required for encrypted remote state}"
: "${TF_HTTP_PASSWORD:?TF_HTTP_PASSWORD is required for encrypted remote state}"
: "${TF_VAR_state_passphrase:?TF_VAR_state_passphrase is required for encrypted OpenTofu outputs}"

log_access_key="$(tofu -chdir="${repo_root}/infra/stacks/admin" output -raw workspace_log_s3_access_key)"
log_secret_key="$(tofu -chdir="${repo_root}/infra/stacks/admin" output -raw workspace_log_s3_secret_key)"
if [[ -z "${log_access_key}" || -z "${log_secret_key}" ]]; then
  echo "workspace log S3 credentials are unavailable" >&2
  exit 1
fi

encryption_key="$(KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" get secret "${secret}" -o jsonpath='{.data.WORKSPACE_LOG_ENCRYPTION_KEY}' 2>/dev/null || true)"
if [[ -z "${encryption_key}" ]]; then
  encryption_key="$(openssl rand 32 | base64 | tr -d '\r\n')"
fi

manifest="$(jq -nc \
  --arg namespace "${namespace}" \
  --arg token "$(printf '%s' "${TWC_TOKEN}" | base64 | tr -d '\r\n')" \
  --arg access "$(printf '%s' "${log_access_key}" | base64 | tr -d '\r\n')" \
  --arg secret_key "$(printf '%s' "${log_secret_key}" | base64 | tr -d '\r\n')" \
  --arg encryption "${encryption_key}" \
  '{apiVersion:"v1",kind:"Secret",metadata:{name:"workspace-manager-secrets",namespace:$namespace,labels:{"app.kubernetes.io/name":"workspace-manager"}},type:"Opaque",data:{TIMEWEB_TOKEN:$token,WORKSPACE_LOG_S3_ACCESS_KEY:$access,WORKSPACE_LOG_S3_SECRET_KEY:$secret_key,WORKSPACE_LOG_ENCRYPTION_KEY:$encryption}}')"

printf '%s' "${manifest}" \
  | KUBECONFIG="${kubeconfig}" kubectl apply --server-side --field-manager=ai-native-paas-secret-sync -f - >/dev/null

unset manifest log_access_key log_secret_key encryption_key
printf 'workspace-manager Timeweb and encrypted-log credentials synchronized\n'
