# A public IP that outlives the VM: recreate the VM, keep the address.
resource "h3_eip" "web" {
  project_id  = var.project_id
  name        = "web-ip"
  description = "frontend, prod" # shown in the console; changed in place
  vm_id       = h3_vm.web.id     # attach/detach in place
}
