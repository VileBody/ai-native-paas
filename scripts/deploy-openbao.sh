#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
chart_version="0.28.4"
chart_sha256="783177e7925cb0d91ec3d5317a80826cb1e265ce17ba176d7a7ad578234c8a4e"
capacity_mode="${ADMIN_CAPACITY_MODE:-ha}"
if [[ "${capacity_mode}" != "ha" && "${capacity_mode}" != "dev" && "${capacity_mode}" != "off" ]]; then
  echo "ADMIN_CAPACITY_MODE must be ha, dev, or off" >&2
  exit 1
fi
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

values_args=(--values "${repo_root}/deploy/admin/openbao/values.yaml")
if [[ "${capacity_mode}" == "dev" || "${capacity_mode}" == "off" ]]; then
  if [[ "${ADMIN_DEV_SNAPSHOT_ACK:-}" != "OPENBAO-RAFT-SNAPSHOT-VERIFIED" ]]; then
    echo "dev/off mode requires ADMIN_DEV_SNAPSHOT_ACK=OPENBAO-RAFT-SNAPSHOT-VERIFIED" >&2
    exit 1
  fi
  values_args+=(--values "${repo_root}/deploy/admin/openbao/values-dev.yaml")
fi
if [[ "${capacity_mode}" == "off" ]]; then
  values_args+=(--values "${repo_root}/deploy/admin/openbao/values-off.yaml")
fi

helm upgrade --install openbao "${archive}" \
  --kubeconfig "${kubeconfig}" \
  --namespace openbao \
  "${values_args[@]}" \
  --history-max 10 \
  --server-side=false \
  --wait=hookOnly

if [[ "${capacity_mode}" == "off" ]]; then
  if kubectl --kubeconfig "${kubeconfig}" get mutatingwebhookconfiguration openbao-agent-injector-cfg >/dev/null 2>&1; then
    echo "OpenBao off mode must remove the injector admission webhook" >&2
    exit 1
  fi
  for _ in $(seq 1 120); do
    replicas="$(kubectl --kubeconfig "${kubeconfig}" -n openbao get statefulset/openbao -o jsonpath='{.status.replicas}')"
    [[ -z "${replicas}" || "${replicas}" == "0" ]] && break
    sleep 1
  done
  replicas="$(kubectl --kubeconfig "${kubeconfig}" -n openbao get statefulset/openbao -o jsonpath='{.status.replicas}')"
  [[ -z "${replicas}" || "${replicas}" == "0" ]]
else
  kubectl --kubeconfig "${kubeconfig}" -n openbao rollout status deployment/openbao-agent-injector --timeout=5m
  kubectl --kubeconfig "${kubeconfig}" -n openbao wait pod -l app.kubernetes.io/name=openbao,component=server \
    --for=jsonpath='{.status.phase}'=Running --timeout=10m
fi

echo "OpenBao is deployed in ${capacity_mode} capacity mode. Continue with docs/runbooks/openbao-shamir-ceremony.md; do not store shares in Kubernetes."
