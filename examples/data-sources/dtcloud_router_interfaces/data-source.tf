data "dtcloud_router_interfaces" "edge" {
  router_id = dtcloud_router.main.id
}

# The port ids dtcloud_router_interface can be imported with. Check `type` first:
# the endpoint puts a subnet id in `id` for the external gateway.
output "internal_port_ids" {
  value = [
    for i in data.dtcloud_router_interfaces.edge.interfaces :
    i.id if i.type == "Internal interface"
  ]
}
