terraform {
  required_providers {
    dtcloud = {
      source = "dtcloudnow/dtcloud"
    }
  }
}

# Credentials, region and endpoint come from the environment:
#   export DTCLOUD_ACCESS_KEY=...
#   export DTCLOUD_SECRET_KEY=...
#   export DTCLOUD_REGION_ID=1
#   export DTCLOUD_API_URL=https://console.dt.net.tr/api/v1
#
# Or run `terraform-provider-dtcloud configure` once and drop them all.
provider "dtcloud" {}

variable "network_id" {
  type        = string
  description = "Network the back-end machines sit on. The VIP is placed here too."
}

# Size by name. Each name is offered twice; `ha` is how high availability is
# chosen, so filter on both or the match is ambiguous.
data "dtcloud_lb_flavors" "small" {
  name = "small"
  ha   = false
}

# The machines already on the network that can serve as members.
data "dtcloud_lb_vms" "candidates" {
  network_id = var.network_id
}

resource "dtcloud_lb" "web" {
  name        = "web-lb"
  description = "HTTPS front end for the web tier"
  flavor_id   = data.dtcloud_lb_flavors.small.flavors[0].id

  vip_network_id = var.network_id
}

resource "dtcloud_lb_listener" "https" {
  lb_id         = dtcloud_lb.web.id
  name          = "https"
  protocol      = "HTTPS"
  protocol_port = 443
}

resource "dtcloud_lb_pool" "web" {
  lb_id       = dtcloud_lb.web.id
  listener_id = dtcloud_lb_listener.https.listener_id

  backend_protocol      = "HTTP"
  backend_protocol_port = 8080
  lb_algorithm          = "ROUND_ROBIN"
}

# A pool carries at most one monitor.
resource "dtcloud_lb_health_monitor" "web" {
  lb_id   = dtcloud_lb.web.id
  pool_id = dtcloud_lb_pool.web.pool_id

  type     = "HTTP"
  url_path = "/healthz"

  interval            = 10
  timeout             = 5
  healthy_threshold   = 2
  unhealthy_threshold = 3
}

# One resource per member: each is created and destroyed on its own, so adding a
# machine does not disturb the others.
resource "dtcloud_lb_member" "web" {
  for_each = { for vm in data.dtcloud_lb_vms.candidates.vms : vm.name => vm }

  lb_id   = dtcloud_lb.web.id
  pool_id = dtcloud_lb_pool.web.pool_id

  name              = each.key
  compute_server_id = each.value.id
  address           = each.value.ip_address
  protocol_port     = 8080
  weight            = 1
}

output "vip" {
  value = dtcloud_lb.web.vip_address
}

output "status" {
  value = dtcloud_lb.web.status
}

output "members" {
  value = [for m in dtcloud_lb_member.web : "${m.vm_name}  ${m.address}  ${m.state}"]
}
