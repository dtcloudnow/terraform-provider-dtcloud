# Lookup is by id. Use this for a router Terraform did not create -- declaring it
# as a resource would hand Terraform ownership, and a later destroy would take
# down everything behind it.
data "dtcloud_router" "edge" {
  id = "3f2a1c9e-77b4-4a01-9d3e-2b6c81f4e5a7"
}

# Attach a network to a router somebody else manages.
resource "dtcloud_router_interface" "app" {
  router_id  = data.dtcloud_router.edge.id
  network_id = dtcloud_network.app.id
}

output "gateway_addresses" {
  value = [for ip in data.dtcloud_router.edge.external_fixed_ip : ip.ip_address]
}
