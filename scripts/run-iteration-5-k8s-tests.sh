#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

: "${KUBECONFIG:?KUBECONFIG must point to the dedicated test cluster}"

namespace="ai-native-paas-user-test"
password="${POSTGRES_PASSWORD:-$(openssl rand -hex 16)}"

kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$namespace" create secret generic postgres-credentials \
  --from-literal=password="$password" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$namespace" get secret craas-ai-native-paas-registry >/dev/null

kubectl apply -f deploy/iteration5/postgres-test.yaml
kubectl -n "$namespace" rollout restart statefulset/user-postgres
kubectl -n "$namespace" rollout status statefulset/user-postgres --timeout=300s

kubectl -n "$namespace" delete job iteration-5-postgres-tests --ignore-not-found
kubectl apply -f deploy/iteration5/postgres-test-job.yaml
kubectl -n "$namespace" wait --for=condition=complete job/iteration-5-postgres-tests --timeout=900s
kubectl -n "$namespace" logs job/iteration-5-postgres-tests
