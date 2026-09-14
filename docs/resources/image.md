---
page_title: "dtcloud: dtcloud_image"
subcategory: "Compute"
---

# dtcloud_image

Uploads a disk image to dtcloud and manages it.

Creating an image is two requests and a wait. The first opens an empty record on the platform;
the second sends the file. Until the file has arrived and the image reaches `active`, nothing
can be built from it — so both happen inside a single `terraform apply`, and the resource is not
considered created until the image is usable.

Only four things about an image can be changed afterwards: `name`, `os_distro`, `min_disk` and
`visibility`. Everything else, the file included, is fixed once the image exists.

## Example Usage

```hcl
resource "dtcloud_image" "golden" {
  name        = "app-base-2204"
  source_file = "/build/output/app-base.qcow2"
  disk_format = "qcow2"
  os_distro   = "ubuntu20.04"
  min_disk    = 20

  # Rebuild the image when the file changes but its path does not.
  source_file_hash = filesha256("/build/output/app-base.qcow2")
}
```

Building a machine from it:

```hcl
resource "dtcloud_vm" "app" {
  name      = "app-01"
  flavor_id = var.flavor_id
  key_name  = dtcloud_ssh_key.deploy.name

  block_device {
    image_id = dtcloud_image.golden.id
    size     = 40
  }

  network {
    network_id      = dtcloud_network.app.id
    fixed_ip        = "10.0.1.20"
    security_groups = [var.security_group_id]
  }
}
```

An ISO, published to everyone who shares the project:

```hcl
resource "dtcloud_image" "installer" {
  name        = "debian-netinst"
  source_file = "/srv/isos/debian-13-netinst.iso"
  disk_format = "iso"
  os_distro   = "debian10"
  min_disk    = 2
  visibility  = "public"
}
```

## Argument Reference

* `name` - (Required) Name of the image. Must not contain `<`, `>`, `&`, `"` or `'`. Can be
  changed in place. Names are **not unique** on the platform, which is why
  [`dtcloud_image`](../data-sources/image.md) prefers a lookup by id.
* `source_file` - (Required) Path to the local disk file to upload. Read during apply, so the
  file has to still be there when Terraform runs. Changing it builds a new image; there is no
  endpoint that replaces the data of an existing one.
* `disk_format` - (Required) Format of the file, such as `qcow2` or `iso`. The accepted values
  come from the platform's own configuration rather than from a list in the provider, so an
  unsupported one is refused by the API during apply rather than during plan. **Not the same
  thing as the `type` attribute**, which is a display category derived from it. Changing it
  builds a new image — the format describes data already uploaded.
* `os_distro` - (Required) Distribution the image carries. The accepted values are the
  platform's, like `disk_format`, and they **carry a version**: `ubuntu20.04`, `centos8`,
  `debian10`, `win2k19`. A bare `ubuntu` is refused. Can be changed in place.
* `min_disk` - (Required) Smallest volume, in GB, a machine built from this image needs. Between
  1 and 512. Can be changed in place.
* `source_file_hash` - (Optional) Hash of the file, so a change to its contents is noticed when
  its path stays the same. Set it to `filesha256(...)` or `filemd5(...)`. Nothing reads the file
  during plan — without this a modified file goes unnoticed. Changing it builds a new image.
* `visibility` - (Optional) Who can see the image: `public`, `private`, `shared` or `community`.
  Defaults to `shared`, which is what the platform applies when the field is omitted. Can be
  changed in place.
* `min_ram` - (Optional) Smallest amount of RAM, in MB, a machine built from this image needs.
  **Write-only** — see below. Changing it builds a new image.
* `tags` - (Optional) Set of tags to attach to the image. **Write-only.** Changing it builds a
  new image.
* `uefi` - (Optional) Boot the image with UEFI firmware instead of BIOS. Defaults to `false`.
  Changing it builds a new image. Unlike the other create-only arguments this one *is* reported
  back, so a change made outside Terraform shows up as drift.

There is deliberately no `protected` argument. See [Protected images](#protected-images).

## Attributes Reference

* `id` - ID of the image. This is what [`dtcloud_vm`](vm.md)'s `block_device.image_id` takes.
* `status` - Status reported by the platform. `active` is the only status a machine can be built
  from; `queued` and `saving` mean the data is not there yet, and `killed` means the upload
  failed.
* `size` - Size of the uploaded data, as the platform formats it — `1.5 GB` or `250 MB`. A
  **string**: no endpoint reports it as a number.
* `type` - How the platform categorises the image: `ISO` or `Template (VM)`. Derived from
  `disk_format`; the two are not interchangeable.
* `os_type` - `linux` or `windows`, when the platform reports it. **Frequently empty here** —
  see [os_type is reported inconsistently](#os_type-is-reported-inconsistently).

## Uploading

### The file is sent during apply, in full

`terraform apply` blocks while the file goes up. A large image makes for a long apply, and the
`create` timeout has to cover the transfer as well as the platform's own work afterwards — that
is why it defaults to two hours rather than to minutes.

The size is declared before the transfer starts, so an image larger than the platform allows is
refused immediately rather than after everything has been sent.

### A failed upload leaves nothing behind

If the upload fails, the platform **deletes the image it just created**. There is no half-built
record to clean up, and nothing to import. Terraform marks the resource as tainted and the next
apply builds it again from the start.

### One upload at a time

The platform allows a limited number of concurrent uploads per user — by default, one. Two
`dtcloud_image` resources applied in parallel will collide, and the second is refused with a
message saying another upload is in progress. This affects `count` and `for_each` over images,
which Terraform runs in parallel by default.

Either order them explicitly:

```hcl
resource "dtcloud_image" "second" {
  # ...
  depends_on = [dtcloud_image.first]
}
```

or run the apply with `-parallelism=1`.

## Notes and limitations

### Write-only arguments

`min_ram` and `tags` are accepted when the image is created and are reported by **no** read
endpoint. Consequences:

* they cannot drift — a change made outside Terraform is invisible;
* they do not survive an import;
* changing either one rebuilds the image, because that is the only way to apply a new value.

`disk_format` behaves the same way for reading purposes: the platform reports the derived `type`
instead, never the format itself.

### `os_type` is reported inconsistently

The single-image endpoint reports whatever the platform holds for `os_type`, which for most
images is nothing. The list endpoint behind [`dtcloud_images`](../data-sources/images.md) fills
it in by looking at `os_distro`.

The same image can therefore have an empty `os_type` on this resource and `linux` in
`dtcloud_images`. Both are reported as they arrive rather than reconciled, because guessing here
would mean inventing a value the platform did not send.

### Sizes are text

`size` comes back as `1.5 GB` or `250 MB`, and the platform reports `min_disk` as `20 GB`. The
provider parses `min_disk` back into a number so it can be compared with your configuration;
`size` is left as the platform formatted it, since nothing reports the underlying byte count.

### Protected images

The platform can mark an image protected, which prevents it from being deleted. The provider
does **not** expose that: the update endpoint accepts only `name`, `os_distro`, `min_disk` and
`visibility`, so nothing here could ever unmark one. An image marked protected cannot be deleted
through this API at all, and a `protected = true` argument would quietly produce a resource
`terraform destroy` could never remove.

If a destroy is refused for that reason, the mark has to be cleared outside Terraform.

## Timeouts

* `create` - Defaults to **2 hours**.
* `update` - Defaults to **15 minutes**.
* `delete` - Defaults to **30 minutes**.

The create default covers the upload itself, which is as long as it takes to send the file over
your connection. Raise it for a large image on a slow link; there is no way for the provider to
estimate it.

Override them with a `timeouts` block:

```hcl
timeouts {
  create = "6h"
}
```

## Import

Images can be imported by ID:

```
terraform import dtcloud_image.golden 3f2a1c9e-77b4-4a01-9d3e-2b6c81f4e5a7
```

**Five arguments do not survive an import**, because no read endpoint reports them:

| Argument | After import |
|---|---|
| `source_file` | empty |
| `source_file_hash` | empty |
| `disk_format` | empty |
| `min_ram` | empty |
| `tags` | empty |

The first plan after an import will therefore want to replace the image. Write those five into
the configuration to match what was originally uploaded before you apply, or the image will be
rebuilt — and `disk_format` in particular has to match, since the platform only reports the
derived `type`.

`name`, `os_distro`, `min_disk`, `visibility` and `uefi` all round-trip.
