resource "h3_vm" "web" {
  project_id = var.project_id
  name       = "web-1"
  cpu        = 2
  memory     = "4Gi"
  image      = "ubuntu:24.04"
  disk_size  = "30Gi"
  # Your h3ssh keys; changed in place without a restart.
  ssh_key_ids = [h3_ssh_key.deploy.id]

  subnet_name = h3_network.app.subnet_name
  white_ip    = true

  # cpu and memory change in place: an increase within the live reserve of the
  # running VM (4x its size at start) is applied without a restart, anything
  # else restarts the VM. false: never restart, such a change fails instead.
  allow_restart_on_resize = true

  # cloud-init user-data: runs once on the first boot; changing it replaces the VM.
  user_data = file("${path.module}/cloud-init.yaml")

  # Refuse deletion through the API until set back to false (changed in place).
  deletion_protection = true

  # ssh_key_ids change in place (live);
  # image, disk_size and subnet_name replace the VM together with its root disk.
  lifecycle {
    prevent_destroy = true
  }
}
