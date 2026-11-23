resource "h3_ssh_key" "deploy" {
  user_id    = var.user_id # must be the owner of the API key
  name       = "deploy"
  public_key = file("~/.ssh/id_ed25519.pub")
}
