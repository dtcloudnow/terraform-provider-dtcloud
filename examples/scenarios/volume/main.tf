terraform {
  required_providers {
    dtcloud = {
      source = "dtcloudnow/dtcloud"
    }
  }
}

# Credentials, region and endpoint come from the environment. `api_endpoint` is
# required: the provider will not guess which environment to build in.
#   export DTCLOUD_ACCESS_KEY=...
#   export DTCLOUD_SECRET_KEY=...
#   export DTCLOUD_API_URL=<your DT Cloud API base URL, ending in /api/v1>
#   export DTCLOUD_REGION_ID=<region/server id>
#
# Or run `terraform-provider-dtcloud configure` once and drop them all.
provider "dtcloud" {}

variable "storage_policy" {
  type        = string
  description = "Name of the storage policy to create volumes on. Leave empty to use the first one this project has quota for."
  default     = ""
}

variable "image_id" {
  type        = string
  description = "Image to build the bootable volume from. Leave empty to skip that example."
  default     = ""
}

variable "size" {
  type        = number
  description = "Size of the example volumes in GB. Small values keep a throwaway run cheap; a clone copies the whole disk."
  default     = 20
}

# What this project can actually create on: the endpoint filters the platform's
# volume types by the project's quota.
data "dtcloud_storage_policies" "available" {}

locals {
  # storage_policy takes the policy *name*. Passing an id would create the
  # volume and then read back as a name, so every later plan would propose a
  # change that never converges.
  policy = var.storage_policy != "" ? var.storage_policy : data.dtcloud_storage_policies.available.names[0]
}

# A blank data disk.
resource "dtcloud_volume" "data" {
  name           = "terraform-volume-example"
  size           = var.size
  storage_policy = local.policy
  description    = "Created by the terraform-provider-dtcloud example"
}

# A copy of it. The clone route answers with a different response shape than the
# create route, which is why source_volume_id is worth exercising here.
resource "dtcloud_volume" "copy" {
  name             = "terraform-volume-example-copy"
  size             = var.size
  storage_policy   = local.policy
  source_volume_id = dtcloud_volume.data.id
}

# A bootable volume, only when an image was supplied.
resource "dtcloud_volume" "boot" {
  count = var.image_id != "" ? 1 : 0

  name           = "terraform-volume-example-boot"
  size           = var.size
  storage_policy = local.policy
  image_id       = var.image_id
}

# Read the managed volume back.
data "dtcloud_volume" "data" {
  id = dtcloud_volume.data.id
}

# Every volume in the region. The API takes no query parameters, so the filters
# on this data source are applied by the provider.
data "dtcloud_volumes" "all" {
  depends_on = [dtcloud_volume.data, dtcloud_volume.copy]
}

# Deleting a volume cascades, so this is what a destroy would take with it.
data "dtcloud_volume_snapshots" "data" {
  volume_id = dtcloud_volume.data.id
}

output "id" {
  value = dtcloud_volume.data.id
}

output "policy_choices" {
  value = data.dtcloud_storage_policies.available.names
}

# false for a blank volume, true for one built from an image.
output "bootable" {
  value = dtcloud_volume.data.bootable
}

# Empty until something attaches it — attachment is dtcloud_vm_volume_attachment.
output "attached_to" {
  value = data.dtcloud_volume.data.attached_to
}

output "volume_count" {
  value = length(data.dtcloud_volumes.all.volumes)
}

output "snapshots_a_destroy_would_delete" {
  value = length(data.dtcloud_volume_snapshots.data.snapshots)
}
