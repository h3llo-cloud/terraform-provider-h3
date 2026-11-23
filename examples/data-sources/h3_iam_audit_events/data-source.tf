# Последние 50 событий IAM за неделю
data "h3_iam_audit_events" "week" {
  from  = timeadd(plantimestamp(), "-168h")
  limit = 50
}

output "iam_changes" {
  value = [for e in data.h3_iam_audit_events.week.events : "${e.occurred_at} ${e.action} ${e.resource_id} by ${e.actor_id}"]
}
