#!/usr/bin/env bash
# Exercise the real private Harbor registry path without ever returning an
# administrative or robot secret to the caller. The short-lived Job receives
# the Harbor admin password from its existing Kubernetes Secret, creates one
# project-scoped robot, pushes an OCI manifest through the registry API, reads
# it back, revokes the robot and removes the project.
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
namespace="harbor-system"
job_name="harbor-live-oci-gate"
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

cleanup

"${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${namespace}" apply -f - >/dev/null <<'YAML'
apiVersion: batch/v1
kind: Job
metadata:
  name: harbor-live-oci-gate
  labels:
    app.kubernetes.io/name: harbor-live-oci-gate
    app.kubernetes.io/part-of: ai-native-paas
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 300
  template:
    metadata:
      labels:
        app.kubernetes.io/name: harbor-live-oci-gate
    spec:
      automountServiceAccountToken: false
      restartPolicy: Never
      securityContext:
        fsGroup: 10000
        runAsUser: 10000
        runAsNonRoot: true
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
          image: goharbor/harbor-core:v2.15.1
          imagePullPolicy: IfNotPresent
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
            privileged: false
            runAsNonRoot: true
            seccompProfile:
              type: RuntimeDefault
          env:
            - name: HARBOR_ADMIN_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: harbor-admin
                  key: HARBOR_ADMIN_PASSWORD
          command:
            - /usr/bin/bash
            - -ec
            - |
              set -Eeuo pipefail

              api_base="https://harbor.harbor-system.svc/api/v2.0"
              registry_base="https://harbor.harbor-system.svc"
              project="platform-live-gate-$(date +%s)"
              repository="artifact"
              work_dir="$(mktemp -d)"
              robot_id=""
              project_created=false
              robot_revoked=false

              cleanup() {
                if [[ -n "${robot_id}" && "${robot_revoked}" != true ]]; then
                  curl --fail-with-body --silent --show-error --insecure \
                    --netrc-file "${work_dir}/admin.netrc" \
                    --output /dev/null \
                    --request DELETE \
                    "${api_base}/robots/${robot_id}" || true
                fi
                if [[ "${project_created}" == true ]]; then
                  curl --fail-with-body --silent --show-error --insecure \
                    --netrc-file "${work_dir}/admin.netrc" \
                    --output /dev/null \
                    --request DELETE \
                    "${api_base}/projects/${project}/repositories/${repository}" || true
                  curl --fail-with-body --silent --show-error --insecure \
                    --netrc-file "${work_dir}/admin.netrc" \
                    --output /dev/null \
                    --request DELETE \
                    "${api_base}/projects/${project}" || true
                fi
                rm -rf "${work_dir}"
              }
              trap cleanup EXIT

              expect_code() {
                local label="$1"
                local actual="$2"
                shift 2
                local expected
                for expected in "$@"; do
                  if [[ "${actual}" == "${expected}" ]]; then
                    return 0
                  fi
                done
                printf '%s returned HTTP %s\n' "${label}" "${actual}" >&2
                exit 1
              }

              cat >"${work_dir}/admin.netrc" <<NETRC
              machine harbor.harbor-system.svc
              login admin
              password ${HARBOR_ADMIN_PASSWORD}
              NETRC
              chmod 600 "${work_dir}/admin.netrc"

              project_code="$(curl --silent --show-error --insecure \
                --netrc-file "${work_dir}/admin.netrc" \
                --output "${work_dir}/project.json" \
                --write-out '%{http_code}' \
                --header 'Content-Type: application/json' \
                --request POST \
                --data "{\"project_name\":\"${project}\",\"public\":false}" \
                "${api_base}/projects")"
              expect_code 'project creation' "${project_code}" 201
              project_created=true

              robot_payload="{\"name\":\"oci-gate\",\"description\":\"one-shot private OCI gate\",\"duration\":1,\"disable\":false,\"level\":\"project\",\"permissions\":[{\"kind\":\"project\",\"namespace\":\"${project}\",\"access\":[{\"resource\":\"repository\",\"action\":\"pull\"},{\"resource\":\"repository\",\"action\":\"push\"}]}]}"
              robot_code="$(curl --silent --show-error --insecure \
                --netrc-file "${work_dir}/admin.netrc" \
                --output "${work_dir}/robot.json" \
                --write-out '%{http_code}' \
                --header 'Content-Type: application/json' \
                --request POST \
                --data "${robot_payload}" \
                "${api_base}/robots")"
              expect_code 'project robot creation' "${robot_code}" 201

              robot_id="$(sed -nE 's/.*"id"[[:space:]]*:[[:space:]]*([0-9]+).*/\1/p' "${work_dir}/robot.json")"
              robot_name="$(sed -nE 's/.*"name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' "${work_dir}/robot.json")"
              robot_secret="$(sed -nE 's/.*"secret"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' "${work_dir}/robot.json")"
              test -n "${robot_id}"
              test -n "${robot_name}"
              test -n "${robot_secret}"

              cat >"${work_dir}/robot.netrc" <<NETRC
              machine harbor.harbor-system.svc
              login ${robot_name}
              password ${robot_secret}
              NETRC
              chmod 600 "${work_dir}/robot.netrc"

              auth_code="$(curl --silent --show-error --insecure \
                --netrc-file "${work_dir}/robot.netrc" \
                --output "${work_dir}/registry-token.json" \
                --write-out '%{http_code}' \
                "${registry_base}/service/token?service=harbor-registry&scope=repository:${project}/${repository}:pull,push")"
              expect_code 'registry token issue' "${auth_code}" 200
              registry_token="$(sed -nE 's/.*"(token|access_token)"[[:space:]]*:[[:space:]]*"([^"]+)".*/\2/p' "${work_dir}/registry-token.json")"
              test -n "${registry_token}"

              cat >"${work_dir}/registry.curlrc" <<CURLRC
              insecure
              header = "Authorization: Bearer ${registry_token}"
              CURLRC
              chmod 600 "${work_dir}/registry.curlrc"

              printf '{}' >"${work_dir}/config.json"
              config_size="$(wc -c <"${work_dir}/config.json")"
              config_size="${config_size//[[:space:]]/}"
              config_sum="$(sha256sum "${work_dir}/config.json")"
              config_digest="sha256:${config_sum%% *}"

              upload_code="$(curl --silent --show-error --insecure \
                --config "${work_dir}/registry.curlrc" \
                --dump-header "${work_dir}/upload.headers" \
                --output /dev/null \
                --write-out '%{http_code}' \
                --request POST \
                "${registry_base}/v2/${project}/${repository}/blobs/uploads/")"
              expect_code 'blob upload start' "${upload_code}" 202
              upload_location="$(sed -nE 's/^([Ll]ocation:)[[:space:]]*(.*)\r?$/\2/p' "${work_dir}/upload.headers")"
              upload_location="${upload_location%$'\r'}"
              test -n "${upload_location}"

              separator='?'
              if [[ "${upload_location}" == *'?'* ]]; then
                separator='&'
              fi
              upload_url="${upload_location}"
              if [[ "${upload_url}" != http://* && "${upload_url}" != https://* ]]; then
                upload_url="${registry_base}${upload_url}"
              fi
              upload_code="$(curl --silent --show-error --insecure \
                --config "${work_dir}/registry.curlrc" \
                --http1.1 \
                --output /dev/null \
                --write-out '%{http_code}' \
                --header 'Content-Type: application/octet-stream' \
                --header 'Expect:' \
                --upload-file "${work_dir}/config.json" \
                "${upload_url}${separator}digest=${config_digest}")"
              expect_code 'blob upload complete' "${upload_code}" 201

              cat >"${work_dir}/manifest.json" <<MANIFEST
              {"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"${config_digest}","size":${config_size}},"layers":[]}
              MANIFEST
              manifest_code="$(curl --silent --show-error --insecure \
                --config "${work_dir}/registry.curlrc" \
                --output /dev/null \
                --write-out '%{http_code}' \
                --header 'Content-Type: application/vnd.oci.image.manifest.v1+json' \
                --request PUT \
                --data-binary "@${work_dir}/manifest.json" \
                "${registry_base}/v2/${project}/${repository}/manifests/live")"
              expect_code 'manifest push' "${manifest_code}" 201

              manifest_code="$(curl --silent --show-error --insecure \
                --config "${work_dir}/registry.curlrc" \
                --output "${work_dir}/manifest-readback.json" \
                --write-out '%{http_code}' \
                --header 'Accept: application/vnd.oci.image.manifest.v1+json' \
                "${registry_base}/v2/${project}/${repository}/manifests/live")"
              expect_code 'manifest readback' "${manifest_code}" 200
              grep -Fq "${config_digest}" "${work_dir}/manifest-readback.json"

              blob_code="$(curl --silent --show-error --insecure \
                --config "${work_dir}/registry.curlrc" \
                --output "${work_dir}/config-readback.json" \
                --write-out '%{http_code}' \
                "${registry_base}/v2/${project}/${repository}/blobs/${config_digest}")"
              expect_code 'blob readback' "${blob_code}" 200
              readback_sum="$(sha256sum "${work_dir}/config-readback.json")"
              test "${config_sum%% *}" = "${readback_sum%% *}"

              robot_delete_code="$(curl --silent --show-error --insecure \
                --netrc-file "${work_dir}/admin.netrc" \
                --output /dev/null \
                --write-out '%{http_code}' \
                --request DELETE \
                "${api_base}/robots/${robot_id}")"
              expect_code 'robot revoke' "${robot_delete_code}" 200 204
              robot_revoked=true

              revoked_code="$(curl --silent --show-error --insecure \
                --netrc-file "${work_dir}/robot.netrc" \
                --output "${work_dir}/revoked-token.json" \
                --write-out '%{http_code}' \
                "${registry_base}/service/token?service=harbor-registry&scope=repository:${project}/${repository}:pull")"
              if [[ "${revoked_code}" == 200 ]]; then
                revoked_token="$(sed -nE 's/.*"(token|access_token)"[[:space:]]*:[[:space:]]*"([^"]+)".*/\2/p' "${work_dir}/revoked-token.json")"
                test -n "${revoked_token}"
                printf '%s\n' insecure "header = \"Authorization: Bearer ${revoked_token}\"" >"${work_dir}/revoked.curlrc"
                chmod 600 "${work_dir}/revoked.curlrc"
                revoked_access_code="$(curl --silent --show-error --insecure \
                  --config "${work_dir}/revoked.curlrc" \
                  --output /dev/null \
                  --write-out '%{http_code}' \
                  --header 'Accept: application/vnd.oci.image.manifest.v1+json' \
                  "${registry_base}/v2/${project}/${repository}/manifests/live")"
                expect_code 'revoked robot artifact access' "${revoked_access_code}" 401 403 404
              else
                expect_code 'revoked robot token request' "${revoked_code}" 401
              fi

              repository_delete_code="$(curl --silent --show-error --insecure \
                --netrc-file "${work_dir}/admin.netrc" \
                --output /dev/null \
                --write-out '%{http_code}' \
                --request DELETE \
                "${api_base}/projects/${project}/repositories/${repository}")"
              expect_code 'test repository deletion' "${repository_delete_code}" 200 204

              project_delete_code="$(curl --silent --show-error --insecure \
                --netrc-file "${work_dir}/admin.netrc" \
                --output /dev/null \
                --write-out '%{http_code}' \
                --request DELETE \
                "${api_base}/projects/${project}")"
              expect_code 'test project deletion' "${project_delete_code}" 200 204
              project_created=false

              printf 'Harbor private OCI/S3 round trip and robot revocation passed\n'
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 250m
              memory: 256Mi
YAML

if ! "${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${namespace}" wait \
  --for=condition=complete "job/${job_name}" --timeout=5m; then
  "${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${namespace}" logs "job/${job_name}" >&2 || true
  exit 1
fi

"${kubectl_bin}" --kubeconfig "${kubeconfig}" -n "${namespace}" logs "job/${job_name}"
