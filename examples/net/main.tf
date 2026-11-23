terraform {
  required_providers {
    h3 = {
      source  = "h3llo-cloud/h3"
      version = "~> 0.3"
    }
  }
}

# export H3_KEY_ID=... H3_SECRET_KEY=...   (api_endpoint: https://api.h3llo.cloud by default)
provider "h3" {}

variable "project_id" {
  type = string
}

resource "h3_vpc" "main" {
  project_id = var.project_id
  name       = "terraform-test-vpc"
}

resource "h3_network" "subnet1" {
  project_id = var.project_id
  name       = "terraform-test-subnet"
  vpc_id     = h3_vpc.main.id
  cidr_block = "10.1.0.0/24"
  protocol   = "IPv4"

}

# Route table of the VPC: drop traffic to a network that must stay unreachable
# (a VM or an address of the VPC can be the target as well, see h3_vpc_route).
resource "h3_vpc_route" "blocked" {
  vpc_id      = h3_vpc.main.id
  destination = "192.168.200.0/24"
  target_type = "blackhole"
  description = "terraform test"
}

resource "h3_eip" "static" {
  project_id = var.project_id
  name       = "terraform-test-eip"
  network_id = h3_network.subnet1.subnet_id
}

output "vpc_id" {
  description = "VPC ID"
  value       = h3_vpc.main.id
}

output "vpc_status" {
  description = "VPC status"
  value       = h3_vpc.main.status
}

output "network_subnet_id" {
  description = "Subnet ID"
  value       = h3_network.subnet1.subnet_id
}

output "network_subnet_name" {
  description = "Subnet K8s name"
  value       = h3_network.subnet1.subnet_name
}

output "network_gateway_id" {
  description = "Gateway ID"
  value       = h3_network.subnet1.gateway_id
}

output "network_gateway_name" {
  description = "Gateway K8s name"
  value       = h3_network.subnet1.gateway_name
}

output "network_status" {
  description = "Network status"
  value       = h3_network.subnet1.status
}

output "eip_id" {
  description = "EIP ID"
  value       = h3_eip.static.id
}

output "eip_address" {
  description = "EIP address"
  value       = h3_eip.static.ip_address
}

output "eip_gateway_name" {
  description = "EIP gateway name"
  value       = h3_eip.static.gateway_name
}

output "eip_status" {
  description = "EIP status"
  value       = h3_eip.static.status
}
