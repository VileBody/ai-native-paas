#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
namespace="harbor-system"
temporary_dir="$(mktemp -d -t harbor-bootstrap.XXXXXX)"
trap 'rm -rf "${temporary_dir}"' EXIT

: "${TF_HTTP_USERNAME:?TF_HTTP_USERNAME is required for encrypted remote state}"
: "${TF_HTTP_PASSWORD:?TF_HTTP_PASSWORD is required for encrypted remote state}"
: "${TF_VAR_state_passphrase:?TF_VAR_state_passphrase is required for encrypted OpenTofu outputs}"
: "${HARBOR_BLOB_S3_ACCESS_KEY_FILE:?HARBOR_BLOB_S3_ACCESS_KEY_FILE is required}"
: "${HARBOR_BLOB_S3_SECRET_KEY_FILE:?HARBOR_BLOB_S3_SECRET_KEY_FILE is required}"

kubectl --kubeconfig "${kubeconfig}" apply -f "${repo_root}/deploy/admin/harbor/namespace.yaml" >/dev/null

output_file() {
  local output="$1"
  local target="$2"
  tofu -chdir="${repo_root}/infra/stacks/admin" output -raw "${output}" >"${target}"
  test -s "${target}"
}

preserve_or_generate() {
  local secret="$1"
  local key="$2"
  local bytes="$3"
  local target="${temporary_dir}/${secret}-${key}"
  local existing
  existing="$(kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" get secret "${secret}" -o "jsonpath={.data.${key}}" 2>/dev/null || true)"
  if [[ -n "${existing}" ]]; then
    printf '%s' "${existing}" | base64 -D >"${target}"
  else
    openssl rand -base64 "${bytes}" | tr -d '\r\n' >"${target}"
  fi
  test -s "${target}"
}

preserve_or_generate_exact() {
  local secret="$1"
  local key="$2"
  local target="${temporary_dir}/${secret}-${key}"
  local existing
  existing="$(kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" get secret "${secret}" -o "jsonpath={.data.${key}}" 2>/dev/null || true)"
  if [[ -n "${existing}" ]]; then
    printf '%s' "${existing}" | base64 -D >"${target}"
  else
    openssl rand -hex 8 | tr -d '\r\n' >"${target}"
  fi
  test "$(wc -c <"${target}" | tr -d ' ')" = "16"
}

apply_secret() {
  local secret="$1"
  shift
  kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" create secret generic "${secret}" \
    "$@" \
    --dry-run=client -o yaml \
    | kubectl --kubeconfig "${kubeconfig}" apply --server-side --field-manager=ai-native-paas-harbor-bootstrap -f - >/dev/null
}

output_file harbor_database_password "${temporary_dir}/database-password"
for source in "${HARBOR_BLOB_S3_ACCESS_KEY_FILE}" "${HARBOR_BLOB_S3_SECRET_KEY_FILE}"; do
  if [[ ! -f "${source}" || -L "${source}" || ! -s "${source}" ]]; then
    echo "Harbor S3 credential files must be nonempty regular files" >&2
    exit 1
  fi
done
tr -d '\r\n' <"${HARBOR_BLOB_S3_ACCESS_KEY_FILE}" >"${temporary_dir}/s3-access-key"
tr -d '\r\n' <"${HARBOR_BLOB_S3_SECRET_KEY_FILE}" >"${temporary_dir}/s3-secret-key"
test -s "${temporary_dir}/s3-access-key"
test -s "${temporary_dir}/s3-secret-key"

preserve_or_generate harbor-admin HARBOR_ADMIN_PASSWORD 30
preserve_or_generate_exact harbor-system-key secretKey
preserve_or_generate_exact harbor-core secret
preserve_or_generate harbor-core CSRF_KEY 32
preserve_or_generate_exact harbor-jobservice JOBSERVICE_SECRET
preserve_or_generate_exact harbor-registry REGISTRY_HTTP_SECRET

apply_secret harbor-database --from-file=password="${temporary_dir}/database-password"
apply_secret harbor-storage \
  --from-file=REGISTRY_STORAGE_S3_ACCESSKEY="${temporary_dir}/s3-access-key" \
  --from-file=REGISTRY_STORAGE_S3_SECRETKEY="${temporary_dir}/s3-secret-key"
apply_secret harbor-admin --from-file=HARBOR_ADMIN_PASSWORD="${temporary_dir}/harbor-admin-HARBOR_ADMIN_PASSWORD"
apply_secret harbor-system-key --from-file=secretKey="${temporary_dir}/harbor-system-key-secretKey"
apply_secret harbor-core \
  --from-file=secret="${temporary_dir}/harbor-core-secret" \
  --from-file=CSRF_KEY="${temporary_dir}/harbor-core-CSRF_KEY"
apply_secret harbor-jobservice --from-file=JOBSERVICE_SECRET="${temporary_dir}/harbor-jobservice-JOBSERVICE_SECRET"
apply_secret harbor-registry --from-file=REGISTRY_HTTP_SECRET="${temporary_dir}/harbor-registry-REGISTRY_HTTP_SECRET"

printf 'Harbor bootstrap secrets synchronized without printing values\n'
