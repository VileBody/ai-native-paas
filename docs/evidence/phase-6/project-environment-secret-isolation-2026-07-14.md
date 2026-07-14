# Project and environment secret isolation — 2026-07-14

Evidence class: `LOCAL_GREEN`, `SECURITY_GREEN`.

Requirement: `A5.2` —
`TestSecret_ProjectAndEnvironmentScopesAreIsolated`.

The canonical test runs the Attachments application service through the real
OpenBao KV v2 HTTP adapter against a deterministic local OpenBao API fixture.
It creates two projects in the same tenant, gives each project its own
environment and writes the same build-secret name with different values.

The test proves that:

- provider references include the tenant, project and environment boundary;
- each project resolves only its own build-secret reference;
- binding project A to environment B, or project B to environment A, returns
  `FORBIDDEN` before any reference is returned;
- plaintext values arrive at distinct OpenBao paths and never cross those
  paths.

The application now exposes `ResolveProjectBuildSecretRefs`, which requires the
verified project/application identity in addition to tenant and environment.
The v1 `ResolveBuildSecretRefs` method remains only as a deprecated in-process
compatibility surface. An architecture test fails if production code in
`adapters`, `cmd` or `internal` wires that unbound method.

Verification:

```text
go test -count=1 ./internal/attachments/application \
  ./internal/attachments/openbao ./test/architecture
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
make fmt-check generate-check
```

Matrix result:

- 157 requirements;
- 982 discovered Go test/fuzz targets;
- `REUSED 94`, `NEW 0`, `LIVE_ONLY 63`;
- zero unmapped requirements;
- `A5.2` is mapped to executable domain and OpenBao HTTP integration evidence.

This is deterministic adapter-level evidence, not a live initialized OpenBao
HA gate. Live TLS, Kubernetes auth, failover and Raft recovery remain separate
provider/system gates.
