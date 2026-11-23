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

variable "user_id" {
  type        = string
  description = "Your user id (the owner of the API key)"
}

variable "ssh_public_key" {
  type        = string
  description = "OpenSSH public key, e.g. the contents of ~/.ssh/id_ed25519.pub"
}

resource "h3_ssh_key" "deploy" {
  user_id    = var.user_id
  name       = "deploy"
  public_key = var.ssh_public_key
}

resource "h3_vm" "test" {
  project_id = var.project_id
  name       = "terraform-test-vm"
  cpu        = 2
  memory     = "4Gi"
  disk_size  = "15Gi"
  image      = "ubuntu:24.04"
  white_ip   = true
  # Keys come from h3ssh only; changing the list does not restart the VM.
  ssh_key_ids = [h3_ssh_key.deploy.id]
}

output "vm_id" {
  value = h3_vm.test.id
}

output "vm_status" {
  value = h3_vm.test.status
}
