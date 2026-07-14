#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

: "${KUBECONFIG:?KUBECONFIG must point to the dedicated admin cluster}"

selector="ai-native-paas.io/pool=system"
expected="$(kubectl get nodes -l "$selector" --no-headers | wc -l | tr -d ' ')"
if [[ "$expected" -lt 3 ]]; then
  echo "expected at least three system nodes, found $expected" >&2
  exit 1
fi

kubectl taint nodes -l "$selector" ai-native-paas.io/system=true:NoSchedule --overwrite

actual="$(kubectl get nodes -l "$selector" -o json | jq '[.items[] | select(any(.spec.taints[]?; .key == "ai-native-paas.io/system" and .value == "true" and .effect == "NoSchedule"))] | length')"
if [[ "$actual" != "$expected" ]]; then
  echo "system taint gate failed: expected=$expected actual=$actual" >&2
  exit 1
fi

echo "admin node boundary enforced: $actual system nodes tainted"
