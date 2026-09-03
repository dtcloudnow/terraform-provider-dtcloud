---
page_title: "dtcloud: dtcloud_snapshots"
subcategory: "Storage"
---

# dtcloud_snapshots (Data Source)

Lists every snapshot visible to the caller, optionally filtered.

## This is not `dtcloud_volume_snapshots`

The provider has two snapshot lists because the API has two, and they are not the same
endpoint answering twice.

| | `dtcloud_snapshots` | [`dtcloud_volume_snapshots`](volume_snapshots.md) |
|---|---|---|
| Endpoint | `GET /openstack/snapshots` | `GET /openstack/volumes/{id}/snapshots` |
| Scope | every snapshot | one volume's |
| Order | the platform's | newest first, sorted by the API |
| Filtering | by the provider | by the API |
| Fields | `description`, `volume_type_id`, `updated_at` | `created_on` only |

Reach for `dtcloud_volume_snapshots` when the question is "what would a
`terraform destroy` of this volume take with it" — that is what it was written for. Reach for
this one when the question is about snapshots in general, or when you need a field the other
does not report.

Both report `storage_policy` as a **name**, and they agree. The raw endpoint behind this data
source reports it as an id; the provider resolves it, the same way the API resolves it behind
the other one.

## Example Usage

Everything:

```hcl
data "dtcloud_snapshots" "all" {}

output "snapshot_count" {
  value = length(data.dtcloud_snapshots.all.snapshots)
}
```

The usable snapshots of one volume:

```hcl
data "dtcloud_snapshots" "restore_points" {
  volume_id = dtcloud_volume.data.id
  status    = "available"
}
```

Finding one by name:

```hcl
data "dtcloud_snapshots" "nightly" {
  name = "app-data-nightly"
}

resource "dtcloud_volume" "restored" {
  name               = "app-data-restored"
  size               = data.dtcloud_snapshots.nightly.snapshots[0].size
  storage_policy     = data.dtcloud_snapshots.nightly.snapshots[0].storage_policy
  source_snapshot_id = data.dtcloud_snapshots.nightly.snapshots[0].id
}
```

Snapshot names are not unique on the platform, so indexing `[0]` is a guess unless you know
otherwise. Prefer [`dtcloud_snapshot`](snapshot.md) with an id when you have one.

## Argument Reference

All three filters are optional and combine with AND. **They are applied by the provider, not by
the API** — the list endpoint accepts no filters, so every snapshot is fetched and then filtered
locally. The filters are a convenience, not a smaller request.

* `volume_id` - (Optional) Only snapshots of this volume.
* `name` - (Optional) Only snapshots with this exact name. Case-sensitive, no wildcards.
* `status` - (Optional) Only snapshots in this status, e.g. `available`. Case-insensitive.

## Attributes Reference

* `snapshots` - The snapshots that matched, in the order the platform returned them. Each
  contains:
  * `id`, `name`, `description`
  * `volume_id` - the volume it was taken from
  * `status`
  * `size` - in GB, inherited from the source volume
  * `volume_type_id` - the **ID** of the volume type
  * `storage_policy` - the same by **name**, resolved by the provider; empty if the
    storage-policies endpoint could not be read
  * `created_at`, `updated_at` - `updated_at` is empty until the snapshot is first renamed

`progress` and `project_id` are **not** here. The list endpoint does not report them; read one
snapshot with [`dtcloud_snapshot`](snapshot.md) if you need either.

An empty list is not an error — a filter that matches nothing returns `snapshots = []`.
