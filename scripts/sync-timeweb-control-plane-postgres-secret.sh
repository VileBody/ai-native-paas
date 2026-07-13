#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

: "${KUBECONFIG:?KUBECONFIG must point to the dedicated test cluster}"

namespace="ai-native-paas-system"
host="$(terraform -chdir=infra/timeweb output -json control_plane_database_networks | jq -r '.[] | select(.type == "local") | .ips[0].ip')"
port="$(terraform -chdir=infra/timeweb output -raw control_plane_database_port)"
user="$(terraform -chdir=infra/timeweb output -raw control_plane_database_login)"
database="$(terraform -chdir=infra/timeweb output -raw control_plane_database_name)"
password="$(terraform -chdir=infra/timeweb output -raw control_plane_database_password)"

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
