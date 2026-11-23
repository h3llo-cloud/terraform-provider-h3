# Every current and future key of a project member (keys the member adds or
# deletes in the console follow automatically, within about two minutes).
resource "h3_vm_ssh_access" "alice" {
  vm_id   = h3_vm.web.id
  user_id = var.alice_user_id
  mode    = "all"
}

# Only the listed keys of the member, e.g. a hardware FIDO key.
resource "h3_vm_ssh_access" "bob" {
  vm_id   = h3_vm.web.id
  user_id = var.bob_user_id
  mode    = "selected"
  key_ids = [var.bob_yubikey_id]
}
