resource "h3_snapshot" "before_upgrade" {
  project_id = var.project_id
  disk_id    = h3_disk.data.id
  name       = "before-upgrade"
}
