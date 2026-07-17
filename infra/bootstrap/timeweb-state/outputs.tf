output "bucket_id" {
  value = twc_s3_bucket.state.id
}

output "bucket_name" {
  value = twc_s3_bucket.state.full_name
}

output "endpoint" {
  value = "https://s3.twcstorage.ru"
}
