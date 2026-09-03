---
page_title: "dtcloud: dtcloud_snapshot"
subcategory: "Storage"
---

# dtcloud_snapshot (Data Source)

Looks up one snapshot by ID.

Use this to reference a snapshot Terraform did not take — restoring a volume from one the
backup schedule produced, for instance. Declaring it as a
[`dtcloud_snapshot` resource](../resources/snapshot.md) instead would hand Terraform ownership
of it, and a later `terraform destroy` would delete the copy it was there to protect.

## Example Usage

```hcl
data "dtcloud_snapshot" "restore_point" {
  id = var.snapshot_id
}

resource "dtcloud_volume" "restored" {
  name               = "app-data-restored"
  size               = data.dtcloud_snapshot.restore_point.size
  storage_policy     = data.dtcloud_snapshot.restore_point.storage_policy
  source_snapshot_id = data.dtcloud_snapshot.restore_point.id
}
```

Taking `size` and `storage_policy` from the snapshot rather than repeating them is the point of
this data source: the restore endpoint requires both, and a size smaller than the snapshot's is
rejected.

## Argument Reference

* `id` - (Required) ID of the snapshot to look up.

## Attributes Reference

* `name` - Name of the snapshot.
* `volume_id` - ID of the volume it was taken from.
* `description` - Free-text description, as the platform reports it.
* `status` - Status reported by the platform. `available` is the only resting state.
* `size` - Size in GB, inherited from the source volume.
* `volume_type_id` - **ID** of the volume type backing the snapshot.
* `storage_policy` - The same volume type by **name**, resolved by the provider. Empty if the
  storage-policies endpoint could not be read.
* `progress` - How far the copy has got, as a percentage string such as `100%`.
* `project_id` - ID of the project the snapshot belongs to.
* `created_at` / `updated_at` - Timestamps as reported by the platform. `updated_at` is empty
  until the snapshot is first renamed or re-described.

This reads the same endpoint the resource does, so it reports everything the resource does —
`description` included. That is not true of
[`dtcloud_volume`](volume.md), where the details endpoint drops the description.

## Errors

A snapshot that does not exist is an **error**, not an empty result:

```
Snapshot "3f2a…" not found
```

That is deliberate. A data source that silently returned nothing would let a configuration
depending on it fail somewhere further down, with a message about the wrong thing.
