#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
namespace="${WORKSPACE_NAMESPACE:-ai-native-paas-system}"
operator_user="${OPENBAO_OPERATOR_USER:-ergin}"
keychain_service="${OPENBAO_OPERATOR_KEYCHAIN_SERVICE:-ai-native-paas-openbao-solo-dev-operator-20260715T210106Z}"
manager_name="${WORKSPACE_MANAGER_DNS_NAME:-workspace-manager.72-56-246-80.sslip.io}"
egress_name="${WORKSPACE_EGRESS_DNS_NAME:-workspace-egress.72-56-246-80.sslip.io}"
local_port="${OPENBAO_LOCAL_PORT:-18200}"
bao_host="openbao-active.openbao.svc"
bao_address="https://${bao_host}:${local_port}"

for tool in curl jq openssl security kubectl; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    if [[ -x "/opt/homebrew/bin/${tool}" ]]; then
      PATH="/opt/homebrew/bin:${PATH}"
    else
      printf '%s is required\n' "${tool}" >&2
      exit 1
    fi
  fi
done

for name in "${manager_name}" "${egress_name}"; do
  case "${name}" in
    *.72-56-246-80.sslip.io) ;;
    *)
      printf 'temporary workspace edge name is outside the reviewed sslip.io suffix: %s\n' "${name}" >&2
      exit 1
      ;;
  esac
done

temporary_directory="$(mktemp -d -t workspace-edge-tls.XXXXXX)"
port_forward_pid=""
operator_token=""
cleanup() {
  set +e
  if [[ -n "${operator_token}" ]]; then
    curl --fail --silent --show-error \
      --cacert "${temporary_directory}/openbao-ca.crt" \
      --resolve "${bao_host}:${local_port}:127.0.0.1" \
      -H "X-Vault-Token: ${operator_token}" \
      -X POST "${bao_address}/v1/auth/token/revoke-self" >/dev/null 2>&1 || true
  fi
  if [[ -n "${port_forward_pid}" ]]; then
    kill "${port_forward_pid}" >/dev/null 2>&1 || true
    wait "${port_forward_pid}" >/dev/null 2>&1 || true
  fi
  rm -rf "${temporary_directory}"
}
trap cleanup EXIT
umask 077

kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" get secret openbao-client-ca \
  -o jsonpath='{.data.ca\.crt}' | openssl base64 -d -A >"${temporary_directory}/openbao-ca.crt"

kubectl --kubeconfig "${kubeconfig}" -n openbao port-forward \
  service/openbao-active "${local_port}:8200" >"${temporary_directory}/port-forward.log" 2>&1 &
port_forward_pid=$!
for _ in $(seq 1 40); do
  if curl --fail --silent --show-error \
    --cacert "${temporary_directory}/openbao-ca.crt" \
    --resolve "${bao_host}:${local_port}:127.0.0.1" \
    "${bao_address}/v1/sys/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done
kill -0 "${port_forward_pid}" >/dev/null 2>&1 || {
  printf 'OpenBao port-forward failed\n' >&2
  exit 1
}

operator_password="$(security find-generic-password -a "${operator_user}" -s "${keychain_service}" -w)"
login_payload="$(jq -nc --arg password "${operator_password}" '{password:$password}')"
unset operator_password
operator_token="$(curl --fail --silent --show-error \
  --cacert "${temporary_directory}/openbao-ca.crt" \
  --resolve "${bao_host}:${local_port}:127.0.0.1" \
  -H 'Content-Type: application/json' \
  --data "${login_payload}" \
  "${bao_address}/v1/auth/userpass/login/${operator_user}" | jq -er '.auth.client_token')"
unset login_payload

role_payload="$(jq -nc '{
  issuer_ref:"default", ttl:"24h", max_ttl:"24h", require_cn:false,
  allowed_domains:["72-56-246-80.sslip.io"], allow_subdomains:true,
  allow_bare_domains:false, allow_ip_sans:false, allow_localhost:false,
  server_flag:true, client_flag:false, code_signing_flag:false,
  email_protection_flag:false, key_type:"ec", key_bits:256,
  no_store:true, generate_lease:false
}')"
curl --fail --silent --show-error \
  --cacert "${temporary_directory}/openbao-ca.crt" \
  --resolve "${bao_host}:${local_port}:127.0.0.1" \
  -H "X-Vault-Token: ${operator_token}" -H 'Content-Type: application/json' \
  --data "${role_payload}" \
  "${bao_address}/v1/workspace-pki/roles/workspace-service" >/dev/null

issue_certificate() {
  local name="$1" secret="$2" directory
  directory="${temporary_directory}/${secret}"
  mkdir -p "${directory}"
  jq -nc --arg common_name "${name}" '{common_name:$common_name,ttl:"24h"}' \
    | curl --fail --silent --show-error \
      --cacert "${temporary_directory}/openbao-ca.crt" \
      --resolve "${bao_host}:${local_port}:127.0.0.1" \
      -H "X-Vault-Token: ${operator_token}" -H 'Content-Type: application/json' \
      --data-binary @- \
      "${bao_address}/v1/workspace-pki/issue/workspace-service" \
    | jq -er '.data | {certificate,private_key,issuing_ca}' >"${directory}/response.json"
  jq -er '.certificate' "${directory}/response.json" >"${directory}/tls.crt"
  jq -er '.private_key' "${directory}/response.json" >"${directory}/tls.key"
  jq -er '.issuing_ca' "${directory}/response.json" >"${directory}/ca.crt"
  chmod 0600 "${directory}"/*
  openssl x509 -in "${directory}/tls.crt" -noout -checkend 3600 >/dev/null
  openssl x509 -in "${directory}/tls.crt" -noout -ext subjectAltName | grep -Fq "DNS:${name}"
  kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" create secret generic "${secret}" \
    --from-file=tls.crt="${directory}/tls.crt" \
    --from-file=tls.key="${directory}/tls.key" \
    --from-file=ca.crt="${directory}/ca.crt" \
    --dry-run=client -o yaml | kubectl --kubeconfig "${kubeconfig}" apply -f - >/dev/null
}

issue_certificate "${manager_name}" workspace-manager-tls
issue_certificate "${egress_name}" workspace-egress-tls

printf 'OpenBao workspace server role and 24h Kubernetes TLS Secrets synchronized\n'
