# Project MCP write-only secrets evidence — 2026-07-14

Evidence class: `LOCAL_GREEN`

Requirement: `G22` —
`TestAgent_SecretSetIsWriteOnlyAcrossMCPAndWorkspace`.

Project MCP v2 now implements `secret_set` and `secret_list_metadata` through
the Attachments write-only application port. Tenant, project/application and
agent identities come only from the verified short-lived access credential;
the secret arguments cannot supply or override them.

Observed invariants:

- `secret_set` accepts a bounded value and a strict runtime/build placement,
  then clears its mutable byte copy after the Attachments call;
- the response contains public metadata, an attachment snapshot reference and
  an opaque `secret://project/environment/name` reference, never the value or
  provider path;
- an exact idempotent retry returns the original metadata/version without a
  second public representation of the value;
- `secret_list_metadata` is bound to both tenant and project; an environment
  belonging to a sibling project in the same tenant is denied;
- a workspace command contains only the opaque secret reference in
  `environment_refs` and passes the existing reference-only command contract;
- MCP responses, repeat response, list, workspace request, Attachments audit,
  outbox, structured logs and provider errors all exclude the sentinel;
- provider errors are converted to stable public errors rather than echoing
  backend text.

Verification commands:

```text
go test ./internal/project/mcp \
  -run '^TestAgent_SecretSetIsWriteOnlyAcrossMCPAndWorkspace$' -count=1 -v
go test -race ./internal/project/mcp \
  -run '^TestAgent_SecretSetIsWriteOnlyAcrossMCPAndWorkspace$' -count=1
go test ./...
go vet ./...
go test -race ./internal/attachments/application ./internal/project/mcp \
  ./internal/workspace/... ./pkg/contracts/workspace/v1
./scripts/generate-pivot-tdd-matrix.py --check
```

This evidence does not claim the live OpenBao adapter, a production
project-api-to-attachments service binding, or a Timeweb workspace VM. Those
remain separate `SECURITY_GREEN` and provider/system gates after OpenBao is
initialized and the production Attachments entrypoint has no development
adapters.
