terraform {
  required_providers {
    h3 = {
      source  = "h3llo-cloud/h3"
      version = "~> 0.3"
    }
  }
}

# export H3_KEY_ID=... H3_SECRET_KEY=... H3_PROJECT_ID=...
# Ключу, которым работает Terraform, нужны scopes iam:write и iam:delete (для terraform destroy);
# scope "*" сервисными аккаунтами не управляет. Это должен быть ключ пользователя: ключ сервисного
# аккаунта читает IAM, но не создаёт аккаунты и ключи.
provider "h3" {}

# Сервисный аккаунт для CI: участник проекта, без прав на IAM.
resource "h3_service_account" "ci" {
  name        = "gitlab-ci"
  description = "Выкатка из GitLab CI"
  role        = "PROJ-MEMB"
}

# Ключ для CI: только ВМ и DNS, на 90 дней (срок по умолчанию).
resource "h3_service_account_key" "ci" {
  service_account_id = h3_service_account.ci.id
  name               = "deploy"
  scopes             = ["vms:write", "dns:write"]
}

# Аккаунт только для чтения, например для мониторинга.
resource "h3_service_account" "monitoring" {
  name = "monitoring"
  role = "PROJ-VIEWER"
}

resource "h3_service_account_key" "monitoring" {
  service_account_id = h3_service_account.monitoring.id
  name               = "grafana"
  scopes             = ["monitoring:read"]
}

# Кто менял сервисные аккаунты за последние сутки
data "h3_iam_audit_events" "day" {
  from = timeadd(plantimestamp(), "-24h")
}

output "ci_key_id" {
  value = h3_service_account_key.ci.id
}

output "ci_secret_key" {
  value     = h3_service_account_key.ci.secret_key
  sensitive = true
}

output "iam_changes_last_day" {
  value = [for e in data.h3_iam_audit_events.day.events : "${e.occurred_at} ${e.action} ${e.resource_id}"]
}
