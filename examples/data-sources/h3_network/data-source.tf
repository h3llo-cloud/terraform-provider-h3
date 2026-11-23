# A subnet created in the console, looked up by name.
data "h3_network" "app" {
  name = "app"
}

resource "h3_vm" "worker" {
  name        = "worker"
  cpu         = 2
  memory      = "4Gi"
  image       = "ubuntu:24.04"
  ssh_key_ids = [var.ssh_key_id]
  subnet_name = data.h3_network.app.subnet_name
}
