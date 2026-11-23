resource "h3_k8s_cluster" "main" {
  project_id   = var.project_id
  name         = "prod"
  cluster_type = "PROD"

  # Changing the version upgrades the cluster in place (newer patch or next minor).
  kubernetes_version     = "v1.35.9"
  control_plane_replicas = 3

  workers = {
    replicas  = 3 # in place
    cpu       = 4 # in place, together with memory_gi (rolling node replacement)
    memory_gi = 8
    disk_gi   = 50 # changing it replaces the cluster
  }

  autoscaler = {
    enabled  = true
    max_size = 10
  }

  # NodeLocal DNSCache: a caching DNS on every node (in place; the first
  # enablement on an older cluster restarts the Cilium agents one by one).
  node_local_dns_cache = true

  maintenance_window = {
    enabled      = true
    day_of_week  = "Sunday"
    start_hour   = 2
    auto_upgrade = "patch" # only patch versions; a new minor via kubernetes_version
    exclusions = [
      { start_date = "2026-12-20", end_date = "2027-01-10", reason = "New Year sales" },
    ]
  }

  # Who may reach the Kubernetes API (in place, no restart); [] = everyone.
  api_allowed_cidrs = ["203.0.113.0/24", "198.51.100.7/32"]
}

# Workers in a network of the project VPC: pods reach the project databases and VMs
# by private addresses. Omit network_id to get the platform default. Changing it
# replaces the cluster. A Service of type LoadBalancer with the annotation
# h3.io/lb-internal: "true" gets an address of this network instead of a public IP.
resource "h3_k8s_cluster" "in_vpc" {
  project_id         = var.project_id
  name               = "apps"
  kubernetes_version = "v1.36.5"
  network_id         = h3_network.app.id

  workers = {
    replicas  = 2
    cpu       = 2
    memory_gi = 4
    disk_gi   = 40
  }
}
