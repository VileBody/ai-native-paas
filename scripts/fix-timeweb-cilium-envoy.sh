#!/usr/bin/env bash
set -Eeuo pipefail

: "${KUBECONFIG:?KUBECONFIG must point to the dedicated Timeweb cluster}"
: "${GH_TOKEN:?GH_TOKEN with read:packages is required}"

namespace="kube-system"
image="ghcr.io/vilebody/ai-native-paas-cilium-envoy:v1.36.8-timeweb@sha256:326f872e19ce8aa45170efbf583b3f301586ba3feead14b864676d4baf3b45ed"

kubectl -n "$namespace" create secret docker-registry ghcr-pull \
  --docker-server=ghcr.io --docker-username=VileBody --docker-password="$GH_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$namespace" patch daemonset cilium-envoy --type=merge \
  -p '{"spec":{"template":{"spec":{"imagePullSecrets":[{"name":"ghcr-pull"}]}}}}'
kubectl -n "$namespace" set image daemonset/cilium-envoy "cilium-envoy=$image"
kubectl -n "$namespace" rollout status daemonset/cilium-envoy --timeout=300s
