# All versions, and the upgrade targets of a cluster on v1.34.1.
data "h3_k8s_versions" "from_current" {
  from = "v1.34.1"
}

output "next_upgrade" {
  value = data.h3_k8s_versions.from_current.next_upgrade
}
