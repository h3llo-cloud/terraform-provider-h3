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

resource "h3_disk" "data" {
  project_id    = "your-project-id-here"
  name          = "my-data-disk"
  size          = "5Gi"
  storage_class = "replicated"
}

resource "h3_snapshot" "backup" {
  project_id = "your-project-id-here"
  disk_id    = h3_disk.data.id
  name       = "daily-backup"
}

resource "h3_backup" "offsite" {
  project_id  = "your-project-id-here"
  snapshot_id = h3_snapshot.backup.id
  name        = "offsite-backup"
}

output "disk_id" {
  description = "The ID of the created disk"
  value       = h3_disk.data.id
}

output "disk_status" {
  description = "Current status of the disk"
  value       = h3_disk.data.status
}

output "disk_size" {
  description = "Size of the disk"
  value       = h3_disk.data.size
}

output "snapshot_id" {
  description = "The ID of the snapshot"
  value       = h3_snapshot.backup.id
}

output "snapshot_status" {
  description = "Current status of the snapshot"
  value       = h3_snapshot.backup.status
}

output "backup_id" {
  description = "The ID of the backup"
  value       = h3_backup.offsite.id
}

output "backup_status" {
  description = "Current status of the backup"
  value       = h3_backup.offsite.status
}
