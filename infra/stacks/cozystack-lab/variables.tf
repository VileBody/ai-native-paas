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

variable "lab_enabled" {
  description = "Second explicit compute cost guard. It must remain false while cozystack_profile is off."
  type        = bool
  default     = false

  validation {
    condition = (
      (var.cozystack_profile == "off" && !var.lab_enabled) ||
      (var.cozystack_profile != "off" && var.lab_enabled)
    )
    error_message = "lab_enabled must be false for off and true for smoke/provider_gate/provider_gate_full."
  }
}

variable "cozystack_profile" {
  description = "Cost profile for the runtime cell: off, smoke, provider_gate, or provider_gate_full."
  type        = string
  default     = "off"

  validation {
    condition     = contains(["off", "smoke", "provider_gate", "provider_gate_full"], var.cozystack_profile)
    error_message = "cozystack_profile must be off, smoke, provider_gate, or provider_gate_full."
  }

  validation {
    condition = var.cozystack_profile == "off" || (
      var.runtime_vpc_id != "" &&
      var.runtime_edge_private_ip != "" &&
      var.runtime_ingress_ip != ""
    )
    error_message = "A live Cozystack profile requires network-foundation runtime VPC and edge outputs."
  }

  validation {
    condition     = var.cozystack_profile == "off" || var.transition_from_profile == "off"
    error_message = "A non-zero profile may only be entered from off. Back up, destroy to off, and recreate instead of resizing in place."
  }
}

variable "cost_guard_acknowledgement" {
  description = "Exact acknowledgement required for the selected non-zero cost profile."
  type        = string
  default     = ""

  validation {
    condition = (
      (var.cozystack_profile == "off" && var.cost_guard_acknowledgement == "") ||
      (var.cozystack_profile == "smoke" && var.cost_guard_acknowledgement == "CREATE-3X-4VCPU-8GIB-COZYSTACK-SMOKE") ||
      (var.cozystack_profile == "provider_gate" && var.cost_guard_acknowledgement == "CREATE-3X-8VCPU-24GIB-COZYSTACK-PROVIDER-GATE") ||
      (var.cozystack_profile == "provider_gate_full" && var.cost_guard_acknowledgement == "CREATE-3X-8VCPU-24GIB-COZYSTACK-PROVIDER-GATE-FULL")
    )
    error_message = "Use the exact cost acknowledgement for smoke/provider_gate/provider_gate_full; off requires an empty acknowledgement."
  }
}

variable "transition_from_profile" {
  description = "Profile recorded before this operation. Entering a live profile requires off; teardown to off accepts any non-zero source."
  type        = string
  default     = "off"

  validation {
    condition     = contains(["off", "smoke", "provider_gate", "provider_gate_full"], var.transition_from_profile)
    error_message = "transition_from_profile must be off, smoke, provider_gate, or provider_gate_full."
  }
}

variable "runtime_vpc_id" {
  description = "Runtime VPC ID from the independently owned network-foundation state."
  type        = string
  default     = ""
}

variable "runtime_router_id" {
  description = "Live shared NAT router ID from network-foundation; required only by the private bootstrap runner."
  type        = string
  default     = ""
}

variable "runtime_gateway_ip" {
  description = "Private gateway of network-foundation's shared NAT router; it is not a DNS resolver or a dedicated public IPv4."
  type        = string
  default     = "192.168.74.4"

  validation {
    condition     = var.runtime_gateway_ip == "192.168.74.4"
    error_message = "runtime_gateway_ip must remain the reviewed Timeweb runtime-router gateway 192.168.74.4."
  }
}

variable "node_bootstrap_ssh_key_ids" {
  description = "Reviewed operator SSH public-key IDs used only by the temporary Ubuntu disk writer; inbound SSH remains blocked."
  type        = set(number)
  default     = []

  validation {
    condition     = var.cozystack_profile == "off" || length(var.node_bootstrap_ssh_key_ids) > 0
    error_message = "A live Cozystack profile requires at least one reviewed SSH key so Timeweb never generates a root password."
  }
}

variable "bootstrap_runner_enabled" {
  description = "Creates the one-shot private bootstrap runner. Set false immediately after encrypted evidence upload."
  type        = bool
  default     = false

  validation {
    condition = !var.bootstrap_runner_enabled || (
      var.cozystack_profile != "off" &&
      var.runtime_router_id != "" &&
      length(var.bootstrap_runner_ssh_key_ids) > 0 &&
      can(regex("^https://", var.bootstrap_bundle_url)) &&
      can(regex("^[0-9a-f]{64}$", var.bootstrap_bundle_sha256)) &&
      length(var.bootstrap_bundle_passphrase) >= 32 &&
      can(regex("^https://", var.bootstrap_evidence_upload_url)) &&
      var.bootstrap_bundle_generation > 0
    )
    error_message = "The private bootstrap runner requires live profile/router, SSH identity, signed HTTPS URLs, SHA-256, 32+ character passphrase and positive generation."
  }
}

variable "talos_bootstrap_dnat_enabled" {
  description = "Adds the node-side /32 exception paired with network-foundation's temporary Talos DNAT for an ephemeral admin Kubernetes Job."
  type        = bool
  default     = false

  validation {
    condition = !var.talos_bootstrap_dnat_enabled || (
      var.cozystack_profile != "off" &&
      !var.bootstrap_runner_enabled &&
      var.runtime_router_id != ""
    )
    error_message = "Talos bootstrap DNAT requires a live Cozystack profile/router and cannot coexist with the private runner VM."
  }
}

variable "talos_bootstrap_source_cidr" {
  description = "Observed admin Kubernetes Job egress IPv4 as one non-wildcard /32; used only by the temporary node firewall rule."
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
  description = "Adds the node-side /32 SSH exception paired with network-foundation's temporary disk-repair DNAT."
  type        = bool
  default     = false

  validation {
    condition = !var.talos_disk_repair_ssh_enabled || (
      var.cozystack_profile != "off" &&
      !var.bootstrap_runner_enabled &&
      var.runtime_router_id != ""
    )
    error_message = "Talos disk-repair SSH requires a live Cozystack profile/router and cannot coexist with the private runner VM."
  }
}

variable "talos_disk_repair_ssh_source_cidr" {
  description = "Observed operator IPv4 as one non-wildcard /32; used only by the temporary SSH repair firewall rule."
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

variable "bootstrap_runner_ssh_key_ids" {
  description = "Reviewed operator SSH public-key IDs. Port 22 remains blocked; this only lets Timeweb omit a root password."
  type        = set(number)
  default     = []
}

variable "bootstrap_runner_preset_id" {
  description = "Small private bootstrap-runner preset in the Moscow ru-3 location."
  type        = number
  default     = 5943

  validation {
    condition     = var.bootstrap_runner_preset_id == 5943
    error_message = "bootstrap_runner_preset_id must remain the reviewed Moscow 1 vCPU / 1 GiB preset 5943."
  }
}

variable "bootstrap_bundle_url" {
  description = "Short-lived signed HTTPS GET URL for the encrypted bootstrap bundle."
  type        = string
  default     = ""
  sensitive   = true
}

variable "bootstrap_bundle_sha256" {
  description = "SHA-256 of the encrypted bootstrap bundle."
  type        = string
  default     = ""
}

variable "bootstrap_bundle_passphrase" {
  description = "Ephemeral passphrase for encrypted input/result bundles; rotate every generation."
  type        = string
  default     = ""
  sensitive   = true
}

variable "bootstrap_evidence_upload_url" {
  description = "Short-lived signed HTTPS PUT URL for encrypted evidence, kubeconfig and talosconfig."
  type        = string
  default     = ""
  sensitive   = true
}

variable "bootstrap_bundle_generation" {
  description = "Monotonic one-shot bootstrap attempt generation."
  type        = number
  default     = 0
}

variable "talosctl_url" {
  description = "Pinned upstream Linux AMD64 talosctl binary used only by the private runner."
  type        = string
  default     = "https://github.com/siderolabs/talos/releases/download/v1.13.0/talosctl-linux-amd64"
}

variable "talosctl_sha256" {
  description = "Official SHA-256 for the pinned Linux AMD64 talosctl binary."
  type        = string
  default     = "4aa5cd191c708b8c1a3b358bfd7dd21fb0cc6bd4dc7a07f2aa925cd2a8473bae"

  validation {
    condition     = can(regex("^[0-9a-f]{64}$", var.talosctl_sha256))
    error_message = "talosctl_sha256 must be 64 lowercase hexadecimal characters."
  }
}

variable "runtime_edge_private_ip" {
  description = "Private Timeweb LB address used as the Talos cluster endpoint."
  type        = string
  default     = ""

  validation {
    condition     = var.runtime_edge_private_ip == "" || var.runtime_edge_private_ip == "192.168.74.6"
    error_message = "runtime_edge_private_ip must be empty or the live Timeweb-assigned 192.168.74.6 address."
  }
}

variable "runtime_ingress_ip" {
  description = "Preserved public runtime ingress included in the Kubernetes API certificate SANs."
  type        = string
  default     = ""

  validation {
    condition     = var.runtime_ingress_ip == "" || var.runtime_ingress_ip == "5.42.126.95"
    error_message = "runtime_ingress_ip must be empty or the reviewed 5.42.126.95 address."
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
