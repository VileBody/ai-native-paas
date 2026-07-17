#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
namespace="harbor-system"
chart_version="1.19.1"
chart_sha256="593f47e1ff6cdb58bd571708535ccef436f44ccf842c5f7925cd7b03e827edc1"
temporary_dir="$(mktemp -d -t harbor-chart.XXXXXX)"
trap 'rm -rf "${temporary_dir}"' EXIT

: "${TF_HTTP_USERNAME:?TF_HTTP_USERNAME is required for encrypted remote state}"
: "${TF_HTTP_PASSWORD:?TF_HTTP_PASSWORD is required for encrypted remote state}"
: "${TF_VAR_state_passphrase:?TF_VAR_state_passphrase is required for encrypted OpenTofu outputs}"

"${repo_root}/scripts/sync-harbor-bootstrap-secrets.sh"

helm repo add harbor https://helm.goharbor.io --force-update >/dev/null
helm repo update harbor >/dev/null
helm pull harbor/harbor --version "${chart_version}" --destination "${temporary_dir}" >/dev/null
archive="${temporary_dir}/harbor-${chart_version}.tgz"
actual_sha256="$(openssl dgst -sha256 "${archive}" | awk '{print $NF}')"
if [[ "${actual_sha256}" != "${chart_sha256}" ]]; then
  echo "Harbor chart digest mismatch" >&2
  exit 1
fi

database_host="$(tofu -chdir="${repo_root}/infra/stacks/admin" output -raw control_plane_database_host)"
database_port="$(tofu -chdir="${repo_root}/infra/stacks/admin" output -raw control_plane_database_port)"
bucket="$(tofu -chdir="${repo_root}/infra/stacks/admin" output -raw harbor_blob_bucket_name)"
test -n "${database_host}"
test -n "${database_port}"
test -n "${bucket}"

helm upgrade --install ai-native-paas-harbor "${archive}" \
  --kubeconfig "${kubeconfig}" \
  --namespace "${namespace}" \
  --values "${repo_root}/deploy/admin/harbor/values-dev.yaml" \
  --set-string "database.external.host=${database_host}" \
  --set-string "database.external.port=${database_port}" \
  --set-string "persistence.imageChartStorage.s3.bucket=${bucket}" \
  --history-max 10 \
  --wait \
  --timeout 10m

# A Kubernetes Secret update does not restart consumers by itself.  The
# registry is the only Harbor component that holds the S3 storage credentials.
kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" rollout restart deployment/ai-native-paas-harbor-registry >/dev/null
kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" rollout status deployment/ai-native-paas-harbor-registry --timeout=300s >/dev/null

kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" get pods -l app.kubernetes.io/instance=ai-native-paas-harbor
printf 'Internal Harbor development profile is deployed; public ingress remains disabled\n'
