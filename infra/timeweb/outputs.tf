output "project_id" {
  value = var.project_id
}

output "vpc_id" {
  value = twc_vpc.platform.id
}

output "cluster_id" {
  value = twc_k8s_cluster.platform.id
}

output "cluster_status" {
  value = twc_k8s_cluster.platform.status
}

output "control_plane_database_id" {
  value = twc_database_cluster.control_plane.id
}

output "control_plane_database_port" {
  value = twc_database_cluster.control_plane.port
}

output "control_plane_database_networks" {
  value = twc_database_cluster.control_plane.networks
}

output "control_plane_database_login" {
  value = "platform_admin"
}

output "control_plane_database_name" {
  value = twc_database_instance.control_plane.name
}

output "control_plane_database_password" {
  value     = random_password.control_plane.result
  sensitive = true
}
