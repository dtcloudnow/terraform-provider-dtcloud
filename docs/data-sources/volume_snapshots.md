---
page_title: "dtcloud: dtcloud_volume_snapshots"
subcategory: "Storage"
---

# dtcloud_volume_snapshots

Lists the snapshots taken of one volume.

Read-only on purpose. Snapshots are managed by [`dtcloud_snapshot`](../resources/snapshot.md); creating one here
would put two resources in charge of the same object.

What this is for is **seeing what a destroy would take with it**. Deleting a volume cascades,
so every snapshot of it goes at the same time.

## Example Usage

```hcl
data "dtcloud_volume_snapshots" "data" {
  volume_id = dtcloud_volume.data.id
}

output "snapshot_names" {
  value = [for s in data.dtcloud_volume_snapshots.data.snapshots : s.name]
}

# A destroy of dtcloud_volume.data would delete all of these too.
output "snapshots_at_risk" {
  value = length(data.dtcloud_volume_snapshots.data.snapshots)
}
```

## Argument Reference

* `volume_id` - (Required) ID of the volume whose snapshots to list.

## Attributes Reference

* `snapshots` - The snapshots of the volume, **newest first** — the API sorts by creation date
  descending. Each has:
  * `id`, `name`, `status`
  * `description`
  * `size` - in GB
  * `storage_policy`
  * `created_on`

A volume with no snapshots returns an empty list rather than an error. A volume that does not
exist is an error.
