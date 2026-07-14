# ADR 0004: Governed workspace egress uses a local proxy and mTLS CONNECT gateway

Status: accepted for controlled beta

## Decision

Disposable workspace VMs have no direct Internet firewall rule. Their only TCP
egress is the central workspace egress gateway on port 8443; DNS is limited to
the VPC resolver. A loopback-only proxy in `workspace-agent` converts standard
`HTTP_PROXY`/`HTTPS_PROXY` traffic into an mTLS-authenticated CONNECT tunnel.

The gateway accepts only a verified workspace SPIFFE identity, HTTPS port 443
and explicitly configured host patterns. It resolves DNS itself, pins the
selected IP for the connection and rejects the entire answer set when any
address is private, loopback, link-local, metadata, multicast or in the
configured admin/control-plane deny ranges. It never falls back to a direct
dial.

BuildKit receives the same loopback proxy environment. Per-workspace Timeweb
firewall groups allow only `gateway/32:8443` plus TCP/UDP DNS to the VPC
resolver, and reconcile away every extra rule.

## Consequences

- plain HTTP and arbitrary destination ports are unavailable in beta;
- repository, registry, provider and package hosts must be onboarded into the
  gateway allowlist before use;
- a workspace certificate rotation immediately applies to new gateway tunnels
  because the local proxy shares the agent's in-memory certificate set;
- gateway availability is a hard dependency and failure is closed rather than
  silently switching to direct Internet access.
