variable "project_id" {
  description = "Existing Timeweb BALOVSTVO project ID."
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

variable "network_mode" {
  description = "Cost profile for the shared edge: off or live."
  type        = string
  default     = "off"

  validation {
    condition     = contains(["off", "live"], var.network_mode)
    error_message = "network_mode must be off or live."
  }
}

variable "cost_guard_acknowledgement" {
  description = "Exact acknowledgement required before creating the paid LB and router."
  type        = string
  default     = ""

  validation {
    condition = (
      (var.network_mode == "off" && var.cost_guard_acknowledgement == "") ||
      (var.network_mode == "live" && var.cost_guard_acknowledgement == "ENABLE-TWO-IP-RUNTIME-EDGE")
    )
    error_message = "live requires ENABLE-TWO-IP-RUNTIME-EDGE; off requires an empty acknowledgement."
  }
}

variable "location" {
  description = "Moscow Timeweb location."
  type        = string
  default     = "ru-3"
}

variable "availability_zone" {
  description = "Moscow availability zone of the two preserved IPv4 addresses."
  type        = string
  default     = "msk-1"
}

variable "load_balancer_preset_id" {
  description = "Timeweb ru-3 basic 140 RPS load-balancer preset."
  type        = number
  default     = 1115
}

variable "router_preset_id" {
  description = "Timeweb ru-3 one-node 1 vCPU router preset."
  type        = number
  default     = 2009
}

variable "kubernetes_api_cidrs" {
  description = "CIDRs allowed to reach the shared public Kubernetes API port."
  type        = set(string)
  default     = ["72.56.246.80/32", "5.129.202.241/32"]

  validation {
    condition     = length(var.kubernetes_api_cidrs) > 0 && alltrue([for cidr in var.kubernetes_api_cidrs : can(cidrhost(cidr, 0))])
    error_message = "kubernetes_api_cidrs must contain valid CIDRs."
  }
}

variable "talos_bootstrap_dnat_enabled" {
  description = "Temporarily maps three high ports on the preserved NAT IP to private Talos maintenance APIs for the ephemeral admin Kubernetes Job. Must return to false immediately after evidence upload."
  type        = bool
  default     = false

  validation {
    condition     = !var.talos_bootstrap_dnat_enabled || var.network_mode == "live"
    error_message = "Talos bootstrap DNAT is allowed only while network_mode is live."
  }
}

variable "talos_bootstrap_source_cidr" {
  description = "Single observed admin Kubernetes Job egress IPv4 allowed by the matching node-firewall exception. The router DNAT itself is not the security boundary."
  type        = string
  default     = ""

  validation {
    condition = !var.talos_bootstrap_dnat_enabled || (
      can(cidrhost(var.talos_bootstrap_source_cidr, 0)) &&
      can(regex("/32$", var.talos_bootstrap_source_cidr)) &&
      var.talos_bootstrap_source_cidr != "0.0.0.0/32"
    )
    error_message = "Enabled Talos bootstrap DNAT requires one non-wildcard IPv4 /32 source CIDR."
  }
}

variable "talos_disk_repair_ssh_enabled" {
  description = "Temporary source-restricted SSH DNAT used only to rerun the failed Ubuntu-to-Talos disk writer on existing nodes. Must return to false before Talos bootstrap evidence."
  type        = bool
  default     = false

  validation {
    condition     = !var.talos_disk_repair_ssh_enabled || var.network_mode == "live"
    error_message = "Talos disk-repair SSH is allowed only while network_mode is live."
  }
}

variable "talos_disk_repair_ssh_source_cidr" {
  description = "Single observed operator IPv4 /32 allowed to reach the temporary SSH DNAT."
  type        = string
  default     = ""

  validation {
    condition = !var.talos_disk_repair_ssh_enabled || (
      can(cidrhost(var.talos_disk_repair_ssh_source_cidr, 0)) &&
      can(regex("/32$", var.talos_disk_repair_ssh_source_cidr)) &&
      var.talos_disk_repair_ssh_source_cidr != "0.0.0.0/32"
    )
    error_message = "Enabled Talos disk-repair SSH requires one non-wildcard IPv4 /32 source CIDR."
  }
}
