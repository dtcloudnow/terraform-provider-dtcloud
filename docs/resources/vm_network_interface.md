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

  fixed_ip {
    ip_version = 4
  }
}

output "internal_address" {
  value = dtcloud_vm_network_interface.internal.primary_ip
}
```

## Argument Reference

* `vm_id` - (Required) ID of the virtual machine. Changing this recreates the interface.
* `network_id` - (Required) ID of the network to attach to. Changing this recreates the interface.
* `security_groups` - (Optional) Security group IDs bound to this interface. **Can be changed in place.**
* `fixed_ip` - (Required) One or more blocks, at least one. Each takes `ip_version` (`4`, the
  default) and an optional `ip_address`. Leave `ip_address` out to have the subnet allocate
  one. **Can be changed in place.**
* `port_security_enabled` - (Optional) Defaults to `true`. Changing this recreates the
  interface. Only meaningful on virtual networks.

~> **`fixed_ip` is required.** An interface attached without one comes up with no address,
which is almost never what you meant.

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

## Import

```
terraform import dtcloud_vm_network_interface.internal <vm-id>:<port-id>
```

`fixed_ip` is not recovered on import; declare it to match the interface.
