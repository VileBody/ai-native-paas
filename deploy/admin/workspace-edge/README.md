# Temporary workspace live edge

These two NodePort Services expose raw TLS only during disposable-workspace
gates. OpenTofu forwards ports `32443` and `32444` from the existing admin
router IPv4 to the retained CI worker; no IPv4 or load balancer is allocated.

The listeners preserve end-to-end mTLS. They do not share Caddy's terminated
legacy `:443` path, and they do not change any retained website route.

Enable the matching admin state input with
`TF_VAR_workspace_live_edge_enabled=true`. Delete these Services and apply the
admin state with the input set to `false` after the live window.
