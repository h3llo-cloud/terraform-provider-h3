# Delegate the domain to h3llo DNS before (or without) creating the zone here,
# e.g. with the registrar's Terraform provider.
data "h3_dns_nameservers" "h3" {}

output "ns" {
  value = data.h3_dns_nameservers.h3.nameservers # ["ns1.h3llo.cloud", ...]
}
