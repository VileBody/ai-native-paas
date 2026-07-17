# Moscow IPv4 reservation plan — 2026-07-15

Evidence tier: `PROVIDER_BLOCKED`

## Capacity derived from the controlled-beta plan

This is historical evidence from the failed reservation attempt. The current
decision is `NETWORK_DEFERRED`: no address may be ordered, imported, attached,
or released without a new explicit user approval.

The future release-unlock orders five new addresses: three Cozystack node
addresses, one Cozystack API/ingress address, and one workspace NAT address.
Existing admin addresses and a provider-managed shared edge are tracked
separately and are not part of that five-address order:

| Boundary | Purpose | Count | Allocation |
|---|---|---:|---|
| Admin | private system-worker NAT | 1 | already live |
| Admin | current CI worker | 1 | already live |
| Cozystack | Talos node management/egress for three smoke nodes | 3 | reserve before VM creation |
| Cozystack | stable Kubernetes API plus smoke ingress | 1 | reserve before VM creation |
| Workspace | outbound-only workspace VPC NAT | 1 | reserve before router creation |
| Admin | shared beta ingress for API/MCP and the two workspace mTLS listener roles | 1 | allocate with the managed Kubernetes load balancer |

The managed Kubernetes control-plane address is provider-owned and is not an
address that this project orders. GitLab.com, managed PostgreSQL and Timeweb S3
also require no platform-owned public address.

The current Kubernetes manifests contain two independent `LoadBalancer`
Services and the public API ingress is still pending. Creating them literally
would allocate three admin ingress addresses and raise the total from eight to
ten. Before those Services are enabled they must be placed behind one shared
gateway with separate HTTPS and mTLS listeners. The consolidation does not
weaken the application NetworkPolicies or client-certificate checks.

Five addresses can be reserved before compute after the explicit gate: four in the
`cozystack-lab` state and the workspace NAT address in `workspace-images`.
Timeweb-managed Kubernetes load-balancer addresses are allocated by the cloud
controller when the Service is created and cannot be preselected through the
documented Service annotations.

At the published rate of 180 RUB/address/month, five new retained reservations
cost 900 RUB/month. DDoS Guard is deliberately disabled for the development
addresses.

## Live provider result

- The Cozystack saved plan contained exactly four creates, zero changes and
  zero destroys.
- A separate targeted workspace plan contains exactly the one NAT address.
- The complete smoke-cluster read-only plan is provider-valid with preset
  `4803` (4 vCPU / 8 GiB / 80 GiB) and 40 GiB data disks. It contains 35
  creates, zero updates and zero deletes: four addresses, one VPC, three
  servers, three disks, three firewalls, 15 firewall rules and the Talos
  bootstrap graph.
- The first Cozystack apply was rejected explicitly by Timeweb with HTTP 403
  `No balance for month`; no request returned an ambiguous/lost response.
- Remote Cozystack state remains empty and provider inventory contains no
  address with a Cozystack managed comment.
- Account readback at the time of the gate reported 12,479.33 RUB balance,
  33,142 RUB current monthly cost and 275 hours remaining. Five new addresses
  would make the nominal monthly total 34,042 RUB, a difference of 21,562.67
  RUB from the reported balance. Timeweb, not OpenTofu, enforces this prepaid
  capacity check.
- Three unrelated free addresses exist in `msk-1`. They were not imported or
  modified because their ownership and future use by other projects is not
  established.

Do not retry merely because the Timeweb balance gate is cleared. A new explicit
user authorization is also required. Then recreate both saved plans from
refreshed state, require a combined five creates and zero updates/deletes, and
apply Cozystack and workspace reservations separately. The three unrelated free
Moscow addresses remain out of scope.
