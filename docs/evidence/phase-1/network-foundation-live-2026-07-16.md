# Network foundation live evidence — 2026-07-16

Evidence tier: partial `PROVIDER_GREEN`; runtime Kubernetes is not green yet.

The dedicated `network-foundation` HTTP state adopted the two pre-existing
Moscow addresses by immutable provider ID. It did not allocate an address:

- runtime ingress `6c842a77-1f4a-436d-ac3e-f86fd1af9454` /
  `5.42.126.95`;
- shared egress `7af71678-a5b2-47bc-9b78-ceb99cd20780` /
  `72.56.234.22`.

Live resources:

- runtime VPC `network-7ae8fa2881384d4db6046b4dd6854b4e`;
- workspace VPC `network-687cddd36c3147b3bff75c79e9779498`;
- L4 load balancer `135509`, live private address `192.168.74.6`;
- shared router `49de7bfa-90b5-4ca5-b94a-b7d6aa7a1de4`.

The load balancer has only `80 -> 30080`, `443 -> 30443`, and
`6443 -> 6443`. The Kubernetes API firewall source is restricted to the
reviewed operator address. Talos API port `50000` is absent from the public
edge. Runtime and workspace networks both declare the preserved SNAT address,
while workload/provider firewalls remain the trust boundary.

Evidence collected without printing provider credentials:

- `PAAS_IPV4_MODE=live ./scripts/check-preserved-timeweb-ips.sh`:
  `PRESERVED_IPV4_GATE=PASS mode=live`;
- refreshed live OpenTofu plan: `No changes`;
- state-service health: HTTP `204`;
- encrypted Cozystack namespace state read: HTTP `200`;
- admin and smoke-compute states also refreshed with `No changes`.

The disposable private bootstrap runner is deliberately absent. Timeweb
rejected its reviewed six-create plan because the account balance policy does
not permit another server. Public Talos DNAT and cross-VPC admin access were not
introduced as workarounds.
