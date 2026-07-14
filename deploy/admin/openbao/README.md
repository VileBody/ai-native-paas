# OpenBao admin-cluster release

- Chart: `openbao/openbao` `0.28.4`
- Chart SHA-256: `783177e7925cb0d91ec3d5317a80826cb1e265ce17ba176d7a7ad578234c8a4e`
- OpenBao: `2.5.5@sha256:6150c4a6b62067db6141c8da7a6a6b5763f4f47c315343d0c848b40fecdfd452`
- Injector: `1.7.2@sha256:ae3d307658b72a1cf35dab9bdf92c995d45cdc7183af0516857714b5bd0ba84d`

Render without touching the cluster:

```bash
helm template openbao openbao/openbao --version 0.28.4 --namespace openbao \
  --values deploy/admin/openbao/values.yaml
```

Deploy the sealed, uninitialized release:

```bash
./scripts/deploy-openbao.sh
```

Initialization is intentionally excluded from automation. Follow
`docs/runbooks/openbao-shamir-ceremony.md` with five holders and a 3-share threshold.
After the cluster is initialized and Kubernetes auth is enabled, configure the
least-privilege workspace PKI role with
`docs/runbooks/openbao-workspace-identity.md`.
