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
