resource "h3_service_account_key" "ci" {
  service_account_id = h3_service_account.ci.id
  name               = "deploy"
  scopes             = ["vms:write", "dns:write"]
  expires_at         = "2027-03-01T00:00:00Z"
}

# Секрет нужен один раз: положите его в переменные CI и не выводите в лог.
output "ci_key_id" {
  value = h3_service_account_key.ci.id
}

output "ci_secret_key" {
  value     = h3_service_account_key.ci.secret_key
  sensitive = true
}
