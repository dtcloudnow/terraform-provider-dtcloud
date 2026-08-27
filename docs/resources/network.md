---
page_title: "dtcloud: dtcloud_network"
subcategory: "Networking"
---

# dtcloud_network

Provides a dtcloud network and, when address management is on, its subnet.

One resource covers both because the API does not let them be managed apart: a single call
creates the pair, the details endpoint reports them together, and there is no endpoint that
creates a subnet on its own.

## Example Usage

```hcl
resource "dtcloud_network" "app" {
  name        = "app-net"
  cidr        = "10.20.30.0/24"
  gateway_ip  = "10.20.30.1"
  enable_dhcp = true

  dns_nameservers = ["8.8.8.8", "1.1.1.1"]

  allocation_pools {
    start = "10.20.30.10"
    end   = "10.20.30.200"
  }
}

output "subnet" {
  value = dtcloud_network.app.subnet_id
}
```

A network with no address management at all — no subnet, no CIDR, no DHCP:

```hcl
resource "dtcloud_network" "plain" {
  name         = "isolated-net"
  ipam_enabled = false
}
```

## Argument Reference

* `name` - (Required) Name of the network. Can be changed in place.
* `ipam_enabled` - (Optional) Defaults to `true`. Whether the platform manages addressing —
  see below, it does two things at once. Changing it recreates the network.
* `cidr` - (Optional) IPv4 range for the subnet, e.g. `10.20.30.0/24`. **Required when
  `ipam_enabled` is `true`, and rejected when it is `false`.** Changing it recreates the
  network.
* `gateway_ip` - (Optional) Gateway address. The platform picks the first usable address when
  omitted. Can be changed in place.
* `enable_dhcp` - (Optional) Defaults to `true`. Whether the subnet hands out addresses over
  DHCP. Can be changed in place.
* `dns_nameservers` - (Optional) DNS servers advertised to instances on this network. Can be
  changed in place.
* `allocation_pools` - (Optional) One or more blocks with `start` and `end`, the ranges the
  subnet allocates from. The platform picks a default when omitted. Can be changed in place.

## Attributes Reference

* `id` - ID of the network.
* `subnet_id` - ID of the subnet. Empty when `ipam_enabled` is `false`.
* `network_type` - `Virtual` or `Physical`, as reported by the platform.
* `ip_version` - IP version of the subnet. The API creates IPv4 only.

## Behaviour worth knowing

### `ipam_enabled` is one flag doing two jobs

This is the argument to understand before any of the others. It:

* becomes the network's **port security** setting, and
* decides whether a **subnet is created at all**.

With it off there is no subnet, so `cidr`, `gateway_ip`, `dns_nameservers` and
`allocation_pools` have nothing to apply to. Setting any of them alongside
`ipam_enabled = false` is rejected at plan time rather than failing against the API.

An IPAM-disabled network also reads back differently: the details endpoint omits the subnet
object entirely instead of returning an empty one.

### Subnet edits are sent as a complete set

The subnet endpoint takes `enable_dhcp`, `dns_nameservers` and `allocation_pools` together and
requires all three, so changing any one of them sends all four values, `gateway_ip` included.
Nothing is lost by this — the provider reads every one of them back — but it does mean an
out-of-band change to a field you do not manage will be overwritten the next time you change a
field you do.

### `ip_version` is not an argument

The API hardcodes IPv4 when creating a subnet, whatever the request asks for. Exposing a
choice that has no effect would be worse than not having one, so `ip_version` is reported back
as an attribute only.

### Creating is asynchronous

The API answers as soon as the request is accepted and then settles the network in the
background. There is no status field on the network to poll, so the provider waits until the
network reads back cleanly before returning.

## Timeouts

* `create` - Defaults to **15 minutes**.
* `update` - Defaults to **10 minutes**.
* `delete` - Defaults to **15 minutes**.

Creating and deleting are asynchronous — the provider polls until the network reads back, or stops resolving.

Override them with a `timeouts` block:

```hcl
timeouts {
  create = "30m"
}
```

## Import

Networks can be imported by ID:

```
terraform import dtcloud_network.app 0166bbb6-d42e-4002-9311-87a63aab1471
```

Everything round-trips, including the subnet settings — they are all reported by the details
endpoint.
