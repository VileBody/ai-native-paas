#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

: "${KUBECONFIG:?KUBECONFIG must point to the dedicated test cluster}"

namespace="ai-native-paas-system"
job="iteration-5-control-plane-postgres-tests"

./scripts/sync-timeweb-control-plane-postgres-secret.sh
kubectl -n "$namespace" get secret control-plane-postgres >/dev/null
kubectl -n "$namespace" get secret craas-ai-native-paas-registry >/dev/null
kubectl -n "$namespace" delete job "$job" --ignore-not-found
kubectl apply -f infra/timeweb/control-plane-postgres-test-job.yaml
kubectl -n "$namespace" wait --for=condition=complete "job/$job" --timeout=900s
kubectl -n "$namespace" logs "job/$job"
