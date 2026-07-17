#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

: "${KUBECONFIG:?KUBECONFIG must point to the dedicated admin cluster}"

namespace="csi-driver-timeweb-cloud"
source_namespace="ai-native-paas-system"
source_secret="craas-ai-native-paas-registry"
mirror_secret="ai-native-paas-registry-mirror"
registrar_image="ai-native-paas-registry.registry.twcstorage.ru/mirror/csi-node-driver-registrar@sha256:fdff3ee285341bc58033b6b2458a5d45fd90ec6922a8ba6ebdd49b0c41e2cd34"
capacity_mode="${ADMIN_CAPACITY_MODE:-ha}"
if [[ "$capacity_mode" != "ha" && "$capacity_mode" != "dev" && "$capacity_mode" != "off" ]]; then
  echo "ADMIN_CAPACITY_MODE must be ha, dev, or off" >&2
  exit 1
fi

# The managed addon currently hardcodes a public sidecar registry. Mirror only
# that sidecar so every node has a deterministic pull path.
kubectl -n "$source_namespace" get secret "$source_secret" -o json \
  | jq --arg namespace "$namespace" --arg name "$mirror_secret" '
      del(.metadata.annotations, .metadata.creationTimestamp, .metadata.ownerReferences,
          .metadata.resourceVersion, .metadata.uid, .metadata.managedFields)
      | .metadata.namespace=$namespace
      | .metadata.name=$name' \
  | kubectl apply -f - >/dev/null

if [[ "$capacity_mode" == "off" ]]; then
  kubectl -n "$namespace" patch deployment csi-driver-timeweb-cloud-controller --type=merge \
    -p '{"spec":{"template":{"spec":{"nodeSelector":{"ai-native-paas.io/pool":"ci"},"tolerations":[]}}}}' >/dev/null
  kubectl -n "$namespace" set resources deployment/csi-driver-timeweb-cloud-controller \
    -c csi-plugin --requests=cpu=20m,memory=128Mi >/dev/null
  for container in liveness-probe external-resizer external-attacher external-provisioner external-health-monitor-controller; do
    kubectl -n "$namespace" set resources deployment/csi-driver-timeweb-cloud-controller \
      -c "$container" --requests=cpu=5m,memory=50Mi >/dev/null
  done

  # A single-node cluster cannot make progress when CoreDNS uses a surge pod
  # together with required pod anti-affinity. Keep one DNS replica available by
  # replacing it in place during managed-cluster reconciles.
  kubectl -n kube-system patch deployment coredns --type=merge \
    -p '{"spec":{"strategy":{"type":"RollingUpdate","rollingUpdate":{"maxUnavailable":1,"maxSurge":0}}}}' >/dev/null
else
  kubectl -n "$namespace" patch deployment csi-driver-timeweb-cloud-controller --type=merge \
    -p '{"spec":{"template":{"spec":{"nodeSelector":{"ai-native-paas.io/pool":"system"},"tolerations":[{"key":"ai-native-paas.io/system","operator":"Equal","value":"true","effect":"NoSchedule"}]}}}}' >/dev/null
  kubectl -n "$namespace" set resources deployment/csi-driver-timeweb-cloud-controller \
    -c csi-plugin --requests=cpu=200m,memory=256Mi >/dev/null
  kubectl -n "$namespace" set resources deployment/csi-driver-timeweb-cloud-controller \
    -c liveness-probe --requests=cpu=50m,memory=70Mi >/dev/null
  for container in external-resizer external-attacher external-provisioner external-health-monitor-controller; do
    kubectl -n "$namespace" set resources deployment/csi-driver-timeweb-cloud-controller \
      -c "$container" --requests=cpu=50m,memory=100Mi >/dev/null
  done
  kubectl -n kube-system patch deployment coredns --type=merge \
    -p '{"spec":{"strategy":{"type":"RollingUpdate","rollingUpdate":{"maxUnavailable":"25%","maxSurge":"25%"}}}}' >/dev/null
fi

kubectl -n "$namespace" patch daemonset csi-driver-timeweb-cloud-node --type=strategic \
  -p "{\"spec\":{\"template\":{\"spec\":{\"imagePullSecrets\":[{\"name\":\"$mirror_secret\"}],\"containers\":[{\"name\":\"node-driver-registrar\",\"image\":\"$registrar_image\",\"imagePullPolicy\":\"IfNotPresent\"}]}}}}" >/dev/null

kubectl -n "$namespace" rollout status deployment/csi-driver-timeweb-cloud-controller --timeout=180s
kubectl -n "$namespace" rollout status daemonset/csi-driver-timeweb-cloud-node --timeout=300s

not_ready="$(kubectl -n "$namespace" get pods -l app=csi-driver-timeweb-cloud -o json | jq '[.items[] | select(any(.status.containerStatuses[]?; .ready != true))] | length')"
if [[ "$not_ready" != "0" ]]; then
  echo "CSI reconciliation failed: not_ready_pods=$not_ready" >&2
  exit 1
fi

echo "TIMEWEB_CSI_RECONCILED: capacity_mode=$capacity_mode, all controller and node sidecars ready"
