output "vpc_id" {
  value = local.cell_enabled ? var.runtime_vpc_id : null
}

output "cozystack_version" {
  value = var.cozystack_version
}

output "cozystack_profile" {
  value = var.cozystack_profile
}

output "profile_resources" {
  value = {
    node_count      = local.cell_enabled ? 3 : 0
    cpu_per_node    = local.profile.cpu
    ram_mb_per_node = local.profile.ram_mb
    system_disk_mb  = local.profile.system_disk_mb
    data_disk_mb    = local.profile.data_disk_mb
    network_mbps    = local.profile.network_mbps
    public_ipv4s    = 0
  }
}

output "node_ids" {
  value = { for name, node in twc_server.node : name => node.id }
}

output "node_private_ips" {
  value = local.nodes
}

output "kubernetes_api_ip" {
  value = local.cell_enabled ? var.runtime_ingress_ip : null
}

output "bootstrap_bundle_inputs" {
  value = local.cell_enabled ? {
    machine_configurations = {
      for name, config in data.talos_machine_configuration.controlplane : name => config.machine_configuration
    }
    talos_config = data.talos_client_configuration.cluster[0].talos_config
    nodes        = local.nodes
  } : null
  sensitive = true
}

output "talosconfig" {
  value     = try(data.talos_client_configuration.cluster[0].talos_config, null)
  sensitive = true
}

output "bootstrap_runner_id" {
  value = try(twc_server.bootstrap_runner[0].id, null)
}

output "bootstrap_runner_private_ip" {
  value = local.bootstrap_runner_enabled ? "192.168.74.7" : null
}

output "talos_bootstrap_dnat_source_cidr" {
  value = var.talos_bootstrap_dnat_enabled ? var.talos_bootstrap_source_cidr : null
}

output "talos_disk_repair_ssh_source_cidr" {
  value = var.talos_disk_repair_ssh_enabled ? var.talos_disk_repair_ssh_source_cidr : null
}
