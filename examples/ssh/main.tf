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

# Create SSH key from inline public key
resource "h3_ssh_key" "example" {
  user_id    = "your-user-id-here" # must be the owner of the API key
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

