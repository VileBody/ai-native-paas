# Legacy quarantine migration evidence — 2026-07-15

## Outcome

- Source cluster `call-analytics-k8s` (`1091210`) was removed with one
  body-less `DELETE /api/v1/k8s/clusters/1091210`; the response was `204`.
- Destination cluster `ai-native-paas-test` (`1099941`) is the only Kubernetes
  cluster left in project `BALOVSTVO` (`2545534`).
- `rec-sidecar` and `vietnam-rent` are isolated by the
  `ai-native-paas.io/scope=legacy-quarantine` namespace label and run only on
  the CI worker pool.
- Argo CD Core reports both exact-SHA Applications `Synced` and `Healthy`:
  `59a6e0c8fe0cfb4022988a9bc3ce5cf43b0d3102` and
  `eb19e80905554639a28d16c9a1538578ae00cf7b`.
- All runtime images are pinned to the private-registry digests in
  `deploy/legacy/images.lock.json`.
- Twelve consecutive four-host probe passes returned HTTP `200` before source
  deletion. All four hosts continued to return `200` after deletion:
  `rec.teamgenius.ru`, `vietnam.teamgenius.ru`, `suffleur.teamgenius.ru`, and
  `klukhanova.teamgenius.ru`.

## Data and rollback evidence

- Source writers were stopped before the final dumps.
- Exact table-by-table row counts matched between the frozen source and the
  destination for both PostgreSQL databases.
- Final backups are in private bucket `ai-native-paas-state` under
  `legacy-migration/final-20260715T191322Z`:
  - rec-sidecar dump:
    `17a235cdfcca01a9fc87863fda78ce17062f89f9f98ffefe5e93bfe2ecd5e6f2`;
  - vietnam-rent dump:
    `eb1a8fdd2748031de3f3d89741880dd01f95c85a51a53edaf0c0f36ba1a9e291`;
  - VietNest media:
    `0cc27c9bc448e6c1b4b84eb54cc909352fcce059e1beaf3c544ec9705f1527e4`.
- The additional local recovery bundle is
  `/Users/ergin/Desktop/legacy-k8s-backup-20260715T132447Z` and remains outside
  the repository with restricted permissions.
- Temporary restore Jobs, S3 download credentials, and unrelated Argo
  repository credentials were removed after the public probes passed.

## Validation

- `go test ./...` passed after the migration.
- OpenTofu validation passed and the final admin-stack plan reported
  `No changes`.
- Server-side dry runs passed for the namespace, Argo, RBAC, edge, and live
  overlay manifests.
- Shell syntax, JSON parsing, and `git diff --check` passed.

## Edge state

- Load balancer `125898` and its public IPv4 `85.193.87.39` are retained. Its
  only backend is the existing Moscow router address `72.56.246.80`.
- Load balancer `129179` and its public IPv4 `185.200.243.10` are retained.
- The admin router maps `30870 -> 192.168.73.5:30870` and
  `30443 -> 192.168.73.5:31443`; no new public IPv4 was allocated.
- Existing Let's Encrypt certificates were transferred pod-to-Secret without
  writing private keys to the repository or a local plaintext file. The three
  migrated certificates expire on 2026-10-06; the rec certificate expires on
  2026-09-16.

## IPv4 preservation incident

The pre-delete `scripts/check-preserved-timeweb-ips.sh` gate passed for all four
exact floating-IP IDs. Timeweb's public documentation says that a public IP
remains on the account when its attached service is deleted:
<https://timeweb.cloud/docs/public-ip>.

Despite that documented behavior, deleting the Kubernetes cluster also
removed its two node floating-IP records from the account:

- `e539c30a-e924-4628-914f-7e74445e3de7` / `87.249.54.207`;
- `da1331bb-7bc5-4464-9053-a5825f3d79c0` / `188.225.45.145`.

No request was made to a floating-IP delete endpoint. The two load-balancer
floating-IP records remain present under their original IDs. The post-delete
preservation gate intentionally remains red; its expectation was not weakened
to hide the incident. Recovering the exact node addresses requires escalation
to Timeweb support because the API no longer lists them as account resources.
