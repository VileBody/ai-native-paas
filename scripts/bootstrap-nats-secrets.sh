#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
operator_dir="${NATS_OPERATOR_DIR:-${repo_root}/.operator/nats}"
namespace="nats"

umask 077
mkdir -p "${operator_dir}"

ca_key="${operator_dir}/ca.key"
ca_cert="${operator_dir}/ca.crt"
server_key="${operator_dir}/tls.key"
server_csr="${operator_dir}/tls.csr"
server_cert="${operator_dir}/tls.crt"
extensions="${operator_dir}/tls.ext"

if [[ ! -s "${ca_key}" || ! -s "${ca_cert}" ]]; then
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:4096 -out "${ca_key}" 2>/dev/null
  openssl req -x509 -new -sha256 -days 3650 -key "${ca_key}" -out "${ca_cert}" \
    -subj "/CN=AI Native PaaS NATS CA/O=AI Native PaaS"
fi

if [[ ! -s "${server_key}" || ! -s "${server_cert}" ]] || ! openssl x509 -checkend 2592000 -noout -in "${server_cert}" >/dev/null; then
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out "${server_key}" 2>/dev/null
  openssl req -new -sha256 -key "${server_key}" -out "${server_csr}" \
    -subj "/CN=nats.nats.svc/O=AI Native PaaS"
  cat >"${extensions}" <<'EOF'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth,clientAuth
subjectAltName=DNS:nats,DNS:nats.nats,DNS:nats.nats.svc,DNS:nats.nats.svc.cluster.local,DNS:nats-headless,DNS:nats-headless.nats.svc,DNS:*.nats-headless.nats.svc,DNS:*.nats-headless.nats.svc.cluster.local
EOF
  openssl x509 -req -sha256 -days 825 -in "${server_csr}" -CA "${ca_cert}" -CAkey "${ca_key}" \
    -CAcreateserial -out "${server_cert}" -extfile "${extensions}"
fi
openssl verify -CAfile "${ca_cert}" "${server_cert}" >/dev/null

kubectl --kubeconfig "${kubeconfig}" apply -f "${repo_root}/deploy/admin/nats/namespace.yaml"
kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" create secret generic nats-server-tls \
  --from-file=tls.crt="${server_cert}" \
  --from-file=tls.key="${server_key}" \
  --from-file=ca.crt="${ca_cert}" \
  --dry-run=client -o yaml | kubectl --kubeconfig "${kubeconfig}" apply -f - >/dev/null

if ! kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" get secret nats-auth >/dev/null 2>&1; then
  token="$(openssl rand -hex 32)"
  kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" create secret generic nats-auth \
    --from-literal=token="${token}" >/dev/null
  unset token
fi

echo "NATS TLS and authentication secrets are present; values were not printed."
