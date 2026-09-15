---
page_title: "dtcloud: dtcloud_router_interfaces"
subcategory: "Networking"
---

# dtcloud_router_interfaces

Lists everything attached to a router: the external gateway and the internal interfaces, in one
list, as the platform returns them — including interfaces Terraform does not manage.

## Example Usage

```hcl
data "dtcloud_router_interfaces" "edge" {
  router_id = dtcloud_router.main.id
}

# The port ids that dtcloud_router_interface can be imported with.
output "internal_port_ids" {
  value = [
    for i in data.dtcloud_router_interfaces.edge.interfaces :
    i.id if i.type == "Internal interface"
  ]
}
```

## Argument Reference

* `router_id` - (Required) ID of the router whose interfaces are listed.

## Attributes Reference

* `interfaces` - The router's interfaces, external gateway first, each with:
  * `id` - **Port id** when `type` is `Internal interface`, **subnet id** when it is
    `External gateway`. See the warning below.
  * `type` - `External gateway` or `Internal interface`.
  * `network_id` - ID of the attached network. Not reported for the external gateway.
  * `network_name` - Name of the attached network.
  * `subnet_id` - ID of the subnet the address came from. Not reported for the external gateway.
  * `ip_address` - Address the interface holds.
  * `cidr` - CIDR of the attached network.
  * `status` - Status of the interface.

~> **`id` means two different things.** The endpoint puts a port id there for an internal
interface and a subnet id for the external gateway, and there is no way to make it mean one.
Check `type` before using it: only an internal interface's id is something
[`dtcloud_router_interface`](../resources/router_interface.md) can be imported with or detached
by.
