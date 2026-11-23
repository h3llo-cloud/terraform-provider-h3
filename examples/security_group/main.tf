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

variable "project_id" { type = string }
variable "vm_id" { type = string }

# Every project has a "default" group that allows all traffic.
data "h3_security_group" "default" {
  project_id = var.project_id
  name       = "default"
}

# Inline rules: the resource owns the whole rule set.
resource "h3_security_group" "web" {
  project_id  = var.project_id
  name        = "web"
  description = "HTTPS from anywhere, SSH from the office"

  rule {
    direction   = "ingress"
    protocol    = "tcp"
    port_from   = 443
    remote_cidr = "0.0.0.0/0"
  }
  rule {
    direction   = "ingress"
    protocol    = "tcp"
    port_from   = 22
    remote_cidr = "203.0.113.0/24"
    description = "office"
  }
  rule {
    direction   = "egress"
    remote_cidr = "0.0.0.0/0"
  }
}

# Separate rule resources: the group itself has no rule blocks.
resource "h3_security_group" "db" {
  project_id            = var.project_id
  name                  = "db"
  revoke_default_egress = true
}

resource "h3_security_group_rule" "pg_from_web" {
  security_group_id        = h3_security_group.db.id
  direction                = "ingress"
  protocol                 = "tcp"
  port_from                = 5432
  remote_security_group_id = h3_security_group.web.id
}

# The VM gets exactly these groups (live, no restart). On destroy the VM goes
# back to the project "default" group.
resource "h3_vm_security_groups" "web" {
  vm_id              = var.vm_id
  security_group_ids = [h3_security_group.web.id]
}

# Another VM keeps its groups and gets one more (non-exclusive).
variable "worker_vm_id" {
  type    = string
  default = "00000000-0000-4000-8000-000000000002"
}

resource "h3_vm_security_group_attachment" "worker_web" {
  vm_id             = var.worker_vm_id
  security_group_id = h3_security_group.web.id
}
