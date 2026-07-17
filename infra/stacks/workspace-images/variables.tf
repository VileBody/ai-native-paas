variable "project_id" {
  description = "Timeweb BALOVSTVO project that owns disposable workspace infrastructure."
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

variable "workspace_vpc_id" {
  description = "Workspace VPC ID from the network-foundation state."
  type        = string
}

variable "workspace_router_id" {
  description = "Optional live shared router ID from network-foundation; empty while network mode is off."
  type        = string
  default     = ""
}

variable "workspace_nat_ip" {
  description = "Reviewed shared egress IPv4 from network-foundation."
  type        = string
  default     = "72.56.234.22"

  validation {
    condition     = var.workspace_nat_ip == "72.56.234.22"
    error_message = "workspace_nat_ip must remain the reviewed 72.56.234.22 reservation."
  }
}

variable "image_staging_bucket_name" {
  description = "Human-readable name for the private custom-image staging bucket."
  type        = string
  default     = "ai-native-paas-images"
}

variable "image_staging_bucket_preset_id" {
  description = "Timeweb S3 Hot 10 GiB preset in ru-1."
  type        = number
  default     = 2669
}

variable "workspace_image_id" {
  description = "Optional Timeweb custom image ID; empty until the signed beta workspace image is published."
  type        = string
  default     = ""
}

variable "workspace_image_digest" {
  description = "Optional sha256 digest for the signed workspace image."
  type        = string
  default     = ""

  validation {
    condition     = var.workspace_image_digest == "" || can(regex("^sha256:[0-9a-f]{64}$", var.workspace_image_digest))
    error_message = "workspace_image_digest must be empty or sha256:<64 hex>."
  }
}
