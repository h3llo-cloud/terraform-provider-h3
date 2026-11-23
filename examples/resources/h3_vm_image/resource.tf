# Import a cloud image over HTTPS (qcow2 or raw, optionally .gz/.xz).
resource "h3_vm_image" "alma" {
  project_id = var.project_id
  name       = "almalinux-9"
  url        = "https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/AlmaLinux-9-GenericCloud-latest.x86_64.qcow2"
  size_gi    = 10 # virtual size, not below the size inside the file

  timeouts {
    create = "90m" # default 60m
  }
}

# Or copy the root disk of a stopped VM of the project ("golden image").
resource "h3_vm_image" "golden" {
  project_id   = var.project_id
  name         = "app-golden-2026-11"
  source_vm_id = var.stopped_vm_id
}

resource "h3_vm" "app" {
  project_id  = var.project_id
  name        = "app-1"
  cpu         = 2
  memory      = "4Gi"
  image       = h3_vm_image.alma.image # "custom:<id>"
  disk_size   = "20Gi"                 # >= size_gi of the image
  ssh_key_ids = [var.ssh_key_id]
}
