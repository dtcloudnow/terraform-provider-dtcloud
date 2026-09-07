---
page_title: "dtcloud: dtcloud_router_static_route"
subcategory: "Networking"
---

# dtcloud_router_static_route

Adds one static route to a [`dtcloud_router`](router.md): traffic for `destination` is sent to
`next_hop` instead of out of the gateway.

## Example Usage

```hcl
resource "dtcloud_router_static_route" "branch" {
  router_id   = dtcloud_router.main.id
  destination = "192.168.50.0/24"
  next_hop    = "10.0.10.20"
}
```

Several routes, one per entry:

```hcl
variable "branch_networks" {
  type    = map(string)
  default = {
    "192.168.50.0/24" = "10.0.10.20"
    "192.168.51.0/24" = "10.0.10.21"
  }
}

resource "dtcloud_router_static_route" "branches" {
  for_each = var.branch_networks

  router_id   = dtcloud_router.main.id
  destination = each.key
  next_hop    = each.value
}
```

## Argument Reference

* `router_id` - (Required) ID of the router. Changing it recreates the route.
* `destination` - (Required) Destination network in CIDR notation, e.g. `192.168.50.0/24`. The
  prefix length is **mandatory** — a bare address is rejected. Changing it recreates the route.
* `next_hop` - (Required) Address to send that traffic to. IPv4, and **without** a prefix length
  — the opposite rule from `destination`. It has to be an address the router can already reach,
  which in practice means one on a network attached with
  [`dtcloud_router_interface`](router_interface.md). Changing it recreates the route.

Both ends are checked during `terraform plan`, so a malformed one is an error before anything is
sent.

There is nothing to change in place: a route *is* its destination and next hop, so changing
either is a different route.

## Attributes Reference

* `id` - `<router-id>:<destination>:<next-hop>`.

## Behaviour worth knowing

### Routes on one router are applied one at a time

The platform applies a route by rewriting the router's **entire** route list. Two routes added
at the same moment therefore both read the list as it was before either of them, and the second
write drops the first — and Terraform applies up to ten resources in parallel by default.

The provider serialises the routes of a single router so this cannot happen, which means a
router with many routes takes proportionally longer to apply. Routes on *different* routers
still go in parallel.

What the provider cannot protect you from is something else writing to the same router at the
same time — another Terraform run, or someone in the web console. There is no way to hold a lock
on the platform itself.

### A route removed elsewhere comes back

If a route disappears — deleted in the console, or lost to the overwrite above — the next
refresh drops the resource from state and the next apply adds it again. That is the intended
behaviour, and it is also the provider's answer to the overwrite: converge again.

### A route pins the interface it points through

If `next_hop` sits on a network attached with
[`dtcloud_router_interface`](router_interface.md), that interface cannot be detached while this
route exists. Terraform has no way to work that out on its own, so say it:

```hcl
resource "dtcloud_router_static_route" "branch" {
  router_id   = dtcloud_router.main.id
  destination = "192.168.50.0/24"
  next_hop    = "10.0.10.20"

  depends_on = [dtcloud_router_interface.app]
}
```

Without the `depends_on`, a destroy — or any change that replaces the interface — can be
attempted in the wrong order and stop with
`… cannot be deleted, as it is required by one or more routes`.

### Routes are not part of the router resource

A route is its own resource rather than a list on [`dtcloud_router`](router.md) so that adding
one does not rewrite the router, and so that a route added outside Terraform is not silently
deleted by the next apply. If you want to see all of them, including the ones Terraform does not
manage, read [`dtcloud_router_static_routes`](../data-sources/router_static_routes.md).

## Timeouts

* `create` - Defaults to **10 minutes**.
* `delete` - Defaults to **10 minutes**.

Create does not return until the route is actually listed on the router, and delete not until it
has gone. Reading the value back is the only honest confirmation available here: the platform
acknowledges these writes before applying them.

## Import

```
terraform import dtcloud_router_static_route.branch <router-id>:192.168.50.0/24:10.0.10.20
```

Everything round-trips: the id carries the whole resource.
