---
page_title: "dtcloud: dtcloud_router_interface"
subcategory: "Networking"
---

# dtcloud_router_interface

Attaches a private network to a [`dtcloud_router`](router.md), which is what gives the machines
on that network a route to everywhere else.

## Example Usage

```hcl
resource "dtcloud_router_interface" "app" {
  router_id  = dtcloud_router.main.id
  network_id = dtcloud_network.app.id
}
```

With a chosen address, rather than one the platform picks:

```hcl
resource "dtcloud_router_interface" "app" {
  router_id  = dtcloud_router.main.id
  network_id = dtcloud_network.app.id
  ip_address = "10.0.10.1"
}
```

## Argument Reference

* `router_id` - (Required) ID of the router to attach the network to. Changing it recreates the
  attachment.
* `network_id` - (Required) ID of the network to attach. Changing it recreates the attachment.
* `ip_address` - (Optional) Address the interface takes on the network. When omitted, the
  platform attaches to the network's **first** subnet and allocates an address from it. Changing
  it recreates the attachment.
* `port_security_enabled` - (Optional, default `true`) Whether port security applies to this
  interface. It only reaches the platform when `ip_address` is set — see below. Changing it
  recreates the attachment.

Nothing on this resource can be changed in place: the platform has an attach endpoint and a
detach endpoint and nothing in between.

## Attributes Reference

* `id` - `<router-id>:<port-id>`.
* `port_id` - ID of the port the platform created for this interface. This is the handle that
  detaching uses.
* `subnet_id` - ID of the subnet the interface's address came from.
* `network_name` - Name of the attached network.
* `cidr` - CIDR of the attached network's first subnet.
* `status` - Status of the interface's port.

## Behaviour worth knowing

### Which subnet you land on, when you do not choose an address

Without `ip_address`, the platform attaches the router to the network's **first** subnet — not
to one you name, and not to all of them. Networks created by [`dtcloud_network`](network.md)
have exactly one subnet, so this is only worth thinking about on a network built elsewhere with
several.

### `port_security_enabled` only applies when you choose an address

The two ways of attaching are different operations on the platform, not one operation with a
field left out. Asking for a specific address creates a port and applies port security to it;
letting the platform choose goes down a path that never looks at the setting. So on an interface
with no `ip_address`, `port_security_enabled` has no effect. It is left as an argument rather
than being silently ignored, because it does apply on the other path.

### Security groups are not offered

A router interface is created without any, and there is no endpoint that adds them afterwards.
Security groups belong on the machines behind the router —
[`dtcloud_vm`](vm.md)'s `network` blocks and
[`dtcloud_vm_network_interface`](vm_network_interface.md).

### A static route through an interface pins it

The platform refuses to detach an interface while a static route's next hop sits on the network
behind it:

```
Router interface for subnet … on router … cannot be deleted, as it is required by one or more routes.
```

So a router's **routes have to be destroyed before its interfaces**, and Terraform only knows
that if the configuration says so. Nothing in the provider can work it out — which route needs
which interface is a routing decision, not something the API reports.

Add a `depends_on` to every route that points through an interface you manage:

```hcl
resource "dtcloud_router_static_route" "branch" {
  router_id   = dtcloud_router.main.id
  destination = "192.168.50.0/24"
  next_hop    = "10.0.10.20"

  depends_on = [dtcloud_router_interface.app]
}
```

Without it, a `terraform destroy` may try the interface first and stop with the refusal above.
Re-running the destroy then succeeds, because the route will have gone in the meantime — but the
first run has already failed. The same applies to any change that **replaces** the interface,
such as setting `ip_address` on one that did not have it.

### Destroying the router takes its interfaces with it

Deleting a router detaches everything on it first. Terraform's dependency graph already destroys
these resources before the router they point at, so this only shows up when something was
attached outside Terraform.

## Timeouts

* `create` - Defaults to **10 minutes**.
* `delete` - Defaults to **10 minutes**.

Create does not return until the new port appears on the router — the attach endpoint answers
with the router rather than with the port it made, so the port id, which is the only handle
detaching has, is found by watching the router's interface list. Delete does not return until
the port has gone.

## Import

```
terraform import dtcloud_router_interface.app <router-id>:<port-id>
```

The port id is the `id` of the entry whose `type` is `Internal interface` in
[`dtcloud_router_interfaces`](../data-sources/router_interfaces.md).

`port_security_enabled` is **not** recovered on import: nothing reports it back. An import
assumes the default, `true`. If the interface was attached with port security off, say so in the
configuration before importing — otherwise the setting in state is wrong and nothing will tell
you.
