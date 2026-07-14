#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

: "${KUBECONFIG:?KUBECONFIG must point to the dedicated test cluster}"

namespace="ai-native-paas-system"
host="$(tofu -chdir=infra/stacks/admin output -json control_plane_database_networks | jq -r '.[] | select(.type == "local") | .ips[0].ip')"
port="$(tofu -chdir=infra/stacks/admin output -raw control_plane_database_port)"
user="$(tofu -chdir=infra/stacks/admin output -raw control_plane_database_login)"
database="$(tofu -chdir=infra/stacks/admin output -raw control_plane_database_name)"
password="$(tofu -chdir=infra/stacks/admin output -raw control_plane_database_password)"

test -n "$host"
test -n "$password"

kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace "$namespace" ai-native-paas.io/owner=control-plane --overwrite
kubectl -n "$namespace" create secret generic control-plane-postgres \
  --from-literal=PGHOST="$host" \
  --from-literal=PGPORT="$port" \
  --from-literal=PGUSER="$user" \
  --from-literal=PGPASSWORD="$password" \
  --from-literal=PGDATABASE="$database" \
  --from-literal=PGSSLMODE=disable \
  --dry-run=client -o yaml | kubectl apply -f -

database_url="host=$host port=$port user=$user password=$password dbname=$database sslmode=disable"
jq -nc --arg database_url "$database_url" \
  '{stringData:{DATABASE_URL:$database_url}}' \
  | kubectl -n "$namespace" patch secret state-service-secrets \
      --type merge --patch-file /dev/stdin >/dev/null

kubectl -n "$namespace" rollout restart deployment/state-service >/dev/null
kubectl -n "$namespace" rollout status deployment/state-service --timeout=180s
