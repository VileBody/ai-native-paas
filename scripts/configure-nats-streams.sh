#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kubeconfig="${KUBECONFIG:-${repo_root}/infra/timeweb/ai-native-paas-test.kubeconfig}"
replicas="${NATS_STREAM_REPLICAS:-3}"
if [[ "${replicas}" != "1" && "${replicas}" != "3" ]]; then
  echo "NATS_STREAM_REPLICAS must be 1 (dev) or 3 (ha)" >&2
  exit 1
fi

nats=(kubectl --kubeconfig "${kubeconfig}" -n nats exec deployment/nats-box -- nats --context default)

reconcile_stream() {
  local name="$1"
  shift
  local subjects=("$@")
  local subject_args=()
  local subject
  for subject in "${subjects[@]}"; do
    subject_args+=(--subjects "${subject}")
  done
  local common=(
    "${subject_args[@]}"
    --storage file
    --replicas "${replicas}"
    --retention limits
    --discard old
    --max-age 168h
    --max-msg-size 8388608
    --dupe-window 2m
    --deny-delete
    --deny-purge
  )
  if "${nats[@]}" stream info "${name}" --json >/dev/null 2>&1; then
    "${nats[@]}" stream edit "${name}" "${common[@]}" --force >/dev/null
  else
    "${nats[@]}" stream add "${name}" "${common[@]}" --defaults >/dev/null
  fi
  "${nats[@]}" stream info "${name}" --json
}

reconcile_stream PLATFORM_EVENTS source.\> build.\> runtime.\> attachments.\> commerce.\> agent.\>
reconcile_stream PLATFORM_OPERATIONS kernel.\> operation.\>
reconcile_stream WORKSPACE_COMMANDS workspace.\>
reconcile_stream PLATFORM_USAGE usage.\>
