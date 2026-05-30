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

# Create SSH key from inline public key
resource "h3_ssh_key" "example" {
  project_id = "your-project-id-here"
  name       = "terraform-example-key"
  public_key = "ssh-ed25519 *** ***"
}


# Outputs
output "ssh_key_id" {
  value       = h3_ssh_key.example.id
  description = "SSH key ID"
}

output "ssh_key_name" {
  value       = h3_ssh_key.example.name
  description = "SSH key name"
}

output "ssh_key_created_at" {
  value       = h3_ssh_key.example.created_at
  description = "SSH key creation timestamp"
}

