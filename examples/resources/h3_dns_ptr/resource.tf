resource "h3_dns_ptr" "mail" {
  project_id = var.project_id
  eip_id     = h3_eip.mail.id
  hostname   = "mail.example.com"
  depends_on = [h3_dns_recordset.mail]
}
