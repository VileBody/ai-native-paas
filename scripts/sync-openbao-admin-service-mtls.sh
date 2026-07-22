#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
namespace="${CONTROL_PLANE_NAMESPACE:-ai-native-paas-system}"
operator_user="${OPENBAO_OPERATOR_USER:-ergin}"
keychain_service="${OPENBAO_OPERATOR_KEYCHAIN_SERVICE:-ai-native-paas-openbao-solo-dev-operator-20260715T210106Z}"
local_port="${OPENBAO_LOCAL_PORT:-18201}"
bao_host="openbao-active.openbao.svc"
bao_address="https://${bao_host}:${local_port}"
trust_domain="admin.platform.test"

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

temporary_directory="$(mktemp -d -t admin-service-mtls.XXXXXX)"
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
  -H 'Content-Type: application/json' --data "${login_payload}" \
  "${bao_address}/v1/auth/userpass/login/${operator_user}" | jq -er '.auth.client_token')"
unset login_payload

bao_request() {
  local method="$1" path="$2" payload="${3:-}" output="${4:-/dev/null}"
  local arguments=(--fail --silent --show-error
    --cacert "${temporary_directory}/openbao-ca.crt"
    --resolve "${bao_host}:${local_port}:127.0.0.1"
    -H "X-Vault-Token: ${operator_token}"
    -X "${method}")
  if [[ -n "${payload}" ]]; then
    arguments+=(-H 'Content-Type: application/json' --data "${payload}")
  fi
  curl "${arguments[@]}" "${bao_address}/v1/${path}" >"${output}"
}

bao_request GET sys/mounts "" "${temporary_directory}/mounts.json"
if ! jq -e 'has("admin-pki/")' "${temporary_directory}/mounts.json" >/dev/null; then
  bao_request POST sys/mounts/admin-pki \
    "$(jq -nc '{type:"pki",config:{max_lease_ttl:"8760h"}}')"
  bao_request POST admin-pki/root/generate/internal \
    "$(jq -nc --arg common_name "${trust_domain}" '{common_name:$common_name,ttl:"8760h",key_type:"ec",key_bits:256}')"
fi

bao_request POST admin-pki/roles/service-server "$(jq -nc '{
  issuer_ref:"default",ttl:"24h",max_ttl:"24h",require_cn:true,
  allowed_domains:["ai-native-paas-system.svc","svc.cluster.local"],allow_subdomains:true,
  allow_bare_domains:false,allow_ip_sans:false,allow_localhost:false,
  server_flag:true,client_flag:false,code_signing_flag:false,email_protection_flag:false,
  key_type:"ec",key_bits:256,no_store:true,generate_lease:false
}')"
bao_request POST admin-pki/roles/service-client "$(jq -nc --arg trust_domain "${trust_domain}" '{
  issuer_ref:"default",ttl:"24h",max_ttl:"24h",require_cn:false,
  allowed_uri_sans:("spiffe://"+$trust_domain+"/service/*"),allow_ip_sans:false,allow_localhost:false,
  server_flag:false,client_flag:true,code_signing_flag:false,email_protection_flag:false,
  key_type:"ec",key_bits:256,no_store:true,generate_lease:false,
  use_csr_common_name:false,use_csr_sans:false
}')"

write_secret() {
  local response="$1" secret="$2" directory
  directory="${temporary_directory}/${secret}"
  mkdir -p "${directory}"
  jq -er '.data.certificate' "${response}" >"${directory}/tls.crt"
  jq -er '.data.private_key' "${response}" >"${directory}/tls.key"
  jq -er '.data.issuing_ca' "${response}" >"${directory}/ca.crt"
  chmod 0600 "${directory}"/*
  openssl x509 -in "${directory}/tls.crt" -noout -checkend 3600 >/dev/null
  kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" create secret generic "${secret}" \
    --from-file=tls.crt="${directory}/tls.crt" \
    --from-file=tls.key="${directory}/tls.key" \
    --from-file=ca.crt="${directory}/ca.crt" \
    --dry-run=client -o yaml | kubectl --kubeconfig "${kubeconfig}" apply -f - >/dev/null
}

commerce_name="commerce-api.${namespace}.svc"
bao_request POST admin-pki/issue/service-server \
  "$(jq -nc --arg common_name "${commerce_name}" --arg alt_names "${commerce_name}.cluster.local" '{common_name:$common_name,alt_names:$alt_names,ttl:"24h"}')" \
  "${temporary_directory}/commerce-server.json"
write_secret "${temporary_directory}/commerce-server.json" commerce-api-mtls
openssl x509 -in "${temporary_directory}/commerce-api-mtls/tls.crt" -noout -ext subjectAltName | grep -Fq "DNS:${commerce_name}"

workspace_identity="spiffe://${trust_domain}/service/workspace-manager"
bao_request POST admin-pki/issue/service-client \
  "$(jq -nc --arg uri_sans "${workspace_identity}" '{uri_sans:$uri_sans,exclude_cn_from_sans:true,ttl:"24h"}')" \
  "${temporary_directory}/workspace-client.json"
write_secret "${temporary_directory}/workspace-client.json" workspace-manager-service-mtls
openssl x509 -in "${temporary_directory}/workspace-manager-service-mtls/tls.crt" -noout -ext subjectAltName | grep -Fq "URI:${workspace_identity}"

printf 'OpenBao admin service PKI and workspace-manager-to-commerce mTLS Secrets synchronized\n'
