---
page_title: "dtcloud: dtcloud_security_group_rule"
subcategory: "Networking"
---

# dtcloud_security_group_rule

Provides one rule inside a [`dtcloud_security_group`](security_group.md).

Rules are their own resource rather than blocks on the group, because that is what the API
offers: a rule can be created and deleted and nothing else. Modelling them separately means
adding or removing one rule disturbs neither the group nor the other rules.

## Example Usage

```hcl
resource "dtcloud_security_group" "web" {
  name = "web-tier"
}

# HTTPS from anywhere.
resource "dtcloud_security_group_rule" "https" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_ip_prefix  = "0.0.0.0/0"
}

# SSH from whoever is running Terraform. Read the warning on dtcloud_my_ip first.
resource "dtcloud_security_group_rule" "ssh" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_ip_prefix  = data.dtcloud_my_ip.current.cidr
}

data "dtcloud_my_ip" "current" {}

# Postgres, but only from machines in another group.
resource "dtcloud_security_group_rule" "db_from_app" {
  security_group_id = dtcloud_security_group.db.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 5432
  port_range_max    = 5432
  remote_group_id   = dtcloud_security_group.app.id
}

# Ping.
resource "dtcloud_security_group_rule" "ping" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "icmp"
  remote_ip_prefix  = "0.0.0.0/0"
}
```

## Argument Reference

Every argument forces a new rule — there is no update endpoint, so a change is a delete
followed by a create, which is exactly what the platform would do anyway.

* `security_group_id` - (Required) ID of the group the rule belongs to.
* `direction` - (Required) `ingress` for inbound, `egress` for outbound.
* `ethertype` - (Optional) `IPv4` or `IPv6`, spelled exactly like that. Defaults to whatever
  the platform picks, which is `IPv4`.
* `protocol` - (Optional) Protocol to match: `tcp`, `udp`, `icmp`, or a protocol number.
  Omit it to match every protocol. Case is normalised, so `TCP` and `tcp` are the same thing
  and neither produces a permanent difference. Not restricted to a fixed list, because the
  platform accepts more than the common names.
* `port_range_min` - (Optional) First port in the range. **For `icmp` this is the message
  type, not a port.**
* `port_range_max` - (Optional) Last port in the range. **For `icmp` this is the message
  code.**
* `remote_ip_prefix` - (Optional) CIDR the rule applies to, e.g. `10.0.0.0/24`. Conflicts
  with `remote_group_id`.
* `remote_group_id` - (Optional) ID of another security group whose members the rule applies
  to — the usual way to let one tier reach another without naming addresses. An **id**, not a
  name. Conflicts with `remote_ip_prefix`.

Omit both `remote_ip_prefix` and `remote_group_id` and the rule applies to the whole address
family: `0.0.0.0/0` for IPv4, `::/0` for IPv6.

## Attributes Reference

* `id` - `<security-group-id>:<rule-id>`.
* `description` - Description of the rule. Read-only: the create route does not accept one,
  so this is only ever non-empty for a rule created outside Terraform.
* `normalized_cidr` - `remote_ip_prefix` with its host bits cleared, as the platform computes
  it. `10.0.0.5/24` is reported here as `10.0.0.0/24`.

## Behaviour worth knowing

### Rejected at plan time

Three combinations the platform refuses are caught before anything is sent, so they fail in
`plan` rather than part-way through an `apply`:

* a port range **without a protocol** — a rule matching every protocol cannot also restrict
  ports;
* `port_range_min` greater than `port_range_max`, **for `tcp` and `udp` only** (see below);
* a `remote_ip_prefix` whose address family disagrees with `ethertype`.

A duplicate rule is not among them: whether one already exists is not knowable at plan time,
so the platform's refusal is passed through as-is.

### ICMP uses the port fields for type and code

For `protocol = "icmp"`, `port_range_min` is the ICMP **type** and `port_range_max` is the
**code**. They are not ports and they are not a range, so `min` greater than `max` is
perfectly ordinary — type 8 code 3 is a real thing — and the ordering check above is
deliberately not applied to ICMP.

### `0` and "not set" are the same value

`port_range_min = 0` is indistinguishable from omitting it. The SDK drops zero-valued port
fields from the request, and the platform reports an unset port as `null`, which arrives as
`0`. In practice this only matters for ICMP type 0 (echo reply), which cannot be expressed
today. Everywhere else port 0 is not a usable port anyway.

### Deleting the group takes its rules

Removing a security group removes everything in it. If a rule resource and its group are
destroyed in the same run, the rule may already be gone by the time Terraform gets to it;
that is handled and is not an error.

The same is true out of band: the platform answers a rule listing for a deleted group with an
empty list rather than an error, so rules whose group vanished simply drop out of state.

## Timeouts

* `create` - Defaults to **5 minutes**.
* `delete` - Defaults to **5 minutes**.

There is no `update` — every change replaces the rule. These calls are synchronous, so the
timeouts bound a hung request rather than a rollout.

## Import

Rules are imported as `<security-group-id>:<rule-id>`:

```
terraform import dtcloud_security_group_rule.https 4d1c8f9e-...:9a2b7c31-...
```

The rule ids are visible in the group's `inbound_rule` and `outbound_rule` attributes, and
through the `dtcloud_security_group` data source.

Everything round-trips. Import is also how the two allow-all egress rules the platform
creates with every group are brought under management — import one, then delete it by
removing the resource from your configuration.
