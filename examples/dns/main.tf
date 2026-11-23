# DNS zone, record sets and PTR in the h3dns service (spec: platform/dns-service-spec-2026-10-07.md §7.3)
terraform {
  required_providers {
    h3 = { source = "h3llo-cloud/h3" }
  }
}

variable "project_id" { type = string }

resource "h3_dns_zone" "main" {
  project_id  = var.project_id
  name        = "example.com"
  dnssec      = true
  default_ttl = 300
}

# Delegate example.com at the registrar to these name servers; with DNSSEC,
# publish ds_records there once the zone is active.
output "nameservers" { value = h3_dns_zone.main.nameservers }
output "ds_records" { value = h3_dns_zone.main.ds_records }

resource "h3_eip" "mail" {
  project_id = var.project_id
  name       = "mail"
}

resource "h3_dns_recordset" "mail" {
  zone_id = h3_dns_zone.main.id
  name    = "mail"
  type    = "A"
  records = [h3_eip.mail.ip_address]
}

resource "h3_dns_recordset" "mx" {
  zone_id = h3_dns_zone.main.id
  name    = "@"
  type    = "MX"
  ttl     = 3600
  records = ["10 mail.example.com"]
}

resource "h3_dns_recordset" "spf" {
  zone_id = h3_dns_zone.main.id
  name    = "@"
  type    = "TXT"
  records = ["v=spf1 mx -all"]
}

resource "h3_dns_recordset" "caa" {
  zone_id = h3_dns_zone.main.id
  name    = "@"
  type    = "CAA"
  records = ["0 issue \"letsencrypt.org\""]
}

# PTR of the EIP: mail.example.com must resolve to the EIP first
resource "h3_dns_ptr" "mail" {
  project_id = var.project_id
  eip_id     = h3_eip.mail.id
  hostname   = "mail.example.com"
  depends_on = [h3_dns_recordset.mail]
}

data "h3_dns_nameservers" "h3" {}

output "delegate_to" {
  value = data.h3_dns_nameservers.h3.nameservers
}
