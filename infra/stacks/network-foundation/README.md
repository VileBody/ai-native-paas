# Shared network foundation

This state is the only owner of the PaaS runtime/workspace VPCs and the two
pre-existing Moscow IPv4 reservations. The import blocks adopt exactly:

- `5.42.126.95` as the single runtime ingress;
- `72.56.234.22` as shared runtime/workspace SNAT.

It must never allocate another floating IP. `5.42.106.8`, the admin/legacy
addresses and all unrelated account resources are outside this state.

`network_mode=off` preserves both addresses and both free VPC objects while
removing the paid load balancer and router. `network_mode=live` requires the
exact `ENABLE-TWO-IP-RUNTIME-EDGE` cost acknowledgement and creates the basic
Timeweb load balancer plus one router. HTTP(S) reaches Envoy Gateway NodePorts;
the same public address exposes a source-restricted Kubernetes API. Talos port
50000 is never public.

The shared router is not a security boundary. Every workspace VM must retain a
deny-by-default Timeweb firewall which permits only DNS and the governed mTLS
egress gateway. Runtime node firewalls accept their own VPC and never the
workspace CIDR.

Timeweb's router API requires the preserved floating IP to be attached through
the legacy `ips` block before either `networks[*].nat_ip` assignment is sent.
The provider then canonicalizes `ips.nat.id` to the workspace VPC (the last
network in the resource); both VPC entries still retain the same explicit SNAT
address. Do not remove or reorder this two-step representation without a live
zero-drift plan, because the API otherwise returns `IP not found` and leaves
the private networks without egress.
