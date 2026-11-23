# The console's EIP selector in Terraform: take a free address of the project
# pool instead of leasing a new one.
data "h3_eips" "free" {
  bound = false
}

resource "h3_database" "app" {
  name     = "app"
  engine   = "postgres"
  cpu      = 1
  memory   = "2Gi"
  size     = "10Gi"
  white_ip = true
  eip_id   = data.h3_eips.free.ids[0]
}
