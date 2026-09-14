resource "dtcloud_security_group" "web" {
  name        = "web-tier"
  description = "HTTPS from anywhere, SSH from the office"
}

# Rules are separate resources, so adding or removing one leaves the group and
# every other rule untouched.
resource "dtcloud_security_group_rule" "https" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_ip_prefix  = "0.0.0.0/0"
}
