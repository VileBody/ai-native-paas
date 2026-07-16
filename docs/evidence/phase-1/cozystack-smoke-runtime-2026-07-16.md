# Cozystack smoke runtime evidence — 2026-07-16

## Scope

Smoke runtime validation for the cheap Cozystack profile:

- three private Talos/Cozystack nodes;
- Envoy Gateway public HTTP edge on `5.42.126.95`;
- LINSTOR-backed `replicated` StorageClass on the 40 GiB data disk;
- Argo CD Core in `argocd`;
- GitOps-managed `hello-go` smoke application.

## Runtime status

- Nodes:
  - `talos-tqe-ag1` — `192.168.74.11`, Ready, `EXTERNAL-IP <none>`
  - `talos-nz5-3qj` — `192.168.74.12`, Ready, `EXTERNAL-IP <none>`
  - `talos-ywl-usi` — `192.168.74.13`, Ready, `EXTERNAL-IP <none>`
- Cozystack `Package` resources: all Ready.
- HelmRelease resources: all Ready.
- Envoy Gateway:
  - Gateway `envoy-gateway-system/runtime` Programmed=True;
  - service exposes NodePorts `30080` and `30443`;
  - Timeweb LB routes `80 -> 30080`, `443 -> 30443`, `6443 -> 6443`.

Runtime edge port check from the Timeweb admin host-network source on
2026-07-17:

```text
open=80
open=443
open=6443
closed=50000
```

## CoreDNS/LINSTOR fix

LINSTOR CSI initially stayed blocked because CoreDNS had the default
`cluster.local` zone while Talos/kubelet search domains used `cozy.local`.

After patching `kube-system/coredns` to `cozy.local` and restarting CoreDNS:

```text
kube_dns=ok:10.96.0.1 kubernetes.default.svc.cozy.local
linstor_dns=ok:10.106.232.234 linstor-controller.cozy-linstor.svc.cozy.local
direct_3371=open
dns_3371=open
```

LINSTOR CSI then became healthy:

```text
linstor-controller                  2/2 Running
linstor-csi-controller              7/7 Running
linstor-csi-node-*                  3/3 Running
linstor-satellite.*                 4/4 Running
```

## Storage evidence

Data disk inventory showed `/dev/sdb` as the 40 GiB data disk on each node;
the 80 GiB system disk is `/dev/sda`.

Applied `LinstorSatelliteConfiguration/ai-native-paas-smoke-data-pool` and
`StorageClass/replicated`.

LINSTOR capacity after pool creation:

```text
talos-nz5-3qj: capacity=0/43GiB free=42945478656 available=42945478656 pool=data /dev/sdb
talos-tqe-ag1: capacity=0/43GiB free=42945478656 available=42945478656 pool=data /dev/sdb
talos-ywl-usi: capacity=0/43GiB free=42945478656 available=42945478656 pool=data /dev/sdb
StorageClass replicated: provisioner=linstor.csi.linbit.com default=true
```

PVC smoke:

```text
PVC replicated-smoke-pvc: phase=Bound storageClass=replicated
PV pvc-7bbfb88b-0de8-4f20-b6b8-4d5578f770ab: driver=linstor.csi.linbit.com
Pod replicated-smoke-write-rt4mn: phase=Succeeded message='replicated-smoke-ok 2026-07-16T19:32:33Z'
```

The temporary PVC namespace was removed after evidence collection.

## Argo CD evidence

Installed Argo CD Core `v3.4.2` from commit
`0dc6b1b57dd5bb925d5b03c3d09419ab9fb4225e`.

Pinned manifest digest:

```text
core-install.yaml sha256=4cadaf95c4cca1bcacb4ae0f635e0fad04192533ee0d497ba9d6a7e2043a4f4b
```

Argo pods:

```text
argocd-application-controller-0                    1/1 Running
argocd-applicationset-controller-bc84c94bc-p4cb7   1/1 Running
argocd-redis-8b5b5b56d-wlqqw                       1/1 Running
argocd-repo-server-ff5578b69-77k6l                 1/1 Running
```

## GitOps hello-go evidence

Git source:

```text
repoURL=https://github.com/VileBody/ai-native-paas.git
targetRevision=codex/runtime-smoke-hello-go
path=deploy/runtime/smoke/hello-go
revision=c01389ba8fe2808cde31f78a367c0d12fd40aaf2
```

Argo Application:

```text
sync Synced
revision c01389ba8fe2808cde31f78a367c0d12fd40aaf2
health Healthy
operationState Succeeded successfully synced (all tasks run)
resource Namespace  hello-go-smoke Synced
resource Service hello-go-smoke hello-go Synced
resource Deployment hello-go-smoke hello-go Synced
resource HTTPRoute hello-go-smoke hello-go Synced
```

Envoy/HTTPRoute:

```text
HTTPRoute hello-go Accepted=True ResolvedRefs=True
Deployment hello-go available=1 ready=1
```

Public edge probe from Timeweb admin hostNetwork:

```text
GET http://5.42.126.95/echo?msg=hello-go
Host: hello-go.5.42.126.95.nip.io

HTTP/1.1 200 OK
content-length: 8

hello-go
```

The same public IP remains unreachable from the local Mac environment during
this run; the Timeweb-side probe is the authoritative network evidence for the
runtime edge path.

## 20× GitOps hello-go smoke — 2026-07-17

The missing repeated-smoke gate was executed from the Timeweb admin cluster
using host-network evidence Jobs. Each run performed:

1. Argo `Application/hello-go-smoke` hard refresh;
2. wait for `Synced`;
3. wait for `Healthy`;
4. HTTP probe through the public Envoy edge with
   `Host: hello-go.5.42.126.95.nip.io`.

Runtime state after the run:

```text
nodes: talos-tqe-ag1, talos-nz5-3qj, talos-ywl-usi Ready with EXTERNAL-IP <none>
application: Synced
health: Healthy
revision: f2d3d127353d51abd2aff1d45a15dd74cf7a12df
httproute: runtime Accepted=True Accepted;ResolvedRefs=True ResolvedRefs;
```

Sequential probe result:

```text
run=1 body=hello-go
run=2 body=hello-go
run=3 body=hello-go
run=4 body=hello-go
run=5 body=hello-go
run=6 body=hello-go
run=7 body=hello-go
run=8 body=hello-go
run=9 body=hello-go
run=10 body=hello-go
run=11 body=hello-go
run=12 body=hello-go
run=13 body=hello-go
run=14 body=hello-go
run=15 body=hello-go
run=16 body=hello-go
run=17 body=hello-go
run=18 body=hello-go
run=19 body=hello-go
run=20 body=hello-go
```

The temporary admin Jobs and runtime kubeconfig Secret were removed after
evidence collection.
