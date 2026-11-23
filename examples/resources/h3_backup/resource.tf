# Off-site copy of a snapshot in the separate backup storage.
resource "h3_backup" "weekly" {
  project_id  = var.project_id
  snapshot_id = h3_snapshot.before_upgrade.id
  name        = "weekly"
}
