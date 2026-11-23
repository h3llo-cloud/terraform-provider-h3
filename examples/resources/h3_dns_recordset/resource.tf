resource "h3_dns_recordset" "www" {
  zone_id = h3_dns_zone.main.id
  name    = "www"
  type    = "A"
  ttl     = 300
  records = [h3_eip.web.ip_address]
}
