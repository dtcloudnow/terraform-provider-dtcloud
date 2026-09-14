---
page_title: "dtcloud: dtcloud_elastic_ip"
subcategory: "Networking"
---

# dtcloud_elastic_ip

Provides a dtcloud elastic IP — a public address drawn from an external network that you can
point at a virtual machine or a load balancer, and move between them without giving the
address up.

The API and the SDK call this a *floating IP*; `dtctl` calls it an elastic IP, and so does
this provider.

## Example Usage

```hcl
resource "dtcloud_elastic_ip" "web" {
  floating_network_id = var.external_network_id
  port_id             = dtcloud_vm.web.network_interface[0].port_id
}

output "public_address" {
  value = dtcloud_elastic_ip.web.ip_address
}
```

Allocated now, pointed at something later — reserving the address is often the point:

```hcl
resource "dtcloud_elastic_ip" "reserved" {
  floating_network_id = var.external_network_id
}
```

## Argument Reference

* `floating_network_id` - (Required) ID of the external network to allocate from. Changing it
  releases the address and allocates a new one. See **Finding the external network** below.
* `port_id` - (Optional) Port to point the address at. Remove it to disassociate. Changing it
  moves the address in place — it is **not** released and reallocated.
* `fixed_ip_address` - (Optional) Which address on the port to map to. Requires `port_id`.
  The platform picks one when omitted — **unless the port carries more than one IPv4 address**,
  in which case it refuses the request and you must name the address. See below.
* `subnet_id` - (Optional) A specific subnet of the external network to take the address from.
  Only used when allocating, so changing it releases the address and allocates a new one.
  Never reported back — see **Import**.

## Attributes Reference

* `id` - ID of the elastic IP.
* `ip_address` - The public address. This is the value to hand out.
* `status` - `ACTIVE` when associated, `DOWN` when not.
* `device_id` - ID of the virtual machine or load balancer behind the port.
* `device_owner` - What kind of thing that is, as the platform labels it, e.g. `compute:nova`.
* `router_id` - Router carrying the traffic, filled in once associated.
* `description` - Read-only; neither route accepts one, so this is only set for an address
  created outside Terraform.
* `created_at` / `updated_at` - RFC 3339 timestamps.

## Behaviour worth knowing

### Finding the port

A VM's ports are on its `network_interface` blocks:

```hcl
port_id = dtcloud_vm.web.network_interface[0].port_id
```

The interfaces exist from the moment the VM boots, so this is populated on the first apply.
For an interface added after boot, use the `dtcloud_vm_network_interface` resource's `port_id`
instead — that way Terraform also knows the address depends on the interface.

The VM's network must be routed to the elastic IP's external network, through a router. An
address pointed at a port on an unrouted network is accepted and does not work.

### Finding the external network

`floating_network_id` is the external network, not the network your VM is on.
`GET /openstack/routers/externals` reports, for each of your networks, the external network it
is routed to — that `externalNetworkId` is the value to use. `dtctl router externals` prints
the same thing. There is no data source for it yet; it belongs to the router service.

### Moving the address is an update, not a replacement

Changing `port_id` points the same address somewhere else. The address is the valuable part —
it is in DNS, in someone's firewall rule — and this resource never gives it up to change what
it reaches. Only `floating_network_id` and `subnet_id` force a new address, because neither
can be changed on an existing one.

### Disassociation is all-or-nothing

The update endpoint has no partial mode: a request that does not name a port is how the
platform is told to **detach**, not how it is told to leave things alone. That is also why
the association lives on this resource rather than in a separate one — two resources writing
the same field through the same call would undo each other on every apply.

### `DOWN` is not a failure

An allocated address with nothing attached reports `DOWN`. That is its resting state. It is
also still occupying quota and still billable, which is what
[`dtcloud_elastic_ips`](../data-sources/elastic_ips.md) with `status = "DOWN"` is for.

### A port with several addresses needs `fixed_ip_address`

If the port carries more than one IPv4 address, the platform will not choose for you:

```
Bad floatingip request: Port <id> has multiple fixed IPv4 addresses.
Must provide a specific IPv4 address when assigning a floating IP.
```

The provider cannot catch this at plan time — what addresses a port has is not knowable until
the call is made — so this arrives at apply. Set `fixed_ip_address` to the one you want. A
VM's addresses are on its `network_interface` blocks, as `primary_ip` and `secondary_ips`.

### `fixed_ip_address` is only sent when you write it

If you leave it out, the provider never sends it, and the platform chooses an address on the
port. The chosen value is reported back but is not treated as something you asked for — so
moving the address to a different port works, rather than failing because the previous port's
address does not exist on the new one.

## Timeouts

* `create` - Defaults to **10 minutes**.
* `update` - Defaults to **10 minutes**.
* `delete` - Defaults to **10 minutes**.

Allocating and associating are asynchronous; the provider polls until the address reports the
port it was asked for.

```hcl
timeouts {
  update = "20m"
}
```

## Import

Elastic IPs are imported by ID:

```
terraform import dtcloud_elastic_ip.web 0d2cb93d-6ec3-4c32-966c-954b68ee584b
```

`port_id`, `fixed_ip_address` and everything computed round-trip.

**`subnet_id` does not.** The API accepts it when allocating and never reports it back, so an
imported address has nothing there. The provider will not propose replacing the address over
that — an empty value is treated as "unknown", not as "different" — but it does mean the
argument is invisible to drift, and you should write it into your configuration by hand if
you care which subnet the address came from.
