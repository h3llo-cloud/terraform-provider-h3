resource "h3_disk" "data" {
  project_id        = var.project_id
  name              = "pg-data"
  size              = "100Gi" # grows in place; shrinking is refused at plan time
  storage_class     = "replicated"
  performance_class = "R_FAST" # changes in place

  lifecycle {
    prevent_destroy = true
  }
}
