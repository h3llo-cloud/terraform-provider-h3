data "h3_database_poolers" "pg" {
  database_id = h3_database.pg.id
}

output "poolers" {
  value = { for p in data.h3_database_poolers.pg.poolers : p.pooler_id => "${p.role}/${p.mode} ${p.ready_instances}/${p.instances} ${p.phase}" }
}

output "pooler_budget_primary" {
  value = data.h3_database_poolers.pg.budget.primary # { used, max }
}
