data "h3_service_accounts" "all" {}

data "h3_service_accounts" "ci" {
  name = "gitlab-ci"
}

output "service_account_names" {
  value = [for sa in data.h3_service_accounts.all.service_accounts : sa.name]
}
