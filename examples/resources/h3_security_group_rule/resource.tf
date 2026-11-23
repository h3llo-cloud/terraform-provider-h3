resource "h3_security_group_rule" "pg_from_web" {
  security_group_id        = h3_security_group.db.id
  direction                = "ingress"
  protocol                 = "tcp"
  port_from                = 5432
  remote_security_group_id = h3_security_group.web.id
}
