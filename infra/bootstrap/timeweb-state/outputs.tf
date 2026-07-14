output "bucket_id" {
  value = twc_s3_bucket.state.id
}

output "bucket_name" {
  value = twc_s3_bucket.state.full_name
}

output "endpoint" {
  value = "https://s3.twcstorage.ru"
}

output "access_key" {
  value     = twc_s3_bucket.state.access_key
  sensitive = true
}

output "secret_key" {
  value     = twc_s3_bucket.state.secret_key
  sensitive = true
}
