output "vpc_id" {
  value = try(twc_vpc.runtime[0].id, null)
}

output "cozystack_version" {
  value = var.cozystack_version
}

output "node_ids" {
  value = { for name, node in twc_server.node : name => node.id }
}

output "node_private_ips" {
  value = local.nodes
}

output "node_public_ips" {
  value = { for name, node in twc_server.node : name => node.main_ipv4 }
}

output "kubernetes_api_ip" {
  value = try(twc_floating_ip.kubernetes_api[0].ip, null)
}

output "kubeconfig" {
  value     = try(talos_cluster_kubeconfig.cluster[0].kubeconfig_raw, null)
  sensitive = true
}

output "talosconfig" {
  value     = try(talos_machine_secrets.cluster[0].client_configuration, null)
  sensitive = true
}
