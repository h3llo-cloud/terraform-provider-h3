data "h3_k8s_cluster" "main" {
  project_id = var.project_id
  id         = var.cluster_id
}

output "k8s_version" {
  value = data.h3_k8s_cluster.main.kubernetes_version
}
