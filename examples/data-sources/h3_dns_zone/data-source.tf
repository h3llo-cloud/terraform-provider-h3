data "h3_dns_zone" "existing" {
  project_id = var.project_id
  name       = "example.com"
}
