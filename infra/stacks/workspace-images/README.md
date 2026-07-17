# Workspace image and network stack

This state owns the private custom-image staging bucket and approved workspace
image identities. The workspace VPC, shared NAT router and both preserved IPv4
addresses belong exclusively to the adjacent `network-foundation` state and
are passed here as reviewed outputs. It cannot own admin or Cozystack resources.

The shared NAT does not grant a workspace Internet access by itself: every
disposable VM gets a deny-by-default provider firewall that permits only the
mTLS egress gateway on port 8443 and the VPC DNS resolver. Staged boot artifacts
are deleted once Timeweb has completed its private custom-image import.

`images.lock.json` pins the Cozystack/Talos boot artifact used by the adjacent
lab stack. The workspace image fields remain empty until the signed BuildKit
image pipeline is implemented; both the Timeweb image ID and digest must then
be supplied together.
