# Workspace egress gateway

This is the only TCP exit allowed by disposable workspace firewall groups. It
accepts TLS 1.3 with a verified workspace SPIFFE client certificate, supports
only CONNECT to port 443, applies the explicit host allowlist and rejects DNS
answers containing private, metadata, admin, runtime-cell or workspace ranges.

Do not apply the manifest until all of these gates are satisfied:

1. Timeweb monthly balance is restored, so the LoadBalancer and workspace NAT
   address can be allocated.
2. The OpenBao Shamir 5/3 ceremony is complete and the workspace PKI exists.
3. `workspace-egress-tls` contains `tls.crt`, `tls.key` and the workspace
   `ca.crt`; the server certificate covers the final gateway DNS name.
4. The final workspace control-plane DNS host is added to `allowed-hosts`.

After the Service obtains an external IPv4, set
`WORKSPACE_EGRESS_GATEWAY_URL=https://<dns>:8443` and the exact `/32` in the
workspace-manager release ConfigMap. A disposable VM firewall must show only
that `/32:8443` plus VPC DNS port 53 before the first live task is accepted.
