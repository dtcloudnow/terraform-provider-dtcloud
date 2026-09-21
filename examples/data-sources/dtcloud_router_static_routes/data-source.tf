# Every route on the router, including any added outside Terraform.
data "dtcloud_router_static_routes" "edge" {
  router_id = dtcloud_router.main.id
}

output "routes" {
  value = {
    for r in data.dtcloud_router_static_routes.edge.routes :
    r.destination => r.next_hop
  }
}
