# Office network behind a VPN gateway VM of the VPC
resource "h3_vpc_route" "office" {
  vpc_id      = h3_vpc.main.id
  destination = "10.50.0.0/16"
  target_type = "vm"
  vm_id       = h3_vm.vpn_gw.id
  description = "IPsec to the office"
}

# Part of the office network that must stay unreachable from the cloud
resource "h3_vpc_route" "office_mgmt" {
  vpc_id      = h3_vpc.main.id
  destination = "10.50.250.0/24"
  target_type = "blackhole"
}

# Next hop by address (e.g. a VRRP address of two firewall VMs)
resource "h3_vpc_route" "partner" {
  vpc_id      = h3_vpc.main.id
  destination = "172.20.0.0/16"
  target_type = "ip"
  next_hop_ip = "10.0.0.250"
}
