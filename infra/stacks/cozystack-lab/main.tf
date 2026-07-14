locals {
  all_nodes = {
    cp-1 = "192.168.74.11"
    cp-2 = "192.168.74.12"
    cp-3 = "192.168.74.13"
  }
  nodes = {
    for name, ip in local.all_nodes : name => ip
    if contains(var.enabled_nodes, name)
  }

  management_rules = {
    for item in setproduct(keys(local.nodes), var.management_cidrs, ["50000", "6443"]) :
    "${item[0]}-${replace(item[1], "/", "-")}-${item[2]}" => {
      node = item[0]
      cidr = item[1]
      port = item[2]
    }
  }
}

resource "twc_vpc" "runtime" {
  name        = "ai-native-paas-cozystack-bgp"
  description = "Dedicated Moscow VPC for the self-managed Talos/Cozystack runtime cell."
  location    = var.location
  subnet_v4   = "192.168.74.0/24"
}

resource "twc_server" "node" {
  for_each = local.nodes

  name                      = "cozystack-${each.key}"
  comment                   = "Talos control-plane and Cozystack worker; no admin services."
  project_id                = var.project_id
  preset_id                 = var.node_preset_id
  os_id                     = var.bootstrap_os_id
  availability_zone         = var.availability_zone
  is_root_password_required = true
  cloud_init = templatefile("${path.module}/templates/talos-bootstrap-cloud-init.yaml.tftpl", {
    talos_asset_url                 = var.talos_asset_url
    talos_compressed_sha256         = var.talos_compressed_sha256
    talos_raw_sha256                = var.talos_raw_sha256
    talos_raw_size_bytes            = var.talos_raw_size_bytes
    talos_bootstrap_timeout_seconds = var.talos_bootstrap_timeout_seconds
  })

  local_network {
    id   = twc_vpc.runtime.id
    ip   = each.value
    mode = "dnat_and_snat"
  }
}

resource "twc_server_disk" "data" {
  for_each = twc_server.node

  source_server_id = tonumber(each.value.id)
  size             = var.data_disk_size_mb
  display_name     = "cozystack-${each.key}-data"
  comment          = "Cozystack replicated storage data disk."
}

resource "twc_floating_ip" "kubernetes_api" {
  availability_zone = var.availability_zone
  comment           = "Stable Talos/Kubernetes API endpoint for the Cozystack lab."

  resource {
    id   = twc_server.node["cp-1"].id
    type = "server"
  }
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

resource "twc_firewall_rule" "management" {
  for_each = local.management_rules

  firewall_id = twc_firewall.node[each.value.node].id
  description = "Operator access to Talos or Kubernetes API."
  direction   = "ingress"
  port        = each.value.port
  protocol    = "tcp"
  cidr        = each.value.cidr
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

resource "talos_machine_secrets" "cluster" {
  talos_version = var.talos_version
}

data "talos_machine_configuration" "controlplane" {
  for_each = local.nodes

  cluster_name       = "ai-native-paas-cozystack"
  machine_type       = "controlplane"
  cluster_endpoint   = "https://${twc_floating_ip.kubernetes_api.ip}:6443"
  machine_secrets    = talos_machine_secrets.cluster.machine_secrets
  talos_version      = var.talos_version
  kubernetes_version = var.kubernetes_version

  config_patches = [yamlencode({
    machine = {
      install = {
        image = var.talos_installer_image
        diskSelector = {
          size = "< 200GB"
        }
      }
      network = {
        hostname = "cozystack-${each.key}"
      }
      kubelet = {
        extraArgs = {
          rotate-server-certificates = true
        }
      }
    }
    cluster = {
      allowSchedulingOnControlPlanes = true
      network = {
        cni = {
          name = "none"
        }
      }
      proxy = {
        disabled = true
      }
    }
  })]
}

resource "talos_machine_configuration_apply" "controlplane" {
  for_each = local.nodes

  client_configuration        = talos_machine_secrets.cluster.client_configuration
  machine_configuration_input = data.talos_machine_configuration.controlplane[each.key].machine_configuration
  node                        = twc_server.node[each.key].main_ipv4

  depends_on = [
    twc_server_disk.data,
    twc_firewall_rule.management,
    twc_firewall_rule.node_tcp,
    twc_firewall_rule.node_udp,
    twc_firewall_rule.node_icmp,
  ]
}

resource "talos_machine_bootstrap" "cluster" {
  client_configuration = talos_machine_secrets.cluster.client_configuration
  node                 = twc_server.node["cp-1"].main_ipv4

  depends_on = [talos_machine_configuration_apply.controlplane]
}

resource "talos_cluster_kubeconfig" "cluster" {
  client_configuration = talos_machine_secrets.cluster.client_configuration
  node                 = twc_server.node["cp-1"].main_ipv4

  depends_on = [talos_machine_bootstrap.cluster]
}
