data "dtcloud_my_ip" "here" {}

# Scope an administrative rule to whoever is running Terraform. Read the note on
# the data source page before using this for anything a service depends on.
resource "dtcloud_security_group_rule" "ssh_from_me" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_ip_prefix  = data.dtcloud_my_ip.here.cidr
}
