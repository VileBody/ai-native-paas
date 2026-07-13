#!/usr/bin/env bash
set -Eeuo pipefail

: "${KUBECONFIG:?KUBECONFIG must point to the dedicated Timeweb cluster}"

namespace="kube-system"
image="ai-native-paas-registry.registry.twcstorage.ru/cilium-envoy:v1.36.8-timeweb@sha256:326f872e19ce8aa45170efbf583b3f301586ba3feead14b864676d4baf3b45ed"

kubectl -n "$namespace" get secret craas-ai-native-paas-registry >/dev/null
kubectl -n "$namespace" patch daemonset cilium-envoy --type=merge \
  -p '{"spec":{"template":{"spec":{"imagePullSecrets":[{"name":"craas-ai-native-paas-registry"}]}}}}'
kubectl -n "$namespace" set image daemonset/cilium-envoy "cilium-envoy=$image"
kubectl -n "$namespace" rollout status daemonset/cilium-envoy --timeout=300s
