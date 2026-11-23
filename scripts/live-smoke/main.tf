# Live compatibility smoke of the provider against a real contour (emu or pub), run by CI job live-smoke.
# Cheap resources only (VPC, network, S3 bucket); names carry the prefix tfacc-<run> so leftovers are easy to find
# and purge. Credentials: H3_API_ENDPOINT, H3_KEY_ID, H3_SECRET_KEY, H3_PROJECT_ID (low-privilege key of the r00t
# test project, protected CI variables).
terraform {
  required_providers {
    h3 = {
      source = "h3llo-cloud/h3"
    }
  }
}

variable "run" {
  type        = string
  description = "unique suffix of this run (CI pipeline id)"
}

provider "h3" {}

resource "h3_vpc" "smoke" {
  name = "tfacc-${var.run}"
}

resource "h3_network" "smoke" {
  name       = "tfacc-${var.run}"
  vpc_id     = h3_vpc.smoke.id
  cidr_block = "10.250.0.0/24"
}

resource "h3_s3_bucket" "smoke" {
  name = "tfacc-${var.run}"
}

data "h3_vm_images" "all" {}

output "images" {
  value = length(data.h3_vm_images.all.images)
}
