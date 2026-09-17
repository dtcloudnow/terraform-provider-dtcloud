resource "dtcloud_router_static_route" "branch" {
  router_id   = dtcloud_router.main.id
  destination = "192.168.50.0/24"
  next_hop    = "10.0.10.20"

  # The interface the next hop sits behind cannot be detached while this route
  # exists, and nothing in the API reports that dependency. Say it here.
  depends_on = [dtcloud_router_interface.app]
}

# Several routes, one resource per entry.
variable "branch_networks" {
  type = map(string)
  default = {
    "192.168.51.0/24" = "10.0.10.21"
    "192.168.52.0/24" = "10.0.10.22"
  }
}

resource "dtcloud_router_static_route" "branches" {
  for_each = var.branch_networks

  router_id   = dtcloud_router.main.id
  destination = each.key
  next_hop    = each.value

  depends_on = [dtcloud_router_interface.app]
}
