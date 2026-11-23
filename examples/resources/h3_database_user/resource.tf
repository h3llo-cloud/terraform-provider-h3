resource "h3_database_user" "checkout" {
  database_id = h3_database.pg.id
  name        = "s_checkout"

  # -1 = unlimited (default), 1..5000. Changed in place, no restart.
  connection_limit = 50

  grants = [
    { database = "app", preset = "readwrite" },
  ]
}

# PostgreSQL monitoring user for an exporter: CONNECT only + pg_monitor,
# with a statement timeout for its own sessions.
resource "h3_database_user" "exporter" {
  database_id = h3_database.pg.id
  name        = "s_pg_exporter"

  grants = [
    { database = "app", preset = "monitor" },
  ]

  # Built-in roles; not set = not managed. GRANT/REVOKE online.
  system_roles = ["pg_monitor"]

  # Defaults of new sessions (ALTER ROLE ... SET). Write values as the API
  # returns them: 30s, 5min, 64MB; 0 = no limit.
  settings = {
    statement_timeout             = "30s"
    lock_timeout                  = "5s"
    default_transaction_isolation = "read committed"
  }
}
