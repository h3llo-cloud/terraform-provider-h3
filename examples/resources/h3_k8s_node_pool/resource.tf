# The cluster's `workers` are the pool "default" (system workloads).
resource "h3_k8s_cluster" "main" {
  project_id         = var.project_id
  name               = "prod"
  cluster_type       = "PROD"
  kubernetes_version = "v1.35.9"

  workers = {
    replicas  = 3
    cpu       = 4
    memory_gi = 8
    disk_gi   = 50
  }

  # Cluster-wide autoscaler settings; here only the extra pools autoscale.
  autoscaler = {
    enabled                     = false
    expander                    = "least-waste"
    balance_similar_node_groups = true
  }
}

# A fixed-size pool for one workload.
resource "h3_k8s_node_pool" "api" {
  project_id = var.project_id
  cluster_id = h3_k8s_cluster.main.id
  name       = "api"

  cpu       = 8  # in place, rolling replacement of the pool nodes
  memory_gi = 16 # in place, rolling
  disk_gi   = 64 # in place, rolling
  replicas  = 3  # in place

  labels = { workload = "api" } # in place, no rollout

  taint {
    key    = "dedicated"
    value  = "api"
    effect = "NoSchedule"
  }

  kubelet = {
    platform_defaults = true # kube-/system-reserved and eviction thresholds by node size
  }

  # Follow the control plane: upgrading the cluster upgrades the pool.
  kubernetes_version = h3_k8s_cluster.main.kubernetes_version
}

# An autoscaled pool that can scale to zero: leave replicas unset.
resource "h3_k8s_node_pool" "batch" {
  project_id = var.project_id
  cluster_id = h3_k8s_cluster.main.id
  name       = "batch"

  cpu       = 16
  memory_gi = 64

  autoscaling = {
    enabled                          = true
    min_size                         = 0
    max_size                         = 10
    scale_down_unneeded_time         = 10
    scale_down_utilization_threshold = "0.5"
  }

  taint {
    key    = "batch"
    effect = "NoSchedule"
  }
}
