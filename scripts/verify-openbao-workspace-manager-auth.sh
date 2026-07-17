#!/usr/bin/env bash
# Proves the live Kubernetes-auth scope expected by workspace-manager without
# issuing a certificate, reading a credential envelope, or printing a token.
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
namespace="ai-native-paas-system"
job_name="openbao-workspace-manager-auth-gate"

kubectl_bin="${KUBECTL_BIN:-kubectl}"
if ! command -v "${kubectl_bin}" >/dev/null 2>&1 && [[ -x /opt/homebrew/bin/kubectl ]]; then
  kubectl_bin=/opt/homebrew/bin/kubectl
fi
command -v "${kubectl_bin}" >/dev/null 2>&1 || {
  printf 'kubectl is required; set KUBECTL_BIN to its absolute path\n' >&2
  exit 1
}

cleanup() {
  "${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${namespace}" delete job "${job_name}" \
    --ignore-not-found --wait >/dev/null 2>&1 || true
}
trap cleanup EXIT

"${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${namespace}" get secret openbao-client-ca >/dev/null
cleanup

"${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${namespace}" apply -f - >/dev/null <<'YAML'
apiVersion: batch/v1
kind: Job
metadata:
  name: openbao-workspace-manager-auth-gate
  labels:
    app.kubernetes.io/name: openbao-workspace-manager-auth-gate
    app.kubernetes.io/part-of: ai-native-paas
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 300
  template:
    metadata:
      labels:
        app.kubernetes.io/name: openbao-workspace-manager-auth-gate
    spec:
      serviceAccountName: workspace-manager
      automountServiceAccountToken: false
      restartPolicy: Never
      securityContext:
        runAsUser: 100
        runAsGroup: 1000
        runAsNonRoot: true
        fsGroup: 1000
        seccompProfile:
          type: RuntimeDefault
      nodeSelector:
        ai-native-paas.io/pool: system
      tolerations:
        - key: ai-native-paas.io/system
          operator: Equal
          value: "true"
          effect: NoSchedule
      containers:
        - name: gate
          image: quay.io/openbao/openbao:2.5.5@sha256:6150c4a6b62067db6141c8da7a6a6b5763f4f47c315343d0c848b40fecdfd452
          imagePullPolicy: IfNotPresent
          command:
            - /bin/sh
            - -ec
            - |
              set -eu
              umask 077
              export HOME=/tmp/bao-home
              mkdir -p "${HOME}"
              export BAO_ADDR=https://openbao-active.openbao.svc:8200
              export BAO_CACERT=/openbao/ca/ca.crt

              bao write -format=json auth/kubernetes/login \
                role=workspace-manager \
                jwt=@/var/run/secrets/kubernetes.io/serviceaccount/token > /tmp/login.json
              token="$(sed -nE 's/.*"client_token"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' /tmp/login.json)"
              test -n "${token}"
              printf '%s' "${token}" >"${HOME}/.vault-token"
              chmod 600 "${HOME}/.vault-token"

              bao token capabilities workspace-pki/issue/workspace-agent > /tmp/pki-capabilities
              grep -qx update /tmp/pki-capabilities
              bao token capabilities workspace-credentials/data/tenant/project/environment > /tmp/credential-capabilities
              grep -qx read /tmp/credential-capabilities
              bao token capabilities transit/sign/build-signing-v1 > /tmp/transit-capabilities
              grep -qx deny /tmp/transit-capabilities
              bao token revoke -self >/dev/null
              printf 'workspace-manager OpenBao Kubernetes auth scope passed\n'
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
            privileged: false
            runAsNonRoot: true
            seccompProfile:
              type: RuntimeDefault
          volumeMounts:
            - name: openbao-service-account-token
              mountPath: /var/run/secrets/kubernetes.io/serviceaccount
              readOnly: true
            - name: openbao-client-ca
              mountPath: /openbao/ca
              readOnly: true
          resources:
            requests:
              cpu: 25m
              memory: 32Mi
            limits:
              cpu: 100m
              memory: 128Mi
      volumes:
        - name: openbao-service-account-token
          projected:
            defaultMode: 288
            sources:
              - serviceAccountToken:
                  audience: openbao
                  expirationSeconds: 900
                  path: token
              - configMap:
                  name: kube-root-ca.crt
                  items:
                    - key: ca.crt
                      path: ca.crt
              - downwardAPI:
                  items:
                    - fieldRef:
                        apiVersion: v1
                        fieldPath: metadata.namespace
                      path: namespace
        - name: openbao-client-ca
          secret:
            secretName: openbao-client-ca
YAML

if ! "${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${namespace}" wait \
  --for=condition=complete "job/${job_name}" --timeout=3m; then
  "${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${namespace}" logs "job/${job_name}" >&2 || true
  exit 1
fi

"${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${namespace}" logs "job/${job_name}"
