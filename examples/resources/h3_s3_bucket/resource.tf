resource "h3_s3_bucket" "assets" {
  project_id = var.project_id
  name       = "assets"
}

output "s3_access_key_id" {
  value     = h3_s3_bucket.assets.access_key_id
  sensitive = true
}
