---
page_title: "dtcloud: dtcloud_volumes"
subcategory: "Storage"
---

# dtcloud_volumes

Lists the volumes visible to the caller, optionally filtered.

~> **The filters are applied by the provider, not the API.** Unlike `dtcloud_networks`, the
list endpoint takes no query parameters, so every read fetches the whole list and narrows it
afterwards. The filters are a convenience, not a smaller request.

## Example Usage

```hcl
data "dtcloud_volumes" "all" {}

output "total_gb" {
  value = sum([for v in data.dtcloud_volumes.all.volumes : v.size])
}
```

Finding one volume by name:

```hcl
data "dtcloud_volumes" "data_disk" {
  name = "app-data"
}

locals {
  data_disk_id = data.dtcloud_volumes.data_disk.volumes[0].id
}
```

Everything attached to one VM:

```hcl
data "dtcloud_volumes" "on_web" {
  attached_to_id = dtcloud_vm.web.id
}
```

## Argument Reference

* `name` - (Optional) Only return volumes with this exact name.
* `attached_to_id` - (Optional) Only return volumes attached to this VM.

## Attributes Reference

* `volumes` - The volumes that matched, each with:
  * `id`, `name`, `status`
  * `storage_policy`, `size`, `bootable`
  * `volume_type` - `HDD` or `CD-ROM`
  * `attached_to`, `attached_to_id`, `attached_to_status`
  * `is_detachable`

~> **`is_detachable` is `true` here for detached volumes**, where
[`dtcloud_volume`](volume.md) and the resource report `false` for the same one. The two
endpoints compute it differently and the provider reports what each said. It only means
anything for an attached volume.

~> **`size` is normalised.** The list endpoint reports it as the string `"20 GB"` where the
details endpoint sends the number `20`. This data source parses it back to an integer count of
GB, so `size` means the same thing here, on `dtcloud_volume` and on the resource.

For the image metadata and the creation timestamps, use the
[`dtcloud_volume`](volume.md) data source on a single id — the list endpoint does not carry
them.
