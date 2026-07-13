resource "twc_vpc" "platform" {
  name        = "ai-native-paas-msk"
  description = "Private Moscow network for the AI Native PaaS test environment."
  location    = var.location
  subnet_v4   = "192.168.73.0/24"
}

resource "twc_k8s_cluster" "platform" {
  name              = "ai-native-paas-test"
  description       = "Dedicated AI Native PaaS test cluster. Isolated from BALOVSTVO workloads."
  project_id        = var.project_id
  preset_id         = var.master_preset_id
  version           = var.kubernetes_version
  network_driver    = "cilium"
  network_id        = twc_vpc.platform.id
  high_availability = false
  ingress           = false
}

resource "twc_k8s_node_group" "platform" {
  cluster_id        = twc_k8s_cluster.platform.id
  name              = "platform-workers"
  preset_id         = var.worker_preset_id
  node_count        = 1
  is_autohealing    = true
  is_autoscaling    = false
  public_ip_enabled = true

  labels {
    key   = "ai-native-paas.io/pool"
    value = "platform"
  }
}

resource "random_password" "control_plane" {
  length      = 16
  special     = false
  min_lower   = 4
  min_upper   = 2
  min_numeric = 4
}

resource "twc_database_cluster" "control_plane" {
  name                         = "ai_native_paas_control"
  description                  = "Managed PostgreSQL 17 for platform users, subscriptions and events."
  type                         = "postgres17"
  preset_id                    = var.postgres_preset_id
  project_id                   = var.project_id
  availability_zone            = var.availability_zone
  is_external_ip               = false
  is_secure_connection_enabled = false

  network {
    id = twc_vpc.platform.id
  }

}

resource "twc_database_user" "control_plane" {
  cluster_id  = twc_database_cluster.control_plane.id
  login       = "platform_admin"
  password    = random_password.control_plane.result
  host        = "%"
  description = "Application owner for the AI Native PaaS control-plane database."

  instance {
    instance_id = twc_database_instance.control_plane.id
    privileges = [
      "SELECT",
      "INSERT",
      "UPDATE",
      "DELETE",
      "CREATE",
      "TRUNCATE",
      "REFERENCES",
      "TRIGGER",
      "TEMPORARY",
    ]
  }
}

resource "twc_database_instance" "control_plane" {
  cluster_id  = twc_database_cluster.control_plane.id
  name        = "platform"
  description = "Users, subscriptions, deployments and usage events."
}
