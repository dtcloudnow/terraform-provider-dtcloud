---
page_title: "dtcloud: dtcloud_image_versions"
subcategory: "Compute"
---

# dtcloud_image_versions

Reads the platform's own image catalogue.

This is **not** the same list as [`dtcloud_images`](images.md). That one is what the image
service holds; this one is the curated set the platform offers, and it is the only place that
says which flavors an image may be built on.

## Example Usage

```hcl
data "dtcloud_image_versions" "ubuntu" {
  type = "ubuntu"
}

locals {
  latest_ubuntu = element(data.dtcloud_image_versions.ubuntu.versions, length(data.dtcloud_image_versions.ubuntu.versions) - 1)
}

resource "dtcloud_vm" "app" {
  name      = "app-01"
  flavor_id = local.latest_ubuntu.valid_flavor_ids[0]
  key_name  = dtcloud_ssh_key.deploy.name

  block_device {
    image_id = local.latest_ubuntu.id
    size     = local.latest_ubuntu.min_volume_size
  }

  network {
    network_id      = dtcloud_network.app.id
    fixed_ip        = "10.0.1.20"
    security_groups = [var.security_group_id]
  }
}
```

## Argument Reference

* `type` - (Optional) Only return entries in this family, e.g. `ubuntu`. Case-insensitive.
  Applied by the provider; the endpoint takes no parameters of its own.

## Attributes Reference

* `versions` - Catalogue entries, sorted by family and then by version. Each entry has:
  * `type` - Family the entry belongs to, e.g. `ubuntu`.
  * `id` - ID of the image, for `block_device.image_id` on [`dtcloud_vm`](../resources/vm.md).
  * `version` - Version string, e.g. `22.04`.
  * `image_type` - `ISO` or `Template (VM)` — the same categories as `type` on
    [`dtcloud_image`](../resources/image.md), **not** an operating system. The endpoint calls
    this `type` too, which is why it is renamed here: the family already has that name.
  * `min_volume_size` - Smallest volume in GB. **A number here**, unlike everywhere else in the
    image endpoints, where sizes are text.
  * `valid_flavor_ids` - Flavors the entry may be built on. Empty when the catalogue places no
    restriction on it.

## Notes

### The result is a sorted list, not a map

The endpoint answers with entries grouped by family. They are flattened into one list, sorted by
family and then by version, so an entry added to the catalogue does not reshuffle the indexes
your configuration refers to. It can still shift them — a new family sorting before yours moves
everything after it — so prefer filtering with `type` and matching on `version` over indexing
into the full list.

### `valid_flavor_ids` is usually empty

The catalogue stores this value rather than the platform computing it, and every entry seen so
far leaves it unset — so in practice this comes back as an empty list, meaning the entry places
no restriction on which flavors it can be built on.

The field is untyped in the catalogue and can also hold a list or a comma-separated string.
Both are accepted and reported as a list of strings, so an entry written with a value does not
fail the read; neither shape has been observed on a live platform.
