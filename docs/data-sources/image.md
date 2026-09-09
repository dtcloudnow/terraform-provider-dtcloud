---
page_title: "dtcloud: dtcloud_image"
subcategory: "Compute"
---

# dtcloud_image

Looks up one image, by id or by name.

Use it to reference an image Terraform did not upload — one the platform provides, or one
somebody built by hand. Declaring such an image as a [`dtcloud_image`
resource](../resources/image.md) instead would hand Terraform ownership of it, and a later
destroy would delete it.

## Example Usage

By id, which is unambiguous:

```hcl
data "dtcloud_image" "base" {
  id = var.base_image_id
}

resource "dtcloud_vm" "app" {
  name      = "app-01"
  flavor_id = var.flavor_id
  key_name  = dtcloud_ssh_key.deploy.name

  block_device {
    image_id = data.dtcloud_image.base.id
    size     = max(data.dtcloud_image.base.min_disk, 40)
  }

  network {
    network_id      = dtcloud_network.app.id
    fixed_ip        = "10.0.1.20"
    security_groups = [var.security_group_id]
  }
}
```

By name:

```hcl
data "dtcloud_image" "base" {
  name = "ubuntu-22.04-cloud"
}
```

## Argument Reference

Exactly one of the following is required.

* `id` - (Optional) ID of the image to look up.
* `name` - (Optional) Name of the image to look up. **It is an error for more than one image to
  carry the name** — see below.

## Attributes Reference

* `id` - ID of the image.
* `name` - Name of the image.
* `os_distro` - Distribution the image carries.
* `visibility` - `public`, `private`, `shared` or `community`.
* `uefi` - Whether the image boots with UEFI firmware rather than BIOS.
* `min_disk` - Smallest volume, in GB, a machine built from this image needs. `0` when the
  platform reports none.
* `status` - Status reported by the platform. Only an `active` image can be built from.
* `size` - Size of the data, as the platform formats it — `1.5 GB` or `250 MB`. A string.
* `type` - `ISO` or `Template (VM)`. A display category, not the disk format.
* `os_type` - `linux` or `windows`, when the platform reports it. **Frequently empty** — see
  below.

## Notes

### Names are not identifiers

Two images may carry the same name. A lookup by name that matches more than one **fails** rather
than picking the first: silently choosing would mean machines get built from an image nobody
chose, and the choice would change the day somebody uploads another one with the same name.

The error names the matching ids so you can pick the one you meant and look it up by `id`.

### `os_type` is often empty here

This data source reads the single-image endpoint, which reports whatever the platform holds for
`os_type` — for most images, nothing. [`dtcloud_images`](images.md) reads the list endpoint,
which fills the value in from `os_distro` instead.

The same image can therefore report an empty `os_type` here and `linux` there. If you need the
value, read it from `dtcloud_images` or derive it from `os_distro` yourself.

### `min_disk` is parsed out of text

The platform reports it as `20 GB`. The provider turns that back into a number so it can be used
in arithmetic, as in the `size` calculation in the example above. `size` is left as text, since
nothing reports the underlying byte count.
