resource "twc_s3_bucket" "state" {
  name        = var.state_bucket_name
  description = "Private versioned OpenTofu state for the AI-native DevOps platform."
  type        = "private"
  preset_id   = var.state_bucket_preset_id
  project_id  = var.project_id

  lifecycle {
    prevent_destroy = true
  }
}
