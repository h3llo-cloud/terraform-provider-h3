# A custom image made in the console or another configuration (the newest with this name).
data "h3_vm_image" "golden" {
  project_id = var.project_id
  name       = "app-golden-2026-11"
}

resource "h3_vm" "app" {
  project_id  = var.project_id
  name        = "app-2"
  cpu         = 2
  memory      = "4Gi"
  image       = data.h3_vm_image.golden.image
  disk_size   = "${data.h3_vm_image.golden.size_gi}Gi"
  ssh_key_ids = [var.ssh_key_id]
}
