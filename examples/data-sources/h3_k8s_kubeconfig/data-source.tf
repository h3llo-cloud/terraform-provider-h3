data "h3_k8s_kubeconfig" "main" {
  project_id = var.project_id
  cluster_id = var.cluster_id
}

provider "kubernetes" {
  host                   = data.h3_k8s_kubeconfig.main.endpoint
  cluster_ca_certificate = data.h3_k8s_kubeconfig.main.cluster_ca_certificate
  config_path            = local_sensitive_file.kubeconfig.filename
}

resource "local_sensitive_file" "kubeconfig" {
  content  = data.h3_k8s_kubeconfig.main.kubeconfig
  filename = "${path.module}/kubeconfig"
}
