# Every VM of a spread group runs on its own host.
resource "h3_vm_placement_group" "db" {
  project_id  = var.project_id
  name        = "postgres-replicas"
  description = "one replica per host"
}

resource "h3_vm" "db" {
  count              = 3
  project_id         = var.project_id
  name               = "pg-${count.index}"
  cpu                = 4
  memory             = "8Gi"
  image              = "ubuntu:24.04"
  ssh_key_ids        = [var.ssh_key_id]
  placement_group_id = h3_vm_placement_group.db.id # set at creation only
}
