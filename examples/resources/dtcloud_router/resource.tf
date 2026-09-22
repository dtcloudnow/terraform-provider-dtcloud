# The external network has to be given by id, and nothing on the platform flags
# which network that is -- ask whoever operates the region, or read the name off
# an existing router with dtcloud_routers and look it up in dtcloud_networks.
variable "external_network_id" {
  type = string
}

resource "dtcloud_router" "main" {
  name                = "edge"
  external_network_id = var.external_network_id
  enable_snat         = true
}

resource "dtcloud_network" "app" {
  name         = "app"
  ipam_enabled = true
  cidr         = "10.0.10.0/24"
}

# Give the app network a way out.
resource "dtcloud_router_interface" "app" {
  router_id  = dtcloud_router.main.id
  network_id = dtcloud_network.app.id
}
