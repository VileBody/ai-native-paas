# Final snapshot provenance and verification

## Provenance

The base is the cumulative Iteration 7 checkpoint, itself based on the verified Iteration 6 repository. The latest surviving Iteration 7 PostgreSQL retry patch, MCP/OpenAPI files, API smoke script and lifecycle acceptance test were overlaid. The complete frozen Iteration 5 public contract was restored from the surviving source workspace.

## Verification performed on this exact merged tree

- `gofmt` over all Go sources;
- Agent Governance domain/application/HTTP tests;
- Agent and Attachments public contract tests;
- architecture-boundary tests with the roadmap guard advanced to Iteration 7;
- Agent lifecycle acceptance tests;
- compilation of all command packages;
- ZIP integrity and SHA-256 manifest verification.

## Important limitation

The final sandbox restart removed the previously restored, complete Iteration 5 working tree before it could be archived. Therefore the directly buildable module contains the frozen Attachments contract rather than the full Attachments implementation. All surviving implementation fragments and evidence are preserved under `recovery/` instead of being represented as a complete package.

Live PostgreSQL and Kubernetes/provider integration suites were not rerun against this final merged snapshot.
