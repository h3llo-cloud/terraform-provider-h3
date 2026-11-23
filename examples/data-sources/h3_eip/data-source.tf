# An address reserved in the console (e.g. already in DNS and firewall allow lists).
data "h3_eip" "mail" {
  ip_address = "203.0.113.25"
}

resource "h3_vm" "mail" {
  name        = "mail"
  cpu         = 2
  memory      = "4Gi"
  image       = "ubuntu:24.04"
  ssh_key_ids = [var.ssh_key_id]
  white_ip    = true
  eip_id      = data.h3_eip.mail.id
}
