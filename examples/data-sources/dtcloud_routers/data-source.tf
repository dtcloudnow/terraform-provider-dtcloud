data "dtcloud_routers" "all" {}

data "dtcloud_routers" "edge" {
  name = "edge"
}

# enable_snat also reads false on a router with no gateway at all, so is_external
# is what tells the two apart.
output "routers_without_a_gateway" {
  value = [for r in data.dtcloud_routers.all.routers : r.name if !r.is_external]
}
