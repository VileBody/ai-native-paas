#!/usr/bin/env bash
# Exercises the Agent Injector's Kubernetes auto-auth using the same projected
# service-account token volume and annotations as workspace-manager. The token
# is tested only for existence; it is never read, printed or copied out.
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
namespace="ai-native-paas-system"
job_name="openbao-workspace-manager-injector-gate"

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
  name: openbao-workspace-manager-injector-gate
  labels:
    app.kubernetes.io/name: openbao-workspace-manager-injector-gate
    app.kubernetes.io/part-of: ai-native-paas
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 300
  template:
    metadata:
      labels:
        app.kubernetes.io/name: openbao-workspace-manager-injector-gate
      annotations:
        vault.hashicorp.com/agent-inject: "true"
        vault.hashicorp.com/agent-inject-token: "true"
        vault.hashicorp.com/agent-pre-populate-only: "true"
        vault.hashicorp.com/role: workspace-manager
        vault.hashicorp.com/agent-service-account-token-volume-name: openbao-service-account-token
        vault.hashicorp.com/service: https://openbao-active.openbao.svc:8200
        vault.hashicorp.com/tls-secret: openbao-client-ca
        vault.hashicorp.com/ca-cert: /vault/tls/ca.crt
        vault.hashicorp.com/agent-run-as-user: "100"
        vault.hashicorp.com/agent-run-as-group: "1000"
        vault.hashicorp.com/agent-set-security-context: "true"
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
              test -s /vault/secrets/token
              test "$(stat -c '%a' /vault/secrets/token 2>/dev/null || stat -f '%Lp' /vault/secrets/token)" -le 640
              printf 'workspace-manager OpenBao injector auth passed\n'
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
            privileged: false
            runAsNonRoot: true
            seccompProfile:
              type: RuntimeDefault
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
YAML

if ! "${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${namespace}" wait \
  --for=condition=complete "job/${job_name}" --timeout=3m; then
  "${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${namespace}" logs "job/${job_name}" --all-containers=true >&2 || true
  exit 1
fi

"${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${namespace}" logs "job/${job_name}" -c gate
