# Timeweb S3 scoped-consumer rotation — 2026-07-22

Evidence tier: `SECURITY_GREEN` input. The full security gate remains pending
on the cross-surface sentinel and live workspace-compromise suite.

## Result

The account-wide Timeweb S3 administrator credential is no longer used by a
platform runtime consumer. Four dedicated S3 users now provide `Manage` access
to exactly one bucket each:

| Consumer | Sole allowed bucket |
|---|---|
| `state-service` | `ai-native-paas-state` |
| workspace command logs | `ai-native-paas-workspace-logs` |
| image staging and Talos artifacts | `ai-native-paas-images` |
| Harbor registry | `ai-native-paas-harbor-blobs` |

For every user, an authenticated `HeadBucket` request succeeded for its own
bucket and was rejected for each of the other three buckets. State, workspace
logs and image staging also passed an isolated object
`write -> read/digest -> delete` round trip.

## Consumer reconciliation

- `state-service-secrets` was patched with the scoped state user and the
  Deployment restarted. Its expired two-hour HTTP credentials were rotated,
  after which a real encrypted `admin` state read through the HTTP backend
  returned `200`.
- `workspace-manager-secrets` now holds the scoped workspace-log user. The
  workspace manager remains intentionally undeployed in the cheap development
  profile, so its bucket was verified directly with the same credentials.
- Image staging has no long-running consumer. Its operator-managed `0600`
  credential files passed the object round trip and remain outside Git.
- Harbor was reconciled with its dedicated bucket user and upgraded to Helm
  release revision 4. The live OCI gate created a private project and scoped
  robot, pushed and read back a blob and manifest through S3, revoked the
  robot, proved post-revocation read denial, and removed the test resources.

A decoded scan of every Kubernetes Secret in the admin cluster found zero
exact matches for the previous account-wide access or secret key.

## Main-user rotation proof

Timeweb does not allow removing the S3 administrator. The operator reset its
secret in the account panel after all consumers were reconciled. The previous
pair was loaded directly from client-encrypted OpenTofu state without printing
or persisting it and then used only for a harmless `HeadBucket` request:

```text
old_main_key_rejected=true
rotated_main_key_accepted=true
scoped_state_key_accepted=true
```

The administrator remains an account control-plane principal, but its rotated
secret is not stored in Kubernetes, Git, the scoped operator files, or this
evidence. The stale value retained in encrypted historical state is rejected
by Timeweb.

## Regression evidence

After reconciliation:

- `go test ./...`: pass;
- `go test -race ./...`: pass;
- `go vet ./...`: pass;
- `go test -tags system_e2e ./test/system -count=1`: harness compiles and
  passes/skips correctly without a live system driver;
- state-service encrypted S3 read: pass;
- Harbor private OCI/S3 round trip and robot revocation: pass.

No credential value was printed or committed. The combined handoff file and
the per-consumer files are Git-ignored and mode `0600`; the containing operator
directory is mode `0700`.
