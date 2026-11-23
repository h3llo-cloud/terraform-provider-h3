terraform {
  required_providers {
    h3 = {
      source  = "h3llo-cloud/h3"
      version = "~> 0.3"
    }
  }
}

# export H3_KEY_ID=... H3_SECRET_KEY=...   (api_endpoint: https://api.h3llo.cloud by default)
provider "h3" {}

variable "project_id" {
  type = string
}

resource "h3_database" "pg" {
  project_id = var.project_id
  name       = "app-db"
  engine     = "postgres" # or "mariadb"

  # Changed in place, up or down (1-16 Gi per vCPU). The database restarts:
  # new connections fail for about 1-4 minutes (PostgreSQL) or ~1 minute (MariaDB).
  cpu    = 2
  memory = "4Gi"

  # Increase in place (online, no restart). Decreasing is refused at plan time.
  size = "20Gi"

  white_ip = false
}

# Public database on an existing project Elastic IP. The EIP outlives the
# database: destroying the database returns it to the project (still billed).
resource "h3_eip" "db" {
  project_id = var.project_id
  name       = "db-public-ip"
  # vm_id stays unset: the database selects the EIP via eip_id.
}

resource "h3_database" "public" {
  project_id = var.project_id
  name       = "app-db-public"
  engine     = "postgres"
  cpu        = 2
  memory     = "4Gi"
  size       = "20Gi"

  white_ip = true # required with eip_id; without eip_id a new billed EIP is leased
  eip_id   = h3_eip.db.id

  # Default false: destroy keeps the EIP in the project (billed until released).
  release_eip_on_destroy = false
}

output "db_public_endpoint" {
  value = h3_database.public.endpoint
}

output "db_internal_ip" {
  value = h3_database.pg.internal_ip
}

output "db_password" {
  value     = h3_database.pg.password
  sensitive = true
}

# Read replica of h3_database.pg (PostgreSQL). The source needs backups and one
# completed base backup: create the replica in an apply after the source exists.
resource "h3_database" "pg_ro" {
  project_id = var.project_id
  name       = "app-db-ro"
  engine     = "postgres"
  cpu        = 2
  memory     = "4Gi"
  size       = "20Gi" # not below the size of the source

  replica_of = h3_database.pg.id # changing it re-creates the replica

  replica_settings {
    max_standby_streaming_delay_seconds = 900 # 0-3600
    hot_standby_feedback                = false
  }

  # One-shot and irreversible: the replica becomes a standalone database.
  # replica_of may stay in the configuration afterwards (no replacement).
  # promote = true
}

output "db_replica_lag_seconds" {
  value = h3_database.pg_ro.replication_lag_seconds
}

# Connection pooler (PgBouncer) of h3_database.public: transaction mode on the
# main public port 5432 of the database (PostgreSQL itself stays in the VPC).
resource "h3_database_pooler" "main" {
  database_id = h3_database.public.id
  pooler_id   = "main"
  role        = "rw"
  mode        = "transaction"
  instances   = 2
  public_port = 5432
  parameters  = { default_pool_size = "50" }
}

output "db_pooler_vpc_endpoint" {
  value = h3_database_pooler.main.vpc_endpoint
}
