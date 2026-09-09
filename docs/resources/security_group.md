---
page_title: "dtcloud: dtcloud_security_group"
subcategory: "Networking"
---

# dtcloud_security_group

Provides a dtcloud security group — the set of firewall rules applied to a VM's network
interfaces.

The group is only a name and a description. The rules inside it are separate
[`dtcloud_security_group_rule`](security_group_rule.md) resources, so that adding or removing
one rule leaves the group and every other rule untouched.

## Example Usage

```hcl
resource "dtcloud_security_group" "web" {
  name        = "web-tier"
  description = "HTTPS from anywhere, SSH from the office"
}

resource "dtcloud_security_group_rule" "https" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_ip_prefix  = "0.0.0.0/0"
}

resource "dtcloud_vm" "web" {
  # ...
  network {
    uuid            = var.network_id
    security_groups = [dtcloud_security_group.web.id]

    fixed_ip {
      ip_version = 4
    }
  }
}
```

Referencing the group by `.id` like this is worth doing for a second reason: it tells
Terraform the VM depends on the group, so `terraform destroy` takes the VM down first. A
group written as a hand-copied uuid has no such edge, and its delete fails until whatever is
using it is gone.

## Argument Reference

* `name` - (Required) Name of the security group. Can be changed in place. Names are **not**
  required to be unique. Must not contain `<`, `>`, `&`, `'` or `"` — the API rejects them,
  and so does the plan.
* `description` - (Optional) Description of the group. Can be changed in place, but **cannot
  be cleared once set** — see below. Same character restriction as `name`.

## Attributes Reference

* `id` - ID of the security group.
* `inbound_rule` / `outbound_rule` - The group's rules **as the platform displays them**.
  Read-only, and lossy — see below. Each block has:
  * `id` - ID of the rule. This is the second half of a `dtcloud_security_group_rule` import
    address.
  * `protocol` - Display name, e.g. `SSH`, `HTTPS`, `ALL TCP`, `ANY`.
  * `port_range` - Rendered range, e.g. `443`, `1-65535`, or `-`.
  * `source` - A CIDR, or the *name* of the referenced security group.

## Behaviour worth knowing

### `inbound_rule` and `outbound_rule` are a display snapshot, not configuration

The details endpoint does not return the rules as they were created. It turns each one into
something for the web panel: a protocol *display name* rather than the protocol, a port range
as a string, and a referenced security group as that group's **name** rather than its id.
Those display names are read from a configurable table on the platform, so `22` is `SSH`
only until someone edits a row.

None of that can be turned back into a rule, which is why rules are managed as their own
resource and read from a different endpoint. Treat these attributes as something to look at,
never as something to compute from.

They are also a snapshot from this resource's **last read**. Rules created in the same
`apply` appear only after the next refresh.

### A new group is not empty

The platform adds two rules to every new security group: allow-all egress, once for IPv4 and
once for IPv6. They show up in `outbound_rule` immediately and Terraform does not own them.

To get rid of them, import one as a `dtcloud_security_group_rule` and then remove it from
your configuration:

```sh
terraform import dtcloud_security_group_rule.default_v4 <group-id>:<rule-id>
```

The rule ids are in this resource's `outbound_rule` blocks.

### `description` cannot be cleared

Setting a description and later removing it is rejected at plan time. The reason is on our
side rather than the API's: the SDK marks the field `omitempty`, so an empty description is
dropped from the update request and the platform keeps the old value — which would leave a
plan that proposes the same change forever. Set a different description instead, or recreate
the group.

Changing it to another non-empty value works normally.

### Deleting a group that is in use

The platform refuses to delete a security group still bound to a network interface, and the
provider surfaces that refusal rather than hiding it. Destroy or detach whatever is using it
first.

## Timeouts

* `create` - Defaults to **5 minutes**.
* `update` - Defaults to **5 minutes**.
* `delete` - Defaults to **5 minutes**.

Unlike networks and VMs, these calls are synchronous — the API answers once the work is done,
so there is nothing to poll and these are a bound on a hung request rather than on a rollout.

Override them with a `timeouts` block:

```hcl
timeouts {
  create = "10m"
}
```

## Import

Security groups can be imported by ID:

```
terraform import dtcloud_security_group.web 4d1c8f9e-0d3b-4a7c-9f2e-2b8a1c6d5e40
```

`name` and `description` both round-trip. The rules do not come with it — each one is its own
resource and has to be imported separately, including the two the platform created itself.
