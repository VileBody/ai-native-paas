# NATS JetStream HA gate — 2026-07-15

Target: `ai-native-paas-test`, namespace `nats`.

The production stream reconciler created these file-backed R3 streams:

- `PLATFORM_EVENTS`;
- `PLATFORM_OPERATIONS`;
- `WORKSPACE_COMMANDS`;
- `PLATFORM_USAGE`.

All use limits retention, a seven-day maximum age, a two-minute duplicate
window, an 8 MiB message limit, and deny direct delete/purge.

Live gate:

1. Published the same non-secret `source.ha_gate` event twice with message ID
   `nats-ha-gate-20260715-a`. JetStream stored sequence 1 once and marked the
   second publish `Duplicate: true`.
2. Deleted the `PLATFORM_EVENTS` leader pod `nats-0` and waited for the NATS
   StatefulSet rollout.
3. Leadership moved to `nats-2`.
4. Published a second event twice with message ID
   `nats-ha-gate-20260715-b`. JetStream stored sequence 2 once and marked the
   duplicate publish.
5. Final stream state contained exactly two messages. Recreated `nats-0` and
   `nats-1` both reported `current: true` as followers of `nats-2`.

Result: deduplication, leader election, retained state, and replica catch-up are
green. This is not yet the complete crash-replay gate for application
outbox/inbox publishers; those publishers and durable consumers remain pending.
