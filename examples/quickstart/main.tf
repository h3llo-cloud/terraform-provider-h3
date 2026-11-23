# VPC + subnet + SSH key + VM with a stable public IP + data disk.
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
  description = "ID of the user who owns the API key"
}

variable "ssh_public_key" {
  type        = string
  description = "OpenSSH public key, e.g. the contents of ~/.ssh/id_ed25519.pub"
}

data "h3_vm_images" "ubuntu" {
  family = "ubuntu"
  status = "supported"
}

resource "h3_vpc" "main" {
  project_id = var.project_id
  name       = "main"
}

resource "h3_network" "app" {
  project_id = var.project_id
  name       = "app"
  vpc_id     = h3_vpc.main.id
  cidr_block = "10.10.0.0/24"
}

resource "h3_ssh_key" "deploy" {
  user_id    = var.user_id
  name       = "deploy"
  public_key = var.ssh_public_key
}

resource "h3_vm" "web" {
  project_id  = var.project_id
  name        = "web-1"
  cpu         = 2
  memory      = "4Gi"
  image       = data.h3_vm_images.ubuntu.images[0].id
  disk_size   = "30Gi"
  ssh_key_ids = [h3_ssh_key.deploy.id]
  subnet_name = h3_network.app.subnet_name
}

# The address belongs to the project, not to the VM: recreate the VM, keep the IP.
resource "h3_eip" "web" {
  project_id = var.project_id
  name       = "web-ip"
  vm_id      = h3_vm.web.id
}

resource "h3_disk" "data" {
  project_id    = var.project_id
  name          = "web-data"
  size          = "50Gi"
  storage_class = "replicated"

  lifecycle {
    prevent_destroy = true
  }
}

output "public_ip" {
  value = h3_eip.web.ip_address
}

output "internal_ip" {
  value = h3_vm.web.internal_ip
}
