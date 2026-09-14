---
page_title: "dtcloud: dtcloud_image"
subcategory: "Compute"
---

# dtcloud_image

Provides a dtcloud disk image, captured from a block storage volume.

**There is no file upload.** The API has an upload endpoint, but customer accounts are not
granted the `upload_image` permission and every call to it answers:

```
403 Forbidden
You are not authorized to complete upload_image action.
```

A volume is the only source the platform accepts. Build the contents in a
[`dtcloud_volume`](volume.md) first — from a platform image, from a snapshot, or by detaching one
from a machine you have configured — then capture it here. To go the other way, build a volume
back out of the image with [`dtcloud_volume`](volume.md)'s `image_id`.

## Example Usage

### Capture a volume

```hcl
resource "dtcloud_volume" "golden" {
  name           = "golden-base"
  size           = 20
  storage_policy = "General SSD"
  image_id       = data.dtcloud_image.debian.id
}

resource "dtcloud_image" "golden" {
  name             = "golden-base"
  source_volume_id = dtcloud_volume.golden.id
  disk_format      = "qcow2"
  visibility       = "shared"
}
```

`os_distro` and `min_disk` are deliberately absent above. The capture inherits the distribution
from the volume and sizes `min_disk` to the volume, which is almost always what you want.

### Override what the capture inherited

```hcl
resource "dtcloud_image" "golden" {
  name             = "golden-base"
  source_volume_id = dtcloud_volume.golden.id
  disk_format      = "qcow2"

  os_distro = "debian12"
  min_disk  = 25
}
```

Both are applied after the capture, through the same endpoint an update uses.

### The full circle

```hcl
resource "dtcloud_volume" "from_golden" {
  name           = "app-01"
  size           = 20
  storage_policy = "General SSD"
  image_id       = dtcloud_image.golden.id
}
```

## Argument Reference

* `name` - (Required) Name of the image. Can be changed in place. Names are **not unique** on the
  platform, which is why [`dtcloud_image`](../data-sources/image.md) prefers a lookup by id.

* `source_volume_id` - (Required) Volume to capture. Its contents at the moment of the call
  become the image; later writes to the volume do not reach it. The volume survives the capture
  and the image outlives it. **ForceNew** — nothing replaces the data of an existing image, so
  pointing this at another volume builds a new one.

* `disk_format` - (Required) Format to capture as. The capture endpoint accepts `raw`, `vmdk`,
  `vdi`, `qcow2`, `vhd`, `vhdx` and `ploop`. **ForceNew.** Note that `iso` is *not* among them: a
  volume cannot be captured as an ISO. Not the same thing as the `type` attribute, which is a
  display category derived from this.

* `container_format` - (Optional) Container wrapped around the disk data. Defaults to `bare`,
  which is almost always right. **ForceNew.** Write-only: no endpoint reports it back.

* `os_distro` - (Optional) Distribution the image carries, such as `debian12`. Inherited from the
  source volume when omitted. Can be changed in place.

  ~> The update endpoint does **not** validate this. The same value the platform would refuse
  elsewhere is stored without complaint, and the next plan is clean — a typo sticks silently.
  The list of accepted values lives on the platform, so the provider has nothing to check
  against.

* `min_disk` - (Optional) Smallest volume, in GB, a machine built from this image needs. Derived
  from the size of the source volume when omitted. Between 1 and 512, checked during plan. Can be
  changed in place. The API reports it as the text `20 GB`, which the provider parses back into a
  number.

* `visibility` - (Optional) Who can see the image: `private`, `shared` or `community`. Defaults
  to `shared`. Can be changed in place.

  ~> `public` is **not accepted**, and the provider rejects it during plan rather than letting
  the request fail. Publishing needs `publicize_image`, which customer accounts are not granted,
  and the capture path is gated separately by
  `volume_extension:volume_actions:upload_public`. Both answer 403.

## Attributes Reference

* `id` - ID of the image. This is what [`dtcloud_vm`](vm.md)'s `block_device.image_id` and
  [`dtcloud_volume`](volume.md)'s `image_id` take.
* `status` - Status reported by the platform. `active` is the only status a machine can be built
  from; `queued` and `saving` mean the copy is still running.
* `size` - Size of the captured data, as the platform formats it — `1.2 GB` or `250 MB`. A
  **string**: no endpoint reports it as a number. It is the size of the *data*, not of the source
  volume: a 20 GB volume holding a Debian install captures to about 1.2 GB.
* `type` - How the platform categorises the image: `ISO` or `Template (VM)`. Derived from
  `disk_format`; the two are not interchangeable.
* `os_type` - `linux` or `windows`, when the platform reports it. **Frequently empty here** —
  see [os_type is reported inconsistently](#os_type-is-reported-inconsistently).
* `uefi` - Whether the image boots with UEFI firmware. Inherited from the source volume;
  **read-only**, because neither the capture nor the update endpoint accepts it.

## Capturing

### The volume must be available, and is held

The platform refuses the capture unless the volume is `available`:

```
Invalid volume: Volume <id> status must be available
```

A capture already running holds the volume, so a second image off the same volume is **refused
outright rather than queued**. The provider waits for the volume to be available before it asks,
which makes two `dtcloud_image` resources on one volume work — they serialise instead of racing —
but a capture started outside Terraform will still collide.

### The capture does not say what it made

The action answers `200` with an empty body and no `Location` header, so there is no id to read.
The provider finds the new image by listing images before and after the call and taking the one
that appeared with a matching name.

This is as reliable as it can be made, and it has one failure mode: if something else creates an
image of the same name at the same moment, two new images match and the provider refuses to adopt
either, rather than guessing. The capture has already happened at that point, so the image exists
and has to be imported or removed by hand. The error says so.

### How long it takes

The copy scales with how much data the volume holds, not with its declared size. A 20 GB volume
carrying a stock Debian install captures in well under a minute.

## Notes and limitations

### `os_type` is reported inconsistently

The single-image endpoint reports whatever the platform holds for `os_type`, which for most
images is nothing. The list endpoint behind [`dtcloud_images`](../data-sources/images.md) fills
it in by looking at `os_distro`.

The same image can therefore have an empty `os_type` on this resource and `linux` in
`dtcloud_images`. Both are reported as they arrive rather than reconciled, because guessing here
would mean inventing a value the platform did not send.

### Sizes are text

`size` comes back as `1.2 GB` or `250 MB`, and the platform reports `min_disk` as `20 GB`. The
provider parses `min_disk` back into a number so it can be compared with your configuration;
`size` is left as the platform formatted it, since nothing reports the underlying byte count.

### Protected images

The platform can mark an image protected, which prevents it from being deleted. The provider does
**not** expose that: the update endpoint accepts only `name`, `os_distro`, `min_disk` and
`visibility`, so nothing here could ever unmark one. An image marked protected cannot be deleted
through this API at all, and a `protected = true` argument would quietly produce a resource
`terraform destroy` could never remove.

If a destroy is refused for that reason, the mark has to be cleared outside Terraform.

## Timeouts

* `create` - Defaults to **2 hours**.
* `update` - Defaults to **15 minutes**.
* `delete` - Defaults to **30 minutes**.

The create default covers waiting for the volume to become available, the capture, and the copy
that follows it. Two hours is generous for anything but a very large volume.

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

**An import cannot produce a clean plan on its own.** Three arguments are reported by no read
endpoint, and all three are ForceNew:

| Argument | After import |
|---|---|
| `source_volume_id` | empty |
| `disk_format` | empty |
| `container_format` | empty |

Writing the correct values into the configuration does not help. Terraform sees them being
*added* to a ForceNew attribute, which is a replacement — it would destroy the image you just
adopted:

```
+ container_format = "bare"      # forces replacement
+ disk_format      = "qcow2"     # forces replacement
+ source_volume_id = "f975f8c9…" # forces replacement
Plan: 1 to add, 0 to change, 1 to destroy.
```

Tell Terraform not to act on what it cannot see:

```hcl
resource "dtcloud_image" "adopted" {
  name       = "golden-base"
  visibility = "shared"

  source_volume_id = "f975f8c9-2ecc-46d1-a420-1081286cff11"
  disk_format      = "qcow2"

  lifecycle {
    ignore_changes = [source_volume_id, disk_format, container_format]
  }
}
```

Nothing on the platform records which volume an image came from or what format it was captured
as, and the volume may be long gone, so the provider cannot recover them for you.

`name`, `os_distro`, `min_disk`, `visibility` and `uefi` all round-trip.
