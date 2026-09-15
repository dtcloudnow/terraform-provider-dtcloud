terraform {
  required_providers {
    dtcloud = {
      source = "dtcloudnow/dtcloud"
    }
  }
}

# Credentials and region come from the environment:
#   export DTCLOUD_ACCESS_KEY=...
#   export DTCLOUD_SECRET_KEY=...
#   export DTCLOUD_REGION_ID=1
provider "dtcloud" {}

variable "office_cidr" {
  type        = string
  description = "Address range allowed to reach SSH."
  default     = "10.10.0.0/16"
}

# ---------------------------------------------------------------------------
# Two groups: a web tier reachable from outside, and an app tier reachable only
# from the web tier. Referring to a group by id rather than by address is the
# point of remote_group_id — the rule keeps working as machines come and go.
# ---------------------------------------------------------------------------

resource "dtcloud_security_group" "web" {
  name        = "terraform-example-web"
  description = "HTTPS from anywhere, SSH from the office"
}

resource "dtcloud_security_group" "app" {
  name        = "terraform-example-app"
  description = "Reachable from the web tier only"
}

resource "dtcloud_security_group_rule" "https" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_ip_prefix  = "0.0.0.0/0"
}

resource "dtcloud_security_group_rule" "ssh" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_ip_prefix  = var.office_cidr
}

# For icmp the two port fields are the message type and code, not ports.
# Leaving both out allows every ICMP message.
resource "dtcloud_security_group_rule" "ping" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "icmp"
  remote_ip_prefix  = "0.0.0.0/0"
}

resource "dtcloud_security_group_rule" "app_from_web" {
  security_group_id = dtcloud_security_group.app.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 8080
  port_range_max    = 8080
  remote_group_id   = dtcloud_security_group.web.id
}

# ---------------------------------------------------------------------------
# Reading groups back.
# ---------------------------------------------------------------------------

# Look one up by name, which is how you reference a group Terraform did not
# create without pasting a uuid into the configuration.
data "dtcloud_security_group" "web" {
  name = dtcloud_security_group.web.name

  depends_on = [dtcloud_security_group.web]
}

data "dtcloud_security_groups" "all" {
  depends_on = [dtcloud_security_group.web, dtcloud_security_group.app]
}

# The address the API sees this machine as. Useful for an administrative
# allow-rule; read docs/data-sources/my_ip.md before wiring it into one, since
# a rule built from it is replaced whenever the address changes.
data "dtcloud_my_ip" "current" {}

output "web_id" {
  value = dtcloud_security_group.web.id
}

# The platform's own view of the group, rules included. This is a display of
# the rules rather than the rules themselves — protocol is a configurable
# display name and source may be another group's *name*.
output "web_rules_as_the_platform_shows_them" {
  value = data.dtcloud_security_group.web.inbound_rule
}

# A new group starts with two of these: allow-all egress, one per address
# family. Terraform does not own them; import one if you want it gone.
output "web_default_egress" {
  value = data.dtcloud_security_group.web.outbound_rule
}

output "all_group_names" {
  value = [for g in data.dtcloud_security_groups.all.security_groups : g.name]
}

output "my_ip" {
  value = data.dtcloud_my_ip.current.cidr
}
