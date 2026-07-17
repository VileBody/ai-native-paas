#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
lock="${repo_root}/deploy/legacy/images.lock.json"
pull_secret="craas-ai-native-paas-registry"

jq -r '.runtime_images | to_entries[] | [.key, .value] | @tsv' "${lock}" |
while IFS=$'\t' read -r workload image; do
  namespace="${workload%%/*}"
  deployment="${workload#*/}"
  container="$(KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" get deployment "${deployment}" -o jsonpath='{.spec.template.spec.containers[0].name}')"

  KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" set image \
    "deployment/${deployment}" "${container}=${image}"
  KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" patch \
    "deployment/${deployment}" --type merge \
    -p "{\"spec\":{\"template\":{\"spec\":{\"nodeSelector\":{\"ai-native-paas.io/pool\":\"ci\"},\"imagePullSecrets\":[{\"name\":\"${pull_secret}\"}]}}}}"
done

for namespace in rec-sidecar vietnam-rent; do
  KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" get deployment -o name |
  while read -r deployment; do
    KUBECONFIG="${kubeconfig}" kubectl -n "${namespace}" rollout status \
      "${deployment}" --timeout=600s
  done
done
