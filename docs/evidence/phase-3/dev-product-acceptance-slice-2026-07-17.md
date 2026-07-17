# dev-product acceptance slice — 2026-07-17

## Scope

This evidence records the cheap executable product slice that runs in ordinary
`go test ./...` without live Cozystack, GitLab, Timeweb VM, Harbor or public
ingress.

It complements the runtime process smoke in
`runtime-sim-dev-slice-2026-07-17.md` and remains development product evidence
only. It does not close `COZYSTACK_LIVE_GREEN`, `K8S_GREEN` or
`PROVIDER_FULL_GREEN`.

## Command

```text
/opt/homebrew/bin/go test ./test/acceptance -run 'TestAgent_AuditConnects|TestAcceptance_AgentCreates'
```

## Result

```text
ok  	github.com/keir-research/ai-native-paas/test/acceptance	1.140s
```

## Verified slice

```text
agent task
-> project create
-> repository patch
-> build request
-> exact production approval
-> runtime_sim_k8s hostname
-> deployment status
-> HTTP probe evidence
-> usage preview
-> workspace destroy evidence
-> redacted audit chain
```

## Important limits

- Runtime is still represented by local testkit/dev adapters, not live Argo.
- Build output is a deterministic local artifact reference, not Harbor
  digest/SBOM/signature/provenance.
- Workspace lifecycle is represented as Project MCP task evidence; disposable
  Timeweb VM creation and teardown remain live gates.
