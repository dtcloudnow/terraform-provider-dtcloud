# Pick the size by name rather than pasting an id. Every name is offered twice --
# once without high availability and once with it -- so filter on both.
data "dtcloud_lb_flavors" "small" {
  name = "small"
  ha   = false
}

resource "dtcloud_lb" "public" {
  name        = "web-lb"
  description = "HTTPS front end for the web tier"
  flavor_id   = data.dtcloud_lb_flavors.small.flavors[0].id

  # Exactly one of vip_network_id, vip_subnet_id or vip_port_id decides where the
  # VIP lives. The platform enforces the same rule.
  vip_network_id = dtcloud_network.app.id
}
