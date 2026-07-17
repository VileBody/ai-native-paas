variable "project_id" {
  description = "Existing Timeweb BALOVSTVO project that owns the admin cluster and managed database."
  type        = number
}

variable "state_passphrase" {
  description = "Offline OpenTofu state recovery passphrase."
  type        = string
  sensitive   = true

  validation {
    condition     = length(var.state_passphrase) >= 32
    error_message = "state_passphrase must contain at least 32 characters."
  }
}

variable "legacy_edge_private_ip" {
  description = "Private IP of the CI worker receiving the temporary ADR 0006 NodePorts."
  type        = string
  default     = "192.168.73.5"

  validation {
    condition     = can(cidrhost("${var.legacy_edge_private_ip}/32", 0))
    error_message = "legacy_edge_private_ip must be an IPv4 address."
  }
}

variable "location" {
  description = "Timeweb Cloud location. ru-3 is Moscow."
  type        = string
  default     = "ru-3"
}

variable "availability_zone" {
  description = "Moscow availability zone."
  type        = string
  default     = "msk-1"
}

variable "kubernetes_version" {
  description = "Pinned managed Kubernetes version."
  type        = string
  default     = "v1.35.6+k0s.0"
}

variable "master_preset_id" {
  description = "Moscow Kubernetes master preset: 2 CPU, 2 GiB RAM, 30 GiB disk."
  type        = number
  default     = 1673
}

variable "ci_worker_preset_id" {
  description = "Existing Moscow CI worker preset: 2 CPU, 4 GiB RAM, 60 GiB disk."
  type        = number
  default     = 1683
}

variable "system_worker_preset_id" {
  description = "Moscow system worker preset: 4 CPU, 8 GiB RAM, 120 GiB disk."
  type        = number
  default     = 1685
}

variable "admin_capacity_mode" {
  description = "Admin service capacity: off removes the system node group, dev uses one system worker, and ha uses three."
  type        = string
  default     = "ha"

  validation {
    condition     = contains(["off", "dev", "ha"], var.admin_capacity_mode)
    error_message = "admin_capacity_mode must be off, dev, or ha."
  }
}

variable "postgres_preset_id" {
  description = "Moscow managed PostgreSQL preset: 1 CPU, 2 GiB RAM, 20 GiB disk."
  type        = number
  default     = 1175
}

variable "workspace_log_bucket_name" {
  description = "Private S3 bucket for client-encrypted workspace command logs."
  type        = string
  default     = "ai-native-paas-workspace-logs"
}

variable "workspace_log_bucket_preset_id" {
  description = "Timeweb S3 Hot 10 GiB preset in ru-1."
  type        = number
  default     = 2669
}
