---
page_title: "dtcloud: dtcloud_volume"
subcategory: "Storage"
---

# dtcloud_volume

Provides a dtcloud block storage volume.

This resource owns the volume itself — creating it, naming it, growing it and changing its
storage policy. **Attaching it to a VM is a separate resource**,
[`dtcloud_vm_volume_attachment`](vm_volume_attachment.md), because the attachment has its own
lifecycle: a volume can be detached and re-attached elsewhere without the volume changing.

## Example Usage

A blank data disk:

```hcl
data "dtcloud_storage_policies" "available" {}

resource "dtcloud_volume" "data" {
  name           = "app-data"
  size           = 100
  storage_policy = data.dtcloud_storage_policies.available.names[0]
}
```

A bootable volume built from an image:

```hcl
resource "dtcloud_volume" "boot" {
  name           = "web-01-boot"
  size           = 40
  storage_policy = "standard"
  image_id       = var.image_id
}
```

A copy of an existing volume:

```hcl
resource "dtcloud_volume" "restore" {
  name             = "app-data-copy"
  size             = 100
  storage_policy   = "standard"
  source_volume_id = dtcloud_volume.data.id
}
```

Attaching one to a VM:

```hcl
resource "dtcloud_vm_volume_attachment" "data" {
  vm_id     = dtcloud_vm.web.id
  volume_id = dtcloud_volume.data.id
}
```

## Argument Reference

* `name` - (Required) Name of the volume. Must not contain `<`, `>`, `&`, `"` or `'`. Can be
  changed in place.
* `size` - (Required) Size in GB, between 1 and 8192. **Can only be grown** — a lower value is
  refused at apply; see below.
* `storage_policy` - (Required) **Name** of the storage policy backing the volume — not its id.
  List the valid values with [`dtcloud_storage_policies`](../data-sources/storage_policies.md).
  Changing it retypes the volume in place.
* `description` - (Optional) Free-text description. Must not contain `<`, `>`, `&`, `"` or `'`.
  **Write-only** — see below.
* `image_id` - (Optional) Create the volume from this image, making it bootable. Changing it
  recreates the volume. Conflicts with `source_volume_id`.
* `source_volume_id` - (Optional) Create the volume as a clone of this one. Changing it
  recreates the volume. Conflicts with `image_id`.

## Attributes Reference

* `id` - ID of the volume.
* `status` - Status reported by the platform: `available`, `in-use`, and so on.
* `bootable` - Whether the volume can be booted from. True for volumes built from an image.
* `volume_type` - `HDD`, or `CD-ROM` when the volume holds an ISO. This is the platform's own
  classification and has nothing to do with `storage_policy`.
* `attached_to` / `attached_to_id` / `attached_to_status` - The VM this volume is attached to.
  Empty when it is detached.
* `is_detachable` - Whether the platform will allow this volume to be detached. **Always `false`
  while the volume is detached** — see below.
* `created` / `last_modified` - Timestamps as reported by the platform.
* `image_metadata` - Where the volume's contents came from. A single block, or empty for a
  blank volume:
  * `image_id`, `image_name`
  * `os_distro`, `os_type`
  * `disk_format`, `container_format`, `checksum`
  * `min_disk`, `min_ram`, `size` - all reported as strings by the platform.

## Behaviour worth knowing

### `size` can only be grown

Neither the API nor OpenStack underneath it can shrink a volume. A configuration that lowers
`size` is **refused when you apply it**:

```
size cannot be reduced from 100 to 50: volumes can only be grown.
```

The alternative would be to mark `size` as forcing a replacement, and a replacement here
deletes the volume and its data to satisfy the plan. Refusing is the safer answer; if you
really do want a smaller volume, create a new one and remove the old one deliberately.

The refusal happens at apply rather than at plan on purpose. A plan-time check cannot tell
"the user lowered the number" from "the volume was grown outside Terraform and the
configuration has not caught up" — both look like a shrink. Refusing at plan therefore also
refused the plan `terraform destroy` builds, which left a volume somebody had grown in the web
console impossible to destroy at all.

So if a plan proposes to shrink a volume you did not edit, somebody grew it elsewhere. Raise
`size` in your configuration to match, and the plan settles.

### An attached volume cannot be deleted

This is the rule most likely to interrupt a `terraform destroy`. The platform will not delete a
volume whose status is `in-use` — not slowly, not with a retry. It has to be detached first.

The provider checks before it tries, so the error names the machine:

```
Volume "0166bbb6-…" is still attached to "web-01" (9f1c2b3a-…) and cannot be
deleted; detach it first.
```

**The provider will not detach for you.** Disconnecting a disk from a running machine is not
something a destroy should do unasked, so this is refused rather than worked around.

In practice there are two cases:

* **The attachment is managed by Terraform.** Nothing to do — a
  [`dtcloud_vm_volume_attachment`](vm_volume_attachment.md) that references this volume makes
  Terraform destroy the attachment first, on its own.
* **It was attached outside Terraform.** Detach it there, then destroy.

One thing to expect either way: a detach can be refused by the guest. If the filesystem is
still mounted the platform reports `detaching` for a while and then puts the volume back to
`in-use`. Unmount it inside the guest, or stop the machine, and try again.

### Boot disks are not `dtcloud_volume`

A `dtcloud_volume` is always a **data** volume. The disk a machine boots from is created by the
`block_device` block on [`dtcloud_vm`](vm.md), belongs to that machine, and is deleted with it
when `delete_on_termination` is set. It cannot be detached, and it is not managed here.

### `description` is write-only

The API accepts a description and stores it, but the details endpoint builds its response field
by field and does not include one. So a description:

* cannot be read back, and therefore **cannot drift** — an out-of-band change is invisible;
* does **not** survive `terraform import`;
* cannot be **cleared**. Sending an empty description leaves the existing one in place. Set it
  to something else instead.

It is also applied as a second call: the create endpoint has no description parameter, so the
provider creates the volume and then updates it.

### Growing and retyping look finished before they are

`osExtend` and `osRetype` are accepted while the volume is `available`, and the volume stays
`available` for a while afterwards, still reporting its old size or policy. The provider
therefore waits on **the value you asked for** rather than on the status — for a grow, until
the reported size reaches the new one; for a retype, until the reported policy matches.

This matters if you are reading the code or writing your own tooling against the same API: a
wait that only watches the status returns immediately, having done nothing.

### `is_detachable` contradicts `dtcloud_volumes`

For one and the same detached volume, this resource reports `is_detachable = false` while
[`dtcloud_volumes`](../data-sources/volumes.md) reports `true`. That is the API's own
disagreement, not the provider's: the details endpoint asks a helper that returns `false`
outright when there is no attached server, and the list endpoint falls through to `true` in the
same situation. Each page reports what the endpoint it read said.

Treat it as meaningful only for an **attached** volume, which is the only case it was designed
to answer. Confirmed live on DEV.

### `storage_policy` is a name

The details endpoint reports the policy by name. Passing a volume type **id** would create the
volume successfully and then read back as the name, so every subsequent plan would propose a
change that could never converge. `dtcloud_storage_policies` exposes both `id` and `name`; use
`name`.

### Deleting takes the snapshots with it

Once the volume is detached, `terraform destroy` deletes it with cascade enabled, so **every
snapshot of that volume is deleted too**. Check what is there first with
[`dtcloud_volume_snapshots`](../data-sources/volume_snapshots.md).

## Timeouts

* `create` - Defaults to **30 minutes**.
* `update` - Defaults to **30 minutes**.
* `delete` - Defaults to **20 minutes**.

All three are asynchronous. Creating from an image copies the image data, and growing a large
volume is not instant, so the defaults are generous rather than tight.

Override them with a `timeouts` block:

```hcl
timeouts {
  create = "1h"
}
```

## Import

Volumes can be imported by ID:

```
terraform import dtcloud_volume.data 0166bbb6-d42e-4002-9311-87a63aab1471
```

**What does not round-trip.** The details endpoint does not report these, so they have to be
written into your configuration by hand or the next plan will propose a change:

* `description` - never reported. Import leaves it empty.
* `image_id` - never reported. `image_metadata.image_id` looks like it would do, but it is the
  image the *contents* came from, which a cloned volume inherits from its source — writing it
  into `image_id` would propose a replacement.
* `source_volume_id` - never reported. A clone is indistinguishable from any other volume once
  it exists.

Since all three force a replacement or are write-only, an import followed by a plan that wants
to change them is a sign the configuration needs the values filled in, not that the volume is
wrong.
