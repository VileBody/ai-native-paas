# Live system E2E driver contract

The twelve tests in `agentic_devops_e2e_test.go` are compiled with the
`system_e2e` build tag and deliberately require an external live driver. The
driver is an executable passed in `PAAS_E2E_DRIVER`; its private configuration
file is passed in `PAAS_E2E_CONFIG` and must have no group/other permissions.

The test invokes:

```text
<driver> --scenario E2E-N --config <absolute-private-file> --json
```

`PAAS_E2E_SENTINEL` contains a per-run secret value. The driver must use it in
the scenario but must never print it. Standard output contains exactly one JSON
object conforming to `evidence.schema.json`; diagnostics go to standard error.
Every assertion named by the Go test must be present and `true`. A skipped test
is not release evidence.

Run the live suite with:

```bash
go test -tags system_e2e ./test/system -count=1 -timeout 4h
```

The evidence driver may orchestrate GitLab, Project MCP, Timeweb, OpenBao,
Harbor, Argo and probes directly, but it must not weaken policies or write raw
secrets into its result. CI stores its signed result outside the repository.
