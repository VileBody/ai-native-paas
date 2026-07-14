resource "twc_vpc" "workspace" {
  name        = "ai-native-paas-workspaces"
  description = "Isolated Moscow VPC for disposable per-task workspace VMs."
  location    = var.location
  subnet_v4   = "192.168.75.0/24"
}

resource "twc_s3_bucket" "image_staging" {
  name        = var.image_staging_bucket_name
  description = "Private staging for pinned Timeweb custom-image imports."
  type        = "private"
  preset_id   = var.image_staging_bucket_preset_id
  project_id  = var.project_id

  lifecycle {
    prevent_destroy = true
  }
}

resource "terraform_data" "workspace_image_lock" {
  input = {
    image_id = var.workspace_image_id
    digest   = var.workspace_image_digest
  }

  lifecycle {
    precondition {
      condition     = (var.workspace_image_id == "") == (var.workspace_image_digest == "")
      error_message = "workspace_image_id and workspace_image_digest must be supplied together."
    }
  }
}
