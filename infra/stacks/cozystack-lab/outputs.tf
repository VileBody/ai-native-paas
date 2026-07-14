output "vpc_id" {
  value = twc_vpc.runtime.id
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
  value = twc_floating_ip.kubernetes_api.ip
}

output "kubeconfig" {
  value     = talos_cluster_kubeconfig.cluster.kubeconfig_raw
  sensitive = true
}

output "talosconfig" {
  value     = talos_machine_secrets.cluster.client_configuration
  sensitive = true
}
