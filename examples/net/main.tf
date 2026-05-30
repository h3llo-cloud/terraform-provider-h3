terraform {
  required_providers {
    h3 = {
      source = "h3llo-cloud/h3"
    }
  }
}

provider "h3" {
  api_endpoint = "https://api.h3llo.cloud"
  key_id       = "your-api-key-id-here"
  secret_key   = "your-api-key-secret-here"
}

resource "h3_ovn_vpc" "main" {
  project_id = "your-project-id-here"
  name       = "terraform-test-vpc"
  
  namespaces = [
    "ns-your-project-id-here"
  ]
  
  static_routes = [
    {
      cidr        = "0.0.0.0/0"
      next_hop_ip = "10.0.0.254"
      policy      = "policyDst"
    }
  ]
}

resource "h3_ovn_network" "subnet1" {
  project_id = "your-project-id-here"
  name       = "terraform-test-subnet"
  vpc_id     = h3_ovn_vpc.main.id
  cidr_block = "10.1.0.0/24"
  protocol   = "IPv4"
  
  external_subnets = ["join"]
}

resource "h3_ovn_eip" "static" {
  project_id = "your-project-id-here"
  name       = "terraform-test-eip"
  network_id = h3_ovn_network.subnet1.subnet_id
}

output "vpc_id" {
  description = "VPC ID"
  value       = h3_ovn_vpc.main.id
}

output "vpc_status" {
  description = "VPC status"
  value       = h3_ovn_vpc.main.status
}

output "network_subnet_id" {
  description = "Subnet ID"
  value       = h3_ovn_network.subnet1.subnet_id
}

output "network_subnet_name" {
  description = "Subnet K8s name"
  value       = h3_ovn_network.subnet1.subnet_name
}

output "network_gateway_id" {
  description = "Gateway ID"
  value       = h3_ovn_network.subnet1.gateway_id
}

output "network_gateway_name" {
  description = "Gateway K8s name"
  value       = h3_ovn_network.subnet1.gateway_name
}

output "network_status" {
  description = "Network status"
  value       = h3_ovn_network.subnet1.status
}

output "eip_id" {
  description = "EIP ID"
  value       = h3_ovn_eip.static.id
}

output "eip_address" {
  description = "EIP address"
  value       = h3_ovn_eip.static.ip_address
}

output "eip_gateway_name" {
  description = "EIP gateway name"
  value       = h3_ovn_eip.static.gateway_name
}

output "eip_status" {
  description = "EIP status"
  value       = h3_ovn_eip.static.status
}
