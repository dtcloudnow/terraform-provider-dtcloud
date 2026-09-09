---
page_title: "dtcloud: dtcloud_my_ip"
subcategory: "Networking"
---

# dtcloud_my_ip

Reports the address the API sees your requests coming from.

**Not necessarily your public internet address.** It is your address *on whatever network you
reach the API over*. Over the corporate VPN that is your VPN address — a `10.x` one — which
is the address a VM would also see you at, and therefore the right thing to put in a rule.
Verified on DEV: the data source returned `10.100.100.176`, this machine's VPN address, while
its public address was something else entirely.

It belongs to the security group service — the route is part of it, and its only purpose is
scoping a rule to your own address: *let me in, and nobody else*.

## Example Usage

```hcl
data "dtcloud_my_ip" "current" {}

resource "dtcloud_security_group_rule" "ssh_from_me" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_ip_prefix  = data.dtcloud_my_ip.current.cidr
}
```

## Argument Reference

None.

## Attributes Reference

* `ip` - The address the API saw the request come from, on the network you reached it over.
* `cidr` - The same address as a single-host CIDR — `/32` for IPv4, `/128` for IPv6 — which
  is the form `remote_ip_prefix` takes.

## Read this before using it in a rule

The value changes when you move between networks — office to VPN to home — and a connection
can renumber on its own.

Because a security group rule is entirely `ForceNew`, a changed address means the next plan
**deletes the rule and creates a new one**. For an administrative allow-rule that is usually
exactly right — the rule follows you. For anything a service depends on it is a poor idea.

It also means whichever machine ran the last `apply` decides who has access. From CI, that is
the build runner's address, which is rarely what anyone intended. Prefer an explicit CIDR
there.
