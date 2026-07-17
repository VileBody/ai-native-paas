#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
namespace="ai-native-paas-system"
secret="state-service-secrets"
ttl_seconds="${STATE_CREDENTIAL_TTL_SECONDS:-7200}"

if ! [[ "${ttl_seconds}" =~ ^[0-9]+$ ]] || (( ttl_seconds < 900 || ttl_seconds > 14400 )); then
  echo "STATE_CREDENTIAL_TTL_SECONDS must be between 900 and 14400" >&2
  exit 1
fi

expires_at="$(date -u -r "$(( $(date +%s) + ttl_seconds ))" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d "+${ttl_seconds} seconds" +%Y-%m-%dT%H:%M:%SZ)"
admin_password="$(openssl rand -hex 32)"
network_password="$(openssl rand -hex 32)"
cozystack_password="$(openssl rand -hex 32)"
workspace_password="$(openssl rand -hex 32)"
bootstrap_password="$(openssl rand -hex 32)"

credentials="$(jq -nc \
  --arg expires_at "${expires_at}" \
  --arg admin_password "${admin_password}" \
  --arg network_password "${network_password}" \
  --arg cozystack_password "${cozystack_password}" \
  --arg workspace_password "${workspace_password}" \
  --arg bootstrap_password "${bootstrap_password}" \
  '[
    {username:"admin-migration",password:$admin_password,namespace:"admin",tenant_id:"platform",project_id:"admin",actor:"operator:rotation",expires_at:$expires_at},
    {username:"network-bootstrap",password:$network_password,namespace:"network-foundation",tenant_id:"platform",project_id:"network-foundation",actor:"operator:rotation",expires_at:$expires_at},
    {username:"cozystack-bootstrap",password:$cozystack_password,namespace:"cozystack-lab",tenant_id:"platform",project_id:"runtime-cell",actor:"operator:rotation",expires_at:$expires_at},
    {username:"workspace-bootstrap",password:$workspace_password,namespace:"workspace-images",tenant_id:"platform",project_id:"workspace-images",actor:"operator:rotation",expires_at:$expires_at},
    {username:"state-bootstrap",password:$bootstrap_password,namespace:"state-bootstrap",tenant_id:"platform",project_id:"state-bootstrap",actor:"operator:rotation",expires_at:$expires_at}
  ]')"

jq -nc --arg credentials "${credentials}" '{stringData:{STATE_CREDENTIALS_JSON:$credentials}}' \
  | KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" patch secret "${secret}" --type merge --patch-file /dev/stdin >/dev/null

umask 077
printf '%s' "${admin_password}" >"${repo_root}/.state-backend/http-password"
printf '%s' "${network_password}" >"${repo_root}/.state-backend/network-foundation-http-password"
printf '%s' "${cozystack_password}" >"${repo_root}/.state-backend/cozystack-lab-http-password"
printf '%s' "${workspace_password}" >"${repo_root}/.state-backend/workspace-images-http-password"
printf '%s' "${bootstrap_password}" >"${repo_root}/.state-backend/state-bootstrap-http-password"

KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" rollout restart deployment/state-service >/dev/null
KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" rollout status deployment/state-service --timeout=180s >/dev/null

unset credentials admin_password network_password cozystack_password workspace_password bootstrap_password
printf 'state-service scoped credentials rotated; expires_at=%s\n' "${expires_at}"
