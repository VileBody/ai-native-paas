#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
chart_version="0.28.4"
chart_sha256="783177e7925cb0d91ec3d5317a80826cb1e265ce17ba176d7a7ad578234c8a4e"
temporary_dir="$(mktemp -d -t openbao-chart.XXXXXX)"
archive="${temporary_dir}/openbao.tgz"
trap 'rm -rf "${temporary_dir}"' EXIT

"${repo_root}/scripts/bootstrap-openbao-tls.sh"

helm repo add openbao https://openbao.github.io/openbao-helm --force-update >/dev/null
helm repo update openbao >/dev/null
helm pull openbao/openbao --version "${chart_version}" --destination "${temporary_dir}" >/dev/null
downloaded="${temporary_dir}/openbao-${chart_version}.tgz"
mv "${downloaded}" "${archive}"
actual_sha256="$(openssl dgst -sha256 "${archive}" | awk '{print $NF}')"
if [[ "${actual_sha256}" != "${chart_sha256}" ]]; then
  echo "OpenBao chart digest mismatch" >&2
  exit 1
fi

helm upgrade --install openbao "${archive}" \
  --kubeconfig "${kubeconfig}" \
  --namespace openbao \
  --values "${repo_root}/deploy/admin/openbao/values.yaml" \
  --history-max 10 \
  --server-side=false \
  --wait=hookOnly

kubectl --kubeconfig "${kubeconfig}" -n openbao rollout status deployment/openbao-agent-injector --timeout=5m
kubectl --kubeconfig "${kubeconfig}" -n openbao wait pod -l app.kubernetes.io/name=openbao,component=server \
  --for=jsonpath='{.status.phase}'=Running --timeout=10m

echo "OpenBao is deployed. Continue with docs/runbooks/openbao-shamir-ceremony.md; do not store shares in Kubernetes."
