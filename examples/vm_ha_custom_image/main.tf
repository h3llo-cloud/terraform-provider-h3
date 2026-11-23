# Three VMs from your own image, each on its own host, configured by cloud-init
# and protected from accidental deletion.
terraform {
  required_providers {
    h3 = {
      source  = "h3llo-cloud/h3"
      version = "~> 0.3"
    }
  }
}

# export H3_KEY_ID=... H3_SECRET_KEY=... H3_PROJECT_ID=...
provider "h3" {}

variable "ssh_key_id" {
  type        = string
  description = "ID of your h3ssh key (h3_ssh_key or the console)"
}

resource "h3_vm_image" "debian" {
  name    = "debian-12"
  url     = "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2"
  size_gi = 3
}

resource "h3_vm_placement_group" "web" {
  name = "web"
}

resource "h3_vm" "web" {
  count       = 3
  name        = "web-${count.index}"
  cpu         = 2
  memory      = "4Gi"
  image       = h3_vm_image.debian.image
  disk_size   = "20Gi"
  ssh_key_ids = [var.ssh_key_id]

  placement_group_id  = h3_vm_placement_group.web.id
  deletion_protection = true

  # Runs once, on the first boot, after the platform setup (user `user`, SSH keys).
  user_data = <<-EOT
    #cloud-config
    package_update: true
    packages: [nginx]
    write_files:
      - path: /var/www/html/index.html
        content: "web-${count.index}\n"
    runcmd:
      - systemctl enable --now nginx
  EOT
}

output "web_ips" {
  value = h3_vm.web[*].internal_ip
}
