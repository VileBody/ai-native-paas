# Recipe dependency planning — 2026-07-14

Evidence class: `LOCAL_GREEN`.

Requirements:

- `A5.10` — `TestRecipe_DependencyGraphRejectsCyclesAndVersionConflict`;
- `A5.11` — `TestRecipe_PlanIsPureAndCreatesNoExternalResource`.

The signed recipe contract now carries canonical, exact-version dependency
constraints. Signing and verification include the normalized dependency graph,
so dependencies cannot be changed without invalidating the content digest and
signature.

The graph resolver:

- considers only active, signature-verified recipe versions;
- intersects all direct and transitive exact-version constraints;
- backtracks deterministically from highest compatible versions;
- rejects cycles and disjoint transitive constraints with `ErrConflict`;
- emits immutable locks in dependency-first order.

`Registry.Plan` hashes the normalized root plus dependency-first locks. The
registry has no provider apply port, and the executable purity test proves two
identical plans have the same structure and hash while performing zero store
writes after activation.

Verification:

```text
go test -count=1 ./pkg/contracts/recipes/v1 ./internal/attachments/recipe
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
make fmt-check generate-check
```

Matrix result:

- 157 requirements;
- 984 discovered Go test/fuzz targets;
- `REUSED 96`, `NEW 0`, `LIVE_ONLY 61`;
- zero unmapped requirements;
- A5.10 and A5.11 are now mapped to executable dependency and purity evidence.

This slice proves deterministic planning before provider execution. It does not
yet apply recipe plans to a live Cozystack provider; those lifecycle operations
remain provider/system gates.
