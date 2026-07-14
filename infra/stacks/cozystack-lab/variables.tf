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

variable "location" {
  description = "Moscow Timeweb location."
  type        = string
  default     = "ru-3"
}

variable "availability_zone" {
  description = "Moscow availability zone."
  type        = string
  default     = "msk-1"
}

variable "node_preset_id" {
  description = "Dedicated CPU 8 vCPU / 32 GiB / 100 GiB Timeweb preset."
  type        = number
  default     = 6633
}

variable "enabled_nodes" {
  description = "Node names managed in this operation; defaults to the complete three-node HA cell."
  type        = set(string)
  default     = ["cp-1", "cp-2", "cp-3"]

  validation {
    condition = (
      length(var.enabled_nodes) >= 2 &&
      alltrue([for node in var.enabled_nodes : contains(["cp-1", "cp-2", "cp-3"], node)])
    )
    error_message = "enabled_nodes must contain at least two of cp-1, cp-2 and cp-3."
  }
}

variable "bootstrap_os_id" {
  description = "Timeweb Ubuntu 24.04 image used only to write the pinned Talos RAW image in Moscow."
  type        = number
  default     = 99
}

variable "talos_asset_url" {
  description = "Pinned Cozystack Talos nocloud xz asset."
  type        = string
  default     = "https://github.com/cozystack/cozystack/releases/download/v1.5.0/nocloud-amd64.raw.xz"

  validation {
    condition     = startswith(var.talos_asset_url, "https://")
    error_message = "talos_asset_url must use HTTPS."
  }
}

variable "talos_compressed_sha256" {
  description = "SHA-256 of the pinned xz asset."
  type        = string
  default     = "92b0caa5d5cc5f802d042671ccbb4b097c5133dfa9a86c101420761f7e2a0df8"

  validation {
    condition     = can(regex("^[0-9a-f]{64}$", var.talos_compressed_sha256))
    error_message = "talos_compressed_sha256 must be 64 lowercase hexadecimal characters."
  }
}

variable "talos_raw_sha256" {
  description = "SHA-256 of the decompressed Talos RAW image."
  type        = string
  default     = "8b33667aa31957df641e832d5fa4c344f48dbd0c64e784fa98a1dc698d524ecb"

  validation {
    condition     = can(regex("^[0-9a-f]{64}$", var.talos_raw_sha256))
    error_message = "talos_raw_sha256 must be 64 lowercase hexadecimal characters."
  }
}

variable "talos_raw_size_bytes" {
  description = "Exact decompressed RAW size from the xz index."
  type        = number
  default     = 4453302272
}

variable "talos_bootstrap_timeout_seconds" {
  description = "Maximum duration for the one-shot Ubuntu-to-Talos disk bootstrap."
  type        = number
  default     = 1800

  validation {
    condition     = var.talos_bootstrap_timeout_seconds >= 600 && var.talos_bootstrap_timeout_seconds <= 3600
    error_message = "talos_bootstrap_timeout_seconds must be between 600 and 3600."
  }
}

variable "talos_version" {
  description = "Pinned Cozystack Talos version used by both the boot image and installer."
  type        = string
  default     = "v1.13.0"
}

variable "kubernetes_version" {
  description = "Pinned Kubernetes version for the runtime cell."
  type        = string
  default     = "1.35.0"
}

variable "talos_installer_image" {
  description = "Immutable installer from the Cozystack v1.5 Talos build."
  type        = string
  default     = "ghcr.io/cozystack/cozystack/talos:v1.13.0@sha256:37caed57ac67316af15ecf55f05b04864527bed9f4b379abd681ea6ece9a64a4"

  validation {
    condition     = can(regex("@sha256:[0-9a-f]{64}$", var.talos_installer_image))
    error_message = "talos_installer_image must be immutable and end in @sha256:<64 hex>."
  }
}

variable "cozystack_version" {
  description = "Cozystack release installed after the Talos cluster is healthy."
  type        = string
  default     = "v1.5.0"
}

variable "management_cidrs" {
  description = "Operator source CIDRs allowed to reach Talos API 50000 and Kubernetes API 6443."
  type        = set(string)

  validation {
    condition     = length(var.management_cidrs) > 0 && alltrue([for cidr in var.management_cidrs : can(cidrnetmask(cidr)) && cidr != "0.0.0.0/0" && cidr != "::/0"])
    error_message = "management_cidrs must contain valid non-public-wildcard CIDRs."
  }
}

variable "data_disk_size_mb" {
  description = "Per-node Cozystack data disk; 260 GiB is the first Timeweb 5 GiB step above 256 GiB."
  type        = number
  default     = 266240

  validation {
    condition     = var.data_disk_size_mb >= 262144 && var.data_disk_size_mb % 5120 == 0
    error_message = "data disk must be at least 256 GiB and use Timeweb's 5 GiB allocation step."
  }
}
