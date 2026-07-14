path "workspace-pki/issue/workspace-agent" {
  capabilities = ["update"]
}

path "workspace-pki/sign/workspace-agent" {
  capabilities = ["update"]
}

path "sys/leases/revoke" {
  capabilities = ["update"]
}

# Values are short-lived, command-bound envelopes. The manager recomputes the
# object path from the exact ref persisted with a RUNNING command and verifies
# all scope metadata returned by OpenBao before releasing a value over mTLS.
path "workspace-credentials/data/*" {
  capabilities = ["read"]
}
