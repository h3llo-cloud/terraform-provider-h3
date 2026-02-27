terraform {
  required_providers {
    h3 = {
      source  = "h3llo-cloud/h3"
    }
  }
}

provider "h3" {
  api_endpoint = "https://api.h3llo.cloud"
  key_id       = "your-api-key-id-here"
  secret_key   = "your-api-key-secret-here"
}

resource "h3_vm" "test" {
  project_id = "your-project-id-here"
  name       = "terraform-test-vm"
  cpu        = 2
  memory     = "4Gi"
  disk_size  = "15Gi"
  image      = "ubuntu:24.04"
  ssh_key    = "ssh-ed25519 ******** key-description"
  white_ip   = true
}

output "vm_id" {
  value = h3_vm.test.id
}

output "vm_status" {
  value = h3_vm.test.status
}
