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

# All images offered for new VMs.
data "h3_vm_images" "all" {}

# Supported Ubuntu versions, names in English.
data "h3_vm_images" "ubuntu" {
  family = "ubuntu"
  status = "supported"
  lang   = "en"
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

resource "h3_vm" "web" {
  project_id = var.project_id
  name       = "web-1"
  cpu        = 2
  memory     = "4Gi"
  # Newest supported Ubuntu; the disk is not smaller than the image minimum.
  image       = data.h3_vm_images.ubuntu.images[0].id
  disk_size   = "${max(20, data.h3_vm_images.ubuntu.images[0].min_disk_gi)}Gi"
  ssh_key_ids = [h3_ssh_key.deploy.id]
}

output "default_image" {
  value = data.h3_vm_images.all.default
}

output "image_ids" {
  value = [for i in data.h3_vm_images.all.images : i.id]
}
