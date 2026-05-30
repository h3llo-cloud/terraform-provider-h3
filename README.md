# H3 Cloud Terraform Provider

Official [Terraform](https://www.terraform.io/) provider for [h3llo cloud](https://h3llo.cloud) — manage cloud infrastructure as code.

[![Terraform Registry](https://img.shields.io/badge/Terraform-Registry-blueviolet)](https://registry.terraform.io/providers/h3llo-cloud/h3/latest)

## Quick Start

```hcl
terraform {
  required_providers {
    h3 = {
      source  = "h3llo-cloud/h3"
      version = "~> 0.1"
    }
  }
}

provider "h3" {
  api_endpoint = "https://api.h3llo.cloud"
  key_id       = var.h3_key_id
  secret_key   = var.h3_secret_key
}

# Create a virtual machine
resource "h3_vm" "web" {
  project_id = var.project_id
  name       = "web-server"
  cpu        = 2
  memory     = "4Gi"
  disk_size  = "25Gi"
  image      = "ubuntu:24.04"
  ssh_key_id = h3_ssh_key.main.id
  white_ip   = true
}

# Upload an SSH key
resource "h3_ssh_key" "main" {
  user_id    = var.user_id
  name       = "deploy-key"
  public_key = file("~/.ssh/id_ed25519.pub")
}
```

```bash
terraform init
terraform plan
terraform apply
```

## Authentication

The provider uses HMAC authentication. Obtain your API credentials from the [h3llo cloud console](https://console.h3llo.cloud).

Configure credentials via provider block or environment variables:

| Provider Attribute | Environment Variable | Required | Description                          |
|--------------------|----------------------|----------|--------------------------------------|
| `api_endpoint`     | `H3_API_ENDPOINT`    | No       | API endpoint (default: `https://api.h3llo.cloud`) |
| `key_id`           | `H3_KEY_ID`          | Yes      | API Key ID                           |
| `secret_key`       | `H3_SECRET_KEY`      | Yes      | API Secret Key for HMAC signing      |
| `timeout`          | —                    | No       | Request timeout in seconds (default: 30) |
| `max_retries`      | —                    | No       | Max retry attempts (default: 3)      |

Using environment variables:

```bash
export H3_KEY_ID="your-key-id"
export H3_SECRET_KEY="your-secret-key"
```

## Resources

| Resource             | Description                     |
|----------------------|---------------------------------|
| `h3_vm`              | Virtual machine                 |
| `h3_disk`            | Block storage disk              |
| `h3_snapshot`        | Disk snapshot                   |
| `h3_backup`          | VM backup                       |
| `h3_ovn_vpc`         | Virtual Private Cloud           |
| `h3_ovn_network`     | Subnet within a VPC             |
| `h3_ovn_eip`         | Elastic IP address              |
| `h3_s3_bucket`       | S3-compatible object storage    |
| `h3_ssh_key`         | SSH public key                  |

Full documentation for each resource is available on the [Terraform Registry](https://registry.terraform.io/providers/h3llo-cloud/h3/latest/docs).

## Examples

### VM with VPC and public IP

```hcl
resource "h3_ovn_vpc" "main" {
  project_id = var.project_id
  name       = "production"
}

resource "h3_ovn_network" "web" {
  project_id = var.project_id
  vpc_id     = h3_ovn_vpc.main.id
  name       = "web-subnet"
  cidr       = "10.0.1.0/24"
}

resource "h3_vm" "web" {
  project_id  = var.project_id
  name        = "web-server"
  cpu         = 4
  memory      = "8Gi"
  disk_size   = "50Gi"
  image       = "ubuntu:24.04"
  ssh_key_id  = h3_ssh_key.main.id
  subnet_name = h3_ovn_network.web.name
  white_ip    = true
}
```

### S3 bucket

```hcl
resource "h3_s3_bucket" "assets" {
  project_id = var.project_id
  name       = "my-assets"
}

output "s3_endpoint" {
  value = h3_s3_bucket.assets.slug
}

output "s3_access_key" {
  value     = h3_s3_bucket.assets.access_key_id
  sensitive = true
}
```

### VM from snapshot

```hcl
resource "h3_snapshot" "before_upgrade" {
  project_id = var.project_id
  vm_id      = h3_vm.web.id
  name       = "pre-upgrade"
}

resource "h3_vm" "restored" {
  project_id         = var.project_id
  name               = "web-restored"
  cpu                = 4
  memory             = "8Gi"
  source_snapshot_id = h3_snapshot.before_upgrade.id
  ssh_key_id         = h3_ssh_key.main.id
}
```

### Additional disk

```hcl
resource "h3_disk" "data" {
  project_id    = var.project_id
  name          = "data-volume"
  size          = "100Gi"
  storage_class = "replicated"
}
```

## Building from Source

```bash
git clone https://github.com/h3llo-cloud/terraform-provider-h3.git
cd terraform-provider-h3
make build
```

For local development, install and configure dev overrides:

```bash
make install
```

Create `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "h3llo-cloud/h3" = "<YOUR_GOPATH>/bin"
  }
  direct {}
}
```

## License

[MPL-2.0](LICENSE)
