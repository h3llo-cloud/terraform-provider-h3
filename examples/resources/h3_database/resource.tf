resource "h3_database" "pg" {
  project_id = var.project_id
  name       = "app-db"
  engine     = "postgres"
  cpu        = 2 # cpu/memory change in place, the database restarts
  memory     = "4Gi"
  size       = "20Gi" # increase in place, online

  # PostgreSQL only, 20..5000 and at most 100 per Gi of memory.
  # Changed in place: the database restarts (~1 min; with replicas a switchover).
  max_connections = 300

  # h3db grows the disk itself up to max_size_gb; the grown size is not a diff
  # (size stays the configured minimum, current_size shows the real one).
  # Optional: without it a new database gets autoscale on with max = 2 x size.
  storage_autoscale = {
    enabled     = true
    max_size_gb = 100
  }

  # Changing name, engine or version creates a new, empty database.
  lifecycle {
    prevent_destroy = true
  }
}
