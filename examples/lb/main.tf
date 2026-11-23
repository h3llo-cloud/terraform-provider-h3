terraform {
  required_providers {
    h3 = {
      source  = "h3llo-cloud/h3"
      version = "~> 0.3"
    }
  }
}

# export H3_KEY_ID=... H3_SECRET_KEY=...   (api_endpoint: https://api.h3llo.cloud by default)
# The load balancer API must be reachable with an API key (pending on the platform side as of 0.3.0).
provider "h3" {}

variable "project_id" {
  type = string
}

variable "ssh_key_id" {
  type = string
}

resource "h3_vm" "web" {
  count       = 2
  project_id  = var.project_id
  name        = "web-${count.index}"
  cpu         = 2
  memory      = "4Gi"
  image       = "ubuntu:24.04"
  disk_size   = "20Gi"
  ssh_key_ids = [var.ssh_key_id]
}

resource "h3_lb" "web" {
  project_id = var.project_id
  name       = "web"

  listener = [
    { port = 80, target_port = 80 },
  ]
  targets = h3_vm.web[*].internal_ip
}

data "h3_lbs" "all" {
  project_id = var.project_id
  depends_on = [h3_lb.web]
}

output "lb_ip" {
  value = h3_lb.web.external_ip
}

output "lb_count" {
  value = length(data.h3_lbs.all.load_balancers)
}
