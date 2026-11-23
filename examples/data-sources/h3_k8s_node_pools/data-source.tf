data "h3_k8s_node_pools" "main" {
  cluster_id = h3_k8s_cluster.main.id
}

output "node_pools" {
  value = { for p in data.h3_k8s_node_pools.main.node_pools : p.name => "${p.current_replicas}/${p.status}" }
}
