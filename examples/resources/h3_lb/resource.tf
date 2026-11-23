# TCP load balancer in front of two VMs. Listener and target changes replace it;
# for zero downtime use create_before_destroy together with a name that changes
# with the configuration (names are unique per project).
resource "h3_lb" "web" {
  project_id = var.project_id
  name       = "web"

  listener = [
    { port = 80, target_port = 8080 },
    { port = 443, target_port = 8443 },
  ]
  targets = [for vm in h3_vm.web : vm.internal_ip]
}

# LB Basic (OVN-native project): publish on a project Elastic IP that outlives
# the load balancer. A re-created load balancer keeps the address; changing
# eip_id moves it in place; destroying it keeps the EIP in the project.
resource "h3_eip" "web" {
  project_id = var.project_id
  name       = "web-lb"
}

resource "h3_lb" "pinned" {
  project_id = var.project_id
  name       = "pinned"
  eip_id     = h3_eip.web.id

  listener = [{ port = 443, target_port = 8443 }]
  targets  = [for vm in h3_vm.web : vm.internal_ip]
}
