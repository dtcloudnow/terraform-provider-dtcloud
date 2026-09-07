---
page_title: "dtcloud: dtcloud_router"
subcategory: "Networking"
---

# dtcloud_router

Provides a virtual router: the thing that gives private networks a way out to the internet, and
a way to reach each other.

A router always has an external gateway. The platform has no endpoint that creates one without,
so `external_network_id` is required rather than optional, and every router consumes one public
address from your project's quota.

Two things that belong to a router are **not** arguments here, because both have a lifecycle of
their own:

* the private networks attached to it — [`dtcloud_router_interface`](router_interface.md);
* its static routes — [`dtcloud_router_static_route`](router_static_route.md).

## Example Usage

```hcl
# The external network has to be given by id — see "Finding the external
# network" below. There is no flag that identifies it.
variable "external_network_id" {
  type = string
}

resource "dtcloud_router" "main" {
  name                = "edge"
  external_network_id = var.external_network_id
  enable_snat         = true
}

resource "dtcloud_network" "app" {
  name         = "app"
  ipam_enabled = true
  cidr         = "10.0.10.0/24"
}

# Give the app network a way out.
resource "dtcloud_router_interface" "app" {
  router_id  = dtcloud_router.main.id
  network_id = dtcloud_network.app.id
}
```

## Argument Reference

* `name` - (Required) Name of the router. Must not contain `<`, `>`, `&`, `"` or `'`. Can be
  changed in place.
* `external_network_id` - (Required) ID of the external network the gateway sits on. Can be
  changed in place. Every router has a gateway; there is no way to ask for one without.
* `enable_snat` - (Optional, default `true`) Whether the gateway masquerades traffic coming from
  the private networks behind it. With it off, those networks need routable addresses of their
  own to be reachable. Can be changed in place.

## Attributes Reference

* `id` - ID of the router.
* `status` - Status reported by the platform. `ACTIVE` is the resting state.
* `admin_state_up` - Whether the router is administratively enabled.
* `description` - Description held by the platform. There is no way to set one through this API,
  so it is reported but not managed.
* `external_fixed_ip` - Addresses the external network allocated to the gateway. A list of:
  * `subnet_id` - Subnet the address came from.
  * `ip_address` - The address itself.
* `project_id` - ID of the project the router belongs to.
* `created_at` - When the router was created.
* `updated_at` - When the router last changed.

## Behaviour worth knowing

### The gateway's address is assigned, not requested

`external_fixed_ip` is read-only. The create endpoint asks the external network for an IPv4
address and takes whatever it is given; there is no argument for choosing one. If you need a
fixed, known public address for a service, that is a floating IP rather than a router gateway.

### Finding the external network

There is no way to ask the platform which of its networks is the external one. The network list
carries no such flag, and a region can hold several networks of type `Physical` of which only
one is routable — so filtering [`dtcloud_networks`](../data-sources/networks.md) by type and
taking the first is a guess that will eventually be wrong.

Two reliable ways to get the id:

* ask whoever operates the region;
* read it off a router that already exists. [`dtcloud_routers`](../data-sources/routers.md)
  reports the network's **name** in `external_network`; look that name up in
  `dtcloud_networks` to get its id.

### A router costs a public address

Creating one consumes a public IP from the project quota, plus a router from the router quota
and address space on the external network. A create that fails on quota says so; check what is
left with [`dtcloud_project_quotas`](../data-sources/project_quotas.md).

### The name and the gateway are two requests

The platform takes a rename and a gateway change through different request bodies, so changing
both in one apply sends two requests rather than one. Nothing about that is visible in the plan
— it is worth knowing only if you are reading API logs.

### Restarting a router is not something Terraform can do

The platform has a restart action, and this provider deliberately does not expose it. It answers
before doing any work, does the work in the background twenty seconds later, and reports neither
success nor failure to whoever asked — there is no handle a resource could hold. It is also an
*event* rather than a state, which is the same reason `dtcloud_vm` has no `reboot` argument.

### The gateway can be removed outside Terraform

If someone detaches the gateway in the web console, `external_network_id` reads back empty and
the next plan proposes putting it back. That is intentional: a router that has lost its gateway
is exactly the kind of difference a plan should show.

## Timeouts

* `create` - Defaults to **10 minutes**.
* `update` - Defaults to **10 minutes**.
* `delete` - Defaults to **10 minutes**.

Create does not return until the platform reports `ACTIVE`, and destroy does not return until
the router has stopped resolving — so a dependent resource is never handed a router that cannot
yet carry traffic, and a destroy does not leave the public address it held still charged.

Override them with a `timeouts` block:

```hcl
timeouts {
  create = "20m"
}
```

## Import

Routers can be imported by ID:

```
terraform import dtcloud_router.main 3f2a1c9e-77b4-4a01-9d3e-2b6c81f4e5a7
```

**Everything round-trips.** `name`, `external_network_id` and `enable_snat` are all reported by
the platform, so an import followed by a plan is empty.

Attached interfaces and static routes are **not** imported with the router. Import each one as
its own resource — see [`dtcloud_router_interface`](router_interface.md) and
[`dtcloud_router_static_route`](router_static_route.md) — or read them without managing them
through [`dtcloud_router_interfaces`](../data-sources/router_interfaces.md) and
[`dtcloud_router_static_routes`](../data-sources/router_static_routes.md).
