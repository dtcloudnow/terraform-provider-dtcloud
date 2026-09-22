resource "dtcloud_router_interface" "app" {
  router_id  = dtcloud_router.main.id
  network_id = dtcloud_network.app.id
}

# With a chosen address rather than one the platform picks. This is also the only
# path on which port_security_enabled reaches the platform.
resource "dtcloud_router_interface" "db" {
  router_id             = dtcloud_router.main.id
  network_id            = dtcloud_network.db.id
  ip_address            = "10.0.20.1"
  port_security_enabled = true
}
