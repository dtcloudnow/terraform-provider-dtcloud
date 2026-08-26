---
page_title: "dtcloud: dtcloud_vm"
subcategory: "Compute"
---

# dtcloud_vm

Provides a dtcloud virtual machine: create it, rename it, resize it, change its power state
and destroy it.

## Example Usage

```hcl
resource "dtcloud_ssh_key" "default" {
  name       = "terraform-poc"
  public_key = file("~/.ssh/id_rsa.pub")
}

resource "dtcloud_vm" "web" {
  name      = "web-01"
  flavor_id = "b8f4e2c1-0000-4a1b-8c2d-000000000001"
  key_name  = dtcloud_ssh_key.default.name

  network {
    uuid            = "11111111-2222-3333-4444-555555555555"
    security_groups = ["sg-web"]

    fixed_ip {
      ip_version = 4
    }
  }

  block_device {
    boot_index            = 0
    volume_size           = 20
    source_type           = "image"
    device_type           = "disk"
    destination_type      = "volume"
    delete_on_termination = true
    volume_type           = "standard"
    uuid                  = "img-ubuntu-22-04"
  }
}

output "address" {
  value = dtcloud_vm.web.primary_ip
}
```

## Argument Reference

* `name` - (Required) Name of the virtual machine. Can be changed in place.
* `flavor_id` - (Required) ID of the flavor. Changing this **resizes** the VM — see below.
* `network` - (Required) One or more network interface blocks, documented below.
* `block_device` - (Required) One or more block device blocks, documented below. The boot disk
  is the entry with `boot_index = 0`.
* `state` - (Optional) Desired power state: `running` (default), `stopped` or `shelved`.
* `graceful_shutdown` - (Optional) Defaults to `true`. How the VM is stopped — see below.
* `key_name` - (Optional) Name of an existing SSH key to inject.
* `user_data` - (Optional) Cloud-init user data.
* `script` - (Optional) Initial guest credentials, documented below.
* `is_gpu_image` - (Optional) Set when booting a GPU image.
* `enable_hot_plug` - (Optional) Allow live vCPU/memory resize. Can be toggled after creation.

~> **At least one of `key_name`, `user_data` or `script` is required.** They are the only ways
credentials or a provisioning payload reach the guest; a VM built with none of the three boots
with no way in. Terraform rejects that at plan time rather than letting you build an
unreachable instance.

### network

* `uuid` - (Required) ID of the network to attach to.
* `security_groups` - (Required) Security group **IDs** bound to this interface. At least one.
* `fixed_ip` - (Required) One or more blocks, at least one. Each takes `ip_version` (`4`,
  the default) and an optional `ip_address`. Leave `ip_address` out to have the subnet
  allocate one; set it to pin a specific address.
* `port_security_enabled` - (Optional) Defaults to `true`. When false, security groups do not
  apply to this interface. **Only meaningful on virtual networks** — see the note below.

### block_device

* `boot_index` - (Required) Boot order; the boot disk is `0`.
* `volume_size` - (Required) Size in GB, between 1 and 8192.
* `source_type` - (Required) One of `blank`, `image`, `volume`, `backup`.
* `device_type` - (Required) One of `cdrom`, `disk`.
* `destination_type` - (Required) One of `volume`, `local`.
* `delete_on_termination` - (Required) Whether the device is deleted along with the VM.
* `uuid` - (Optional) ID of the source image, volume or backup. Omit for `blank`.
* `volume_type` - (Required) Storage policy / volume type the disk is created on.

### script

* `os` - (Required) One of `linux`, `windows`.
* `password` - (Required, sensitive) Initial password for the guest account.
* `username` - (Optional) Account to create.
* `hostname` - (Optional) Hostname to set inside the guest.
* `disable_root` - (Optional) Disable direct root login.

~> **`username`, `hostname` and `disable_root` are optional only because of Windows.** Windows
template images ignore them. On a **Linux** image all three are expected: leave one out and the
guest is configured with the platform's defaults instead of yours — no error, just a machine
that is not what your configuration describes.
<br /><br />
The provider cannot turn this into a schema rule, because whether the fields apply depends on
`os`, a sibling field, and Terraform validates one field at a time. Instead it **warns during
`apply`**, naming the fields you left out, while there is still time to act:
<br /><br />
`Warning: Incomplete script block for a Linux image`
<br />
`script.os is "linux" but username, hostname not set. ...`

## Attributes Reference

* `id` - ID of the virtual machine.
* `status` - Raw platform status, e.g. `ACTIVE`, `SHUTOFF`, `SHELVED_OFFLOADED`, `ERROR`.
* `task_state` - In-flight OpenStack task; empty when settled.
* `image` / `image_os_type` - Image the VM was built from, and its OS family.
* `ssh_key` - Name of the injected SSH key.
* `flavor_name` / `vcpus` / `ram` - Resolved sizing, e.g. `tiny` / `1` / `512 MB`.
* `created_at` / `last_modified` - RFC 3339 timestamps.
* `primary_ip` - The primary IP of the first public interface, falling back to the first
  interface that has an address. Use this for outputs, DNS records and provisioners.
* `network_interface` - Interfaces attached to the VM: `port_id`, `network_id`, `network_name`,
  `mac_address`, `primary_ip`, `secondary_ips`, `is_public`, `spoofing_protection` and
  `security_groups` (`id` / `name`).
* `volume` - Volumes attached to the VM, including the boot disk: `id`, `name`,
  `storage_policy`, `size`, `delete_on_termination`.

## Behaviour worth knowing

### `state` — power, after the VM exists

`state` is not a creation setting. The VM is always built first; `state` is then the power
state Terraform keeps it in, and changing it in your configuration is what drives the change:

| From | To | What the provider does |
|------|----|------------------------|
| `running` | `stopped` | `softStop`, or `hardStop` — see `graceful_shutdown` |
| `stopped` | `running` | `start` |
| any | `shelved` | `shelve` — releases the compute resources, keeps the VM and its disks |
| `shelved` | `running` | `unshelve` |
| `shelved` | `stopped` | `unshelve`, then stop — leaving the shelved state always goes through unshelve first |

Shelving is the one worth knowing: a shelved VM stops consuming compute while keeping
everything else, so it is how you park a machine you are not using without destroying it.

### `graceful_shutdown` — how it stops, not whether

This is a modifier on stopping, not an operation of its own. With the default `true` the
provider sends `softStop`, which asks the guest OS to shut itself down cleanly. With `false`
it sends `hardStop`, which cuts the power — faster, and it risks whatever the guest had not
yet flushed to disk. It also applies to the stop that happens during a resize.

### Changing `flavor_id`

**With `enable_hot_plug = true`** the instance takes vCPU and memory changes while it is
`ACTIVE`. The provider resizes in place and the VM stays up.

**Without hot plug** the platform refuses to resize a running instance, so the provider stops
the VM, resizes it, and returns it to the `state` you configured. Expect it to be unavailable
for a minute or two. The platform parks it in `VERIFY_RESIZE` on the way through and settles
on its own — no confirmation step is needed.

### What changes in place, and what rebuilds

In place: `name`, `flavor_id` (resize), `state` (power) and `enable_hot_plug`.

`ForceNew`: `network`, `block_device` and the boot-time settings (`key_name`, `user_data`,
`script`, `is_gpu_image`) — the API cannot change those on a live instance. Interfaces and
volumes *can* be added after boot, but through the separate `dtcloud_vm_network_interface` and
`dtcloud_vm_volume_attachment` resources, not by editing this resource's boot-time blocks.

### Every interface needs an address

`fixed_ip` is required for a reason: an interface built without one comes up with no address
at all and the VM stays unreachable. Declaring `fixed_ip { ip_version = 4 }` without an
`ip_address` is the normal case — it asks the subnet to allocate one.

### `port_security_enabled` and physical networks

Port security is a virtual-network concept. On a physical network the field does not apply,
and it should not be sent.

~> The provider currently sends it on every interface, including physical ones. Set it only
on virtual networks until that is fixed.

### `network_interface` and `volume` are a snapshot

They reflect the VM's last refresh. An attachment created by a separate resource in the same
`apply` will not appear in these lists until the next refresh; read the attachment resource's
own attributes instead.

### Creating several identical VMs

The API accepts a `vmCount`, but this resource does not expose it — one Terraform resource
must map to one instance, otherwise only the first would be tracked. Use Terraform's own
`count` or `for_each`.

## Import

VMs can be imported by ID:

```
terraform import dtcloud_vm.web 9f1c2b3a-0000-4a1b-8c2d-1234567890ab
```

`name`, `flavor_id`, `state` and the computed attributes are recovered on import. The
boot-time arguments (`network`, `block_device`, `key_name`, `user_data`, `script`,
`is_gpu_image`) are not returned by any endpoint — declare them in your configuration to match
the instance, or Terraform will plan a replacement.
