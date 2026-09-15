---
page_title: "dtcloud: dtcloud_router"
subcategory: "Networking"
---

# dtcloud_router

Looks up one router by ID.

Use it to reference a router Terraform did not create — attaching a network to it, or reading
the public address its gateway holds. Declaring such a router as a
[resource](../resources/router.md) instead would hand Terraform ownership of it, and a later
destroy would take down everything behind it.

## Example Usage

```hcl
data "dtcloud_router" "edge" {
  id = "3f2a1c9e-77b4-4a01-9d3e-2b6c81f4e5a7"
}

resource "dtcloud_router_interface" "app" {
  router_id  = data.dtcloud_router.edge.id
  network_id = dtcloud_network.app.id
}

output "edge_public_address" {
  value = data.dtcloud_router.edge.external_fixed_ip[0].ip_address
}
```

## Argument Reference

* `id` - (Required) ID of the router to look up. A router that does not exist is an error, not an
  empty result.

## Attributes Reference

* `name` - Name of the router.
* `external_network_id` - **ID** of the external network the gateway sits on. Empty if the
  gateway was removed outside Terraform.
* `enable_snat` - Whether the gateway masquerades traffic from the networks behind it.
* `status` - Status reported by the platform. `ACTIVE` is the resting state.
* `admin_state_up` - Whether the router is administratively enabled.
* `description` - Description held by the platform.
* `external_fixed_ip` - Addresses allocated to the gateway, each with `subnet_id` and
  `ip_address`.
* `project_id` - ID of the project the router belongs to.
* `created_at`, `updated_at` - When the router was created and last changed.

~> **This reads a different endpoint from [`dtcloud_routers`](routers.md).** Here the external
network is an **id**; there it is a **name**, and its id is not reported at all. Neither is
derived from the other — both are passed through as the platform sends them.
