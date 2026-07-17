# Admin stack

This stack adopts the existing `ai-native-paas-test` managed Kubernetes cluster,
its `192.168.73.0/24` VPC and private managed PostgreSQL. It adds three tainted
4 vCPU / 8 GiB system workers while preserving the original CI worker.

Only platform APIs, MCP gateway, OpenBao, NATS, Harbor, state service and
monitoring may tolerate `ai-native-paas.io/system=true:NoSchedule`. User
workloads and user-requested databases belong to the Cozystack runtime cell.

The stack owns dedicated private S3 buckets for client-encrypted workspace
command logs and Harbor OCI blobs. Both are separate from OpenTofu state,
custom image staging and all user S3 resources; `prevent_destroy` is mandatory.
Harbor metadata receives its own database and least-privilege user inside the
admin managed PostgreSQL cluster.

The Timeweb network-drive CSI is installed through the provider's managed addon
surface once available in the API. The live gate requires
`network-drives.csi.timeweb.cloud` and the Moscow-only
`nvme.network-drives.csi.timeweb.cloud` StorageClass before stateful admin
services are installed.
