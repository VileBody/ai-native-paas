locals {
  system_worker_count   = var.admin_capacity_mode == "ha" ? 3 : var.admin_capacity_mode == "dev" ? 1 : 0
  system_worker_enabled = local.system_worker_count > 0
}

resource "twc_vpc" "platform" {
  name        = "ai-native-paas-msk"
  description = "Private Moscow network for AI-native DevOps admin services."
  location    = var.location
  subnet_v4   = "192.168.73.0/24"
}

data "twc_router_preset" "admin" {
  location   = var.location
  node_count = 1
}

resource "twc_floating_ip" "admin_egress" {
  availability_zone = var.availability_zone
  comment           = "Outbound NAT for private admin system workers."
}

resource "twc_router" "admin" {
  name       = "ai-native-paas-admin-router"
  comment    = "Private admin worker routing and outbound NAT."
  preset_id  = tonumber(data.twc_router_preset.admin.id)
  project_id = var.project_id

  networks {
    id              = twc_vpc.platform.id
    is_dhcp_enabled = true
  }

  ips {
    ip = twc_floating_ip.admin_egress.ip

    nat {
      id = twc_vpc.platform.id
    }
  }
}

# Temporary edge for the operator-owned workloads documented by ADR 0006.
# It reuses the admin router IPv4 and forwards only the two ports expected by
# the retained SPB load balancer. No new public address is allocated.
resource "twc_router_dnat_rule" "legacy_http" {
  router_id = twc_router.admin.id
  protocol  = "tcp"

  public_ip   = twc_floating_ip.admin_egress.ip
  public_port = "30870"
  local_ip    = var.legacy_edge_private_ip
  local_port  = "30870"
}

resource "twc_router_dnat_rule" "legacy_https" {
  router_id = twc_router.admin.id
  protocol  = "tcp"

  public_ip   = twc_floating_ip.admin_egress.ip
  public_port = "30443"
  local_ip    = var.legacy_edge_private_ip
  local_port  = "31443"
}

resource "twc_k8s_cluster" "platform" {
  name              = "ai-native-paas-test"
  description       = "Admin control plane. PaaS tenant workloads are forbidden. ADR 0006 quarantines operator legacy apps."
  project_id        = var.project_id
  preset_id         = var.master_preset_id
  version           = var.kubernetes_version
  network_driver    = "cilium"
  network_id        = twc_vpc.platform.id
  high_availability = false
  ingress           = false
}

resource "twc_k8s_node_group" "ci" {
  cluster_id        = twc_k8s_cluster.platform.id
  name              = "platform-workers"
  preset_id         = var.ci_worker_preset_id
  node_count        = 1
  is_autohealing    = true
  is_autoscaling    = false
  public_ip_enabled = true

  labels {
    key   = "ai-native-paas.io/pool"
    value = "ci"
  }
}

moved {
  from = twc_k8s_node_group.platform
  to   = twc_k8s_node_group.ci
}

resource "twc_k8s_node_group" "system" {
  count = local.system_worker_enabled ? 1 : 0

  cluster_id        = twc_k8s_cluster.platform.id
  name              = "system-workers"
  preset_id         = var.system_worker_preset_id
  node_count        = local.system_worker_count
  is_autohealing    = true
  is_autoscaling    = false
  public_ip_enabled = false
  virtual_router_id = twc_router.admin.id

  labels {
    key   = "ai-native-paas.io/pool"
    value = "system"
  }

  taints {
    key    = "ai-native-paas.io/system"
    value  = "true"
    effect = "NoSchedule"
  }
}

moved {
  from = twc_k8s_node_group.system
  to   = twc_k8s_node_group.system[0]
}

resource "random_password" "control_plane" {
  # Explicit rotation marker. Advance only through an audited credential
  # rotation that also reconciles the Kubernetes consumer secret.
  keepers = {
    rotation = "2026-07-14-operator-output-containment-1"
  }

  length      = 16
  special     = false
  min_lower   = 4
  min_upper   = 2
  min_numeric = 4
}

resource "twc_database_cluster" "control_plane" {
  name                         = "ai_native_paas_control"
  description                  = "Managed PostgreSQL 17 for users, projects, operations, audit and usage."
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

resource "twc_database_instance" "control_plane" {
  cluster_id  = twc_database_cluster.control_plane.id
  name        = "platform"
  description = "Admin/control-plane data only; never user-managed databases."
}

resource "twc_database_instance" "integration_test" {
  cluster_id  = twc_database_cluster.control_plane.id
  name        = "ai_native_paas_integration_test"
  description = "Destructive PostgreSQL integration-test database; never used by production services."
}

resource "twc_database_instance" "harbor" {
  cluster_id  = twc_database_cluster.control_plane.id
  name        = "harbor"
  description = "Internal Harbor metadata only; tenant application data is forbidden."
}

resource "twc_database_user" "control_plane" {
  cluster_id  = twc_database_cluster.control_plane.id
  login       = "platform_admin"
  password    = random_password.control_plane.result
  host        = "%"
  description = "Application owner for the AI-native DevOps control-plane database."

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

  instance {
    instance_id = twc_database_instance.integration_test.id
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

resource "random_password" "harbor" {
  # Keep this independent from platform_admin. Advance only through the Harbor
  # rotation runbook and reconcile every in-cluster consumer atomically.
  keepers = {
    rotation = "2026-07-17-harbor-internal-bootstrap-2"
  }

  # Timeweb DBaaS accepts passwords from 8 through 30 characters.
  length  = 30
  special = true
  # Timeweb permits this subset and rejects some punctuation generated by the
  # provider default alphabet.
  override_special = "!@#%^_"
  min_lower        = 8
  min_upper        = 4
  min_numeric      = 4
  min_special      = 4
}

resource "twc_database_user" "harbor" {
  cluster_id = twc_database_cluster.control_plane.id
  # Timeweb requires database logins to be at least eight characters.
  login       = "harbor_admin"
  password    = random_password.harbor.result
  host        = "%"
  description = "Least-privilege owner for the internal Harbor metadata database."

  instance {
    instance_id = twc_database_instance.harbor.id
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

resource "twc_s3_bucket" "workspace_logs" {
  name        = var.workspace_log_bucket_name
  description = "Private encrypted stdout/stderr artifacts from disposable workspace commands."
  type        = "private"
  preset_id   = var.workspace_log_bucket_preset_id
  project_id  = var.project_id

  lifecycle {
    prevent_destroy = true
  }
}

resource "twc_s3_bucket" "harbor_blobs" {
  name        = var.harbor_blob_bucket_name
  description = "Private Harbor OCI blobs only; never reuse for state, logs, images or tenant S3."
  type        = "private"
  preset_id   = var.harbor_blob_bucket_preset_id
  project_id  = var.project_id

  lifecycle {
    prevent_destroy = true
  }
}
