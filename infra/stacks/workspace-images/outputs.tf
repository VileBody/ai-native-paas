output "workspace_vpc_id" {
  value = var.workspace_vpc_id
}

output "workspace_vpc_cidr" {
  value = "192.168.75.0/24"
}

output "workspace_router_id" {
  value = var.workspace_router_id
}

output "workspace_nat_ip" {
  value = var.workspace_nat_ip
}

output "image_staging_bucket_name" {
  value = twc_s3_bucket.image_staging.full_name
}

output "image_staging_endpoint" {
  value = "https://s3.twcstorage.ru"
}

output "image_staging_access_key" {
  value     = twc_s3_bucket.image_staging.access_key
  sensitive = true
}

output "image_staging_secret_key" {
  value     = twc_s3_bucket.image_staging.secret_key
  sensitive = true
}

output "workspace_image_lock" {
  value = terraform_data.workspace_image_lock.output
}
