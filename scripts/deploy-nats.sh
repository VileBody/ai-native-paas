#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
chart_version="2.14.2"
chart_sha256="86e0fbfe000b6d7fe70a3032ad3a667252f2d3dd2022e95f7330704d947d0f84"
capacity_mode="${ADMIN_CAPACITY_MODE:-ha}"
if [[ "${capacity_mode}" != "ha" && "${capacity_mode}" != "dev" && "${capacity_mode}" != "off" ]]; then
  echo "ADMIN_CAPACITY_MODE must be ha, dev, or off" >&2
  exit 1
fi
temporary_dir="$(mktemp -d -t nats-chart.XXXXXX)"
archive="${temporary_dir}/nats.tgz"
trap 'rm -rf "${temporary_dir}"' EXIT

"${repo_root}/scripts/bootstrap-nats-secrets.sh"

helm repo add nats https://nats-io.github.io/k8s/helm/charts/ --force-update >/dev/null
helm repo update nats >/dev/null
helm pull nats/nats --version "${chart_version}" --destination "${temporary_dir}" >/dev/null
mv "${temporary_dir}/nats-${chart_version}.tgz" "${archive}"
actual_sha256="$(openssl dgst -sha256 "${archive}" | awk '{print $NF}')"
if [[ "${actual_sha256}" != "${chart_sha256}" ]]; then
  echo "NATS chart digest mismatch" >&2
  exit 1
fi

values_args=(--values "${repo_root}/deploy/admin/nats/values.yaml")
if [[ "${capacity_mode}" == "dev" || "${capacity_mode}" == "off" ]]; then
  if [[ "${ADMIN_DEV_SNAPSHOT_ACK:-}" != "NATS-JETSTREAM-BACKUP-VERIFIED" ]]; then
    echo "dev/off mode requires ADMIN_DEV_SNAPSHOT_ACK=NATS-JETSTREAM-BACKUP-VERIFIED" >&2
    exit 1
  fi
  values_args+=(--values "${repo_root}/deploy/admin/nats/values-dev.yaml")
fi
if [[ "${capacity_mode}" == "off" ]]; then
  values_args+=(--values "${repo_root}/deploy/admin/nats/values-off.yaml")
fi

kubectl --kubeconfig "${kubeconfig}" apply -f "${repo_root}/deploy/admin/nats/network-policy.yaml"
helm upgrade --install nats "${archive}" \
  --kubeconfig "${kubeconfig}" \
  --namespace nats \
  "${values_args[@]}" \
  --history-max 10 \
  --wait \
  --timeout 10m

kubectl --kubeconfig "${kubeconfig}" -n nats rollout status statefulset/nats --timeout=10m
if [[ "${capacity_mode}" != "off" ]]; then
  kubectl --kubeconfig "${kubeconfig}" -n nats rollout status deployment/nats-box --timeout=5m
fi
echo "NATS JetStream is deployed in ${capacity_mode} capacity mode."
