# Workspace image and network stack

This state owns only the disposable-workspace VPC (`192.168.75.0/24`), the
private custom-image staging bucket and approved workspace image identities. It
cannot own admin or Cozystack resources. Staged boot artifacts are deleted once
Timeweb has completed its private custom-image import.

`images.lock.json` pins the Cozystack/Talos boot artifact used by the adjacent
lab stack. The workspace image fields remain empty until the signed BuildKit
image pipeline is implemented; both the Timeweb image ID and digest must then
be supplied together.
