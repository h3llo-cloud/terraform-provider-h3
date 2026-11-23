resource "h3_vm_security_groups" "web" {
  vm_id              = h3_vm.web.id
  security_group_ids = [h3_security_group.web.id]
}
