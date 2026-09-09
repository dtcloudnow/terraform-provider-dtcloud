---
page_title: "dtcloud: dtcloud_images"
subcategory: "Compute"
---

# dtcloud_images

Lists every image visible to the caller, with optional filters.

Use it to find an image by something other than its id — a distribution, a visibility, or a
naming convention — or to check what is available before building from one.

## Example Usage

```hcl
data "dtcloud_images" "usable" {
  os_distro = "ubuntu20.04"
  status    = "active"
}

output "ubuntu_image_ids" {
  value = [for i in data.dtcloud_images.usable.images : i.id]
}
```

Everything private to this project:

```hcl
data "dtcloud_images" "mine" {
  visibility = "private"
}
```

## Argument Reference

All filters are optional, and images must match every one that is given.

* `name` - (Optional) Only return images with this exact name.
* `os_distro` - (Optional) Only return images carrying this distribution, e.g. `ubuntu20.04`.
  The platform's distribution names carry a version. Case-insensitive.
* `visibility` - (Optional) Only return images with this visibility: `public`, `private`,
  `shared` or `community`. Case-insensitive.
* `status` - (Optional) Only return images in this status, e.g. `active`. Case-insensitive.

## Attributes Reference

* `images` - The images that matched, in the order the platform returned them. Each entry has:
  * `id` - ID of the image.
  * `name` - Name of the image.
  * `status` - Status reported by the platform.
  * `os_distro` - Distribution the image carries.
  * `os_type` - `linux` or `windows`. Filled in from `os_distro` when the platform reports
    nothing — see below.
  * `visibility` - `public`, `private`, `shared` or `community`.
  * `uefi` - Whether the image boots with UEFI firmware rather than BIOS.
  * `type` - `ISO` or `Template (VM)`. A display category, not the disk format.
  * `size` - Size of the data as the platform formats it, e.g. `1.5 GB`. A string.
  * `min_disk` - Smallest volume in GB, parsed out of the platform's `20 GB`. `0` when it
    reports none.

## Notes

### Filters are applied by the provider

The endpoint's own visibility filter only separates public images from everything else: asking
it for `private` returns the private, shared and community ones together. The provider therefore
filters the full list itself, so `visibility = "private"` returns private images, which is what
it says.

The whole list is fetched either way, so a filter here saves nothing at the API — it only makes
the result correct.

### `os_type` is filled in here, and not elsewhere

The list endpoint guesses `os_type` from `os_distro` when the platform holds no value.
[`dtcloud_image`](image.md) and the [resource](../resources/image.md) read the single-image
endpoint, which makes no such guess and usually reports nothing.

Both are passed through as they arrive rather than reconciled — inventing a value the platform
did not send would be worse than reporting two different ones.
