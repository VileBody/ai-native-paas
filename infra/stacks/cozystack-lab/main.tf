locals {
  profile_specs = {
    off = {
      enabled           = false
      use_configuration = false
      preset_id         = 0
      configurator_id   = 0
      cpu               = 0
      ram_mb            = 0
      system_disk_mb    = 0
      data_disk_mb      = 0
      network_mbps      = 0
    }
    smoke = {
      enabled           = true
      use_configuration = false
      preset_id         = 4803
      configurator_id   = 0
      cpu               = 4
      ram_mb            = 8192
      system_disk_mb    = 81920
      data_disk_mb      = 40960
      network_mbps      = 1000
    }
    provider_gate = {
      enabled           = true
      use_configuration = true
      preset_id         = 0
      configurator_id   = 31
      cpu               = 8
      ram_mb            = 24576
      system_disk_mb    = 81920
      data_disk_mb      = 266240
      network_mbps      = 1000
    }
    provider_gate_full = {
      enabled           = true
      use_configuration = true
      preset_id         = 0
      configurator_id   = 31
      cpu               = 8
      ram_mb            = 24576
      system_disk_mb    = 81920
      data_disk_mb      = 266240
      network_mbps      = 1000
    }
  }
  profile                  = local.profile_specs[var.cozystack_profile]
  cell_enabled             = var.lab_enabled && local.profile.enabled
  bootstrap_runner_enabled = local.cell_enabled && var.bootstrap_runner_enabled

  all_nodes = {
    cp-1 = "192.168.74.11"
    cp-2 = "192.168.74.12"
    cp-3 = "192.168.74.13"
  }
  nodes = {
    for name, ip in local.all_nodes : name => ip
    if local.cell_enabled
  }
}

resource "twc_server" "node" {
  for_each = local.nodes

  name                      = "cozystack-${each.key}"
  comment                   = "${var.cozystack_profile} Talos control-plane and Cozystack worker; no admin services."
  project_id                = var.project_id
  preset_id                 = local.profile.use_configuration ? null : local.profile.preset_id
  os_id                     = var.bootstrap_os_id
  availability_zone         = var.availability_zone
  ssh_keys_ids              = var.node_bootstrap_ssh_key_ids
  is_root_password_required = false
  cloud_init = templatefile("${path.module}/templates/talos-bootstrap-cloud-init.yaml.tftpl", {
    talos_asset_url                 = var.talos_asset_url
    talos_compressed_sha256         = var.talos_compressed_sha256
    talos_raw_sha256                = var.talos_raw_sha256
    talos_raw_size_bytes            = var.talos_raw_size_bytes
    talos_bootstrap_timeout_seconds = var.talos_bootstrap_timeout_seconds
    runtime_gateway_ip              = var.runtime_gateway_ip
  })

  dynamic "configuration" {
    for_each = local.profile.use_configuration ? [local.profile] : []

    content {
      configurator_id = configuration.value.configurator_id
      cpu             = configuration.value.cpu
      ram             = configuration.value.ram_mb
      disk            = configuration.value.system_disk_mb
    }
  }

  local_network {
    id   = var.runtime_vpc_id
    ip   = each.value
    mode = "no_nat"
  }
}

resource "twc_server_disk" "data" {
  for_each = twc_server.node

  source_server_id = tonumber(each.value.id)
  size             = local.profile.data_disk_mb
  display_name     = "cozystack-${each.key}-data"
  comment          = "Cozystack replicated storage data disk."
}

resource "twc_firewall" "node" {
  for_each = local.nodes

  name        = "ai-native-paas-cozystack-${each.key}"
  description = "Deny-by-default Talos/Cozystack firewall for ${each.key}."

  link {
    id   = twc_server.node[each.key].id
    type = "server"
  }
}

resource "twc_firewall_rule" "node_tcp" {
  for_each = local.nodes

  firewall_id = twc_firewall.node[each.key].id
  description = "All node-to-node TCP inside the dedicated VPC."
  direction   = "ingress"
  protocol    = "tcp"
  cidr        = "192.168.74.0/24"
}

resource "twc_firewall_rule" "node_udp" {
  for_each = local.nodes

  firewall_id = twc_firewall.node[each.key].id
  description = "All node-to-node UDP inside the dedicated VPC."
  direction   = "ingress"
  protocol    = "udp"
  cidr        = "192.168.74.0/24"
}

resource "twc_firewall_rule" "node_icmp" {
  for_each = local.nodes

  firewall_id = twc_firewall.node[each.key].id
  description = "Node-to-node ICMP inside the dedicated VPC."
  direction   = "ingress"
  protocol    = "icmp"
  cidr        = "192.168.74.0/24"
}

resource "twc_firewall_rule" "node_talos_bootstrap_dnat" {
  for_each = local.cell_enabled && var.talos_bootstrap_dnat_enabled ? local.nodes : {}

  firewall_id = twc_firewall.node[each.key].id
  description = "Ephemeral Talos bootstrap from the observed admin Kubernetes Job egress IP; remove after evidence upload."
  direction   = "ingress"
  port        = 50000
  protocol    = "tcp"
  cidr        = var.talos_bootstrap_source_cidr
}

resource "twc_firewall_rule" "node_talos_disk_repair_ssh" {
  for_each = local.cell_enabled && var.talos_disk_repair_ssh_enabled ? local.nodes : {}

  firewall_id = twc_firewall.node[each.key].id
  description = "Ephemeral SSH repair for the failed Ubuntu-to-Talos disk writer; remove before bootstrap evidence."
  direction   = "ingress"
  port        = 22
  protocol    = "tcp"
  cidr        = var.talos_disk_repair_ssh_source_cidr
}

resource "talos_machine_secrets" "cluster" {
  count = local.cell_enabled ? 1 : 0

  talos_version = var.talos_version
}

data "talos_machine_configuration" "controlplane" {
  for_each = local.nodes

  cluster_name       = "ai-native-paas-cozystack"
  machine_type       = "controlplane"
  cluster_endpoint   = "https://${var.runtime_edge_private_ip}:6443"
  machine_secrets    = talos_machine_secrets.cluster[0].machine_secrets
  talos_version      = var.talos_version
  kubernetes_version = var.kubernetes_version

  config_patches = [yamlencode({
    machine = {
      install = {
        image = var.talos_installer_image
        diskSelector = {
          # The smoke preset has an 80 GiB system disk and a separate 40 GiB
          # storage disk. Keep Talos upgrades pinned to the system disk.
          size = "> 70GB"
        }
      }
      network = {
        nameservers = ["1.1.1.1", "8.8.8.8"]
        interfaces = [{
          interface = "eth1"
          addresses = ["${each.value}/24"]
          routes = [{
            network = "0.0.0.0/0"
            gateway = var.runtime_gateway_ip
          }]
        }]
      }
      kernel = {
        modules = [
          { name = "openvswitch" },
          {
            name = "drbd"
            parameters = [
              "usermode_helper=disabled",
            ]
          },
          { name = "zfs" },
          { name = "spl" },
          { name = "vfio_pci" },
          { name = "vfio_iommu_type1" },
        ]
      }
      kubelet = {
        nodeIP = {
          validSubnets = ["192.168.74.0/24"]
        }
        extraConfig = {
          maxPods = 512
        }
        extraArgs = {
          rotate-server-certificates = true
        }
      }
      sysctls = {
        "net.ipv4.neigh.default.gc_thresh1" = "4096"
        "net.ipv4.neigh.default.gc_thresh2" = "8192"
        "net.ipv4.neigh.default.gc_thresh3" = "16384"
        "net.ipv4.tcp_fin_timeout"          = "10"
        "net.ipv4.tcp_keepalive_intvl"      = "10"
        "net.ipv4.tcp_keepalive_probes"     = "6"
        "net.ipv4.tcp_keepalive_time"       = "600"
        "net.ipv4.tcp_orphan_retries"       = "1"
        "net.core.netdev_budget"            = "600"
        "net.core.netdev_budget_usecs"      = "6000"
        "net.core.netdev_max_backlog"       = "4096"

        # Kubernetes 1.35 serves the secure API on an IPv6 wildcard even when
        # bind-address is IPv4. Keep the socket family enabled; the Timeweb
        # node firewalls remain default-DROP and declare no IPv6 ingress.
        "net.ipv6.conf.all.disable_ipv6"     = "0"
        "net.ipv6.conf.default.disable_ipv6" = "0"
      }
      files = [
        {
          path    = "/etc/cri/conf.d/20-customization.part"
          op      = "create"
          content = <<-EOT
            [plugins]
              [plugins."io.containerd.grpc.v1.cri"]
                device_ownership_from_security_context = true
              [plugins."io.containerd.cri.v1.runtime"]
                device_ownership_from_security_context = true
          EOT
        },
        {
          path        = "/etc/lvm/lvm.conf"
          op          = "overwrite"
          permissions = 420
          content     = <<-EOT
            backup {
              backup = 0
              archive = 0
            }
            devices {
              global_filter = [ "r|^/dev/drbd.*|", "r|^/dev/dm-.*|", "r|^/dev/zd.*|", "r|^/dev/loop.*|" ]
            }
          EOT
        },
      ]
    }
    cluster = {
      allowSchedulingOnControlPlanes = true
      apiServer = {
        certSANs = [var.runtime_edge_private_ip, var.runtime_ingress_ip, "127.0.0.1"]
        # Kubernetes 1.35 otherwise binds the secure listener to [::]:6443.
        # This cell intentionally disables guest IPv6, so make the public L4
        # edge terminate on an explicit IPv4 listener.
        extraArgs = {
          bind-address = "0.0.0.0"
        }
      }
      controllerManager = {
        extraArgs = {
          bind-address = "0.0.0.0"
        }
      }
      scheduler = {
        extraArgs = {
          bind-address = "0.0.0.0"
        }
      }
      network = {
        dnsDomain      = "cozy.local"
        podSubnets     = ["10.244.0.0/16"]
        serviceSubnets = ["10.96.0.0/16"]
        cni = {
          name = "none"
        }
      }
      proxy = {
        disabled = true
      }
      discovery = {
        enabled = false
      }
    }
  })]
}

data "talos_client_configuration" "cluster" {
  count = local.cell_enabled ? 1 : 0

  cluster_name         = "ai-native-paas-cozystack"
  client_configuration = talos_machine_secrets.cluster[0].client_configuration
  endpoints            = [local.all_nodes["cp-1"]]
  nodes                = values(local.all_nodes)
}

resource "twc_server" "bootstrap_runner" {
  count = local.bootstrap_runner_enabled ? 1 : 0

  name                      = "cozystack-bootstrap-runner"
  comment                   = "One-shot private Talos bootstrap runner generation ${var.bootstrap_bundle_generation}; destroy after encrypted evidence upload."
  project_id                = var.project_id
  preset_id                 = var.bootstrap_runner_preset_id
  os_id                     = var.bootstrap_os_id
  availability_zone         = var.availability_zone
  ssh_keys_ids              = var.bootstrap_runner_ssh_key_ids
  is_root_password_required = false
  cloud_init = templatefile("${path.module}/templates/private-bootstrap-runner-cloud-init.yaml.tftpl", {
    bundle_url_base64            = base64encode(var.bootstrap_bundle_url)
    bundle_sha256                = var.bootstrap_bundle_sha256
    decryption_passphrase_base64 = base64encode(var.bootstrap_bundle_passphrase)
    evidence_upload_url_base64   = base64encode(var.bootstrap_evidence_upload_url)
    talosctl_url_base64          = base64encode(var.talosctl_url)
    talosctl_sha256              = var.talosctl_sha256
    bootstrap_generation         = var.bootstrap_bundle_generation
    runtime_gateway_ip           = var.runtime_gateway_ip
  })

  local_network {
    id   = var.runtime_vpc_id
    ip   = "192.168.74.7"
    mode = "no_nat"
  }

  lifecycle {
    precondition {
      condition     = var.runtime_router_id != ""
      error_message = "The private bootstrap runner requires the live network-foundation NAT router."
    }


    precondition {
      condition     = !var.talos_bootstrap_dnat_enabled
      error_message = "Choose exactly one bootstrap transport: private runner VM or ephemeral admin Kubernetes Job DNAT."
    }
  }

  depends_on = [twc_server_disk.data]
}

resource "twc_firewall" "bootstrap_runner" {
  count = local.bootstrap_runner_enabled ? 1 : 0

  name        = "ai-native-paas-cozystack-bootstrap-runner"
  description = "No ingress; only Talos API, DNS and signed HTTPS artifact transfer."

  link {
    id   = twc_server.bootstrap_runner[0].id
    type = "server"
  }
}

resource "twc_firewall_rule" "bootstrap_runner_talos" {
  count = local.bootstrap_runner_enabled ? 1 : 0

  firewall_id = twc_firewall.bootstrap_runner[0].id
  description = "Apply Talos configuration only to private runtime nodes."
  direction   = "egress"
  port        = 50000
  protocol    = "tcp"
  cidr        = "192.168.74.0/24"
}

resource "twc_firewall_rule" "bootstrap_runner_dns_udp" {
  count = local.bootstrap_runner_enabled ? 1 : 0

  firewall_id = twc_firewall.bootstrap_runner[0].id
  description = "Resolve signed artifact endpoints through the reviewed public resolver over shared NAT."
  direction   = "egress"
  port        = 53
  protocol    = "udp"
  cidr        = "1.1.1.1/32"
}

resource "twc_firewall_rule" "bootstrap_runner_dns_tcp" {
  count = local.bootstrap_runner_enabled ? 1 : 0

  firewall_id = twc_firewall.bootstrap_runner[0].id
  description = "TCP DNS fallback through the reviewed public resolver over shared NAT."
  direction   = "egress"
  port        = 53
  protocol    = "tcp"
  cidr        = "1.1.1.1/32"
}

resource "twc_firewall_rule" "bootstrap_runner_https" {
  count = local.bootstrap_runner_enabled ? 1 : 0

  firewall_id = twc_firewall.bootstrap_runner[0].id
  description = "Download and upload short-lived signed encrypted artifacts."
  direction   = "egress"
  port        = 443
  protocol    = "tcp"
  cidr        = "0.0.0.0/0"
}
