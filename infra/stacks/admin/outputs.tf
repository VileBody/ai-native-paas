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

output "admin_router_id" {
  value = twc_router.admin.id
}

output "ci_node_group_id" {
  value = twc_k8s_node_group.ci.id
}

output "system_node_group_id" {
  value = twc_k8s_node_group.system.id
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

output "workspace_log_bucket_name" {
  value = twc_s3_bucket.workspace_logs.full_name
}

output "workspace_log_s3_endpoint" {
  value = "https://s3.twcstorage.ru"
}

output "workspace_log_s3_access_key" {
  value     = twc_s3_bucket.workspace_logs.access_key
  sensitive = true
}

output "workspace_log_s3_secret_key" {
  value     = twc_s3_bucket.workspace_logs.secret_key
  sensitive = true
}
