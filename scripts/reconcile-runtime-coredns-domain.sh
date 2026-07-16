#!/usr/bin/env bash
set -Eeuo pipefail

domain="${RUNTIME_DNS_DOMAIN:-cozy.local}"
kubeconfig="${KUBECONFIG:-}"
server="${RUNTIME_KUBE_SERVER:-}"

kubectl_args=(--request-timeout=60s)
if [[ -n "${kubeconfig}" ]]; then
  kubectl_args+=(--kubeconfig="${kubeconfig}")
fi
if [[ -n "${server}" ]]; then
  kubectl_args+=(--server="${server}")
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

"${KUBECTL:-kubectl}" "${kubectl_args[@]}" -n kube-system get configmap coredns -o json \
  >"${tmpdir}/coredns.json"

python3 - "${domain}" "${tmpdir}/coredns.json" "${tmpdir}/Corefile" <<'PY'
import json
import re
import sys

domain, src, dst = sys.argv[1:]
with open(src, encoding="utf-8") as fh:
    obj = json.load(fh)

corefile = obj.get("data", {}).get("Corefile", "")
patched, count = re.subn(
    r"(?m)^(\s*kubernetes\s+)(\S+)(.*)$",
    rf"\g<1>{domain}\g<3>",
    corefile,
    count=1,
)
if count != 1:
    raise SystemExit("CoreDNS Corefile does not contain a kubernetes plugin line")
if f"kubernetes {domain}" not in patched:
    raise SystemExit(f"CoreDNS Corefile was not patched to kubernetes {domain}")

with open(dst, "w", encoding="utf-8") as fh:
    fh.write(patched)
PY

"${KUBECTL:-kubectl}" "${kubectl_args[@]}" -n kube-system create configmap coredns \
  --from-file=Corefile="${tmpdir}/Corefile" \
  --dry-run=client \
  -o yaml \
  | "${KUBECTL:-kubectl}" "${kubectl_args[@]}" apply -f -

if "${KUBECTL:-kubectl}" "${kubectl_args[@]}" -n kube-system rollout restart deployment/coredns; then
  "${KUBECTL:-kubectl}" "${kubectl_args[@]}" -n kube-system rollout status deployment/coredns --timeout=180s
else
  "${KUBECTL:-kubectl}" "${kubectl_args[@]}" -n kube-system delete pod -l k8s-app=kube-dns --ignore-not-found
  "${KUBECTL:-kubectl}" "${kubectl_args[@]}" -n kube-system wait pod -l k8s-app=kube-dns --for=condition=Ready --timeout=180s
fi

"${KUBECTL:-kubectl}" "${kubectl_args[@]}" -n kube-system get configmap coredns \
  -o 'jsonpath={.data.Corefile}' \
  | grep -q "kubernetes ${domain}"

echo "CoreDNS kubernetes plugin reconciled to ${domain}"
