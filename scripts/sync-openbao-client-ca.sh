#!/usr/bin/env bash
# Copies only the public OpenBao server CA into the control-plane namespace.
# It never copies the server certificate key, an OpenBao token, or any Shamir
# material. Re-run after a deliberate OpenBao server-CA rotation.
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
source_namespace="openbao"
target_namespace="ai-native-paas-system"
source_secret="openbao-server-tls"
target_secret="openbao-client-ca"
temporary_dir="$(mktemp -d -t openbao-client-ca.XXXXXX)"
trap 'rm -rf "${temporary_dir}"' EXIT

kubectl_bin="${KUBECTL_BIN:-kubectl}"
if ! command -v "${kubectl_bin}" >/dev/null 2>&1 && [[ -x /opt/homebrew/bin/kubectl ]]; then
  kubectl_bin=/opt/homebrew/bin/kubectl
fi
command -v "${kubectl_bin}" >/dev/null 2>&1 || {
  printf 'kubectl is required; set KUBECTL_BIN to its absolute path\n' >&2
  exit 1
}

encoded_ca="$("${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${source_namespace}" \
  get secret "${source_secret}" -o 'jsonpath={.data.ca\.crt}')"
test -n "${encoded_ca}"
printf '%s' "${encoded_ca}" | openssl base64 -d -A >"${temporary_dir}/ca.crt"
openssl x509 -in "${temporary_dir}/ca.crt" -noout >/dev/null

"${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${target_namespace}" create secret generic "${target_secret}" \
  --from-file=ca.crt="${temporary_dir}/ca.crt" \
  --dry-run=client -o yaml \
  | "${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${target_namespace}" \
    apply --server-side --field-manager=ai-native-paas-openbao-ca -f - >/dev/null

printf 'OpenBao public client CA synchronized to %s/%s\n' "${target_namespace}" "${target_secret}"
