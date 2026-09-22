resource "dtcloud_snapshot" "nightly" {
  name        = "app-data-2026-01-31"
  volume_id   = dtcloud_volume.data.id
  description = "Before the schema migration"
}

# Restoring is not done here: a restore produces a volume, so it belongs to the
# volume resource.
resource "dtcloud_volume" "restored" {
  name               = "app-data-restored"
  size               = 100
  storage_policy     = "standard"
  source_snapshot_id = dtcloud_snapshot.nightly.id
}
