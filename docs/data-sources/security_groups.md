---
page_title: "dtcloud: dtcloud_security_groups"
subcategory: "Networking"
---

# dtcloud_security_groups

Lists the security groups visible to the caller.

## Example Usage

```hcl
data "dtcloud_security_groups" "all" {}

output "names" {
  value = [for g in data.dtcloud_security_groups.all.security_groups : g.name]
}

# Every group called "baseline", as ids, ready for a VM interface.
data "dtcloud_security_groups" "baseline" {
  name = "baseline"
}

resource "dtcloud_vm" "app" {
  # ...
  network {
    uuid            = var.network_id
    security_groups = data.dtcloud_security_groups.baseline.ids

    fixed_ip {
      ip_version = 4
    }
  }
}
```

## Argument Reference

* `name` - (Optional) Only return groups with this exact name. Names are not unique, so this
  can still match several.

The filter is applied by the provider, not by the API — the list route takes no query
parameters. Every group is fetched either way.

## Attributes Reference

* `security_groups` - The groups that matched, each with `id`, `name` and `description`.
* `ids` - Just the ids, for the common case of filling a VM interface's `security_groups`,
  which takes ids and requires at least one.

The list endpoint reports an id, a name and a description and nothing else. Rules would be a
separate call per group, so use the singular
[`dtcloud_security_group`](security_group.md) data source when you need them.

`ids` follows the same order as `security_groups`. If your configuration depends on a
specific group rather than "all of them", look it up by name with the singular data source —
a list that grows by one changes every index in it.
