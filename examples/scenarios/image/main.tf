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

variable "source_file" {
  type        = string
  description = "Path to the local disk file to upload. Keep it small for a throwaway run: apply blocks for as long as the transfer takes."
}

variable "disk_format" {
  type        = string
  description = "Format of the file, e.g. qcow2 or iso. The accepted values come from the platform, so an unsupported one is refused during apply."
  default     = "qcow2"
}

variable "os_distro" {
  type        = string
  description = "Distribution the image carries. Validated by the platform, not the provider, and the names carry a version: ubuntu20.04, centos8, debian10, win2k19."
  default     = "ubuntu20.04"
}

variable "min_disk" {
  type        = number
  description = "Smallest volume in GB a machine built from this image needs."
  default     = 20
}

# The image. Creating it opens a record and then uploads the file; the resource
# is not finished until the platform reports the image as active.
resource "dtcloud_image" "example" {
  name        = "terraform-image-example"
  source_file = var.source_file
  disk_format = var.disk_format
  os_distro   = var.os_distro
  min_disk    = var.min_disk

  # Without this, a file that changes in place goes unnoticed: nothing reads it
  # during plan.
  source_file_hash = filesha256(var.source_file)
}

# The same image read back. Looking one up by id is unambiguous; names are not
# unique on the platform.
data "dtcloud_image" "example" {
  id = dtcloud_image.example.id
}

# Every image this project can see, narrowed to the ones that can be built from.
data "dtcloud_images" "usable" {
  status     = "active"
  depends_on = [dtcloud_image.example]
}

# The platform's own catalogue, which is a different list: it is the only place
# that says which flavors an image may be built on.
data "dtcloud_image_versions" "catalogue" {}

output "image_id" {
  description = "ID of the uploaded image, for block_device.image_id on dtcloud_vm."
  value       = dtcloud_image.example.id
}

output "image_status" {
  description = "Only an active image can be built from."
  value       = data.dtcloud_image.example.status
}

output "image_size" {
  description = "Formatted by the platform, e.g. 1.5 GB. There is no endpoint that reports a byte count."
  value       = data.dtcloud_image.example.size
}

output "usable_image_names" {
  description = "Names of every image in this project that is ready to build from."
  value       = [for i in data.dtcloud_images.usable.images : i.name]
}

output "catalogue" {
  description = "The platform's image catalogue, sorted by family and version."
  value       = [for v in data.dtcloud_image_versions.catalogue.versions : "${v.type} ${v.version} (${v.id})"]
}
