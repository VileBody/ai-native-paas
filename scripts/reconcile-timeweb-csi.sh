#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

: "${KUBECONFIG:?KUBECONFIG must point to the dedicated admin cluster}"

namespace="csi-driver-timeweb-cloud"
source_namespace="ai-native-paas-system"
source_secret="craas-ai-native-paas-registry"
mirror_secret="ai-native-paas-registry-mirror"
registrar_image="ai-native-paas-registry.registry.twcstorage.ru/mirror/csi-node-driver-registrar@sha256:fdff3ee285341bc58033b6b2458a5d45fd90ec6922a8ba6ebdd49b0c41e2cd34"

# The managed addon currently hardcodes a public sidecar registry. Mirror only
# that sidecar so every node has a deterministic pull path.
kubectl -n "$source_namespace" get secret "$source_secret" -o json \
  | jq --arg namespace "$namespace" --arg name "$mirror_secret" '
      del(.metadata.annotations, .metadata.creationTimestamp, .metadata.ownerReferences,
          .metadata.resourceVersion, .metadata.uid, .metadata.managedFields)
      | .metadata.namespace=$namespace
      | .metadata.name=$name' \
  | kubectl apply -f - >/dev/null

kubectl -n "$namespace" patch deployment csi-driver-timeweb-cloud-controller --type=merge \
  -p '{"spec":{"template":{"spec":{"nodeSelector":{"ai-native-paas.io/pool":"system"},"tolerations":[{"key":"ai-native-paas.io/system","operator":"Equal","value":"true","effect":"NoSchedule"}]}}}}' >/dev/null

kubectl -n "$namespace" patch daemonset csi-driver-timeweb-cloud-node --type=strategic \
  -p "{\"spec\":{\"template\":{\"spec\":{\"imagePullSecrets\":[{\"name\":\"$mirror_secret\"}],\"containers\":[{\"name\":\"node-driver-registrar\",\"image\":\"$registrar_image\",\"imagePullPolicy\":\"IfNotPresent\"}]}}}}" >/dev/null

kubectl -n "$namespace" rollout status deployment/csi-driver-timeweb-cloud-controller --timeout=180s
kubectl -n "$namespace" rollout status daemonset/csi-driver-timeweb-cloud-node --timeout=300s

not_ready="$(kubectl -n "$namespace" get pods -l app=csi-driver-timeweb-cloud -o json | jq '[.items[] | select(any(.status.containerStatuses[]?; .ready != true))] | length')"
if [[ "$not_ready" != "0" ]]; then
  echo "CSI reconciliation failed: not_ready_pods=$not_ready" >&2
  exit 1
fi

echo "TIMEWEB_CSI_RECONCILED: controller on system pool, all node sidecars ready"
