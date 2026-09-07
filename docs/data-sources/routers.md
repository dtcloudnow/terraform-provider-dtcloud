---
page_title: "dtcloud: dtcloud_routers"
subcategory: "Networking"
---

# dtcloud_routers

Lists every router visible to the caller.

## Example Usage

```hcl
data "dtcloud_routers" "all" {}

data "dtcloud_routers" "edge" {
  name = "edge"
}

output "routers_without_a_gateway" {
  value = [for r in data.dtcloud_routers.all.routers : r.name if !r.is_external]
}
```

## Argument Reference

* `name` - (Optional) Only return routers with this exact name.
* `status` - (Optional) Only return routers in this status, e.g. `ACTIVE`.

Both filters are applied by the provider after the full list has been fetched, because the
endpoint takes no query parameters.

## Attributes Reference

* `routers` - The routers that matched, in the order the platform returned them, each with:
  * `id`, `name`, `status`
  * `enable_snat` - Whether the gateway masquerades traffic. Reported as `false` for a router
    with no gateway at all, so it does not on its own distinguish "SNAT off" from "no gateway" —
    `is_external` does.
  * `external_network` - **Name** of the external network the gateway is on, or `-` when there is
    no gateway. This endpoint does not report its id.
  * `cidr` - CIDR of that external network.
  * `is_external` - Whether the router has an external gateway.

~> **The external network is a name here and an id on [`dtcloud_router`](router.md).** The two
data sources read different endpoints, and this one is not given the id. If you need the id — to
build a [`dtcloud_router`](../resources/router.md) matching an existing one, for instance — look
the router up by id with `dtcloud_router`.
