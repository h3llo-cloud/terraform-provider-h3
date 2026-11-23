# The monitoring module adds its own group to a VM it does not own;
# the VM keeps every other group.
resource "h3_vm_security_group_attachment" "node_exporter" {
  vm_id             = h3_vm.web.id
  security_group_id = h3_security_group.node_exporter.id
}
