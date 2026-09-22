# Allow SSH from one address range.
resource "dtcloud_security_group_rule" "ssh_from_office" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_ip_prefix  = "10.10.0.0/16"
}

# Allow traffic from every machine in another group. Referring to a group rather
# than to an address keeps the rule correct as machines come and go.
resource "dtcloud_security_group_rule" "app_from_web" {
  security_group_id = dtcloud_security_group.app.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 8080
  port_range_max    = 8080
  remote_group_id   = dtcloud_security_group.web.id
}
