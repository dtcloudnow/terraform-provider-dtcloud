---
page_title: "dtcloud: dtcloud_network"
subcategory: "Networking"
---

# dtcloud_network

Looks up a single network by ID, along with its subnet when it has one.

Use this to reference a network Terraform did not create — which is the usual case, since
networks tend to outlive the things attached to them. Declaring one as a `resource` instead
would hand Terraform ownership of it, and a later `terraform destroy` would take it down along
with everything else.

## Example Usage

```hcl
data "dtcloud_network" "app" {
  id = "0166bbb6-d42e-4002-9311-87a63aab1471"
}

resource "dtcloud_vm" "web" {
  # ...
  network {
    uuid            = data.dtcloud_network.app.id
    security_groups = ["sg-web"]

    fixed_ip {
      ip_version = 4
    }
  }
}
```

## Argument Reference

* `id` - (Required) ID of the network to look up.

## Attributes Reference

* `name` - Name of the network.
* `network_type` - `Virtual` or `Physical`.
* `ipam_enabled` - Whether the platform manages addressing, i.e. whether there is a subnet.
* `subnet_id` - ID of the subnet. Empty when IPAM is off.
* `cidr` - Address range of the subnet.
* `gateway_ip` - Gateway address of the subnet.
* `enable_dhcp` - Whether the subnet hands out addresses over DHCP.
* `ip_version` - IP version of the subnet.
* `dns_nameservers` - DNS servers advertised on this network.
* `allocation_pools` - Address ranges the subnet allocates from: `start` and `end`.

~> A network with IPAM off has no subnet, so everything from `subnet_id` down is empty.
Check `ipam_enabled` before relying on them.
