#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
profile="${1:-smoke}"
case "${profile}" in
  smoke)
    values="values-smoke.yaml"
    overlay="base"
    ;;
  provider_gate)
    values="values-provider-gate.yaml"
    overlay="provider-gate"
    ;;
  *)
    echo "usage: $0 smoke|provider_gate" >&2
    exit 2
    ;;
esac

root="${repo_root}/deploy/runtime/envoy-gateway"
lock="${root}/images.lock.json"
chart="$(jq -r '.chart.ref' "${lock}")"
version="$(jq -r '.chart.version' "${lock}")"
expected_digest="$(jq -r '.chart.digest' "${lock}")"

actual_digest="$(helm show chart "${chart}" --version "${version}" 2>&1 >/dev/null | sed -n 's/^Digest: //p' | tail -n 1)"
if [[ "${actual_digest}" != "${expected_digest}" ]]; then
  echo "Envoy Gateway chart digest mismatch: expected ${expected_digest}, got ${actual_digest}" >&2
  exit 1
fi

cat "${root}/base/namespace.yaml"
printf -- "---\n"
helm template envoy-gateway "${chart}" \
  --version "${version}" \
  --namespace envoy-gateway-system \
  --include-crds \
  --values "${root}/${values}"
printf -- "---\n"
kubectl kustomize "${root}/${overlay}"
