# Production identity header-confusion hardening — 2026-07-14

Evidence class: `LOCAL_GREEN`

Requirement: `G30` —
`TestAgent_APIRequiresOIDCOrMTLSAndRejectsIdentityHeadersInProduction`.

Production authentication now rejects every legacy identity/authorization
header before either verified authentication path runs, including
`X-Principal-Role`. Previously that one role header was omitted from the deny
list even though the Commerce admin routes consume it.

Validated invariants:

- unauthenticated requests carrying any tenant, project, principal, kind,
  role, agent, user or scope header are rejected;
- a request with a valid OIDC bearer token cannot add a forged
  `platform-admin` role;
- a request with a valid mTLS certificate cannot add the same role;
- rejected role-confusion attempts do not reach application dispatch;
- the trusted verifier still overwrites upstream kind-like claims for normal
  OIDC and mTLS requests.

Verification:

```text
go test -race -count=1 ./internal/identity/httpauth \
  ./internal/commerce/httpapi ./cmd/commerce-api
go vet ./internal/identity/httpauth ./internal/commerce/httpapi \
  ./cmd/commerce-api
make generate-check
```

This closes the local header-confusion path only. Commerce production still
needs a configured OIDC/mTLS verifier and an explicit trusted role mapping
before its admin endpoints are usable; this evidence does not claim that live
identity integration gate.
