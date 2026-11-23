terraform {
  required_providers {
    h3 = {
      source  = "h3llo-cloud/h3"
      version = "~> 0.3"
    }
  }
}

# export H3_KEY_ID=... H3_SECRET_KEY=...   (api_endpoint: https://api.h3llo.cloud by default)
provider "h3" {}

variable "project_id" {
  type = string
}

variable "ssh_key_id" {
  type = string
}

# Requires a platform version with EIP selection on VM create (h3vm eip_id).
# A project Elastic IP that outlives the VM: recreate the VM and keep the same address.
resource "h3_eip" "web" {
  project_id = var.project_id
  name       = "web-ip"
  # vm_id is intentionally unset: the VM selects the EIP via eip_id.
}

resource "h3_vm" "web" {
  project_id  = var.project_id
  name        = "web"
  cpu         = 2
  memory      = "4Gi"
  image       = "ubuntu:24.04"
  ssh_key_ids = [var.ssh_key_id]

  white_ip = true # required with eip_id
  eip_id   = h3_eip.web.id

  # Default false: destroying the VM returns the EIP to the project pool (still billed).
  release_eip_on_destroy = false
}

output "public_ip" {
  value = h3_eip.web.ip_address
}
