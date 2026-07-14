variable "project_id" {
  description = "BALOVSTVO Timeweb project ID."
  type        = number
}

variable "state_passphrase" {
  description = "Offline recovery passphrase used for client-side OpenTofu state and plan encryption."
  type        = string
  sensitive   = true

  validation {
    condition     = length(var.state_passphrase) >= 32
    error_message = "state_passphrase must contain at least 32 characters."
  }
}

variable "state_bucket_name" {
  description = "Human-readable Timeweb S3 bucket name; Timeweb prefixes the final bucket name."
  type        = string
  default     = "ai-native-paas-state"
}

variable "state_bucket_preset_id" {
  description = "Timeweb S3 Hot 10 GiB preset in ru-1."
  type        = number
  default     = 2669
}
