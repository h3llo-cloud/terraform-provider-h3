# Default pooler of the database: transaction mode, two PgBouncer pods, port
# 6432 on the VPC address and on the public EIP of the database.
resource "h3_database_pooler" "main" {
  database_id  = h3_database.pg.id
  pooler_id    = "main" # changing it replaces the pooler
  role         = "rw"   # rw | ro | r; changing it replaces the pooler
  display_name = "main (transaction)"
  mode         = "transaction" # in place: the pods roll, PostgreSQL is not restarted
  instances    = 2
  default      = true

  vpc_mode    = "port" # port | address | none
  vpc_port    = 6432
  public_port = 6432 # needs white_ip = true on the database; 0 removes it

  # Authoritative: a key removed here returns to the h3db default.
  parameters = {
    default_pool_size = "50"
    max_client_conn   = "3000"
  }
}

# Session pooler for reports on the replicas (the database needs a read
# replica or an HA standby), on its own address in the VPC.
resource "h3_database_pooler" "reports" {
  database_id = h3_database.pg.id
  pooler_id   = "reports"
  role        = "ro"
  mode        = "session"
  instances   = 1
  vpc_mode    = "address"
}

output "pooler_endpoints" {
  value = {
    vpc    = h3_database_pooler.main.vpc_endpoint     # e.g. 10.10.0.20:6432
    public = h3_database_pooler.main.public_endpoints # e.g. ["45.93.22.149:6432"]
  }
}
