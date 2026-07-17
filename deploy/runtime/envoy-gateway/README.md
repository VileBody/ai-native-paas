# Runtime Envoy Gateway

Envoy Gateway `v1.8.2` is the only HTTP(S) entrypoint into the Cozystack
runtime cell. The Timeweb L4 balancer sends public ports 80 and 443 to the fixed
NodePorts 30080 and 30443. Kubernetes API traffic on the same public IP bypasses
Envoy and terminates on the Talos control planes; Talos API port 50000 is never
public.

The chart, controller, data plane and shutdown-manager identities are immutable
in `images.lock.json`. `values-smoke.yaml` and the base Kustomization use one
replica. `values-provider-gate.yaml` and the provider-gate overlay use two
anti-affine replicas and a PDB.

Only namespaces labelled `ai-native-paas.io/runtime-project=true` may attach
HTTPRoutes. Backend references remain same-namespace because this directory
does not create a cross-namespace ReferenceGrant. The `runtime-wildcard-tls`
secret is intentionally supplied later by cert-manager after the beta domain
and ownership proof are available.
