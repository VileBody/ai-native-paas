output "network_mode" {
  value = var.network_mode
}

output "runtime_vpc_id" {
  value = twc_vpc.runtime.id
}

output "runtime_vpc_cidr" {
  value = "192.168.74.0/24"
}

output "workspace_vpc_id" {
  value = twc_vpc.workspace.id
}

output "workspace_vpc_cidr" {
  value = "192.168.75.0/24"
}

output "runtime_ingress_ip" {
  value = twc_floating_ip.runtime_ingress.ip
}

output "shared_egress_ip" {
  value = twc_floating_ip.shared_egress.ip
}

output "runtime_load_balancer_id" {
  value = try(twc_lb.runtime_edge[0].id, null)
}

output "shared_router_id" {
  value = try(twc_router.shared_egress[0].id, null)
}

output "runtime_edge_private_ip" {
  value = local.edge_enabled ? "192.168.74.6" : null
}

output "runtime_router_gateway_ip" {
  description = "Private runtime-VPC gateway. The VPC itself has no dedicated public IPv4."
  value       = local.edge_enabled ? "192.168.74.4" : null
}

output "talos_bootstrap_dnat" {
  value = var.talos_bootstrap_dnat_enabled ? {
    public_ip   = twc_floating_ip.shared_egress.ip
    source_cidr = var.talos_bootstrap_source_cidr
    ports       = { for name, route in local.talos_bootstrap_dnat : name => route.public_port }
  } : null
}

output "talos_disk_repair_ssh" {
  value = var.talos_disk_repair_ssh_enabled ? {
    public_ip   = twc_floating_ip.shared_egress.ip
    source_cidr = var.talos_disk_repair_ssh_source_cidr
    ports       = { for name, route in local.talos_disk_repair_ssh : name => route.public_port }
  } : null
}
