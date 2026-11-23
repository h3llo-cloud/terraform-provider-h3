data "h3_vm_placement_group" "db" {
  project_id = var.project_id
  name       = "postgres-replicas"
}

output "db_vms" {
  value = data.h3_vm_placement_group.db.vm_ids
}
