# Iteration 5 — Architecture Decisions

1. **Secret values never enter the control-plane database.** Only opaque provider references and metadata are persisted.
2. **Snapshot publication is the delivery boundary.** Runtime receives immutable references, not mutable attachment aggregates.
3. **Attachment changes create runtime releases.** A same-image/new-snapshot release keeps rollout, health, rollback and audit semantics in one place.
4. **Provider calls are sagas, not database transactions.** Every side effect has an immutable idempotency key and a discovery/reconciliation path.
5. **Data services outlive applications by default.** Application deletion revokes access; data purge is an explicit, separately authorized operation.
6. **Credential rotation is cutover-before-revoke.** Old credentials remain valid until the replacement snapshot is adopted.
7. **Domain ownership is independently verified.** A control-plane request alone cannot activate a custom hostname.
8. **TLS readiness gates route activation.** Verified DNS without a usable certificate is not an active domain.
9. **Tenant identity comes from authenticated context.** Request bodies and provider callbacks cannot select another tenant.
10. **No cross-domain database joins.** Runtime and attachments communicate via versioned contracts and ports.
