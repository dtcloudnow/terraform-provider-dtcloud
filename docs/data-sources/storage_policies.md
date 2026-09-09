---
page_title: "dtcloud: dtcloud_storage_policies"
subcategory: "Storage"
---

# dtcloud_storage_policies

Lists the storage policies available in the region.

`storage_policy` is required on every [`dtcloud_volume`](../resources/volume.md) and there is no
default, so this is how you find out what the valid values are without being told out of band.

The endpoint cross-references the project's storage quota against the platform's volume types
and returns only the policies with a **non-zero quota** — so what comes back is what this
project can actually create on, not the whole catalogue.

## Example Usage

```hcl
data "dtcloud_storage_policies" "available" {}

resource "dtcloud_volume" "data" {
  name           = "app-data"
  size           = 100
  storage_policy = data.dtcloud_storage_policies.available.names[0]
}

output "choices" {
  value = data.dtcloud_storage_policies.available.names
}
```

Checking a specific policy is available before using it:

```hcl
data "dtcloud_storage_policies" "fast" {
  name = "fast"
}

resource "dtcloud_volume" "cache" {
  count = length(data.dtcloud_storage_policies.fast.policies) > 0 ? 1 : 0

  name           = "cache"
  size           = 50
  storage_policy = "fast"
}
```

## Argument Reference

* `name` - (Optional) Only return the policy with this exact name. Applied by the provider —
  the API has no filter.

## Attributes Reference

* `policies` - The policies this project has quota for, each with:
  * `id` - ID of the volume type.
  * `name` - Name of the policy.
* `names` - Just the names, for when all you need is to check that one is available.

~> **Pass the `name`, not the `id`.** The volume details endpoint reports the policy by name, so
a volume created with an id reads back as a name and every subsequent plan proposes a change
that can never converge. The `id` is exposed for cross-referencing against other services, not
for `dtcloud_volume.storage_policy`.
