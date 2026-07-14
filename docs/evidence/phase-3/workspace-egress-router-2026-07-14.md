# Workspace egress router gate — 2026-07-14

Evidence tier: `PROVIDER_BLOCKED`

- Timeweb project: `BALOVSTVO`, Moscow workspace infrastructure boundary;
- exact OpenTofu plan: two creates, zero updates, zero deletes;
- intended resources: one floating NAT IPv4 and one single-node router attached
  to `192.168.75.0/24` with DHCP;
- first apply attempt: provider process did not start locally while the host disk
  was full; no provider API request and no state change;
- second exact-plan apply attempt: Timeweb rejected floating IP creation with
  HTTP 403 `No balance for month`;
- the router was not attempted because it depends on that address;
- no resource was added to remote state, and a provider API readback found no
  floating IP with the managed comment (no ghost resource after the 403).

Do not retry automatically. The Timeweb monthly balance/account limit must be
restored first. Once restored, regenerate an exact plan and confirm it still
contains only the two creates before applying.
