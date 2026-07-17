locals {
  edge_enabled = var.network_mode == "live"

  runtime_nodes = {
    cp-1 = "192.168.74.11"
    cp-2 = "192.168.74.12"
    cp-3 = "192.168.74.13"
  }

  public_http_ports = {
    http  = 80
    https = 443
  }

  # Temporary operator-approved escape hatch for bootstrapping Talos without
  # allocating another VM or IPv4. These ports exist only while the ephemeral
  # admin Kubernetes Job runs and map one-to-one to private maintenance APIs.
  talos_bootstrap_dnat = {
    cp-1 = { public_port = "50011", private_ip = local.runtime_nodes["cp-1"] }
    cp-2 = { public_port = "50012", private_ip = local.runtime_nodes["cp-2"] }
    cp-3 = { public_port = "50013", private_ip = local.runtime_nodes["cp-3"] }
  }

  talos_disk_repair_ssh = {
    cp-1 = { public_port = "22011", private_ip = local.runtime_nodes["cp-1"] }
    cp-2 = { public_port = "22012", private_ip = local.runtime_nodes["cp-2"] }
    cp-3 = { public_port = "22013", private_ip = local.runtime_nodes["cp-3"] }
  }
}

# These import blocks adopt the two already-paid, currently unassigned Moscow
# addresses. No resource in this repository is allowed to allocate a third IP.
import {
  to = twc_floating_ip.runtime_ingress
  id = "6c842a77-1f4a-436d-ac3e-f86fd1af9454"
}

import {
  to = twc_floating_ip.shared_egress
  id = "7af71678-a5b2-47bc-9b78-ceb99cd20780"
}

resource "twc_floating_ip" "runtime_ingress" {
  availability_zone = var.availability_zone
  comment           = "AI-native PaaS runtime ingress; preserve across off windows."

  lifecycle {
    prevent_destroy = true
    ignore_changes  = [resource]

    postcondition {
      condition     = self.id == "6c842a77-1f4a-436d-ac3e-f86fd1af9454" && self.ip == "5.42.126.95"
      error_message = "runtime ingress IP identity changed; refuse to bind an unreviewed address."
    }
  }
}

resource "twc_floating_ip" "shared_egress" {
  availability_zone = var.availability_zone
  comment           = "AI-native PaaS runtime/workspace SNAT; preserve across off windows."

  lifecycle {
    prevent_destroy = true
    ignore_changes  = [resource]

    postcondition {
      condition     = self.id == "7af71678-a5b2-47bc-9b78-ceb99cd20780" && self.ip == "72.56.234.22"
      error_message = "shared egress IP identity changed; refuse to bind an unreviewed address."
    }
  }
}

resource "twc_vpc" "runtime" {
  name        = "ai-native-paas-cozystack-bgp"
  description = "Dedicated Moscow VPC for the self-managed Talos/Cozystack runtime cell."
  location    = var.location
  subnet_v4   = "192.168.74.0/24"
}

resource "twc_vpc" "workspace" {
  name        = "ai-native-paas-workspaces"
  description = "Isolated Moscow VPC for disposable per-task workspace VMs."
  location    = var.location
  subnet_v4   = "192.168.75.0/24"
}

resource "twc_router" "shared_egress" {
  count = local.edge_enabled ? 1 : 0

  name       = "ai-native-paas-shared-egress"
  comment    = "SNAT only; provider firewalls remain the runtime/workspace trust boundary."
  preset_id  = var.router_preset_id
  project_id = var.project_id

  # The Timeweb API must first attach an existing floating IP to the router
  # before the per-network SNAT calls can reference it. Without this block the
  # router itself is created, but the API rejects networks[*].nat_ip with
  # "IP not found" and leaves both networks without egress.
  ips {
    ip = twc_floating_ip.shared_egress.ip

    nat {
      # Timeweb canonicalizes this legacy attachment field to the last
      # network updated by networks[*].nat_ip (workspace in this resource).
      # Both networks still carry the same explicit SNAT address below.
      id = twc_vpc.workspace.id
    }
  }

  networks {
    id              = twc_vpc.runtime.id
    is_dhcp_enabled = true
    nat_ip          = twc_floating_ip.shared_egress.ip
  }

  networks {
    id              = twc_vpc.workspace.id
    is_dhcp_enabled = true
    nat_ip          = twc_floating_ip.shared_egress.ip
  }
}

resource "twc_router_dnat_rule" "talos_bootstrap" {
  for_each = local.edge_enabled && var.talos_bootstrap_dnat_enabled ? local.talos_bootstrap_dnat : {}

  router_id = twc_router.shared_egress[0].id
  protocol  = "tcp"

  public_ip   = twc_floating_ip.shared_egress.ip
  public_port = each.value.public_port
  local_ip    = each.value.private_ip
  local_port  = "50000"
}

resource "twc_router_dnat_rule" "talos_disk_repair_ssh" {
  for_each = local.edge_enabled && var.talos_disk_repair_ssh_enabled ? local.talos_disk_repair_ssh : {}

  router_id = twc_router.shared_egress[0].id
  protocol  = "tcp"

  public_ip   = twc_floating_ip.shared_egress.ip
  public_port = each.value.public_port
  local_ip    = each.value.private_ip
  local_port  = "22"
}

resource "twc_lb" "runtime_edge" {
  count = local.edge_enabled ? 1 : 0

  name              = "ai-native-paas-runtime-edge"
  project_id        = var.project_id
  preset_id         = var.load_balancer_preset_id
  availability_zone = var.availability_zone
  floating_ip_id    = twc_floating_ip.runtime_ingress.id
  ips               = values(local.runtime_nodes)
  algo              = "roundrobin"
  is_sticky         = false
  is_use_proxy      = false
  is_ssl            = false
  is_keepalive      = true

  local_network {
    id = twc_vpc.runtime.id
    # Timeweb reserves the earlier addresses in this VPC; the live API
    # canonicalizes the requested balancer port to .6.
    ip = "192.168.74.6"
  }

  health_check {
    proto   = "tcp"
    port    = 6443
    inter   = 10
    timeout = 5
    fall    = 3
    rise    = 2
  }
}

resource "twc_lb_rule" "runtime_edge" {
  for_each = local.edge_enabled ? {
    kubernetes = { balancer_port = 6443, server_port = 6443 }
    http       = { balancer_port = 80, server_port = 30080 }
    https      = { balancer_port = 443, server_port = 30443 }
  } : {}

  lb_id          = twc_lb.runtime_edge[0].id
  balancer_proto = "tcp"
  balancer_port  = each.value.balancer_port
  server_proto   = "tcp"
  server_port    = each.value.server_port
}

resource "twc_firewall" "runtime_edge" {
  count = local.edge_enabled ? 1 : 0

  name        = "ai-native-paas-runtime-edge"
  description = "Only HTTP(S) and scoped Kubernetes API traffic may enter the runtime cell."

  link {
    id   = twc_lb.runtime_edge[0].id
    type = "balancer"
  }
}

resource "twc_firewall_rule" "runtime_http" {
  for_each = local.edge_enabled ? local.public_http_ports : {}

  firewall_id = twc_firewall.runtime_edge[0].id
  description = "Public ${each.key} entrypoint terminated by Envoy Gateway."
  direction   = "ingress"
  port        = each.value
  protocol    = "tcp"
  cidr        = "0.0.0.0/0"
}

resource "twc_firewall_rule" "kubernetes_api" {
  for_each = local.edge_enabled ? var.kubernetes_api_cidrs : []

  firewall_id = twc_firewall.runtime_edge[0].id
  description = "Scoped operator/runtime-bridge access to Kubernetes API."
  direction   = "ingress"
  port        = 6443
  protocol    = "tcp"
  cidr        = each.value
}
