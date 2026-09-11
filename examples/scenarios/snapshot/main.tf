terraform {
  required_providers {
    dtcloud = {
      source = "dtcloudnow/dtcloud"
    }
  }
}

# Credentials and region come from the environment:
#   export DTCLOUD_ACCESS_KEY=...
#   export DTCLOUD_SECRET_KEY=...
#   export DTCLOUD_REGION_ID=1
provider "dtcloud" {}

variable "storage_policy" {
  type        = string
  description = "Name of the storage policy to create the volume on. Leave empty to use the first one this project has quota for."
  default     = ""
}

variable "size" {
  type        = number
  description = "Size of the example volume in GB. A snapshot copies the whole disk, so keep this small for a throwaway run."
  default     = 20
}

data "dtcloud_storage_policies" "available" {}

locals {
  policy = var.storage_policy != "" ? var.storage_policy : data.dtcloud_storage_policies.available.names[0]
}

# The volume to copy. A snapshot is always taken against a volume, and its size
# and storage policy come from that volume.
resource "dtcloud_volume" "data" {
  name           = "terraform-snapshot-example-source"
  size           = var.size
  storage_policy = local.policy
  description    = "Created by the terraform-provider-dtcloud snapshot example"
}

# The snapshot itself.
resource "dtcloud_snapshot" "nightly" {
  name        = "terraform-snapshot-example"
  volume_id   = dtcloud_volume.data.id
  description = "Created by the terraform-provider-dtcloud snapshot example"
}

# Restoring produces an ordinary volume with no record of where it came from,
# which is why source_snapshot_id forces a replacement rather than tracking
# drift. `size` must be at least the snapshot's, so it is read from it.
resource "dtcloud_volume" "restored" {
  name               = "terraform-snapshot-example-restored"
  size               = dtcloud_snapshot.nightly.size
  storage_policy     = dtcloud_snapshot.nightly.storage_policy
  source_snapshot_id = dtcloud_snapshot.nightly.id
}

# Reading one snapshot back by id.
data "dtcloud_snapshot" "one" {
  id = dtcloud_snapshot.nightly.id
}

# Every available snapshot of the source volume.
data "dtcloud_snapshots" "of_source" {
  volume_id  = dtcloud_volume.data.id
  status     = "available"
  depends_on = [dtcloud_snapshot.nightly]
}

# The other snapshot list: scoped to one volume, sorted newest-first, and with a
# narrower set of fields. Useful for seeing what a destroy of the volume would
# take with it.
data "dtcloud_volume_snapshots" "of_source" {
  volume_id  = dtcloud_volume.data.id
  depends_on = [dtcloud_snapshot.nightly]
}

output "snapshot" {
  description = "The snapshot as the platform reports it."
  value = {
    id             = dtcloud_snapshot.nightly.id
    status         = dtcloud_snapshot.nightly.status
    size           = dtcloud_snapshot.nightly.size
    progress       = dtcloud_snapshot.nightly.progress
    volume_type_id = dtcloud_snapshot.nightly.volume_type_id
    storage_policy = dtcloud_snapshot.nightly.storage_policy
    created_at     = dtcloud_snapshot.nightly.created_at
  }
}

output "restored_volume_id" {
  description = "The volume built back out of the snapshot."
  value       = dtcloud_volume.restored.id
}
