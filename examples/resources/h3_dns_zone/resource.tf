resource "h3_dns_zone" "main" {
  project_id  = var.project_id
  name        = "example.com"
  dnssec      = true
  default_ttl = 300
}
