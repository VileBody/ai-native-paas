#!/usr/bin/env bash
set -Eeuo pipefail
export COPYFILE_DISABLE=1

usage() {
  echo "usage: $0 <cozystack-stack-dir> <output.enc> <passphrase-file>" >&2
  exit 2
}

[[ $# -eq 3 ]] || usage
stack_dir="$1"
output="$2"
passphrase_file="$3"

for command in tofu jq openssl sha256sum tar; do
  command -v "$command" >/dev/null || { echo "missing command: $command" >&2; exit 1; }
done
[[ -d "$stack_dir" ]] || usage
if [[ ! -e "$passphrase_file" ]]; then
  mkdir -p "$(dirname "$passphrase_file")"
  umask 077
  openssl rand -base64 48 >"$passphrase_file"
fi
[[ -f "$passphrase_file" ]] || usage
mode="$(stat -f '%Lp' "$passphrase_file" 2>/dev/null || stat -c '%a' "$passphrase_file")"
if (( 10#$mode % 100 != 0 )); then
  echo "passphrase file must not grant group/other permissions" >&2
  exit 1
fi
if [[ $(wc -c <"$passphrase_file" | tr -d ' ') -lt 33 ]]; then
  echo "passphrase file must contain at least 32 characters plus newline" >&2
  exit 1
fi

tmp="$(mktemp -d "${TMPDIR:-/tmp}/paas-talos-bundle.XXXXXX")"
cleanup() {
  find "$tmp" -type f -exec rm -P {} + 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT
mkdir -p "$tmp/input/machine-configs"

(cd "$stack_dir" && tofu output -json bootstrap_bundle_inputs) >"$tmp/inputs.json"
if [[ $(jq -r 'type' "$tmp/inputs.json") != object ]]; then
  echo "bootstrap_bundle_inputs is unavailable; create the live nodes first" >&2
  exit 1
fi
names="$(jq -r '.machine_configurations | keys | join(" ")' "$tmp/inputs.json")"
if [[ "$names" != "cp-1 cp-2 cp-3" ]]; then
	echo "unexpected Talos machine configuration set: $names" >&2
	exit 1
fi
for name in cp-1 cp-2 cp-3; do
  jq -er --arg name "$name" '.machine_configurations[$name]' "$tmp/inputs.json" >"$tmp/input/machine-configs/$name.yaml"
done
jq -er '.talos_config' "$tmp/inputs.json" >"$tmp/input/talosconfig"
if [[ $(jq -c '.nodes' "$tmp/inputs.json") != '{"cp-1":"192.168.74.11","cp-2":"192.168.74.12","cp-3":"192.168.74.13"}' ]]; then
  echo "bootstrap node identities do not match the reviewed private addresses" >&2
  exit 1
fi

(
  cd "$tmp/input"
  sha256sum machine-configs/cp-1.yaml machine-configs/cp-2.yaml machine-configs/cp-3.yaml talosconfig >MANIFEST.sha256
  tar --create --gzip --file "$tmp/bootstrap.tar.gz" MANIFEST.sha256 machine-configs talosconfig
)
mkdir -p "$(dirname "$output")"
openssl enc -aes-256-cbc -salt -pbkdf2 -iter 600000 -md sha256 \
  -pass "file:$passphrase_file" -in "$tmp/bootstrap.tar.gz" -out "$output"
chmod 0600 "$output"
digest="$(sha256sum "$output" | cut -d' ' -f1)"
printf 'encrypted_bundle=%s\nsha256=%s\n' "$output" "$digest"
