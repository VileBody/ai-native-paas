# Pinned deterministic Helm render — 2026-07-14

Evidence class: `LOCAL_GREEN`.

Requirement: `R2` —
`TestGitOps_HelmRenderIsDeterministicForPinnedInputs`.

The production boundary now computes a canonical digest over a symlink-free
local Helm chart tree and verifies the exact chart version immediately before
render scheduling. Chart dependencies require exact versions and a local
`Chart.lock`; any change to chart metadata, templates, packaged dependencies or
lock data changes the digest.

Controlled-beta validation rejects Helm template functions that read wall
time, randomness, process environment, DNS or live Kubernetes state. Values
such as `.Values.env` remain normal pinned inputs and are not confused with the
`env` function.

The canonical integration test uses the installed Helm CLI through the
production workspace-agent executor, never through a control-plane shell. It
renders the same local chart twice with fixed:

- chart digest and version;
- values file;
- release name and namespace;
- Kubernetes version and API capabilities;
- loopback-denied HTTP/HTTPS proxy settings.

The resulting manifest bytes are identical and pass the rendered-resource
policy. Mutating the chart fails digest verification, and wall-clock template
input is rejected before execution.

Verification:

```text
helm version --short
go test -count=1 ./internal/runtime/gitops ./test/pivot \
  -run 'TestPinnedHelmChartDigestRejectsTamperingAndLiveInputs|TestGitOps_HelmRenderIsDeterministicForPinnedInputs' -v
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
make fmt-check generate-check
```

Local render evidence used Helm `v4.1.1`.

Matrix result:

- 157 requirements;
- 989 discovered Go test/fuzz targets;
- `REUSED 100`, `NEW 0`, `LIVE_ONLY 57`;
- zero unmapped requirements;
- R2 now maps to executable pinned Helm/workspace integration evidence.

OCI registry fetch and live Argo sync remain separate provider/system gates;
this slice proves deterministic rendering once the pinned chart is present in
the disposable workspace.
