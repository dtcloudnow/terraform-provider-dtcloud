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

The `network` block above leaves `fixed_ip` out, which is the common case: one address is
requested and the platform allocates it. To choose the address yourself, or to ask for more
than one, write the block:

```hcl
  network {
    uuid            = "11111111-2222-3333-4444-555555555555"
    security_groups = ["sg-web"]

    fixed_ip {
      ip_address = "10.0.0.50"
    }
  }
```

Either way the addresses actually assigned are reported back into these blocks after apply,
so `dtcloud_vm.web.network[0].fixed_ip[0].ip_address` holds the real value.

### Getting into the machine

`key_name` is one of three ways, and **at least one of them is required** — an instance built
with none of them boots with no way in. Use `script` for a machine you log into with a
password, which is what Windows images expect:

```hcl
resource "dtcloud_vm" "win" {
  name      = "win-01"
  flavor_id = var.flavor_id

  script {
    os       = "windows"
    password = var.admin_password
  }

  network { uuid = var.network_id }

  block_device {
    boot_index            = 0
    volume_size           = 64
    source_type           = "image"
    device_type           = "disk"
    destination_type      = "volume"
    delete_on_termination = true
    volume_type           = "standard"
    uuid                  = var.windows_image_id
  }
}
```

On a Linux image set `username`, `hostname` and `disable_root` as well. They are optional only
because Windows template images ignore them; leaving them out on Linux gives you a guest
configured with the platform's defaults instead of yours, with no error to tell you so:

```hcl
  script {
    os           = "linux"
    username     = "deploy"
    password     = var.initial_password
    hostname     = "app-01"
    disable_root = true
  }
```

`user_data` is the third way, for anything cloud-init can do:

```hcl
  user_data = <<-EOT
    #cloud-config
    packages:
      - nginx
  EOT
```

~> `user_data` and `script` cannot both be set. The platform builds cloud-init from `script`
only when `user_data` is absent, so setting both would silently discard the script. The
provider rejects that during plan.

### Several interfaces and several disks

Both blocks repeat. The boot disk is the one with `boot_index = 0`:

```hcl
resource "dtcloud_vm" "db" {
  name      = "db-01"
  flavor_id = var.flavor_id
  key_name  = dtcloud_ssh_key.default.name

  network { uuid = var.frontend_network_id }
  network { uuid = var.storage_network_id }

  block_device {
    boot_index            = 0
    volume_size           = 20
    source_type           = "image"
    device_type           = "disk"
    destination_type      = "volume"
    delete_on_termination = true
    volume_type           = "standard"
    uuid                  = var.image_id
  }

  block_device {
    boot_index            = 1
    volume_size           = 200
    source_type           = "blank"
    device_type           = "disk"
    destination_type      = "volume"
    delete_on_termination = false
    volume_type           = "fast"
  }
}
```

Adding or removing either block after the instance exists **replaces** it. To attach an
interface or a disk to a running instance, use `dtcloud_vm_network_interface` and
`dtcloud_vm_volume_attachment`.

## Argument Reference

* `name` - (Required) Name of the virtual machine. Can be changed in place.
* `flavor_id` - (Required) ID of the flavor. Changing this **resizes** the VM — see below.
* `network` - (Required) One or more network interface blocks, documented below. An instance
  with no interface has no address, so the provider rejects that at plan time.
* `block_device` - (Required) One or more block device blocks, documented below. The boot disk
  is the entry with `boot_index = 0`. Like `network`, the rule is enforced at plan time rather
  than by the schema, so that `terraform import` can fill the blocks in.
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

* `uuid` - (Required, ForceNew) ID of the network to attach to.
* `security_groups` - (Optional) Security group **IDs** bound to this interface. **Can be
  changed in place** — the platform rebinds them on the existing port, no rebuild. Left out,
  the port keeps whatever the platform gave it, and that is reported back here.
* `fixed_ip` - (Optional) One or more blocks. **Omit it entirely for the normal case**: the
  provider then asks for a single address and the platform allocates one. Set `ip_address` to
  pin a specific address. **Can be changed in place.** After apply, the addresses the platform
  actually assigned are reported back in these blocks.
  * `ip_address` - (Optional) Address to pin. Allocated by the platform when omitted, and
    reported back either way.
  * `ip_version` - (Optional) `4` or `6`. Advisory: the platform allocates from the attached
    network whatever version you ask for, so pin `ip_address` when you need a specific one.
    Reported back from the address actually assigned. The provider omits it from the request
    when `ip_address` already implies the version, because every extra attribute is one more
    Neutron policy rule the caller has to hold — the physical-network refusal named
    `create_port:fixed_ips:ip_version` explicitly.
* `port_security_enabled` - (Optional, ForceNew) Whether port security applies to this
  interface. **Left unset it is not sent at all** and the network's own setting stands — see
  the note below. It cannot be combined with `security_groups`.

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
* `metadata` - Metadata the platform attaches to the instance, e.g. `ha_enabled`.

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

```hcl
resource "dtcloud_vm" "batch" {
  name      = "batch-runner"
  flavor_id = var.flavor_id
  key_name  = dtcloud_ssh_key.default.name

  # Park it between runs: no compute is consumed, the disks stay.
  state = "shelved"

  network { uuid = var.network_id }

  block_device {
    boot_index            = 0
    volume_size           = 20
    source_type           = "image"
    device_type           = "disk"
    destination_type      = "volume"
    delete_on_termination = true
    volume_type           = "standard"
    uuid                  = var.image_id
  }
}
```

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

```hcl
resource "dtcloud_vm" "api" {
  name      = "api-01"
  flavor_id = var.flavor_id # change this and the instance is resized without stopping
  key_name  = dtcloud_ssh_key.default.name

  enable_hot_plug   = true
  graceful_shutdown = true # applies to any stop, including a non-hot-plug resize

  network { uuid = var.network_id }

  block_device {
    boot_index            = 0
    volume_size           = 20
    source_type           = "image"
    device_type           = "disk"
    destination_type      = "volume"
    delete_on_termination = true
    volume_type           = "standard"
    uuid                  = var.image_id
  }
}
```

**Without hot plug** the platform refuses to resize a running instance, so the provider stops
the VM, resizes it, and returns it to the `state` you configured. Expect it to be unavailable
for a minute or two. The platform parks it in `VERIFY_RESIZE` on the way through and settles
on its own — no confirmation step is needed.

-> **How to tell the two apart in a plan.** A resize that power-cycles the instance shows
`status` as `(known after apply)` alongside the `flavor_id` change; a hot-plug resize does not,
because the instance never leaves `ACTIVE`. The provider cannot say more than that while
planning — the Terraform SDK it is built on lets a plan raise an error but not a warning — so
the full explanation arrives as a warning on the apply that performs the resize.

### Changes that need something done inside the guest

Three in-place updates finish successfully on the platform and still leave the instance not
quite as you intended. The provider raises a warning for each on the apply that makes them:

| Change | What is left to do |
|---|---|
| `flavor_id` without hot plug | Nothing, but everything running inside was interrupted by the power cycle. |
| `volume_size` increased | The block device is bigger; the partition and filesystem inside are not. Extend them in the guest (`growpart`, `resize2fs` or your filesystem's equivalent) or the space stays unused. |
| `fixed_ip.ip_address` changed | The address moved on the port, where the platform enforces it. The guest keeps the old one until its DHCP lease renews, and forever if the interface is configured statically — so the instance can be unreachable in between. Renew the lease or reboot. |

### What changes in place, and what rebuilds

**In place:** `name`, `flavor_id` (resize), `state` (power), `enable_hot_plug`, and inside the
blocks — `network.security_groups`, `network.fixed_ip`, `block_device.volume_size` (increase
only) and `block_device.volume_type`.

**Rebuilds:** adding or removing a whole `network` or `block_device` block, `network.uuid`,
`network.port_security_enabled`, the rest of `block_device`, and the boot-time settings
(`key_name`, `user_data`, `script`, `is_gpu_image`) — the API cannot change those on a live
instance. Interfaces and volumes *can* be added after boot, but through the separate
`dtcloud_vm_network_interface` and `dtcloud_vm_volume_attachment` resources.

### Every interface needs an address

An interface built with an empty `fixed_ips` list comes up with no address at all and the VM
stays unreachable — an empty list means "no address", not "allocate one". You do not have to
guard against this: leave `fixed_ip` out and the provider requests a single address for you.

### `port_security_enabled` and physical networks

Port security is a virtual-network concept. On a physical network Neutron's policy refuses a
port that asks for it, and the instance lands in `ERROR` with a scheduling failure rather than
a clear message.

The field therefore has **no default**. Left unset it stays out of the request entirely and
the network decides. Set it only when you actually mean to override a virtual network's
setting.

### What drift Terraform can see

*Drift* is when reality stops matching what Terraform recorded — someone renamed the VM in the
console, resized it, swapped its key. On the next `plan` Terraform refreshes from the API,
notices the difference and offers to put it back.

It can only notice what the API reports. Here that is: `name`, `flavor_id`, `state`,
`key_name`, `enable_hot_plug` and everything under Attributes Reference. A key swapped outside Terraform shows up
as a proposed **replacement**, because `key_name` is `ForceNew` — which is the honest answer
when the way into the machine has changed.

`block_device` is refreshed too, as far as the platform reports it: `volume_size`,
`volume_type` and `delete_on_termination` come from the volume list, so a disk grown or
retyped outside Terraform shows up.

What it cannot see: `user_data`, `script` and `is_gpu_image`. Nothing reports them, so a change
made to them outside Terraform stays invisible and `plan` says "no changes". That is an API
limitation, not a choice.

### `network_interface` and `volume` are a snapshot

They reflect the VM's last refresh. An attachment created by a separate resource in the same
`apply` will not appear in these lists until the next refresh; read the attachment resource's
own attributes instead.

### Creating several identical VMs

The API accepts a `vmCount`, but this resource does not expose it — one Terraform resource
must map to one instance, otherwise only the first would be tracked. Use Terraform's own
`count` or `for_each`.

## Timeouts

* `create` - Defaults to **20 minutes**.
* `update` - Defaults to **10 minutes**.
* `delete` - Defaults to **15 minutes**.

Creating waits for the VM to reach `ACTIVE`; a resize stops and restarts it, so an update can legitimately take several minutes.

Override them with a `timeouts` block:

```hcl
timeouts {
  create = "30m"
}
```

## Import

VMs can be imported by ID:

```
terraform import dtcloud_vm.web 9f1c2b3a-0000-4a1b-8c2d-1234567890ab
```

**Recovered on import:** `name`, `flavor_id`, `state`, `key_name`, `enable_hot_plug`, the
`network` blocks (network id, security groups and port security) and every computed attribute.

**Not recovered — write these into your configuration to match the instance, or the first plan
will propose a replacement:**

| Argument | Why not |
|----------|---------|
| `block_device` | Partly recovered: the boot disk comes back with its size, volume type, delete-on-termination and source image id. `source_type`, `device_type` and `destination_type` are not reported, so they are filled in from what the volume implies. Disks attached after boot are **not** recovered here — they belong to `dtcloud_vm_volume_attachment`. |
| `user_data` | Nothing echoes it back. |
| `script` | Nothing echoes it back — which is right, since it carries a password. |
| `is_gpu_image` | Exists only in configuration. |
| `graceful_shutdown` | Exists only in configuration — it describes *how* to stop, not a property of the machine. |

-> **`network` is read back from the ports.** Each block is filled in with the network id, the
security group ids, port security and the addresses actually assigned. On a refresh, blocks are
matched to ports by network id, so the order you wrote them in is preserved and interfaces
attached separately with `dtcloud_vm_network_interface` are left out.

!> **Import a VM before attaching extra interfaces to it.** An import starts with no blocks to
match against, so *every* interface the VM currently has becomes a `network` block. If some of
them belong to `dtcloud_vm_network_interface` resources, your configuration declares fewer
blocks than the import recovered, and removing a block forces a replacement — the first plan
after the import will propose to destroy the machine. Either import while the VM still has only
its boot interface, or afterwards add matching `network` blocks to the configuration and drop
the separate interface resources.
