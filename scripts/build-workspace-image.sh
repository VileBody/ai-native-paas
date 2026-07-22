#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

for tool in curl docker jq sha256sum sha512sum tar unzip qemu-img guestfish virt-copy-out xz go; do
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

package_resolver_image="$(jq -er '.package_resolver.image' "$lock")"
package_downloads="$work_directory/debian-packages"
package_status="$work_directory/base-package-status"
mkdir -p "$package_downloads" "$package_status"
virt-copy-out -a "$base" /var/lib/dpkg/status "$package_status"
docker run --rm --platform linux/amd64 \
  --mount "type=bind,source=$PWD/infra/images/workspace/debian-snapshot.sources,target=/etc/apt/sources.list.d/debian.sources,readonly" \
  --mount "type=bind,source=/etc/ssl/certs,target=/etc/ssl/certs,readonly" \
  --mount "type=bind,source=$package_status/status,target=/var/lib/dpkg/status,readonly" \
  --mount "type=bind,source=$package_downloads,target=/out" \
  "$package_resolver_image" /bin/bash -euxo pipefail -c '
    rm -f /etc/apt/sources.list
    apt-get -o Acquire::Check-Valid-Until=false update
    apt-get -q -y --download-only --reinstall install \
      ca-certificates git jq make python3 ripgrep uidmap slirp4netns \
      fuse-overlayfs runc
    cp /var/cache/apt/archives/*.deb /out/
  '
if ! compgen -G "$package_downloads/*.deb" >/dev/null; then
  echo "Debian package resolver produced no archives" >&2
  exit 1
fi
(
  cd "$package_downloads"
  sha256sum ./*.deb | sort -k2 > SHA256SUMS
)

provision="$work_directory/provision-workspace-image.sh"
cat >"$provision" <<'PROVISION'
#!/usr/bin/env bash
set -euxo pipefail

export DEBIAN_FRONTEND=noninteractive
rm -f /etc/apt/sources.list
dpkg --force-confnew --unpack /tmp/debian-packages/*.deb
dpkg --force-confnew --configure -a
dpkg --purge openssh-server || true

getent group workspace-agent >/dev/null 2>&1 || groupadd --gid 1000 workspace-agent
id workspace-agent >/dev/null 2>&1 || \
  useradd --uid 1000 --gid workspace-agent --create-home \
    --home-dir /home/workspace-agent --shell /usr/sbin/nologin workspace-agent
getent group workspace-task >/dev/null 2>&1 || groupadd --gid 1001 workspace-task
id workspace-task >/dev/null 2>&1 || \
  useradd --uid 1001 --gid workspace-task --create-home \
    --home-dir /home/workspace-task --shell /usr/sbin/nologin workspace-task
getent group workspace-verified >/dev/null 2>&1 || groupadd --gid 1002 workspace-verified
id workspace-verified >/dev/null 2>&1 || \
  useradd --uid 1002 --gid workspace-verified --create-home \
    --home-dir /home/workspace-verified --shell /usr/sbin/nologin workspace-verified
getent group workspace-shared >/dev/null 2>&1 || groupadd --gid 2000 workspace-shared
usermod --append --groups workspace-shared workspace-agent
usermod --append --groups workspace-shared workspace-task
usermod --append --groups workspace-shared workspace-verified
[ "$(id -u workspace-agent)" = 1000 ]
[ "$(id -g workspace-agent)" = 1000 ]
[ "$(id -u workspace-task)" = 1001 ]
[ "$(id -g workspace-task)" = 1001 ]
[ "$(id -u workspace-verified)" = 1002 ]
[ "$(id -g workspace-verified)" = 1002 ]
[ "$(getent group workspace-shared | cut -d: -f3)" = 2000 ]
install -d -m 0750 -o root -g workspace-agent /var/lib/ai-native-paas
install -d -m 2750 -o root -g workspace-agent /var/lib/ai-native-paas/identity
install -d -m 0770 -o root -g workspace-agent /var/lib/ai-native-paas/journal
install -d -m 0770 -o workspace-task -g workspace-shared /workspace
install -d -m 0700 -o workspace-task -g workspace-task \
  /home/workspace-task/.local/share/buildkit
install -d -m 0700 -o workspace-verified -g workspace-verified /home/workspace-verified
printf 'workspace-task:100000:65536\n' > /etc/subuid
printf 'workspace-task:100000:65536\n' > /etc/subgid
printf 'kernel.unprivileged_userns_clone=1\n' > \
  /etc/sysctl.d/90-workspace-rootless.conf
passwd -l root
passwd -l workspace-agent
passwd -l workspace-task
passwd -l workspace-verified
systemctl disable ssh.service ssh.socket 2>/dev/null || true
systemctl enable ai-native-paas-workspace-agent.service \
  ai-native-paas-buildkit.service
dpkg-query -W -f='${Package}\t${Version}\n' | sort > \
  /usr/share/ai-native-paas/debian-packages.tsv
/usr/local/bin/syft scan dir:/ -o spdx-json=/usr/share/ai-native-paas/sbom.spdx.json
rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb /tmp/* /var/tmp/*
cloud-init clean --logs --machine-id
PROVISION

image="$work_directory/workspace.qcow2"
cp "$base" "$image"
qemu-img resize "$image" 40G >/dev/null

guestfish --rw -a "$image" -i <<GUESTFISH
upload infra/images/workspace/debian-snapshot.sources /etc/apt/sources.list.d/debian.sources
mkdir-p /usr/local/bin
copy-in "$work_directory/root/usr/local/bin" /usr/local
mkdir-p /usr/share/ai-native-paas
upload infra/images/workspace/ai-native-paas-workspace-agent.service /etc/systemd/system/ai-native-paas-workspace-agent.service
upload infra/images/workspace/ai-native-paas-buildkit.service /etc/systemd/system/ai-native-paas-buildkit.service
upload "$lock" /usr/share/ai-native-paas/workspace-image-inputs.json
copy-in "$package_downloads" /tmp
upload "$package_downloads/SHA256SUMS" /usr/share/ai-native-paas/debian-package-archives.sha256
upload "$provision" /tmp/provision-workspace-image.sh
sh "/bin/bash /tmp/provision-workspace-image.sh"
rm-f /etc/resolv.conf
sync
GUESTFISH

artifact="$output_directory/ai-native-paas-workspace.qcow2"
qemu-img convert -f qcow2 -O qcow2 -c "$image" "$artifact"
qemu-img check -f qcow2 "$artifact"
image_sha256="$(sha256sum "$artifact" | awk '{print $1}')"
image_size="$(stat -c '%s' "$artifact")"
virtual_size="$(qemu-img info --output=json "$artifact" | jq -er '.["virtual-size"]')"
virt-copy-out -a "$image" /usr/share/ai-native-paas/sbom.spdx.json "$output_directory"
revision="$(git rev-parse HEAD)"
jq -n \
  --arg schema 'ai-native-paas.io/workspace-image-result/v1' \
  --arg revision "$revision" \
  --arg image_sha256 "$image_sha256" \
  --argjson image_size "$image_size" \
  --argjson virtual_size "$virtual_size" \
  '{schema:$schema,git_revision:$revision,format:"qcow2",image_sha256:$image_sha256,image_size_bytes:$image_size,virtual_size_bytes:$virtual_size}' \
  > "$output_directory/manifest.json"
sha256sum "$artifact" "$output_directory/manifest.json" > "$output_directory/SHA256SUMS"
printf 'workspace image qcow2 sha256:%s virtual-size:%s\n' "$image_sha256" "$virtual_size"
