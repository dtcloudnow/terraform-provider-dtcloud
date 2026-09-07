---
page_title: "dtcloud: dtcloud_router_static_routes"
subcategory: "Networking"
---

# dtcloud_router_static_routes

Lists the static routes on a router, including any added outside Terraform.

## Example Usage

```hcl
data "dtcloud_router_static_routes" "edge" {
  router_id = dtcloud_router.main.id
}

output "routes" {
  value = {
    for r in data.dtcloud_router_static_routes.edge.routes :
    r.destination => r.next_hop
  }
}
```

## Argument Reference

* `router_id` - (Required) ID of the router whose static routes are listed.

## Attributes Reference

* `routes` - The router's static routes, in the order the platform holds them, each with:
  * `destination` - Destination network in CIDR notation.
  * `next_hop` - Address traffic for that destination is sent to.

The endpoint behind this reports the two fields under different names from the ones used
everywhere else in the provider. They are given back here under the names
[`dtcloud_router_static_route`](../resources/router_static_route.md) uses, so the same route is
called the same thing throughout.
