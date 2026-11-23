terraform {
  required_providers {
    h3 = {
      source  = "h3llo-cloud/h3"
      version = "~> 0.3"
    }
  }
}

# export H3_KEY_ID=... H3_SECRET_KEY=...   (api_endpoint: https://api.h3llo.cloud by default)
# Note: managed Kubernetes needs API-key access to h3k8s, which is not published yet (0.3.0).
provider "h3" {}

variable "project_id" {
  type = string
}

variable "kubernetes_version" {
  type    = string
  default = "v1.35.9"
}

data "h3_k8s_versions" "available" {}

resource "h3_k8s_cluster" "main" {
  project_id = var.project_id
  name       = "demo"

  # Bump to the next minor (or a newer patch) to upgrade in place; never recreates the cluster.
  kubernetes_version = var.kubernetes_version

  workers = {
    replicas  = 2
    cpu       = 2
    memory_gi = 4
    disk_gi   = 50
  }

  lifecycle {
    prevent_destroy = true
  }
}

# An additional pool for batch jobs: autoscaled from zero, tainted so that only
# pods with a matching toleration land there. The workers above are the pool "default".
resource "h3_k8s_node_pool" "batch" {
  project_id = var.project_id
  cluster_id = h3_k8s_cluster.main.id
  name       = "batch"
  cpu        = 4
  memory_gi  = 16

  autoscaling = {
    enabled  = true
    min_size = 0
    max_size = 5
  }

  labels = { workload = "batch" }

  taint {
    key    = "workload"
    value  = "batch"
    effect = "NoSchedule"
  }
}

output "available_versions" {
  value = [for v in data.h3_k8s_versions.available.versions : v.version]
}

output "endpoint" {
  value = h3_k8s_cluster.main.endpoint
}

output "kubeconfig" {
  value     = h3_k8s_cluster.main.kubeconfig
  sensitive = true
}
