data "h3_security_group" "default" {
  project_id = var.project_id
  name       = "default"
}
