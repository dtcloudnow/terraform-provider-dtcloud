---
page_title: "dtcloud: dtcloud_volume"
subcategory: "Storage"
---

# dtcloud_volume

Looks up one volume by ID.

Use it to reference a volume Terraform did not create — attaching an existing disk to a new VM,
for instance. Declaring it as a `resource` instead would hand Terraform ownership of it, and a
later `terraform destroy` would delete the volume, its data and its snapshots along with
everything else.

## Example Usage

```hcl
data "dtcloud_volume" "existing" {
  id = "0166bbb6-d42e-4002-9311-87a63aab1471"
}

resource "dtcloud_vm_volume_attachment" "data" {
  vm_id     = dtcloud_vm.web.id
  volume_id = data.dtcloud_volume.existing.id
}

output "free_to_attach" {
  value = data.dtcloud_volume.existing.attached_to_id == ""
}
```

## Argument Reference

* `id` - (Required) ID of the volume to look up.

## Attributes Reference

* `name` - Name of the volume.
* `size` - Size in GB.
* `storage_policy` - Name of the storage policy backing the volume.
* `status` - `available`, `in-use`, and so on.
* `bootable` - Whether the volume can be booted from.
* `volume_type` - `HDD`, or `CD-ROM` when the volume holds an ISO.
* `attached_to` / `attached_to_id` / `attached_to_status` - The VM this volume is attached to.
  Empty when it is detached.
* `is_detachable` - Whether the platform will allow this volume to be detached. Always `false`
  while the volume is detached, where [`dtcloud_volumes`](volumes.md) says `true` for the same
  one — the two endpoints disagree. See the [resource page](../resources/volume.md).
* `created` / `last_modified` - Timestamps as reported by the platform.
* `image_metadata` - Where the volume's contents came from. A single block, or empty for a
  blank volume: `image_id`, `image_name`, `os_distro`, `os_type`, `disk_format`,
  `container_format`, `checksum`, `min_disk`, `min_ram`, `size`.

~> **There is no `description`.** The API accepts one when writing but does not report it back,
so there is nothing for this data source to read. See the
[resource page](../resources/volume.md).
