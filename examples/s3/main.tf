terraform {
  required_providers {
    h3 = {
      source = "h3llo-cloud/h3"
    }
  }
}

provider "h3" {
  api_endpoint = "https://api.h3llo.cloud"
  key_id       = "your-api-key-id-here"
  secret_key   = "your-api-key-secret-here"
}

resource "h3_s3_bucket" "data" {
  project_id = "your-project-id-here"
  name       = "my-data-bucket"
}

resource "h3_s3_bucket" "media" {
  project_id = "your-project-id-here"
  name       = "my-media-bucket"
}

output "data_bucket_id" {
  description = "The ID of the data bucket"
  value       = h3_s3_bucket.data.id
}

output "data_bucket_name" {
  description = "Name of the data bucket"
  value       = h3_s3_bucket.data.name
}

output "data_bucket_access_key_id" {
  description = "S3 Access Key ID for data bucket"
  value       = h3_s3_bucket.data.access_key_id
  sensitive   = true
}

output "data_bucket_secret_access_key" {
  description = "S3 Secret Access Key for data bucket"
  value       = h3_s3_bucket.data.secret_access_key
  sensitive   = true
}

output "media_bucket_id" {
  description = "The ID of the media bucket"
  value       = h3_s3_bucket.media.id
}

output "media_bucket_name" {
  description = "Name of the media bucket"
  value       = h3_s3_bucket.media.name
}
