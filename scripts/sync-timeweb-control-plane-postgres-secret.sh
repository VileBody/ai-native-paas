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
database_url="host=$host port=$port user=$user password=$password dbname=$database sslmode=disable"

kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace "$namespace" ai-native-paas.io/owner=control-plane --overwrite
manifest="$(jq -nc \
  --arg namespace "$namespace" \
  --arg host "$host" \
  --arg port "$port" \
  --arg user "$user" \
  --arg password "$password" \
  --arg database "$database" \
  --arg database_url "$database_url" \
  '{apiVersion:"v1",kind:"Secret",metadata:{name:"control-plane-postgres",namespace:$namespace,labels:{"ai-native-paas.io/owner":"control-plane"}},type:"Opaque",stringData:{PGHOST:$host,PGPORT:$port,PGUSER:$user,PGPASSWORD:$password,PGDATABASE:$database,PGSSLMODE:"disable",DATABASE_URL:$database_url}}')"
printf '%s' "$manifest" \
  | kubectl apply --server-side --force-conflicts --field-manager=ai-native-paas-postgres-sync -f - >/dev/null
kubectl -n "$namespace" annotate secret control-plane-postgres kubectl.kubernetes.io/last-applied-configuration- >/dev/null 2>&1 || true

jq -nc --arg database_url "$database_url" \
  '{stringData:{DATABASE_URL:$database_url}}' \
  | kubectl -n "$namespace" patch secret state-service-secrets \
      --type merge --patch-file /dev/stdin >/dev/null

kubectl -n "$namespace" rollout restart deployment/state-service >/dev/null
kubectl -n "$namespace" rollout status deployment/state-service --timeout=180s

unset manifest database_url password
