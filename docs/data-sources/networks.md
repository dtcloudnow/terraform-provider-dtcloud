---
page_title: "dtcloud: dtcloud_networks"
subcategory: "Networking"
---

# dtcloud_networks

Lists the networks visible to the caller, optionally filtered.

Both filters are applied by the API rather than in Terraform, so a filtered list is still a
single call.

## Example Usage

```hcl
data "dtcloud_networks" "virtual" {
  network_type = "Virtual"
}

output "network_names" {
  value = [for n in data.dtcloud_networks.virtual.networks : n.name]
}
```

Finding one network by name:

```hcl
data "dtcloud_networks" "app" {
  name = "app-net"
}

locals {
  app_network_id = data.dtcloud_networks.app.networks[0].id
}
```

## Argument Reference

* `name` - (Optional) Only return networks whose name matches.
* `network_type` - (Optional) Only return networks of this type: `Virtual` or `Physical`.

## Attributes Reference

* `networks` - The networks that matched, each with:
  * `id`, `name`, `network_type`
  * `subnet_id`, `cidr`, `gateway`
  * `ipam` - `Enabled` or `Disabled`
  * `dhcp` - `Enabled` or `Disabled`
  * `port_security_enabled`

~> **The list endpoint reports a flatter shape than the details one.** It folds the subnet's
CIDR and gateway up onto the network and reports IPAM and DHCP as words rather than booleans.
For the full subnet — DNS servers, allocation pools, IP version — use the `dtcloud_network`
data source on a single id.
