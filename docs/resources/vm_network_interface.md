---
page_title: "dtcloud: dtcloud_vm_network_interface"
subcategory: "Compute"
---

# dtcloud_vm_network_interface

Attaches an additional network interface to a virtual machine.

Interfaces declared in a `dtcloud_vm`'s `network` blocks are created with the VM and live and
die with it. Use this resource for interfaces added afterwards: they get their own port id and
can be detached without recreating the VM.

## Example Usage

```hcl
resource "dtcloud_vm_network_interface" "internal" {
  vm_id      = dtcloud_vm.web.id
  network_id = "99999999-8888-7777-6666-555555555555"

  security_groups = ["sg-internal"]
}

output "internal_address" {
  value = dtcloud_vm_network_interface.internal.primary_ip
}
```

## Argument Reference

* `vm_id` - (Required) ID of the virtual machine. Changing this recreates the interface.
* `network_id` - (Required) ID of the network to attach to. Changing this recreates the interface.
* `security_groups` - (Optional) Security group IDs bound to this interface. **Can be changed
  in place.** Left out, the port keeps whatever the platform gave it, reported back here.
* `fixed_ip` - (Optional) One or more blocks. **Omit it for the normal case**: one address is
  requested and the platform allocates it. Set `ip_address` to pin a specific one. **Can be
  changed in place.** After apply the assigned addresses are reported back in these blocks.
  * `ip_address` - (Optional) Address to pin. Allocated by the platform when omitted.
  * `ip_version` - (Optional) `4` or `6`, advisory: the platform allocates from the attached
    network regardless. Reported back from the assigned address.
* `port_security_enabled` - (Optional) Whether port security applies. **Left unset it is not
  sent at all** and the network's own setting stands. Changing this recreates the interface.

-> **These arguments are identical to the `network` block on `dtcloud_vm`.** The two describe
the same thing — a port on a network — so a block that works inline works here unchanged. Use
`dtcloud_vm`'s block for interfaces the instance boots with, and this resource for ones added
afterwards, which have their own lifecycle and can be detached without rebuilding the instance.

~> **An interface never ends up without an address.** An empty `fixed_ips` list means "no
address" to the platform, not "allocate one", so the provider requests one when you leave the
block out rather than sending an empty list.

## Attributes Reference

* `id` - `<vm-id>:<port-id>`.
* `port_id` - ID of the port the platform created.
* `mac_address` - MAC address the platform assigned. Read-only; requesting a specific MAC is
  not offered.
* `network_name` - Name of the network.
* `primary_ip` - Primary IP address on this interface.
* `is_public` - Whether the interface is on a public network.

## Behaviour worth knowing

-> **How the port id is found.** The attach endpoint does not report which port it created, so
the provider lists the VM's interfaces before and after the call and takes the new one. If
something else attaches an interface to the same VM at the same moment, the wrong port could be
picked up. In practice this only matters if you are attaching interfaces to one VM from two
places concurrently.

~> The `network_interface` list on `dtcloud_vm` is a snapshot from that resource's last refresh.
An interface attached here in the same `apply` will not appear there until the next refresh.

## Timeouts

* `create` - Defaults to **10 minutes**.
* `update` - Defaults to **10 minutes**.
* `delete` - Defaults to **10 minutes**.

Override them with a `timeouts` block:

```hcl
timeouts {
  create = "30m"
}
```

## Import

```
terraform import dtcloud_vm_network_interface.internal <vm-id>:<port-id>
```

`fixed_ip` is not recovered on import; declare it to match the interface.
