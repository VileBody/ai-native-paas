#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

for tool in curl jq sha256sum sha512sum tar unzip qemu-img guestfish virt-copy-out xz go; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done

lock="infra/images/workspace/images.lock.json"
output_directory="${1:-dist/workspace-image}"
work_directory="${WORKSPACE_IMAGE_WORK_DIR:-$(mktemp -d)}"
keep_work="${WORKSPACE_IMAGE_KEEP_WORK:-false}"
if [[ "$keep_work" != "true" ]]; then
  trap 'rm -rf "$work_directory"' EXIT
fi
mkdir -p "$output_directory" "$work_directory/downloads" "$work_directory/root/usr/local/bin"

download_sha256() {
  local name="$1"
  local url checksum target
  url="$(jq -er --arg name "$name" '.tools[$name].url' "$lock")"
  checksum="$(jq -er --arg name "$name" '.tools[$name].sha256' "$lock")"
  target="$work_directory/downloads/$name"
  curl --fail --location --silent --show-error "$url" --output "$target"
  printf '%s  %s\n' "$checksum" "$target" | sha256sum --check --status
  printf '%s\n' "$target"
}

base_url="$(jq -er '.base.url' "$lock")"
base_sha512="$(jq -er '.base.sha512' "$lock")"
base="$work_directory/base.qcow2"
curl --fail --location --silent --show-error "$base_url" --output "$base"
printf '%s  %s\n' "$base_sha512" "$base" | sha512sum --check --status

tofu_archive="$(download_sha256 opentofu)"
buildkit_archive="$(download_sha256 buildkit)"
rootlesskit_archive="$(download_sha256 rootlesskit)"
helm_archive="$(download_sha256 helm)"
kustomize_archive="$(download_sha256 kustomize)"
cosign_binary="$(download_sha256 cosign)"
syft_archive="$(download_sha256 syft)"

unzip -q "$tofu_archive" -d "$work_directory/tofu"
tar -xzf "$buildkit_archive" -C "$work_directory"
tar -xzf "$rootlesskit_archive" -C "$work_directory/root/usr/local/bin"
tar -xzf "$helm_archive" -C "$work_directory"
tar -xzf "$kustomize_archive" -C "$work_directory/root/usr/local/bin"
tar -xzf "$syft_archive" -C "$work_directory/root/usr/local/bin" syft
install -m 0755 "$work_directory/tofu/tofu" "$work_directory/root/usr/local/bin/tofu"
install -m 0755 "$work_directory/bin/buildctl" "$work_directory/root/usr/local/bin/buildctl"
install -m 0755 "$work_directory/bin/buildkitd" "$work_directory/root/usr/local/bin/buildkitd"
install -m 0755 "$work_directory/linux-amd64/helm" "$work_directory/root/usr/local/bin/helm"
install -m 0755 "$cosign_binary" "$work_directory/root/usr/local/bin/cosign"
install -m 0755 "$cosign_binary" "$output_directory/cosign"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$work_directory/root/usr/local/bin/workspace-agent" ./cmd/workspace-agent

upstream_dns="${WORKSPACE_IMAGE_DNS:-}"
if [[ -z "$upstream_dns" ]]; then
  upstream_dns="$(awk '$1 == "nameserver" && $2 !~ /^(127\.|::1$)/ { print $2; exit }' \
    /run/systemd/resolve/resolv.conf /etc/resolv.conf 2>/dev/null || true)"
fi
upstream_dns="${upstream_dns:-1.1.1.1}"
if [[ ! "$upstream_dns" =~ ^[0-9a-fA-F:.]+$ ]]; then
  echo "WORKSPACE_IMAGE_DNS must be an IPv4 or IPv6 address" >&2
  exit 1
fi

provision="$work_directory/provision-workspace-image.sh"
cat >"$provision" <<'PROVISION'
#!/usr/bin/env bash
set -euxo pipefail

export DEBIAN_FRONTEND=noninteractive
apt_opts=(-q -y -o Dpkg::Options::=--force-confnew)
rm -f /etc/apt/sources.list
apt-get "${apt_opts[@]}" -o Acquire::Check-Valid-Until=false update
apt-get "${apt_opts[@]}" install \
  ca-certificates git jq make python3 ripgrep uidmap slirp4netns \
  fuse-overlayfs runc
apt-get "${apt_opts[@]}" purge openssh-server || true

id workspace-agent >/dev/null 2>&1 || \
  useradd --uid 1000 --create-home --home-dir /home/workspace-agent \
    --shell /usr/sbin/nologin workspace-agent
install -d -m 0700 -o workspace-agent -g workspace-agent \
  /workspace /var/lib/ai-native-paas /var/lib/ai-native-paas/identity \
  /var/lib/ai-native-paas/journal /home/workspace-agent/.local/share/buildkit
printf 'workspace-agent:100000:65536\n' > /etc/subuid
printf 'workspace-agent:100000:65536\n' > /etc/subgid
printf 'kernel.unprivileged_userns_clone=1\n' > \
  /etc/sysctl.d/90-workspace-rootless.conf
passwd -l root
passwd -l workspace-agent
systemctl disable ssh.service ssh.socket 2>/dev/null || true
systemctl enable ai-native-paas-workspace-agent.service \
  ai-native-paas-buildkit.service
dpkg-query -W -f='${Package}\t${Version}\n' | sort > \
  /usr/share/ai-native-paas/debian-packages.tsv
syft scan dir:/ -o spdx-json=/usr/share/ai-native-paas/sbom.spdx.json
rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb /tmp/* /var/tmp/*
cloud-init clean --logs --machine-id
PROVISION

image="$work_directory/workspace.qcow2"
cp "$base" "$image"
qemu-img resize "$image" 40G >/dev/null

guestfish --network --rw -a "$image" -i <<GUESTFISH
debug sh "ip link set dev eth0 up; ip address replace 169.254.2.15/16 dev eth0; ip route replace default via 169.254.2.2 dev eth0; printf 'nameserver %s\\n' '$upstream_dns' > /etc/resolv.conf; getent ahostsv4 snapshot.debian.org"
upload infra/images/workspace/debian-snapshot.sources /etc/apt/sources.list.d/debian.sources
mkdir-p /usr/local/bin
copy-in $work_directory/root/usr/local/bin /usr/local
mkdir-p /usr/share/ai-native-paas
upload infra/images/workspace/ai-native-paas-workspace-agent.service /etc/systemd/system/ai-native-paas-workspace-agent.service
upload infra/images/workspace/ai-native-paas-buildkit.service /etc/systemd/system/ai-native-paas-buildkit.service
upload $lock /usr/share/ai-native-paas/workspace-image-inputs.json
upload $provision /tmp/provision-workspace-image.sh
sh "/bin/bash /tmp/provision-workspace-image.sh"
rm-f /etc/resolv.conf
sync
GUESTFISH

raw="$output_directory/ai-native-paas-workspace.raw"
compressed="$raw.xz"
qemu-img convert -f qcow2 -O raw "$image" "$raw"
raw_sha256="$(sha256sum "$raw" | awk '{print $1}')"
raw_size="$(stat -c '%s' "$raw")"
xz --threads=0 --compress --force --keep --check=sha256 -9 "$raw"
compressed_sha256="$(sha256sum "$compressed" | awk '{print $1}')"
rm -f "$raw"
virt-copy-out -a "$image" /usr/share/ai-native-paas/sbom.spdx.json "$output_directory"
revision="$(git rev-parse HEAD)"
jq -n \
  --arg schema 'ai-native-paas.io/workspace-image-result/v1' \
  --arg revision "$revision" \
  --arg raw_sha256 "$raw_sha256" \
  --arg compressed_sha256 "$compressed_sha256" \
  --argjson raw_size "$raw_size" \
  --argjson compressed_size "$(stat -c '%s' "$compressed")" \
  '{schema:$schema,git_revision:$revision,raw_sha256:$raw_sha256,compressed_sha256:$compressed_sha256,raw_size_bytes:$raw_size,compressed_size_bytes:$compressed_size}' \
  > "$output_directory/manifest.json"
sha256sum "$compressed" "$output_directory/manifest.json" > "$output_directory/SHA256SUMS"
printf 'workspace image raw sha256:%s compressed sha256:%s\n' "$raw_sha256" "$compressed_sha256"
