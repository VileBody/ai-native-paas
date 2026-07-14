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

variable "location" {
  description = "Moscow Timeweb location."
  type        = string
  default     = "ru-3"
}

variable "availability_zone" {
  description = "Moscow availability zone for the workspace NAT address."
  type        = string
  default     = "msk-1"
}

variable "router_preset_id" {
  description = "Timeweb one-node 1 vCPU router preset in ru-3."
  type        = number
  default     = 2009
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
