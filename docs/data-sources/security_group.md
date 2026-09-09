---
page_title: "dtcloud: dtcloud_security_group"
subcategory: "Networking"
---

# dtcloud_security_group

Looks up one security group, by id or by name.

Use it to reference a group Terraform did not create — the common case, since security groups
tend to be shared across the things that use them. Declaring one as a `resource` instead
would hand Terraform ownership, and a later `terraform destroy` would take it down along with
everything else.

The name lookup exists to spare you a hand-copied uuid: `security_groups` is required on every
VM network interface, and it takes ids.

## Example Usage

```hcl
data "dtcloud_security_group" "shared" {
  name = "platform-baseline"
}

resource "dtcloud_vm" "app" {
  # ...
  network {
    uuid            = var.network_id
    security_groups = [data.dtcloud_security_group.shared.id]

    fixed_ip {
      ip_version = 4
    }
  }
}
```

## Argument Reference

Exactly one of:

* `id` - (Optional) ID of the security group.
* `name` - (Optional) Name of the security group. Names are **not** unique on this platform,
  so a name matching more than one group is an error rather than an arbitrary pick. Look the
  group up by id when that happens.

## Attributes Reference

* `id` - ID of the security group.
* `name` - Name of the security group.
* `description` - Description of the security group.
* `inbound_rule` / `outbound_rule` - The group's rules as the platform displays them, each
  with `id`, `protocol`, `port_range` and `source`.

The rule blocks are a **display** of the rules, not the rules themselves: `protocol` is a
configurable display name, `port_range` is a string, and `source` is a CIDR *or the name of
another security group*. See
[the resource page](../resources/security_group.md#inbound_rule-and-outbound_rule-are-a-display-snapshot-not-configuration)
for why they cannot be anything else. Their `id` fields are useful, though — that is the
second half of a `dtcloud_security_group_rule` import address.
