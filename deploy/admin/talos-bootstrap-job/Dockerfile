FROM ai-native-paas-registry.registry.twcstorage.ru/mirror/argocd@sha256:a8679d1bbf7679ad27bf39fadbd30e486f59446eadd4f5c2c11ce6a41053a216

USER 0

ARG TALOSCTL_URL=https://github.com/siderolabs/talos/releases/download/v1.13.0/talosctl-linux-amd64
ARG TALOSCTL_SHA256=4aa5cd191c708b8c1a3b358bfd7dd21fb0cc6bd4dc7a07f2aa925cd2a8473bae

# The release pipeline supplies the checksum-verified Talos release directory
# as the named `talosctl` build context. Avoiding a RUN step keeps the amd64
# image reproducible even when it is built on arm64.
COPY --from=talosctl --chmod=0555 /talosctl-linux-amd64 /usr/local/bin/talosctl

COPY --chmod=0555 bootstrap.sh /usr/local/bin/ai-native-paas-talos-bootstrap

USER 999:999
ENTRYPOINT ["/usr/local/bin/ai-native-paas-talos-bootstrap"]
