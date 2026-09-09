---
page_title: "dtcloud: dtcloud_snapshot"
subcategory: "Storage"
---

# dtcloud_snapshot

Provides a point-in-time copy of a dtcloud block storage volume.

A snapshot is taken against a volume and stays with it. Its size and its storage policy are the
source volume's, decided at the moment the copy is taken — the only things this resource can
change afterwards are `name` and `description`.

**Restoring is not here.** Building a volume back out of a snapshot produces a *volume*, and a
volume has one owner in this provider: set
[`source_snapshot_id` on `dtcloud_volume`](volume.md).

## Example Usage

```hcl
resource "dtcloud_volume" "data" {
  name           = "app-data"
  size           = 100
  storage_policy = "standard"
}

resource "dtcloud_snapshot" "nightly" {
  name        = "app-data-nightly"
  volume_id   = dtcloud_volume.data.id
  description = "Taken before the schema migration"
}
```

Restoring from it:

```hcl
resource "dtcloud_volume" "restored" {
  name               = "app-data-restored"
  size               = 100
  storage_policy     = "standard"
  source_snapshot_id = dtcloud_snapshot.nightly.id
}
```

Snapshotting a disk that is in use — which is allowed, see below:

```hcl
resource "dtcloud_vm_volume_attachment" "data" {
  vm_id     = dtcloud_vm.web.id
  volume_id = dtcloud_volume.data.id
}

resource "dtcloud_snapshot" "live" {
  name       = "app-data-live"
  volume_id  = dtcloud_volume.data.id
  depends_on = [dtcloud_vm_volume_attachment.data]
}
```

## Argument Reference

* `name` - (Required) Name of the snapshot. Must not contain `<`, `>`, `&`, `"` or `'`. Can be
  changed in place.
* `volume_id` - (Required) ID of the volume to copy. Changing it takes a new snapshot and
  destroys this one — there is no endpoint that re-points an existing snapshot at another
  volume.
* `description` - (Optional) Free-text description. Must not contain `<`, `>`, `&`, `"` or `'`.
  Removing it from the configuration clears it on the platform.

## Attributes Reference

* `id` - ID of the snapshot.
* `status` - Status reported by the platform. `available` is the only resting state.
* `size` - Size in GB, inherited from the source volume. A snapshot cannot be resized.
* `volume_type_id` - **ID** of the volume type backing the snapshot, as the snapshots endpoint
  reports it.
* `storage_policy` - The same volume type by **name**, resolved by the provider. Empty if the
  storage-policies endpoint could not be read; see below.
* `progress` - How far the copy has got, as a percentage string such as `100%`.
* `project_id` - ID of the project the snapshot belongs to.
* `created_at` - When the snapshot was taken.
* `updated_at` - When its metadata last changed. Empty until it is first renamed or
  re-described.

## Behaviour worth knowing

### The source volume may be attached and running

A volume attached to a running machine can be copied without detaching it. That is the ordinary
production case and needs nothing special.

What that does **not** give you is a consistent filesystem. The copy is of the block device as
it is at that instant, mid-write and all. If the data matters, quiesce the application, or take
the snapshot with the machine stopped.

### `description` round-trips, unlike a volume's

A snapshot's description is reported back by the platform, so it drifts like any
other argument, survives `terraform import`, and can be removed:

```hcl
resource "dtcloud_snapshot" "nightly" {
  name      = "app-data-nightly"
  volume_id = dtcloud_volume.data.id
  # description deleted -> cleared on the platform on the next apply
}
```

That is the opposite of [`dtcloud_volume`](volume.md), where `description` is
write-only: it cannot be read, cannot drift, does not survive import and cannot
be cleared. If you are moving between the two resources, this is the difference
to keep in mind.

The platform expresses "no description" differently from an empty string, which
it rejects. The provider handles the difference for you.

### `storage_policy` is resolved by the provider

The snapshots endpoint reports the storage policy as `volume_type_id`, a UUID. Everywhere else
in this provider — [`dtcloud_volume`](volume.md), [`dtcloud_volumes`](../data-sources/volumes.md),
[`dtcloud_volume_snapshots`](../data-sources/volume_snapshots.md) — a storage policy is a
**name**. So this resource makes a second call to the storage-policies endpoint and reports
both: `volume_type_id` raw, `storage_policy` resolved.

That second call is made **once per Terraform run**, not once per snapshot: the platform's
volume types do not change while an apply is running, so the answer is memoised on the provider
configuration and shared by every snapshot in the configuration.

It is also best-effort. If the storage-policies endpoint cannot be read, the name is left empty
and everything else is still reported — a snapshot is perfectly usable without it. An empty
`storage_policy` means exactly that. One consequence of the memoisation is worth knowing: a
failure is cached too, so if that endpoint is unavailable at the moment of the first lookup,
`storage_policy` stays empty for every snapshot for the rest of the run. `volume_type_id` is
always reported and never depends on the second call.

### Deleting a volume deletes its snapshots

`terraform destroy` on a [`dtcloud_volume`](volume.md) cascades: every snapshot of that volume
goes with it, whether or not Terraform manages the snapshot. Terraform will not warn you, since
from its point of view the snapshot resource was never touched.

If a volume and its snapshots are both in your configuration, order them with `depends_on` or
let the reference in `volume_id` do it — Terraform destroys the snapshot first, and the cascade
then has nothing left to take.

### Deleting a snapshot can be refused

The platform will not delete a snapshot while it is still `creating`, nor while a volume built
from it still depends on it. Both come back as the API's own message, passed through unchanged.
Destroy the dependent volume first.

If both are in your configuration, `source_snapshot_id` already orders the destroy correctly:
the restored volume goes first, then the snapshot, then the source volume.

## Timeouts

* `create` - Defaults to **30 minutes**.
* `update` - Defaults to **15 minutes**.
* `delete` - Defaults to **20 minutes**.

Creating is a copy of the whole volume, and `progress` exists precisely because it can take a
while on a large one, so the create default is generous rather than tight. A small snapshot
completes in well under a minute; raise the timeout if your volumes are large.

Override them with a `timeouts` block:

```hcl
timeouts {
  create = "1h"
}
```

## Import

Snapshots can be imported by ID:

```
terraform import dtcloud_snapshot.nightly 3f2a1c9e-77b4-4a01-9d3e-2b6c81f4e5a7
```

**Everything round-trips.** Unlike [`dtcloud_volume`](volume.md), `name`, `volume_id` and
`description` are all reported by the platform, so an import followed by a plan is empty.
