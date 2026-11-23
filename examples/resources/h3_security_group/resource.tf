resource "h3_security_group" "web" {
  project_id = var.project_id
  name       = "web"

  rule {
    direction   = "ingress"
    protocol    = "tcp"
    port_from   = 443
    remote_cidr = "0.0.0.0/0"
  }
  rule {
    direction   = "egress"
    remote_cidr = "0.0.0.0/0"
  }
}
