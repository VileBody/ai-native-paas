#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

: "${KUBECONFIG:?KUBECONFIG must point to the dedicated admin cluster}"

namespace="ai-native-paas-csi-smoke"
cleanup() {
  kubectl delete namespace "$namespace" --ignore-not-found --wait=false >/dev/null
}
trap cleanup EXIT

cleanup
kubectl wait --for=delete "namespace/$namespace" --timeout=120s 2>/dev/null || true
kubectl apply -f verification/k8s/timeweb-csi-smoke.yaml
kubectl -n "$namespace" wait --for=condition=Ready pod/csi-writer --timeout=300s
kubectl -n "$namespace" wait --for=jsonpath='{.status.phase}'=Bound pvc/nvme-smoke --timeout=120s

kubectl -n "$namespace" delete pod csi-writer --wait=true
kubectl apply -f verification/k8s/timeweb-csi-reader.yaml
kubectl -n "$namespace" wait --for=condition=Ready pod/csi-reader --timeout=300s
kubectl -n "$namespace" exec csi-reader -- /bin/sh -ec 'test "$(cat /data/sentinel)" = "ai-native-paas-csi-v1"'

echo "TIMEWEB_CSI_SMOKE_GREEN: NVMe PVC persisted across pod recreation"
