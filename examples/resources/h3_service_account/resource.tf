resource "h3_service_account" "ci" {
  name        = "gitlab-ci"
  description = "Выкатка из GitLab CI"
  role        = "PROJ-MEMB"
}
