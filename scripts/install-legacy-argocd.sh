#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
version="v3.4.2"
expected_sha256="4cadaf95c4cca1bcacb4ae0f635e0fad04192533ee0d497ba9d6a7e2043a4f4b"
url="https://raw.githubusercontent.com/argoproj/argo-cd/${version}/manifests/core-install.yaml"
registry="ai-native-paas-registry.registry.twcstorage.ru"
argocd_image="${registry}/mirror/argocd@sha256:a8679d1bbf7679ad27bf39fadbd30e486f59446eadd4f5c2c11ce6a41053a216"
redis_image="${registry}/mirror/redis@sha256:e499175dfb27569cd40010c2eee346113db95fdd0efc88ab9fd70a9e807f4542"
manifest="$(mktemp)"
trap 'rm -f "${manifest}"' EXIT

curl --fail --silent --show-error --location "${url}" --output "${manifest}"
actual_sha256="$(openssl dgst -sha256 "${manifest}" | awk '{print $NF}')"
if [[ "${actual_sha256}" != "${expected_sha256}" ]]; then
  echo "Argo CD Core manifest digest mismatch" >&2
  exit 1
fi

KUBECONFIG="${kubeconfig}" kubectl apply -f "${repo_root}/deploy/legacy/namespaces.yaml"
KUBECONFIG="${kubeconfig}" kubectl apply -f "${repo_root}/deploy/legacy/k0s-argocd-rbac.yaml"
KUBECONFIG="${kubeconfig}" kubectl apply \
  --namespace legacy-argocd \
  --server-side \
  --force-conflicts \
  -f "${manifest}"

KUBECONFIG="${kubeconfig}" kubectl -n ai-native-paas-system get secret craas-ai-native-paas-registry -o json |
  jq 'del(.metadata.namespace,.metadata.resourceVersion,.metadata.uid,.metadata.creationTimestamp,.metadata.managedFields,.metadata.ownerReferences)' |
  KUBECONFIG="${kubeconfig}" kubectl -n legacy-argocd apply -f -

KUBECONFIG="${kubeconfig}" kubectl -n legacy-argocd scale deployment/argocd-applicationset-controller --replicas=0
KUBECONFIG="${kubeconfig}" kubectl -n legacy-argocd set image deployment/argocd-redis \
  "secret-init=${argocd_image}" "redis=${redis_image}"
KUBECONFIG="${kubeconfig}" kubectl -n legacy-argocd set image deployment/argocd-repo-server \
  "copyutil=${argocd_image}" "argocd-repo-server=${argocd_image}"
KUBECONFIG="${kubeconfig}" kubectl -n legacy-argocd set image statefulset/argocd-application-controller \
  "argocd-application-controller=${argocd_image}"

for target in \
  deployment/argocd-redis \
  deployment/argocd-repo-server \
  statefulset/argocd-application-controller; do
  KUBECONFIG="${kubeconfig}" kubectl -n legacy-argocd patch "${target}" \
    --type merge \
    -p '{"spec":{"template":{"spec":{"nodeSelector":{"ai-native-paas.io/pool":"ci"},"imagePullSecrets":[{"name":"craas-ai-native-paas-registry"}]}}}}'
done

KUBECONFIG="${kubeconfig}" kubectl -n legacy-argocd delete pod argocd-application-controller-0 --ignore-not-found --wait=false

KUBECONFIG="${kubeconfig}" kubectl -n legacy-argocd rollout status deployment/argocd-redis --timeout=300s
KUBECONFIG="${kubeconfig}" kubectl -n legacy-argocd rollout status deployment/argocd-repo-server --timeout=300s
KUBECONFIG="${kubeconfig}" kubectl -n legacy-argocd rollout status statefulset/argocd-application-controller --timeout=300s
