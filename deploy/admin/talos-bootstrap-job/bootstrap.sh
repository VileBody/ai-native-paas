#!/usr/bin/env bash
set -Eeuo pipefail

umask 077
readonly work=/work
if [[ "${TRANSPARENT_DNAT:-false}" == true ]]; then
  readonly cp1_endpoint="192.168.74.11"
  readonly cp2_endpoint="192.168.74.12"
  readonly cp3_endpoint="192.168.74.13"
else
  readonly public_ip=72.56.234.22
  readonly cp1_endpoint="${public_ip}:50011"
  readonly cp2_endpoint="${public_ip}:50012"
  readonly cp3_endpoint="${public_ip}:50013"
fi
readonly cp1_node="192.168.74.11"
readonly cp2_node="192.168.74.12"
readonly cp3_node="192.168.74.13"

for name in BUNDLE_URL BUNDLE_SHA256 BUNDLE_PASSPHRASE EVIDENCE_UPLOAD_URL BOOTSTRAP_GENERATION; do
  test -n "${!name:-}" || { echo "missing required bootstrap input: $name" >&2; exit 1; }
done
[[ "$BUNDLE_SHA256" =~ ^[0-9a-f]{64}$ ]]
[[ "$BOOTSTRAP_GENERATION" =~ ^[1-9][0-9]*$ ]]
test "${#BUNDLE_PASSPHRASE}" -ge 32

cleanup() {
  find "$work" -type f -exec shred -fuz {} + 2>/dev/null || true
  rm -rf "$work"/*
}
trap cleanup EXIT
mkdir -p "$work/input"

if [[ ! -s "$work/bootstrap.enc" ]]; then
  curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
    --retry 8 --retry-all-errors --connect-timeout 10 --max-time 300 \
    "$BUNDLE_URL" --output "$work/bootstrap.enc"
fi
printf '%s  %s\n' "$BUNDLE_SHA256" "$work/bootstrap.enc" | sha256sum --check --strict
printf '%s' "$BUNDLE_PASSPHRASE" \
  | openssl enc -d -aes-256-cbc -pbkdf2 -iter 600000 -md sha256 \
      -pass stdin -in "$work/bootstrap.enc" -out "$work/bootstrap.tar.gz"

while IFS= read -r entry; do
  case "$entry" in
    /*|../*|*/../*|*/..) echo "unsafe bootstrap archive path" >&2; exit 1 ;;
  esac
done < <(tar --list --gzip --file "$work/bootstrap.tar.gz")
tar --extract --gzip --file "$work/bootstrap.tar.gz" --directory "$work/input" \
  --no-same-owner --no-same-permissions
(cd "$work/input" && sha256sum --check --strict MANIFEST.sha256)

talosctl_bin=talosctl
if [[ -x "$work/talosctl" ]]; then
  talosctl_bin="$work/talosctl"
fi

config_apply_mode="${CONFIG_APPLY_MODE:-insecure}"
if [[ "${CONFIG_ALREADY_APPLIED:-false}" != true ]]; then
  for mapping in \
    "cp-1=$cp1_node@$cp1_endpoint" \
    "cp-2=$cp2_node@$cp2_endpoint" \
    "cp-3=$cp3_node@$cp3_endpoint"; do
    name="${mapping%%=*}"
    target_and_endpoint="${mapping#*=}"
    target="${target_and_endpoint%%@*}"
    endpoint="${target_and_endpoint#*@}"
    applied=false
    for attempt in $(seq 1 90); do
      if [[ "$config_apply_mode" == secure ]]; then
        apply_command=("$talosctl_bin" --talosconfig "$work/input/talosconfig" \
          --nodes "$target" --endpoints "$endpoint" apply-config \
          --file "$work/input/machine-configs/$name.yaml")
      else
        apply_command=("$talosctl_bin" apply-config --insecure --nodes "$endpoint" \
          --file "$work/input/machine-configs/$name.yaml")
      fi
      if "${apply_command[@]}"; then
        applied=true
        echo "$name-config=applied"
        break
      fi
      sleep 5
    done
    test "$applied" = true
  done
fi

if [[ "${ETCD_ALREADY_BOOTSTRAPPED:-false}" != true ]]; then
  bootstrapped=false
  for attempt in $(seq 1 120); do
    if "$talosctl_bin" --talosconfig "$work/input/talosconfig" \
      --nodes "$cp1_node" --endpoints "$cp1_endpoint" bootstrap; then
      bootstrapped=true
      echo 'etcd-bootstrap=complete'
      break
    fi
    sleep 5
  done
  test "$bootstrapped" = true
fi

kubeconfig_ready=false
for attempt in $(seq 1 120); do
  if "$talosctl_bin" --talosconfig "$work/input/talosconfig" \
    --nodes "$cp1_node" --endpoints "$cp1_endpoint" kubeconfig "$work/kubeconfig" --force; then
    kubeconfig_ready=true
    echo 'kubeconfig=ready'
    break
  fi
  sleep 5
done
test "$kubeconfig_ready" = true

for mapping in \
  "$cp1_node@$cp1_endpoint" \
  "$cp2_node@$cp2_endpoint" \
  "$cp3_node@$cp3_endpoint"; do
  target="${mapping%%@*}"
  endpoint="${mapping#*@}"
  "$talosctl_bin" --talosconfig "$work/input/talosconfig" \
    --nodes "$target" --endpoints "$endpoint" version --short
done

bundle_hash="$(sha256sum "$work/bootstrap.enc" | cut -d' ' -f1)"
kubeconfig_hash="$(sha256sum "$work/kubeconfig" | cut -d' ' -f1)"
completed_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '%s\n' \
  "{\"schema\":\"ai-native-paas.io/talos-bootstrap-evidence/v1\",\"generation\":${BOOTSTRAP_GENERATION},\"nodes\":[\"192.168.74.11\",\"192.168.74.12\",\"192.168.74.13\"],\"bundle_sha256\":\"sha256:${bundle_hash}\",\"kubeconfig_sha256\":\"sha256:${kubeconfig_hash}\",\"completed_at\":\"${completed_at}\",\"success\":true}" \
  >"$work/evidence.json"
cp "$work/input/talosconfig" "$work/talosconfig"
tar --create --gzip --file "$work/result.tar.gz" --directory "$work" \
  evidence.json kubeconfig talosconfig
printf '%s' "$BUNDLE_PASSPHRASE" \
  | openssl enc -aes-256-cbc -salt -pbkdf2 -iter 600000 -md sha256 \
      -pass stdin -in "$work/result.tar.gz" -out "$work/result.enc"
if [[ "${UPLOAD_EXTERNAL:-false}" == logs ]]; then
  printf 'TALOS_BOOTSTRAP_RESULT_BASE64='
  base64 "$work/result.enc" | tr -d '\n'
  printf '\n'
elif [[ "${UPLOAD_EXTERNAL:-false}" == true ]]; then
  touch "$work/result.ready"
  for attempt in $(seq 1 120); do
    [[ -e "$work/upload.complete" ]] && break
    sleep 1
  done
  test -e "$work/upload.complete"
else
  curl --fail --silent --show-error --request PUT --upload-file "$work/result.enc" \
    --proto '=https' --tlsv1.2 --connect-timeout 10 --max-time 300 \
    "$EVIDENCE_UPLOAD_URL"
fi
echo "TALOS_BOOTSTRAP_JOB=PASS generation=$BOOTSTRAP_GENERATION"
