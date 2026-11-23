resource "h3_network" "app" {
  project_id = var.project_id
  name       = "app"
  vpc_id     = h3_vpc.main.id
  cidr_block = "10.10.0.0/24"
}
