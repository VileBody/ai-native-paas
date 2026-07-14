# Cozystack lab stack

This is a separate self-managed runtime cell in `BALOVSTVO`; it does not install
Cozystack on the managed k0s admin cluster. It creates:

- VPC `192.168.74.0/24`;
- three dedicated-CPU Timeweb VMs, each 8 vCPU / 32 GiB / 100 GiB;
- a 260 GiB data disk on every node (the first Timeweb 5 GiB step above 256 GiB);
- Talos with no preinstalled CNI or kube-proxy;
- one stable floating API IP and a deny-by-default firewall.

All three nodes are control-plane plus schedulable workers for this beta lab.
KubeVirt and nested tenant clusters are intentionally disabled. Cozystack owns
CNI, ingress and storage in this cluster.

The stack is cost-guarded and creates no provider resources by default. Set
`TF_VAR_lab_enabled=true` only for a scheduled live gate. A normal development
plan must keep it `false`; after the gate, preserve the encrypted state/evidence
bundle and destroy the complete stack rather than leaving dedicated-CPU nodes
idle. Enabling the lab also requires the exact acknowledgement
`CREATE-3X-DEDICATED-CPU-COZYSTACK-LAB`; the plan fails closed without the
second signal or when fewer than all three nodes are selected.

Timeweb's custom-image API does not expose the Moscow `ru-3` location. Nodes
therefore start once from Ubuntu 24.04 and use cloud-init to download the pinned
Cozystack asset into RAM, verify both compressed and decompressed SHA-256 values,
write the system disk with static BusyBox, and reboot into Talos. The system disk
is selected separately from the 260 GiB Cozystack data disk. No interactive SSH
or VNC bootstrap is required.
