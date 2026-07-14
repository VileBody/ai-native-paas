# Workspace image and network stack

This state owns only the disposable-workspace VPC (`192.168.75.0/24`), its
outbound NAT router, the private custom-image staging bucket and approved
workspace image identities. It cannot own admin or Cozystack resources. The NAT
does not grant a workspace Internet access by itself: every disposable VM gets
a deny-by-default firewall that permits only the mTLS egress gateway on port
8443 and the VPC DNS resolver. Staged boot artifacts are deleted once Timeweb
has completed its private custom-image import.

`images.lock.json` pins the Cozystack/Talos boot artifact used by the adjacent
lab stack. The workspace image fields remain empty until the signed BuildKit
image pipeline is implemented; both the Timeweb image ID and digest must then
be supplied together.
