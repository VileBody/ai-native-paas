variable "project_id" {
  description = "Existing Timeweb Cloud project that owns the isolated cluster and managed database."
  type        = number
}

variable "location" {
  description = "Timeweb Cloud location. ru-3 is Moscow."
  type        = string
  default     = "ru-3"
}

variable "availability_zone" {
  description = "Availability zone used by the Kubernetes and managed PostgreSQL resources."
  type        = string
  default     = "msk-1"
}

variable "kubernetes_version" {
  description = "Kubernetes version reported by the Timeweb Cloud API."
  type        = string
  default     = "v1.35.6+k0s.0"
}

variable "master_preset_id" {
  description = "Moscow Kubernetes master preset: 2 CPU, 2 GiB RAM, 30 GiB disk."
  type        = number
  default     = 1673
}

variable "worker_preset_id" {
  description = "Moscow Kubernetes worker preset: 2 CPU, 4 GiB RAM, 60 GiB disk."
  type        = number
  default     = 1683
}

variable "postgres_preset_id" {
  description = "Moscow managed database preset: 1 CPU, 2 GiB RAM, 20 GiB disk."
  type        = number
  default     = 1175
}
