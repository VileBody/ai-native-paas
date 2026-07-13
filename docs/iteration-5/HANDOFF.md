# Iteration 5 — Handoff to Iteration 6

Commercial Governance must consume usage and entitlement contracts; it must not read `attachments` tables.

Iteration 5 exports:

- service-instance identity, plan reference and lifecycle events;
- binding identity and lifecycle events;
- secret metadata counts/versions, never values;
- domain/TLS lifecycle events;
- immutable `AttachmentSnapshotRef`;
- provider-operation usage dimensions suitable for metering.

Iteration 6 may authorize or reject a requested plan and meter the resulting allocation. It must not provision services directly, mutate provider credentials, inspect OpenBao values, or modify attachment snapshots.

All defects found later in secret/service/domain semantics remain Iteration 5 corrections rather than workarounds in billing.
